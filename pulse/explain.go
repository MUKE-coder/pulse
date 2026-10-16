package pulse

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Query plans. A slow query says what took long; its plan says why — a
// sequential scan, a sort spilling to disk, an index that wasn't used.
// When Database.ExplainSlowQueries is on, Pulse asks the database for the
// plan of a slow SELECT: at most one per query pattern per
// Database.ExplainInterval, on a background goroutine, so the request that
// triggered it is long gone and the request path is untouched.

const (
	defaultExplainInterval = time.Hour
	explainTimeout         = 2 * time.Second
	explainQueueSize       = 64
	maxPlanBytes           = 8 << 10
	maxPlanHints           = 4
)

// QueryPlan is a database's own explanation of how it runs a query.
type QueryPlan struct {
	NormalizedSQL string        `json:"normalized_sql"`
	Operation     string        `json:"operation,omitempty"`
	Table         string        `json:"table,omitempty"`
	Duration      time.Duration `json:"duration"` // the slow run that prompted it
	Plan          string        `json:"plan"`     // what the database answered
	Hints         []string      `json:"hints,omitempty"`
	CapturedAt    time.Time     `json:"captured_at"`
}

// planStore is implemented by storage backends that keep query plans.
// Unexported for the same reason as rollupStore.
type planStore interface {
	storeQueryPlan(p QueryPlan) error
	queryPlans(limit int) ([]QueryPlan, error)
}

// explainer asks the database for the plans of slow queries.
type explainer struct {
	pulse *Pulse
	db    *gorm.DB
	queue chan explainJob

	mu   sync.Mutex
	seen map[string]time.Time // normalized SQL → when it was last explained
}

// explainJob is a slow query waiting for its plan: the statement as the
// driver will run it, and the values it was run with.
type explainJob struct {
	metric QueryMetric
	sql    string
	vars   []any
}

func newExplainer(p *Pulse, db *gorm.DB) *explainer {
	return &explainer{pulse: p, db: db, queue: make(chan explainJob, explainQueueSize),
		seen: make(map[string]time.Time)}
}

// explainInterval is the minimum time between plans for one pattern.
func (e *explainer) interval() time.Duration {
	if d := e.pulse.config.Database.ExplainInterval; d > 0 {
		return d
	}
	return defaultExplainInterval
}

// consider queues a slow query for explaining, unless its pattern was
// explained recently or the queue is full. It never blocks the caller.
func (e *explainer) consider(job explainJob) {
	m := job.metric
	if m.Error != "" || !strings.EqualFold(m.Operation, "SELECT") || m.NormalizedSQL == "" {
		return
	}
	now := e.pulse.now()
	e.mu.Lock()
	if last, ok := e.seen[m.NormalizedSQL]; ok && now.Sub(last) < e.interval() {
		e.mu.Unlock()
		return
	}
	e.seen[m.NormalizedSQL] = now
	e.mu.Unlock()

	select {
	case e.queue <- job:
	default: // busy explaining; this pattern will come round again
	}
}

// startExplainer drains the queue until the Pulse context ends.
func startExplainer(p *Pulse) {
	e := p.explainer
	if e == nil {
		return
	}
	p.startBackground("query-explainer", func(ctx context.Context) {
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-e.queue:
				e.explain(ctx, job)
			}
		}
	})
}

// explain asks the database how it runs the query, and keeps the answer.
func (e *explainer) explain(ctx context.Context, job explainJob) {
	dialect := e.db.Dialector.Name()
	stmt, ok := explainStatement(dialect, job.sql)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, explainTimeout)
	defer cancel()

	// Straight down the connection pool rather than through GORM: the
	// statement already carries the placeholders the driver expects, and
	// GORM would rewrite them — and would route Pulse's own EXPLAIN back
	// through the callbacks that captured the query in the first place.
	pool, err := e.db.DB()
	if err != nil {
		e.pulse.internalError("database: explain", err)
		return
	}
	rows, err := pool.QueryContext(ctx, stmt, job.vars...)
	if err != nil {
		e.pulse.internalError("database: explain", err)
		return
	}
	plan, err := readPlan(rows)
	_ = rows.Close()
	if err != nil {
		e.pulse.internalError("database: explain", err)
		return
	}
	if plan == "" {
		return
	}
	// A plan can echo the values a query filtered on.
	plan = e.pulse.redactor.scrubValue(plan)

	e.pulse.internalError("storage: query plans", e.store(QueryPlan{
		NormalizedSQL: job.metric.NormalizedSQL,
		Operation:     job.metric.Operation,
		Table:         job.metric.Table,
		Duration:      job.metric.Duration,
		Plan:          clipString(plan, maxPlanBytes),
		Hints:         planHints(dialect, plan),
		CapturedAt:    e.pulse.now(),
	}))
}

func (e *explainer) store(p QueryPlan) error {
	ps, ok := e.pulse.storage.(planStore)
	if !ok {
		return nil
	}
	return ps.storeQueryPlan(p)
}

