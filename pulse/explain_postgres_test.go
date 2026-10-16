package pulse

import (
	"context"
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
