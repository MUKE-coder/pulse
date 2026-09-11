package pulse

import (
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// SQL backends. SQLiteStorage and PostgresStorage share one implementation,
// sqlStore, written in SQL both engines accept. A sqlDialect adapts each
// statement on its way to PostgreSQL: "?" placeholders become $1, $2, …,
// Pulse's tables are qualified with their schema, and string arguments are
// made acceptable to PostgreSQL's text type.

// sqlTables are Pulse's tables; see sqliteSchema.
var sqlTables = []string{
	"requests", "queries", "runtime_samples", "errors", "health_results", "alerts",
	"dependencies", "n1_detections", "test_runs", "request_rollups", "latency_rollups",
	"slo_rollups", "pulse_meta", "lifecycle_events", "logs",
}

// tableRef matches a Pulse table name where a statement names a table.
var tableRef = regexp.MustCompile(`(?i)\b(FROM|INTO|UPDATE|JOIN|TABLE|EXISTS|ON)(\s+)(` +
	strings.Join(sqlTables, "|") + `)\b`)

type sqlDialect struct {
	postgres bool
	schema   string // PostgreSQL: the schema holding Pulse's tables

	cache  sync.Map     // statement → rewritten statement
	cached atomic.Int32 // entries in cache
}

// maxDialectCache bounds the statement cache; statements that embed values
// (LIMIT n) would otherwise grow it without end.
const maxDialectCache = 512

var sqliteDialect = &sqlDialect{}

// sql adapts a statement to the dialect.
func (d *sqlDialect) sql(q string) string {
	if !d.postgres {
		return q
	}
	if v, ok := d.cache.Load(q); ok {
		return v.(string)
	}
	out := rebindPostgres(tableRef.ReplaceAllString(q, "${1}${2}"+d.schema+".${3}"))
	if d.cached.Load() < maxDialectCache {
		if _, loaded := d.cache.LoadOrStore(q, out); !loaded {
			d.cached.Add(1)
		}
	}
	return out
}

// rebindPostgres numbers "?" placeholders as $1, $2, …. Pulse's statements
// have no "?" inside string literals.
func rebindPostgres(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] != '?' {
			b.WriteByte(q[i])
			continue
		}
		n++
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// args adapts argument values to the dialect, in place. PostgreSQL rejects
// text that isn't valid UTF-8 or that contains NUL bytes; both can arrive in
// request paths, headers and log lines.
func (d *sqlDialect) args(args []any) []any {
	if !d.postgres {
		return args
	}
	for i, a := range args {
		if s, ok := a.(string); ok && (!utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0) {
			args[i] = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
		}
	}
	return args
}

func (s *sqlStore) exec(q string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.d.sql(q), s.d.args(args)...)
}

func (s *sqlStore) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.d.sql(q), s.d.args(args)...)
}

func (s *sqlStore) queryRow(q string, args ...any) *sql.Row {
	return s.db.QueryRow(s.d.sql(q), s.d.args(args)...)
}

func (s *sqlStore) txExec(tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return tx.Exec(s.d.sql(q), s.d.args(args)...)
}