// explainStatement is how each database is asked for a plan. Only SELECTs
// are explained: explaining a write runs it on some databases.
func explainStatement(dialect, query string) (string, bool) {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
		return "", false
	}
	switch dialect {
	case "postgres", "pgx", "mysql":
		return "EXPLAIN " + query, true
	case "sqlite", "sqlite3":
		return "EXPLAIN QUERY PLAN " + query, true
	}
	return "", false // a database Pulse doesn't know how to ask
}

// readPlan joins the rows a database answers with into one block of text.
// SQLite's EXPLAIN QUERY PLAN prefixes each row with node bookkeeping
// (id, parent, notused) and puts the readable part in "detail", so when a
// column of that name is there, it is the only one worth keeping.
func readPlan(rows *sql.Rows) (string, error) {
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	detail := -1
	for i, col := range cols {
		if strings.EqualFold(col, "detail") {
			detail = i
		}
	}
	var out strings.Builder
	for rows.Next() && out.Len() < maxPlanBytes {
		values := make([]sql.NullString, len(cols))
		cells := make([]any, len(cols))
		for i := range values {
			cells[i] = &values[i]
		}
		if err := rows.Scan(cells...); err != nil {
			return "", err
		}
		var parts []string
		for i, v := range values {
			if detail >= 0 && i != detail {
				continue
			}
			if v.Valid && strings.TrimSpace(v.String) != "" {
				parts = append(parts, strings.TrimSpace(v.String))
			}
		}
		if len(parts) > 0 {
			out.WriteString(strings.Join(parts, "  "))
			out.WriteByte('\n')
		}
	}
	return strings.TrimRight(out.String(), "\n"), rows.Err()
}

// planHints reads the handful of things that most often explain a slow
// query. It stays shallow on purpose: the plan itself is kept alongside, and
// a wrong guess is worse than none.
func planHints(dialect, plan string) []string {
	var hints []string
	add := func(h string) {
		for _, seen := range hints {
			if seen == h {
				return
			}
		}
		if len(hints) < maxPlanHints {
			hints = append(hints, h)
		}
	}

	for _, line := range strings.Split(plan, "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "->"))
		upper := strings.ToUpper(trimmed)
		switch dialect {
		case "sqlite", "sqlite3":
			// SCAN reads the whole table; SEARCH ... USING INDEX doesn't.
			if strings.HasPrefix(upper, "SCAN ") && !strings.Contains(upper, "USING INDEX") &&
				!strings.Contains(upper, "USING COVERING INDEX") {
				add("reads the whole table: " + trimmed)
			}
			if strings.Contains(upper, "USE TEMP B-TREE") {
				add("sorts into a temporary B-tree: add an index matching the ORDER BY")
			}
		case "postgres", "pgx":
			if strings.HasPrefix(trimmed, "Seq Scan on ") {
				add("sequential scan on " + strings.Fields(strings.TrimPrefix(trimmed, "Seq Scan on "))[0])
			}
			if strings.Contains(upper, "EXTERNAL MERGE") || strings.Contains(upper, "DISK:") {
				add("the sort spilled to disk: raise work_mem, or index the ORDER BY")
			}
		case "mysql":
			if strings.Contains(upper, "  ALL  ") || strings.HasSuffix(upper, "  ALL") {
				add("full table scan: no usable index for this filter")
			}
			if strings.Contains(upper, "USING FILESORT") {
				add("sorts rows after reading them: index the ORDER BY")
			}
			if strings.Contains(upper, "USING TEMPORARY") {
				add("builds a temporary table for this query")
			}
		}
	}
	return hints
}

// --- MemoryStorage ---

// maxStoredPlans caps the plans the Memory backend keeps, one per pattern.
const maxStoredPlans = 200

func (s *MemoryStorage) storeQueryPlan(p QueryPlan) error {
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	if s.plans == nil {
		s.plans = make(map[string]QueryPlan)
	}
	if _, replacing := s.plans[p.NormalizedSQL]; !replacing && len(s.plans) >= maxStoredPlans {
		oldest, at := "", time.Time{}
		for key, plan := range s.plans {
			if at.IsZero() || plan.CapturedAt.Before(at) {
				oldest, at = key, plan.CapturedAt
			}
		}
		delete(s.plans, oldest)
	}
	s.plans[p.NormalizedSQL] = p
	return nil
}

