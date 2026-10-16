package pulse

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// uniqueTable names a table this test alone owns, the way the Postgres
// storage tests name a schema.
func uniqueTable() string {
	return fmt.Sprintf("pulse_explain_%d", rand.Uint64()>>1)
}

// The PostgreSQL half of generic planning: a parameterised statement is
// planned through a prepared statement under force_generic_plan, so Pulse
// never needs the values the query ran with. Runs against the CI service
// container; skipped when PULSE_TEST_POSTGRES_DSN is unset.
func TestExplainQuery_PostgresPlansGenerically(t *testing.T) {
	db := openPostgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := uniqueTable()
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+table+" (id bigserial primary key, user_id bigint, status text, total bigint)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) })
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+table+" (user_id, status, total) SELECT i % 500, 'paid', i FROM generate_series(1, 20000) i"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, "ANALYZE "+table); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	query := "SELECT * FROM " + table + " WHERE user_id = $1 AND status = $2"

	// No values: the case the dashboard's button has to answer.
	plan, generic, err := explainQuery(ctx, db, "pgx", query, nil)
	if err != nil {
		t.Fatalf("generic explain: %v", err)
	}
	if !generic {
		t.Error("a plan made without values should be marked generic")
	}
	if !strings.Contains(plan.text, "Scan") {
		t.Errorf("plan does not look like a plan:\n%s", plan.text)
	}
	if !strings.Contains(plan.text, table) {
		t.Errorf("plan does not mention %s:\n%s", table, plan.text)
	}
	// The whole point of force_generic_plan: without it the planner builds
	// a plan around the NULLs standing in for the parameters, which reads
	// as "never matches" and plans one row.
	if strings.Contains(plan.text, "rows=0") {
		t.Errorf("the plan looks like it was built around NULLs:\n%s", plan.text)
	}

	// With values, the plan is the real one.
	withValues, generic, err := explainQuery(ctx, db, "pgx", query, []any{int64(7), "paid"})
	if err != nil {
		t.Fatalf("explain with values: %v", err)
	}
	if generic {
		t.Error("a plan made with the query's own values is not generic")
	}
	if withValues.text == "" {
		t.Error("no plan came back for the value-bound query")
	}

	// A sequential scan over a table this size should be named, with the
	// columns an index would have to cover.
	scanQuery := "SELECT * FROM " + table + " WHERE total = $1"
	scan, _, err := explainQuery(ctx, db, "pgx", scanQuery, nil)
	if err != nil {
		t.Fatalf("explain scan: %v", err)
	}
	joined := strings.Join(scan.hints, " | ")
	if joined != "" && !strings.Contains(joined, "total") && !strings.Contains(joined, "reads all of") {
		t.Errorf("unexpected hint for an unindexed filter: %q (plan: %s)", joined, scan.text)
	}
}

// Explaining must not leave a prepared statement or an open transaction
// behind on a pooled connection.
func TestExplainQuery_PostgresLeavesNoStatementBehind(t *testing.T) {
	db := openPostgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := uniqueTable()
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id bigint)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) })

	query := "SELECT * FROM " + table + " WHERE id = $1"
	for i := 0; i < 3; i++ {
		if _, _, err := explainQuery(ctx, db, "pgx", query, nil); err != nil {
			t.Fatalf("explain %d: %v", i, err)
		}
	}

	var prepared int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_prepared_statements WHERE name LIKE 'pulse_explain%'").Scan(&prepared); err != nil {
		t.Fatalf("count prepared: %v", err)
	}
	if prepared != 0 {
		t.Errorf("%d prepared statements left behind", prepared)
	}

	// The connection still works, which a leaked transaction would break.
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("connection unusable after explaining: %v", err)
	}
}

