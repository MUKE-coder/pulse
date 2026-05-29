// SQLite-backed [Storage] implementation. Persistent counterpart to
// [MemoryStorage].
//
// Design notes:
//
//   - Pure Go via github.com/glebarez/go-sqlite — no CGo, no toolchain
//     gymnastics.
//   - WAL journal mode + busy_timeout=5000ms so concurrent readers don't
//     trip writers under load.
//   - Writes are synchronous (one row per call) in v1.0.0; high-throughput
//     workloads should stay on [MemoryStorage] until batched writes land
//     in a later release. The win is durability + cheap historical queries.
//   - One table per metric type. Errors are upserted by fingerprint so
//     duplicate detections aggregate, mirroring MemoryStorage's behaviour.
package pulse

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	// Side-effect import: registers the "sqlite" driver name with database/sql.
	_ "github.com/glebarez/go-sqlite"
)

// SQLiteStorage persists Pulse metrics to a SQLite database file.
type SQLiteStorage struct {
	db        *sql.DB
	appName   string
	startTime time.Time

	// Pool stats are tiny + frequently overwritten — keep the latest in
	// memory rather than reading SQLite on every dashboard render.
	poolMu sync.RWMutex
	pool   *PoolStats

	// SQLite's single-writer model means concurrent writes serialise
	// automatically, but a Go-side mutex around explicit transactions
	// avoids the SQLITE_BUSY retry loop entirely.
	writeMu sync.Mutex
}

// NewSQLiteStorage opens (or creates) a SQLite database at dsn and prepares
// the schema. Pass ":memory:" for an ephemeral database.
func NewSQLiteStorage(dsn, appName string) (*SQLiteStorage, error) {
	if dsn == "" {
		dsn = "pulse.db"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("pulse/sqlite: open %q: %w", dsn, err)
	}
	// SQLite serialises writes; cap pool size to avoid the BUSY churn.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := initSQLitePragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := initSQLiteSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStorage{
		db:        db,
		appName:   appName,
		startTime: time.Now(),
	}, nil
}