func (s *MemoryStorage) queryPlans(limit int) ([]QueryPlan, error) {
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	out := make([]QueryPlan, 0, len(s.plans))
	for _, p := range s.plans {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- sqlStore ---

// maxPlanRows caps the plans the SQL backends keep: the oldest capture goes
// when a new pattern arrives past the cap, the same as the Memory backend.
const maxPlanRows = 200

func (s *sqlStore) storeQueryPlan(p QueryPlan) error {
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.exec(`INSERT INTO query_plans
		(normalized_sql, operation, table_name, duration_ns, plan, hints, captured_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (normalized_sql) DO UPDATE SET operation = excluded.operation,
		    table_name = excluded.table_name, duration_ns = excluded.duration_ns,
		    plan = excluded.plan, hints = excluded.hints, captured_at = excluded.captured_at`,
		p.NormalizedSQL, p.Operation, p.Table, int64(p.Duration), p.Plan,
		strings.Join(p.Hints, "\n"), p.CapturedAt.UnixNano()); err != nil {
		return err
	}
	// Keep the newest captures, so a long-running process doesn't collect a
	// plan for every pattern it has ever run. Portable between the two
	// dialects: no LIMIT -1, no DELETE ... LIMIT.
	_, err := s.exec(`DELETE FROM query_plans WHERE captured_at < (
		SELECT MIN(captured_at) FROM (
			SELECT captured_at FROM query_plans ORDER BY captured_at DESC LIMIT ?
		) AS keep)`, maxPlanRows)
	return err
}

func (s *sqlStore) queryPlans(limit int) ([]QueryPlan, error) {
	s.sync()
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.query(`SELECT normalized_sql, operation, table_name, duration_ns, plan, hints,
		captured_at FROM query_plans ORDER BY captured_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryPlan
	for rows.Next() {
		var (
			plan     QueryPlan
			duration int64
			hints    string
			captured int64
		)
		if err := rows.Scan(&plan.NormalizedSQL, &plan.Operation, &plan.Table, &duration,
			&plan.Plan, &hints, &captured); err != nil {
			return nil, err
		}
		plan.Duration = time.Duration(duration)
		plan.CapturedAt = time.Unix(0, captured)
		if hints != "" {
			plan.Hints = strings.Split(hints, "\n")
		}
		out = append(out, plan)
	}
	return out, rows.Err()
}

// plansHandler serves GET /pulse/api/database/plans: the most recently
// captured query plans, newest first.
func plansHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		plans := []QueryPlan{}
		ps, supported := p.storage.(planStore)
		if supported {
			if got, err := ps.queryPlans(queryInt(c, "limit", 20)); err == nil && got != nil {
				plans = got
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"plans":     plans,
			"capturing": boolValue(p.config.Database.ExplainSlowQueries),
			"supported": supported,
		})
	}
}

var (
	_ planStore = (*MemoryStorage)(nil)
	_ planStore = (*SQLiteStorage)(nil)
	_ planStore = (*PostgresStorage)(nil)
)

// placeholderCount counts the bind parameters in a statement: ? for SQLite
// and MySQL, $1… for PostgreSQL. It answers one question — can Pulse run
// this statement with no values? — so it counts a $ inside a string literal
// too: waiting for the real values is the safe way to be wrong.
func placeholderCount(query string) int {
	n := strings.Count(query, "?")
	for i := 0; i+1 < len(query); i++ {
		if query[i] == '$' && query[i+1] >= '1' && query[i+1] <= '9' {
			n++
		}
	}
	return n
}

// explainNowHandler serves POST /pulse/api/database/explain: explain the
// slowest SELECT Pulse has recorded, now, instead of waiting for it to come
// round again. It is what the Database page's "Run EXPLAIN" button calls.
func explainNowHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if p.explainer == nil {
			c.JSON(http.StatusConflict, gin.H{
				"error": "query plans are off",
				"hint":  "mount Pulse with pulse.WithExplainSlowQueries(0) to let it ask the database for plans",
			})
			return
		}

		queries, err := p.storage.GetSlowQueries(0, 200)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var pick *QueryMetric
		for i := range queries {
			q := queries[i]
			if q.Error != "" || !strings.EqualFold(q.Operation, "SELECT") || q.SQL == "" {
				continue
			}
			if pick == nil || q.Duration > pick.Duration {
				pick = &queries[i]
			}
		}
		if pick == nil {
			c.JSON(http.StatusOK, gin.H{
				"explained": false,
				"message":   "no SELECT has been recorded yet, so there is nothing to explain",
			})
			return
		}

		// An explicit ask overrides the per-pattern interval.
		e := p.explainer
		e.mu.Lock()
		delete(e.seen, pick.NormalizedSQL)
		e.mu.Unlock()

		// Recorded queries keep the statement but not the values it ran
		// with — those are the application's data, and Pulse does not store
		// them. A statement with no placeholders can be explained right
		// here; one with placeholders has to wait for its next run, when
		// the GORM callback has the values to bind.
		if placeholderCount(pick.SQL) > 0 {
			c.JSON(http.StatusOK, gin.H{
				"explained": false,
				"armed":     true,
				"query":     pick.NormalizedSQL,
				"message": "This query takes parameters, and Pulse does not keep the values it ran with. " +
					"Its plan will be captured the next time it runs.",
			})
			return
		}

		e.explain(c.Request.Context(), explainJob{metric: *pick, sql: pick.SQL})
		c.JSON(http.StatusOK, gin.H{
			"explained": true,
			"query":     pick.NormalizedSQL,
			"duration":  pick.Duration,
		})
	}
}