// The catalog is what turns a hint into advice: a scan on a table that
// already has an index leading with the filtered column was the planner's
// choice, and telling someone to add the index they have is noise.
func TestExplainQuery_PostgresAsksTheCatalogBeforeAdvising(t *testing.T) {
	db := openPostgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	table := uniqueTable()
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+table+" (id bigserial primary key, user_id bigint, status text, note text)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) })
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+table+" (user_id, status, note) SELECT i % 1000, 'paid', 'note-' || i FROM generate_series(1, 50000) i"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An index on user_id, and nothing on status.
	if _, err := db.ExecContext(ctx, "CREATE INDEX ON "+table+" (user_id)"); err != nil {
		t.Fatalf("index: %v", err)
	}
	if _, err := db.ExecContext(ctx, "ANALYZE "+table); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// note is unindexed and nearly unique, so an index would genuinely
	// help: Pulse should name the column.
	selective, _, err := explainQuery(ctx, db, "pgx", "SELECT * FROM "+table+" WHERE note = $1", nil)
	if err != nil {
		t.Fatalf("explain selective: %v", err)
	}
	hints := strings.Join(selective.hints, " | ")
	if !strings.Contains(hints, "consider an index on "+table+"(note)") {
		t.Errorf("an unindexed selective filter should suggest an index, got %q\nplan:\n%s",
			hints, selective.text)
	}
	// The size comes from the catalog, not from the one row the scan
	// returns.
	if !strings.Contains(hints, "50k") {
		t.Errorf("hint should carry the table's real size, got %q", hints)
	}

	// status is unindexed too, but every row has the same value, so an
	// index could not serve it. Saying "add an index" there would be
	// wrong, and the hint says what is actually happening instead.
	wide, _, err := explainQuery(ctx, db, "pgx", "SELECT * FROM "+table+" WHERE status = $1", nil)
	if err != nil {
		t.Fatalf("explain non-selective: %v", err)
	}
	wideHints := strings.Join(wide.hints, " | ")
	if strings.Contains(wideHints, "consider an index") {
		t.Errorf("a filter matching the whole table should not suggest an index: %q", wideHints)
	}
	if !strings.Contains(wideHints, "reads all of") {
		t.Errorf("want a full-scan hint, got %q\nplan:\n%s", wideHints, wide.text)
	}

	// user_id is indexed. A scan that still happens on it is the planner's
	// decision, and suggesting the index that exists would be noise.
	indexed, _, err := explainQuery(ctx, db,
		"pgx", "SELECT * FROM "+table+" WHERE user_id = $1 AND note = $2", nil)
	if err != nil {
		t.Fatalf("explain indexed: %v", err)
	}
	for _, h := range indexed.hints {
		if strings.Contains(h, "consider an index") && strings.Contains(h, "user_id") {
			t.Errorf("user_id is already indexed; suggesting it again is noise: %q\nplan:\n%s",
				h, indexed.text)
		}
	}
}

// The catalog lookups must not disturb the connection or outlive the
// rolled-back transaction they run in.
func TestLookupTableFacts_ReadsRowsAndLeadingIndexColumns(t *testing.T) {
	db := openPostgresTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := uniqueTable()
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+table+" (id bigserial primary key, user_id bigint, status text)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) })
	if _, err := db.ExecContext(ctx,
		"INSERT INTO "+table+" (user_id, status) SELECT i, 'paid' FROM generate_series(1, 1000) i"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX ON "+table+" (user_id, status)"); err != nil {
		t.Fatalf("index: %v", err)
	}
	if _, err := db.ExecContext(ctx, "ANALYZE "+table); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	facts := lookupTableFacts(ctx, tx, table)
	if facts == nil {
		t.Fatal("no facts for a table that exists")
	}
	if facts.rows < 900 || facts.rows > 1100 {
		t.Errorf("row estimate %d, want about 1000", facts.rows)
	}
	// The leading column of each index, and only the leading one: an index
	// on (user_id, status) does not serve a filter on status alone.
	if !facts.indexed["user_id"] || !facts.indexed["id"] {
		t.Errorf("leading index columns missing: %+v", facts.indexed)
	}
	if facts.indexed["status"] {
		t.Error("status is not the leading column of any index")
	}

	// A name that is not there is answered, not an error.
	if facts := lookupTableFacts(ctx, tx, "pulse_no_such_table"); facts != nil && facts.rows > 0 {
		t.Errorf("unknown table reported %d rows", facts.rows)
	}
}