func initSQLitePragmas(db *sql.DB) error {
	stmts := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous  = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("pulse/sqlite: %s: %w", s, err)
		}
	}
	return nil
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS requests (
	timestamp     INTEGER NOT NULL,
	method        TEXT    NOT NULL,
	path          TEXT    NOT NULL,
	status_code   INTEGER NOT NULL,
	latency_ns    INTEGER NOT NULL,
	request_size  INTEGER NOT NULL DEFAULT 0,
	response_size INTEGER NOT NULL DEFAULT 0,
	client_ip     TEXT    NOT NULL DEFAULT '',
	user_agent    TEXT    NOT NULL DEFAULT '',
	error         TEXT    NOT NULL DEFAULT '',
	trace_id      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_requests_ts        ON requests (timestamp);
CREATE INDEX IF NOT EXISTS idx_requests_path      ON requests (method, path, timestamp);

CREATE TABLE IF NOT EXISTS queries (
	timestamp        INTEGER NOT NULL,
	sql_text         TEXT    NOT NULL,
	normalized_sql   TEXT    NOT NULL,
	duration_ns      INTEGER NOT NULL,
	rows_affected    INTEGER NOT NULL DEFAULT 0,
	error            TEXT    NOT NULL DEFAULT '',
	operation        TEXT    NOT NULL DEFAULT '',
	table_name       TEXT    NOT NULL DEFAULT '',
	caller_file      TEXT    NOT NULL DEFAULT '',
	caller_line      INTEGER NOT NULL DEFAULT 0,
	request_trace_id TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_queries_ts        ON queries (timestamp);
CREATE INDEX IF NOT EXISTS idx_queries_duration  ON queries (duration_ns);

CREATE TABLE IF NOT EXISTS runtime_samples (
	timestamp       INTEGER NOT NULL,
	heap_alloc      INTEGER NOT NULL,
	heap_in_use     INTEGER NOT NULL,
	heap_objects    INTEGER NOT NULL,
	stack_in_use    INTEGER NOT NULL,
	total_alloc     INTEGER NOT NULL,
	sys             INTEGER NOT NULL,
	num_goroutine   INTEGER NOT NULL,
	gc_pause_ns     INTEGER NOT NULL,
	num_gc          INTEGER NOT NULL,
	gc_cpu_fraction REAL    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runtime_ts ON runtime_samples (timestamp);

CREATE TABLE IF NOT EXISTS errors (
	fingerprint    TEXT    PRIMARY KEY,
	id             TEXT    NOT NULL,
	method         TEXT    NOT NULL,
	route          TEXT    NOT NULL,
	error_message  TEXT    NOT NULL,
	error_type     TEXT    NOT NULL,
	stack_trace    TEXT    NOT NULL DEFAULT '',
	request_ctx    TEXT    NOT NULL DEFAULT '',
	count          INTEGER NOT NULL DEFAULT 1,
	first_seen     INTEGER NOT NULL,
	last_seen      INTEGER NOT NULL,
	muted          INTEGER NOT NULL DEFAULT 0,
	resolved       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_errors_id        ON errors (id);
CREATE INDEX IF NOT EXISTS idx_errors_last_seen ON errors (last_seen);

CREATE TABLE IF NOT EXISTS health_results (
	timestamp INTEGER NOT NULL,
	name      TEXT    NOT NULL,
	type      TEXT    NOT NULL DEFAULT '',
	status    TEXT    NOT NULL,
	latency_ns INTEGER NOT NULL,
	error     TEXT    NOT NULL DEFAULT '',
	metadata  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_health_name_ts ON health_results (name, timestamp);

CREATE TABLE IF NOT EXISTS alerts (
	id           TEXT    PRIMARY KEY,
	rule_name    TEXT    NOT NULL,
	metric       TEXT    NOT NULL,
	value        REAL    NOT NULL,
	threshold    REAL    NOT NULL,
	operator     TEXT    NOT NULL DEFAULT '',
	severity     TEXT    NOT NULL DEFAULT '',
	state        TEXT    NOT NULL,
	route        TEXT    NOT NULL DEFAULT '',
	message      TEXT    NOT NULL DEFAULT '',
	fired_at     INTEGER NOT NULL,
	resolved_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_alerts_fired_at ON alerts (fired_at);

CREATE TABLE IF NOT EXISTS dependencies (
	timestamp     INTEGER NOT NULL,
	name          TEXT    NOT NULL,
	method        TEXT    NOT NULL DEFAULT '',
	url           TEXT    NOT NULL DEFAULT '',
	status_code   INTEGER NOT NULL DEFAULT 0,
	latency_ns    INTEGER NOT NULL,
	request_size  INTEGER NOT NULL DEFAULT 0,
	response_size INTEGER NOT NULL DEFAULT 0,
	error         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_dependencies_name_ts ON dependencies (name, timestamp);

CREATE TABLE IF NOT EXISTS n1_detections (
	detected_at      INTEGER NOT NULL,
	pattern          TEXT    NOT NULL,
	count            INTEGER NOT NULL,
	total_duration_ns INTEGER NOT NULL,
	avg_duration_ns  INTEGER NOT NULL DEFAULT 0,
	request_trace_id TEXT    NOT NULL DEFAULT '',
	route            TEXT    NOT NULL DEFAULT '',
	suggested_fix    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_n1_detected_at ON n1_detections (detected_at);

CREATE TABLE IF NOT EXISTS test_runs (
	id         TEXT    PRIMARY KEY,
	name       TEXT    NOT NULL,
	type       TEXT    NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL,
	ended_at   INTEGER,
	metadata   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_test_runs_started ON test_runs (started_at);
`

func initSQLiteSchema(db *sql.DB) error {
	for _, stmt := range strings.Split(sqliteSchema, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("pulse/sqlite: schema %q: %w", firstLine(s), err)
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// --- Request metrics ---

func (s *SQLiteStorage) StoreRequest(m RequestMetric) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO requests (timestamp, method, path, status_code, latency_ns, request_size, response_size, client_ip, user_agent, error, trace_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Timestamp.UnixNano(), m.Method, m.Path, m.StatusCode, int64(m.Latency),
		m.RequestSize, m.ResponseSize, m.ClientIP, m.UserAgent, m.Error, m.TraceID,
	)
	return err
}

func (s *SQLiteStorage) GetRequests(f RequestFilter) ([]RequestMetric, error) {
	var (
		clauses []string
		args    []any
	)
	if !f.TimeRange.Start.IsZero() {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, f.TimeRange.Start.UnixNano())
	}
	if !f.TimeRange.End.IsZero() {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, f.TimeRange.End.UnixNano())
	}
	if f.Method != "" {
		clauses = append(clauses, "method = ?")
		args = append(args, f.Method)
	}
	if f.Path != "" {
		clauses = append(clauses, "path = ?")
		args = append(args, f.Path)
	}
	if f.StatusCode != 0 {
		clauses = append(clauses, "status_code = ?")
		args = append(args, f.StatusCode)
	}
	if f.MinLatency > 0 {
		clauses = append(clauses, "latency_ns >= ?")
		args = append(args, int64(f.MinLatency))
	}

	q := `SELECT timestamp, method, path, status_code, latency_ns, request_size, response_size, client_ip, user_agent, error, trace_id FROM requests`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY timestamp ASC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RequestMetric
	for rows.Next() {
		var (
			ts, lat   int64
			m         RequestMetric
			reqSize   int64
			respSize  int64
		)
		if err := rows.Scan(&ts, &m.Method, &m.Path, &m.StatusCode, &lat,
			&reqSize, &respSize, &m.ClientIP, &m.UserAgent, &m.Error, &m.TraceID); err != nil {
			return nil, err
		}
		m.Timestamp = time.Unix(0, ts)
		m.Latency = time.Duration(lat)
		m.RequestSize = reqSize
		m.ResponseSize = respSize
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) GetRouteStats(tr TimeRange) ([]RouteStats, error) {
	reqs, err := s.GetRequests(RequestFilter{TimeRange: tr})
	if err != nil {
		return nil, err
	}
	groups := map[struct{ m, p string }][]RequestMetric{}
	for _, r := range reqs {
		k := struct{ m, p string }{r.Method, r.Path}
		groups[k] = append(groups[k], r)
	}
	dur := tr.End.Sub(tr.Start)
	out := make([]RouteStats, 0, len(groups))
	for k, list := range groups {
		out = append(out, computeRouteStats(k.m, k.p, list, dur))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestCount > out[j].RequestCount })
	return out, nil
}

func (s *SQLiteStorage) GetRouteDetail(method, path string, tr TimeRange) (*RouteDetail, error) {
	reqs, err := s.GetRequests(RequestFilter{TimeRange: tr, Method: method, Path: path})
	if err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, nil
	}
	rs := computeRouteStats(method, path, reqs, tr.End.Sub(tr.Start))
	recent := reqs
	if len(recent) > 50 {
		recent = recent[len(recent)-50:]
	}
	errs, _ := s.GetErrors(ErrorFilter{TimeRange: tr, Route: fmt.Sprintf("%s %s", method, path), Limit: 20})
	pats, _ := s.GetQueryPatterns(tr)
	return &RouteDetail{
		RouteStats:     rs,
		RecentRequests: recent,
		RecentErrors:   errs,
		TopQueries:     pats,
	}, nil
}

// --- Query metrics ---

func (s *SQLiteStorage) StoreQuery(m QueryMetric) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO queries (timestamp, sql_text, normalized_sql, duration_ns, rows_affected, error, operation, table_name, caller_file, caller_line, request_trace_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Timestamp.UnixNano(), m.SQL, m.NormalizedSQL, int64(m.Duration),
		m.RowsAffected, m.Error, m.Operation, m.Table, m.CallerFile, m.CallerLine, m.RequestTraceID,
	)
	return err
}

func (s *SQLiteStorage) GetSlowQueries(threshold time.Duration, limit int) ([]QueryMetric, error) {
	q := `SELECT timestamp, sql_text, normalized_sql, duration_ns, rows_affected, error, operation, table_name, caller_file, caller_line, request_trace_id
	      FROM queries WHERE duration_ns >= ? ORDER BY duration_ns DESC`
	args := []any{int64(threshold)}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryMetric
	for rows.Next() {
		var (
			ts, dur int64
			m       QueryMetric
		)
		if err := rows.Scan(&ts, &m.SQL, &m.NormalizedSQL, &dur, &m.RowsAffected, &m.Error,
			&m.Operation, &m.Table, &m.CallerFile, &m.CallerLine, &m.RequestTraceID); err != nil {
			return nil, err
		}
		m.Timestamp = time.Unix(0, ts)
		m.Duration = time.Duration(dur)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) GetQueryPatterns(tr TimeRange) ([]QueryPattern, error) {
	rows, err := s.db.Query(
		`SELECT normalized_sql, operation, table_name, COUNT(*) AS count,
		        SUM(duration_ns) AS total, MAX(duration_ns) AS max,
		        SUM(CASE WHEN error <> '' THEN 1 ELSE 0 END) AS errs
		 FROM queries
		 WHERE timestamp BETWEEN ? AND ?
		 GROUP BY normalized_sql, operation, table_name
		 ORDER BY total DESC`,
		tr.Start.UnixNano(), tr.End.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryPattern
	for rows.Next() {
		var (
			p          QueryPattern
			total, max int64
		)
		if err := rows.Scan(&p.NormalizedSQL, &p.Operation, &p.Table, &p.Count, &total, &max, &p.ErrorCount); err != nil {
			return nil, err
		}
		p.TotalDuration = time.Duration(total)
		p.MaxDuration = time.Duration(max)
		if p.Count > 0 {
			p.AvgDuration = time.Duration(total / p.Count)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) StoreN1Detection(d N1Detection) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO n1_detections (detected_at, pattern, count, total_duration_ns, avg_duration_ns, request_trace_id, route, suggested_fix)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.DetectedAt.UnixNano(), d.Pattern, d.Count, int64(d.TotalDuration), int64(d.AvgDuration),
		d.RequestTraceID, d.Route, d.SuggestedFix,
	)
	return err
}

func (s *SQLiteStorage) GetN1Detections(tr TimeRange) ([]N1Detection, error) {
	rows, err := s.db.Query(
		`SELECT detected_at, pattern, count, total_duration_ns, avg_duration_ns, request_trace_id, route, suggested_fix
		 FROM n1_detections
		 WHERE detected_at BETWEEN ? AND ?
		 ORDER BY detected_at DESC`,
		tr.Start.UnixNano(), tr.End.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []N1Detection
	for rows.Next() {
		var (
			ts, total, avg int64
			d              N1Detection
		)
		if err := rows.Scan(&ts, &d.Pattern, &d.Count, &total, &avg,
			&d.RequestTraceID, &d.Route, &d.SuggestedFix); err != nil {
			return nil, err
		}
		d.DetectedAt = time.Unix(0, ts)
		d.TotalDuration = time.Duration(total)
		d.AvgDuration = time.Duration(avg)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) UpdatePoolStats(p PoolStats) error {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	s.pool = &p
	return nil
}

func (s *SQLiteStorage) GetConnectionPoolStats() (*PoolStats, error) {
	s.poolMu.RLock()
	defer s.poolMu.RUnlock()
	if s.pool == nil {
		return nil, nil
	}
	cp := *s.pool
	return &cp, nil
}

// --- Runtime metrics ---

func (s *SQLiteStorage) StoreRuntime(m RuntimeMetric) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO runtime_samples (timestamp, heap_alloc, heap_in_use, heap_objects, stack_in_use, total_alloc, sys, num_goroutine, gc_pause_ns, num_gc, gc_cpu_fraction)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Timestamp.UnixNano(), m.HeapAlloc, m.HeapInUse, m.HeapObjects, m.StackInUse,
		m.TotalAlloc, m.Sys, m.NumGoroutine, m.GCPauseNs, m.NumGC, m.GCCPUFraction,
	)
	return err
}

func (s *SQLiteStorage) GetRuntimeHistory(tr TimeRange) ([]RuntimeMetric, error) {
	rows, err := s.db.Query(
		`SELECT timestamp, heap_alloc, heap_in_use, heap_objects, stack_in_use, total_alloc, sys, num_goroutine, gc_pause_ns, num_gc, gc_cpu_fraction
		 FROM runtime_samples
		 WHERE timestamp BETWEEN ? AND ?
		 ORDER BY timestamp ASC`,
		tr.Start.UnixNano(), tr.End.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuntimeMetric
	for rows.Next() {
		var (
			ts int64
			m  RuntimeMetric
		)
		if err := rows.Scan(&ts, &m.HeapAlloc, &m.HeapInUse, &m.HeapObjects, &m.StackInUse,
			&m.TotalAlloc, &m.Sys, &m.NumGoroutine, &m.GCPauseNs, &m.NumGC, &m.GCCPUFraction); err != nil {
			return nil, err
		}
		m.Timestamp = time.Unix(0, ts)
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- Error records ---

func (s *SQLiteStorage) StoreError(e ErrorRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ctxJSON, _ := json.Marshal(e.RequestContext)

	// UPSERT by fingerprint: increment count + bump last_seen on duplicate.
	_, err := s.db.Exec(
		`INSERT INTO errors (fingerprint, id, method, route, error_message, error_type, stack_trace, request_ctx, count, first_seen, last_seen, muted, resolved)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
		 ON CONFLICT (fingerprint) DO UPDATE SET
		     count       = count + 1,
		     last_seen   = excluded.last_seen,
		     stack_trace = CASE WHEN excluded.stack_trace <> '' THEN excluded.stack_trace ELSE errors.stack_trace END,
		     request_ctx = CASE WHEN excluded.request_ctx <> '' THEN excluded.request_ctx ELSE errors.request_ctx END
		`,
		e.Fingerprint, e.ID, e.Method, e.Route, e.ErrorMessage, e.ErrorType,
		e.StackTrace, string(ctxJSON), e.Count, e.FirstSeen.UnixNano(), e.LastSeen.UnixNano(),
	)
	return err
}

func (s *SQLiteStorage) GetErrors(f ErrorFilter) ([]ErrorRecord, error) {
	var (
		clauses []string
		args    []any
	)
	if !f.TimeRange.Start.IsZero() {
		clauses = append(clauses, "last_seen >= ?")
		args = append(args, f.TimeRange.Start.UnixNano())
	}
	if !f.TimeRange.End.IsZero() {
		clauses = append(clauses, "first_seen <= ?")
		args = append(args, f.TimeRange.End.UnixNano())
	}
	if f.ErrorType != "" {
		clauses = append(clauses, "error_type = ?")
		args = append(args, f.ErrorType)
	}
	if f.Route != "" {
		clauses = append(clauses, "route = ?")
		args = append(args, f.Route)
	}
	if f.Muted != nil {
		clauses = append(clauses, "muted = ?")
		args = append(args, boolToInt(*f.Muted))
	}
	if f.Resolved != nil {
		clauses = append(clauses, "resolved = ?")
		args = append(args, boolToInt(*f.Resolved))
	}

	q := `SELECT fingerprint, id, method, route, error_message, error_type, stack_trace, request_ctx, count, first_seen, last_seen, muted, resolved FROM errors`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY last_seen DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrorRecord
	for rows.Next() {
		e, err := scanErrorRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) GetErrorGroups(tr TimeRange) ([]ErrorGroup, error) {
	errs, err := s.GetErrors(ErrorFilter{TimeRange: tr})
	if err != nil {
		return nil, err
	}
	out := make([]ErrorGroup, 0, len(errs))
	for _, e := range errs {
		out = append(out, ErrorGroup{
			Fingerprint:  e.Fingerprint,
			ErrorMessage: e.ErrorMessage,
			ErrorType:    e.ErrorType,
			Route:        e.Route,
			Method:       e.Method,
			Count:        e.Count,
			FirstSeen:    e.FirstSeen,
			LastSeen:     e.LastSeen,
			Muted:        e.Muted,
			Resolved:     e.Resolved,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

func (s *SQLiteStorage) GetErrorByID(id string) (*ErrorRecord, error) {
	row := s.db.QueryRow(
		`SELECT fingerprint, id, method, route, error_message, error_type, stack_trace, request_ctx, count, first_seen, last_seen, muted, resolved FROM errors WHERE id = ?`,
		id,
	)
	e, err := scanErrorRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("error record not found: %s", id)
		}
		return nil, err
	}
	return &e, nil
}

func (s *SQLiteStorage) UpdateError(id string, updates map[string]interface{}) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var (
		sets []string
		args []any
	)
	if v, ok := updates["muted"]; ok {
		if b, ok := v.(bool); ok {
			sets = append(sets, "muted = ?")
			args = append(args, boolToInt(b))
		}
	}
	if v, ok := updates["resolved"]; ok {
		if b, ok := v.(bool); ok {
			sets = append(sets, "resolved = ?")
			args = append(args, boolToInt(b))
		}
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	res, err := s.db.Exec(`UPDATE errors SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("error record not found: %s", id)
	}
	return nil
}

func (s *SQLiteStorage) DeleteError(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM errors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("error record not found: %s", id)
	}
	return nil
}

// rowScanner is satisfied by both *sql.Rows and *sql.Row, so scanErrorRow
// can be reused by GetErrors (cursor) and GetErrorByID (single row).
type rowScanner interface {
	Scan(dest ...any) error
}

func scanErrorRow(r rowScanner) (ErrorRecord, error) {
	var (
		first, last int64
		muted, resv int
		ctxJSON     string
		e           ErrorRecord
	)
	if err := r.Scan(&e.Fingerprint, &e.ID, &e.Method, &e.Route, &e.ErrorMessage, &e.ErrorType,
		&e.StackTrace, &ctxJSON, &e.Count, &first, &last, &muted, &resv); err != nil {
		return e, err
	}
	e.FirstSeen = time.Unix(0, first)
	e.LastSeen = time.Unix(0, last)
	e.Muted = muted != 0
	e.Resolved = resv != 0
	if ctxJSON != "" && ctxJSON != "null" {
		var rc RequestContext
		if err := json.Unmarshal([]byte(ctxJSON), &rc); err == nil {
			e.RequestContext = &rc
		}
	}
	return e, nil
}

// --- Health results ---

func (s *SQLiteStorage) StoreHealthResult(r HealthCheckResult) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	meta, _ := json.Marshal(r.Metadata)
	_, err := s.db.Exec(
		`INSERT INTO health_results (timestamp, name, type, status, latency_ns, error, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Timestamp.UnixNano(), r.Name, r.Type, r.Status, int64(r.Latency), r.Error, string(meta),
	)
	return err
}

func (s *SQLiteStorage) GetHealthHistory(name string, limit int) ([]HealthCheckResult, error) {
	q := `SELECT timestamp, name, type, status, latency_ns, error, metadata
	      FROM health_results WHERE name = ? ORDER BY timestamp DESC`
	args := []any{name}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HealthCheckResult
	for rows.Next() {
		var (
			r       HealthCheckResult
			ts, lat int64
			meta    string
		)
		if err := rows.Scan(&ts, &r.Name, &r.Type, &r.Status, &lat, &r.Error, &meta); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(0, ts)
		r.Latency = time.Duration(lat)
		if meta != "" && meta != "null" {
			_ = json.Unmarshal([]byte(meta), &r.Metadata)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) GetLatestHealthResults() map[string]HealthCheckResult {
	rows, err := s.db.Query(
		`SELECT h.timestamp, h.name, h.type, h.status, h.latency_ns, h.error, h.metadata
		 FROM health_results h
		 INNER JOIN (
		   SELECT name, MAX(timestamp) AS ts FROM health_results GROUP BY name
		 ) latest ON latest.name = h.name AND latest.ts = h.timestamp`,
	)
	if err != nil {
		return map[string]HealthCheckResult{}
	}
	defer rows.Close()
	out := map[string]HealthCheckResult{}
	for rows.Next() {
		var (
			r       HealthCheckResult
			ts, lat int64
			meta    string
		)
		if err := rows.Scan(&ts, &r.Name, &r.Type, &r.Status, &lat, &r.Error, &meta); err != nil {
			continue
		}
		r.Timestamp = time.Unix(0, ts)
		r.Latency = time.Duration(lat)
		if meta != "" && meta != "null" {
			_ = json.Unmarshal([]byte(meta), &r.Metadata)
		}
		out[r.Name] = r
	}
	return out
}

// --- Alerts ---

func (s *SQLiteStorage) StoreAlert(a AlertRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var resolvedAt any
	if a.ResolvedAt != nil {
		resolvedAt = a.ResolvedAt.UnixNano()
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO alerts (id, rule_name, metric, value, threshold, operator, severity, state, route, message, fired_at, resolved_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.RuleName, a.Metric, a.Value, a.Threshold, a.Operator, a.Severity,
		string(a.State), a.Route, a.Message, a.FiredAt.UnixNano(), resolvedAt,
	)
	return err
}

func (s *SQLiteStorage) GetAlerts(f AlertFilter) ([]AlertRecord, error) {
	var (
		clauses []string
		args    []any
	)
	if !f.TimeRange.Start.IsZero() {
		clauses = append(clauses, "fired_at >= ?")
		args = append(args, f.TimeRange.Start.UnixNano())
	}
	if !f.TimeRange.End.IsZero() {
		clauses = append(clauses, "fired_at <= ?")
		args = append(args, f.TimeRange.End.UnixNano())
	}
	if f.State != "" {
		clauses = append(clauses, "state = ?")
		args = append(args, string(f.State))
	}
	if f.Severity != "" {
		clauses = append(clauses, "severity = ?")
		args = append(args, f.Severity)
	}
	q := `SELECT id, rule_name, metric, value, threshold, operator, severity, state, route, message, fired_at, resolved_at FROM alerts`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY fired_at DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRecord
	for rows.Next() {
		var (
			a          AlertRecord
			fired      int64
			resolved   sql.NullInt64
			stateStr   string
		)
		if err := rows.Scan(&a.ID, &a.RuleName, &a.Metric, &a.Value, &a.Threshold, &a.Operator,
			&a.Severity, &stateStr, &a.Route, &a.Message, &fired, &resolved); err != nil {
			return nil, err
		}
		a.State = AlertState(stateStr)
		a.FiredAt = time.Unix(0, fired)
		if resolved.Valid {
			t := time.Unix(0, resolved.Int64)
			a.ResolvedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- Dependencies ---

func (s *SQLiteStorage) StoreDependencyMetric(m DependencyMetric) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO dependencies (timestamp, name, method, url, status_code, latency_ns, request_size, response_size, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Timestamp.UnixNano(), m.Name, m.Method, m.URL, m.StatusCode,
		int64(m.Latency), m.RequestSize, m.ResponseSize, m.Error,
	)
	return err
}

func (s *SQLiteStorage) GetDependencyStats(tr TimeRange) ([]DependencyStats, error) {
	rows, err := s.db.Query(
		`SELECT timestamp, name, status_code, latency_ns, error
		 FROM dependencies WHERE timestamp BETWEEN ? AND ?`,
		tr.Start.UnixNano(), tr.End.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := map[string]*struct {
		latencies []time.Duration
		errCount  int64
		total     int64
		lastTS    time.Time
		lastCode  int
	}{}
	for rows.Next() {
		var (
			ts, lat   int64
			name      string
			code      int
			errStr    string
		)
		if err := rows.Scan(&ts, &name, &code, &lat, &errStr); err != nil {
			return nil, err
		}
		g, ok := groups[name]
		if !ok {
			g = &struct {
				latencies []time.Duration
				errCount  int64
				total     int64
				lastTS    time.Time
				lastCode  int
			}{}
			groups[name] = g
		}
		g.latencies = append(g.latencies, time.Duration(lat))
		g.total++
		if errStr != "" || code >= 500 {
			g.errCount++
		}
		t := time.Unix(0, ts)
		if t.After(g.lastTS) {
			g.lastTS = t
			g.lastCode = code
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	duration := tr.End.Sub(tr.Start)
	out := make([]DependencyStats, 0, len(groups))
	for name, g := range groups {
		sort.Slice(g.latencies, func(i, j int) bool { return g.latencies[i] < g.latencies[j] })
		errRate := 0.0
		if g.total > 0 {
			errRate = float64(g.errCount) / float64(g.total) * 100
		}
		lastStatus := "healthy"
		if g.lastCode >= 500 {
			lastStatus = "unhealthy"
		}
		rpm := 0.0
		if duration.Minutes() > 0 {
			rpm = float64(g.total) / duration.Minutes()
		}
		out = append(out, DependencyStats{
			Name:         name,
			RequestCount: g.total,
			ErrorCount:   g.errCount,
			ErrorRate:    errRate,
			AvgLatency:   ComputeAvg(g.latencies),
			P50Latency:   Percentile(g.latencies, 50),
			P95Latency:   Percentile(g.latencies, 95),
			P99Latency:   Percentile(g.latencies, 99),
			RPM:          rpm,
			Availability: 100 - errRate,
			LastStatus:   lastStatus,
			LastChecked:  g.lastTS,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestCount > out[j].RequestCount })
	return out, nil
}

// --- Test runs ---

func (s *SQLiteStorage) StoreTestRun(r TestRun) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	meta, _ := json.Marshal(r.Metadata)
	var endedAt any
	if !r.EndedAt.IsZero() {
		endedAt = r.EndedAt.UnixNano()
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO test_runs (id, name, type, started_at, ended_at, metadata) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.Type, r.StartedAt.UnixNano(), endedAt, string(meta),
	)
	return err
}

func (s *SQLiteStorage) GetTestRuns(tr TimeRange) ([]TestRun, error) {
	// "Overlap" rules — see MemoryStorage.GetTestRuns.
	rows, err := s.db.Query(
		`SELECT id, name, type, started_at, ended_at, metadata FROM test_runs
		 WHERE COALESCE(ended_at, started_at) >= ?
		   AND started_at <= ?
		 ORDER BY started_at DESC`,
		tr.Start.UnixNano(), tr.End.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TestRun
	for rows.Next() {
		var (
			r        TestRun
			started  int64
			ended    sql.NullInt64
			meta     string
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &started, &ended, &meta); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(0, started)
		if ended.Valid {
			r.EndedAt = time.Unix(0, ended.Int64)
		}
		if meta != "" && meta != "null" {
			_ = json.Unmarshal([]byte(meta), &r.Metadata)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Overview ---

func (s *SQLiteStorage) GetOverview(tr TimeRange) (*Overview, error) {
	reqs, err := s.GetRequests(RequestFilter{TimeRange: tr})
	if err != nil {
		return nil, err
	}
	var (
		latencies []time.Duration
		errCount  int64
	)
	for _, r := range reqs {
		latencies = append(latencies, r.Latency)
		if r.StatusCode >= 400 {
			errCount++
		}
	}
	total := int64(len(reqs))
	errRate := 0.0
	if total > 0 {
		errRate = float64(errCount) / float64(total) * 100
	}
	duration := tr.End.Sub(tr.Start)
	rpm := 0.0
	if duration.Minutes() > 0 {
		rpm = float64(total) / duration.Minutes()
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	var goroutines int
	var heapMB float64
	if hist, _ := s.GetRuntimeHistory(Last5m()); len(hist) > 0 {
		latest := hist[len(hist)-1]
		goroutines = latest.NumGoroutine
		heapMB = float64(latest.HeapAlloc) / (1024 * 1024)
	}

	topRoutes, _ := s.GetRouteStats(tr)
	if len(topRoutes) > 10 {
		topRoutes = topRoutes[:10]
	}
	recent, _ := s.GetErrors(ErrorFilter{TimeRange: tr, Limit: 5})

	var activeAlerts int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE state = 'firing'`).Scan(&activeAlerts)

	return &Overview{
		AppName:          s.appName,
		Uptime:           formatDuration(time.Since(s.startTime)),
		TotalRequests:    total,
		TotalErrors:      errCount,
		ErrorRate:        errRate,
		AvgLatency:       ComputeAvg(latencies),
		P95Latency:       Percentile(latencies, 95),
		RPM:              rpm,
		ActiveGoroutines: goroutines,
		HeapAllocMB:      heapMB,
		ActiveAlerts:     activeAlerts,
		TopRoutes:        topRoutes,
		RecentErrors:     recent,
		Timestamp:        time.Now(),
	}, nil
}

// --- Maintenance ---

func (s *SQLiteStorage) Cleanup(retention time.Duration) error {
	cutoff := time.Now().Add(-retention).UnixNano()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	deletes := []struct {
		table  string
		column string
	}{
		{"requests", "timestamp"},
		{"queries", "timestamp"},
		{"runtime_samples", "timestamp"},
		{"dependencies", "timestamp"},
		{"health_results", "timestamp"},
		{"n1_detections", "detected_at"},
		{"alerts", "fired_at"},
		{"errors", "last_seen"},
	}
	for _, d := range deletes {
		if _, err := s.db.Exec(
			fmt.Sprintf(`DELETE FROM %s WHERE %s < ?`, d.table, d.column), cutoff,
		); err != nil {
			return fmt.Errorf("pulse/sqlite cleanup %s: %w", d.table, err)
		}
	}
	// Test runs use a CASE expression because ended_at may be NULL for
	// still-running test runs.
	if _, err := s.db.Exec(
		`DELETE FROM test_runs WHERE COALESCE(ended_at, started_at) < ?`, cutoff,
	); err != nil {
		return fmt.Errorf("pulse/sqlite cleanup test_runs: %w", err)
	}
	return nil
}

func (s *SQLiteStorage) Reset() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tables := []string{"requests", "queries", "runtime_samples", "errors",
		"health_results", "alerts", "dependencies", "n1_detections", "test_runs"}
	for _, t := range tables {
		if _, err := s.db.Exec(`DELETE FROM ` + t); err != nil {
			return fmt.Errorf("pulse/sqlite reset %s: %w", t, err)
		}
	}

	s.poolMu.Lock()
	s.pool = nil
	s.poolMu.Unlock()

	return nil
}

func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// boolToInt is a tiny helper used wherever a Go bool needs to be stored in
// a SQLite INTEGER column (SQLite has no native BOOLEAN type).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ensure SQLiteStorage satisfies the Storage interface at compile time.
var _ Storage = (*SQLiteStorage)(nil)
