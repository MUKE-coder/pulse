package pulse

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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
	// Generic is set when the plan was made without the values the query
	// ran with. Pulse keeps the statement but never the values, so a plan
	// asked for after the fact is generic; one captured as the query ran
	// is not. The planner can choose differently with real values, and the
	// dashboard says which kind it is showing.
	Generic    bool      `json:"generic,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
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
//
// Pulse keeps the statement but never the values it ran with — those are
// the application's data. When a live run hands its values over the plan is
// the real one; otherwise the database plans the statement generically,
// with no values at all, which every backend can do: PostgreSQL through a
// prepared statement under force_generic_plan, SQLite and MySQL by binding
// NULL for each placeholder.
func (e *explainer) explain(ctx context.Context, job explainJob) {
	dialect := e.db.Dialector.Name()
	if _, ok := explainStatement(dialect, job.sql); !ok {
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

	plan, generic, err := explainQuery(ctx, pool, dialect, job.sql, job.vars)
	if err != nil {
		e.pulse.internalError("database: explain", err)
		return
	}
	if plan.text == "" {
		return
	}
	// A plan can echo the values a query filtered on.
	text := e.pulse.redactor.scrubValue(plan.text)

	e.pulse.internalError("storage: query plans", e.store(QueryPlan{
		NormalizedSQL: job.metric.NormalizedSQL,
		Operation:     job.metric.Operation,
		Table:         job.metric.Table,
		Duration:      job.metric.Duration,
		Plan:          clipString(text, maxPlanBytes),
		Hints:         plan.hints,
		Generic:       generic,
		CapturedAt:    e.pulse.now(),
	}))
}

// planResult is a plan rendered for reading, plus what Pulse read off it.
type planResult struct {
	text  string
	hints []string
}

// explainQuery plans one statement without running it. Everything happens
// inside a transaction that is rolled back: EXPLAIN does not execute the
// query, but a statement can still call a volatile function, and a
// read-only transaction is a boundary rather than a promise.
func explainQuery(ctx context.Context, pool *sql.DB, dialect, query string, vars []any) (planResult, bool, error) {
	params := placeholderCount(query)
	generic := params > 0 && len(vars) != params

	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		// Not every driver takes a read-only transaction; a plain one
		// still rolls back.
		if tx, err = pool.BeginTx(ctx, nil); err != nil {
			return planResult{}, generic, err
		}
	}
	defer tx.Rollback() //nolint:errcheck // rolled back on every path

	switch dialect {
	case "postgres", "pgx":
		setExplainLimits(ctx, tx)
		plan, err := explainPostgres(ctx, tx, query, params, vars, generic)
		return plan, generic, err
	case "sqlite", "sqlite3", "mysql":
		stmt, ok := explainStatement(dialect, query)
		if !ok {
			return planResult{}, generic, nil
		}
		args := vars
		if generic {
			// The drivers refuse a statement with unbound placeholders, so
			// a plan asked for without values binds NULL for each one.
			args = make([]any, params)
		}
		text, err := queryPlanText(ctx, tx, stmt, args)
		if err != nil {
			return planResult{}, generic, err
		}
		return planResult{text: text, hints: planHints(dialect, text)}, generic, nil
	}
	return planResult{}, generic, nil // a database Pulse doesn't know how to ask
}

// setExplainLimits keeps a plan from stalling: EXPLAIN still takes the
// locks the statement needs, and the planner may read statistics.
func setExplainLimits(ctx context.Context, tx *sql.Tx) {
	_, _ = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '5s'")
	_, _ = tx.ExecContext(ctx, "SET LOCAL lock_timeout = '2s'")
}

// explainPostgres plans a statement as JSON, which carries what the text
// output only hints at: node types, row estimates, filters and sort
// methods. A parameterised statement is planned generically through a
// prepared statement, so Pulse never needs the values it ran with.
//
// PostgreSQL 16 can do the same in one statement with
// EXPLAIN (GENERIC_PLAN), but that form needs the simple query protocol to
// reach the server intact, and the prepared-statement route works the same
// way on every version Pulse supports.
func explainPostgres(ctx context.Context, tx *sql.Tx, query string, params int, vars []any, generic bool) (planResult, error) {
	const format = "EXPLAIN (FORMAT JSON, VERBOSE) "

	var (
		raw string
		err error
	)
	switch {
	case params == 0:
		raw, err = queryPlanText(ctx, tx, format+query, nil)
	case !generic:
		raw, err = queryPlanText(ctx, tx, format+query, vars)
	default:
		raw, err = explainPreparedPostgres(ctx, tx, format, query, params)
	}
	if err != nil {
		return planResult{}, err
	}

	node, perr := parsePostgresPlan(raw)
	if perr != nil || node == nil {
		// Unparseable JSON is still worth showing as text.
		return planResult{text: raw, hints: planHints("postgres", raw)}, nil
	}

	// The plan says a scan happened; the catalog says whether that is news.
	// A table with an index already leading on the filtered column was
	// scanned on purpose, and saying "add an index" there would be noise.
	scans, hints := walkPlan(node)
	hints = append(hints, catalogHints(ctx, tx, scans)...)
	if len(hints) > maxPlanHints {
		hints = hints[:maxPlanHints]
	}
	return planResult{text: renderPlanTree(node), hints: hints}, nil
}

// explainPreparedPostgres plans a parameterised statement with no values.
// force_generic_plan stops the planner building a plan around the NULLs
// that stand in for them, which would be a plan for a query nobody runs.
func explainPreparedPostgres(ctx context.Context, tx *sql.Tx, format, query string, params int) (string, error) {
	// A name per statement, so two explains in one process can't collide.
	name := "pulse_explain_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	// Older than PostgreSQL 12 has no plan_cache_mode; the plan then comes
	// out shaped around the NULLs, so the failure is worth passing on.
	if _, err := tx.ExecContext(ctx, "SET LOCAL plan_cache_mode = force_generic_plan"); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "PREPARE "+name+" AS "+query); err != nil {
		return "", err
	}
	defer tx.ExecContext(ctx, "DEALLOCATE "+name) //nolint:errcheck // inside a rolled-back tx

	args := strings.TrimSuffix(strings.Repeat("NULL, ", params), ", ")
	return queryPlanText(ctx, tx, format+"EXECUTE "+name+"("+args+")", nil)
}

// queryPlanText runs a plan-producing statement and joins what came back.
func queryPlanText(ctx context.Context, tx *sql.Tx, stmt string, args []any) (string, error) {
	rows, err := tx.QueryContext(ctx, stmt, args...)
	if err != nil {
		return "", err
	}
	text, err := readPlan(rows)
	_ = rows.Close()
	return text, err
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

// --- PostgreSQL JSON plans ---

// planNode is the part of a PostgreSQL JSON plan Pulse reads. The text
// output only hints at these; the JSON carries them outright, which is what
// lets a hint say "sequential scan on orders, ~1.2M rows, filtering on
// user_id" rather than repeating a line back.
type planNode struct {
	NodeType      string     `json:"Node Type"`
	Relation      string     `json:"Relation Name"`
	Schema        string     `json:"Schema"`
	Alias         string     `json:"Alias"`
	IndexName     string     `json:"Index Name"`
	Filter        string     `json:"Filter"`
	IndexCond     string     `json:"Index Cond"`
	JoinType      string     `json:"Join Type"`
	SortMethod    string     `json:"Sort Method"`
	SortSpaceType string     `json:"Sort Space Type"`
	SortSpaceKB   int64      `json:"Sort Space Used"`
	PlanRows      float64    `json:"Plan Rows"`
	TotalCost     float64    `json:"Total Cost"`
	ParallelAware bool       `json:"Parallel Aware"`
	WorkersPlan   int        `json:"Workers Planned"`
	Plans         []planNode `json:"Plans"`
}

func parsePostgresPlan(raw string) (*planNode, error) {
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &plans); err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return nil, nil
	}
	return &plans[0].Plan, nil
}

// renderPlanTree prints the plan the way psql does, which is what someone
// reading the page expects to see.
func renderPlanTree(root *planNode) string {
	var b strings.Builder
	var walk func(n *planNode, depth int)
	walk = func(n *planNode, depth int) {
		if n == nil {
			return
		}
		b.WriteString(strings.Repeat("  ", depth))
		if depth > 0 {
			b.WriteString("-> ")
		}
		b.WriteString(n.NodeType)
		if n.Relation != "" {
			b.WriteString(" on " + n.Relation)
			if n.Alias != "" && n.Alias != n.Relation {
				b.WriteString(" " + n.Alias)
			}
		}
		if n.IndexName != "" {
			b.WriteString(" using " + n.IndexName)
		}
		fmt.Fprintf(&b, "  (rows=%s cost=%.2f)", formatRowEstimate(n.PlanRows), n.TotalCost)
		if n.Filter != "" {
			b.WriteString("\n" + strings.Repeat("  ", depth+1) + "Filter: " + n.Filter)
		}
		if n.IndexCond != "" {
			b.WriteString("\n" + strings.Repeat("  ", depth+1) + "Index Cond: " + n.IndexCond)
		}
		if n.SortMethod != "" {
			fmt.Fprintf(&b, "\n%sSort Method: %s", strings.Repeat("  ", depth+1), n.SortMethod)
			if n.SortSpaceType != "" {
				fmt.Fprintf(&b, "  %s: %dkB", n.SortSpaceType, n.SortSpaceKB)
			}
		}
		b.WriteString("\n")
		for i := range n.Plans {
			walk(&n.Plans[i], depth+1)
		}
	}
	walk(root, 0)
	return strings.TrimRight(b.String(), "\n")
}

func formatRowEstimate(rows float64) string {
	switch {
	case rows >= 1e6:
		return fmt.Sprintf("%.1fM", rows/1e6)
	case rows >= 1e3:
		return fmt.Sprintf("%.0fk", rows/1e3)
	default:
		return strconv.FormatFloat(rows, 'f', -1, 64)
	}
}

// A sequential scan is worth mentioning when the table it reads is big.
// Not when the scan *returns* a lot of rows: the worst case of all is a
// selective filter with no index, which reads a million rows to return
// one, and gating on the output would hide exactly that.
const (
	bigScanRows = 10_000
	// bigScanCost stands in for the table's size when the catalog has not
	// been asked. A sequential scan costs roughly one unit per page plus a
	// hundredth per row, so this is a table of some tens of thousands.
	bigScanCost = 500
)

// planScan is a sequential scan the walk thinks is worth mentioning.
type planScan struct {
	relation string
	columns  []string // the columns its filter tests, which an index would cover
	rows     float64  // rows the scan is expected to return, across parallel workers
	cost     float64  // the planner's cost for the scan, a proxy for the table's size
	filtered bool     // the scan has a filter, so it reads more than it returns
}

// walkPlan reads the handful of things that most often explain a slow query
// off the plan tree. Walking the tree rather than the text means a scan
// buried under a join or a sort is still found, a scan a LIMIT stops early
// is not reported, and a parallel scan's rows are counted the way the
// planner counts them.
func walkPlan(root *planNode) ([]planScan, []string) {
	var (
		scans []planScan
		hints []string
	)
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

	var walk func(n *planNode, limited bool, workers int)
	walk = func(n *planNode, limited bool, workers int) {
		if n == nil {
			return
		}
		switch n.NodeType {
		case "Seq Scan":
			rows := n.PlanRows
			if n.ParallelAware {
				rows *= parallelRowDivisor(workers)
			}
			// A LIMIT above the scan can stop it before it reads the table.
			if !limited {
				scans = append(scans, planScan{
					relation: n.Relation,
					columns:  filterColumns(n.Filter),
					rows:     rows,
					cost:     n.TotalCost,
					filtered: n.Filter != "",
				})
			}
		case "Limit":
			limited = true
		case "Gather", "Gather Merge":
			workers = n.WorkersPlan
		case "Result", "Append", "Subquery Scan":
			// These stream rows, so a LIMIT above them still stops the
			// scan below early.
		default:
			limited = false
		}

		if strings.Contains(strings.ToLower(n.SortMethod), "external") ||
			strings.EqualFold(n.SortSpaceType, "Disk") {
			add("the sort spilled to disk: raise work_mem, or index the ORDER BY")
		}
		if n.NodeType == "Nested Loop" && n.PlanRows >= bigScanRows {
			add(fmt.Sprintf("nested loop over ~%s rows: check the join condition is indexed",
				formatRowEstimate(n.PlanRows)))
		}

		for i := range n.Plans {
			walk(&n.Plans[i], limited, workers)
		}
	}
	walk(root, false, 0)
	return scans, hints
}

// postgresHints is what a plan says on its own, without asking the database
// anything else about the tables involved.
func postgresHints(root *planNode) []string {
	scans, hints := walkPlan(root)
	for _, scan := range scans {
		if h := scanHint(scan, nil); h != "" && len(hints) < maxPlanHints {
			hints = append(hints, h)
		}
	}
	return hints
}

// scanHint words one sequential scan. With facts about the table it can say
// how big it really is — and say nothing at all when an index already leads
// with a filtered column, because then the planner chose the scan on
// purpose and an index suggestion would be noise.
func scanHint(scan planScan, facts *tableFacts) string {
	where := scan.relation
	if where == "" {
		where = "a table"
	}

	// How big the table is, which is what decides whether a scan matters.
	// The catalog knows; without it, the scan's own cost is the proxy,
	// because the rows it returns say nothing about the rows it reads.
	tableRows := scan.rows
	known := false
	if facts != nil && facts.rows > 0 {
		tableRows, known = float64(facts.rows), true
	}
	if known {
		if tableRows < bigScanRows {
			return ""
		}
	} else if scan.rows < bigScanRows && scan.cost < bigScanCost {
		return ""
	}

	// A filter that keeps most of the table cannot use an index well, and
	// a scan with no filter reads it all by definition.
	if !scan.filtered || (known && scan.rows >= tableRows/2) {
		return fmt.Sprintf("reads all of %s (~%s rows) with a sequential scan", where, formatRowEstimate(tableRows))
	}
	if len(scan.columns) == 0 {
		return fmt.Sprintf("sequential scan on %s (~%s rows) with a filter an index could serve",
			where, formatRowEstimate(tableRows))
	}
	if facts != nil {
		for _, col := range scan.columns {
			if facts.indexed[col] {
				return "" // an index leads with this column; the planner passed on it
			}
		}
	}
	cols := strings.Join(scan.columns, ", ")
	return fmt.Sprintf("sequential scan on %s (~%s rows) filtering on %s: consider an index on %s(%s)",
		where, formatRowEstimate(tableRows), cols, where, cols)
}

// tableFacts is what the catalog knows about a table a scan touched: how
// many rows it holds, and which columns an index already leads with.
type tableFacts struct {
	rows    int64
	indexed map[string]bool
}

// maxCatalogLookups bounds what explaining one query asks of the catalog.
const maxCatalogLookups = 3

// catalogHints words each scan with the catalog's help. The two queries are
// read-only lookups in the same rolled-back transaction as the EXPLAIN.
func catalogHints(ctx context.Context, tx *sql.Tx, scans []planScan) []string {
	var hints []string
	for i, scan := range scans {
		var facts *tableFacts
		if i < maxCatalogLookups && scan.relation != "" {
			facts = lookupTableFacts(ctx, tx, scan.relation)
		}
		if h := scanHint(scan, facts); h != "" && len(hints) < maxPlanHints {
			hints = append(hints, h)
		}
	}
	return hints
}

// lookupTableFacts reads the row estimate the planner itself uses and the
// leading column of every index on the table. A nil result means the
// catalog had nothing to say, and the hint falls back to the plan's own
// numbers.
func lookupTableFacts(ctx context.Context, tx *sql.Tx, relation string) *tableFacts {
	facts := &tableFacts{indexed: map[string]bool{}}

	// to_regclass resolves the name through the search path and returns
	// NULL rather than erroring on a name that isn't there.
	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(c.reltuples, -1)::bigint FROM pg_class c WHERE c.oid = to_regclass($1)`, relation)
	if err := row.Scan(&facts.rows); err != nil {
		return nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
		WHERE i.indrelid = to_regclass($1)`, relation)
	if err != nil {
		return facts
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err == nil {
			facts.indexed[col] = true
		}
	}
	return facts
}

// parallelRowDivisor mirrors how the planner splits a parallel scan's rows
// between the workers and the leader, so a parallel scan isn't reported as
// smaller than it is.
func parallelRowDivisor(workers int) float64 {
	if workers <= 0 {
		return 1
	}
	leader := 1 - 0.3*float64(workers)
	if leader < 0 {
		leader = 0
	}
	return float64(workers) + leader
}

// filterColumns picks the column names out of a plan filter, which is what
// an index would have to cover: "((user_id = $1) AND (status = 'paid'))"
// gives user_id and status.
func filterColumns(filter string) []string {
	var cols []string
	seen := map[string]bool{}
	for i := 0; i < len(filter); i++ {
		c := filter[i]
		if !isIdentStart(c) {
			continue
		}
		j := i
		for j < len(filter) && isIdentPart(filter[j]) {
			j++
		}
		word := filter[i:j]
		i = j
		// Only words compared against something are candidates, and a
		// qualified name keeps its column half.
		if k := strings.LastIndexByte(word, '.'); k >= 0 {
			word = word[k+1:]
		}
		rest := strings.TrimLeft(filter[i:], " ")
		if word == "" || seen[word] || sqlKeyword(word) {
			continue
		}
		if strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ">") ||
			strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, "IS ") ||
			strings.HasPrefix(rest, "ANY") || strings.HasPrefix(rest, "~~") {
			seen[word] = true
			cols = append(cols, word)
		}
	}
	return cols
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.'
}

func sqlKeyword(word string) bool {
	switch strings.ToUpper(word) {
	case "AND", "OR", "NOT", "NULL", "IS", "ANY", "ALL", "TRUE", "FALSE", "CASE", "WHEN", "THEN", "ELSE", "END":
		return true
	}
	return false
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
		(normalized_sql, operation, table_name, duration_ns, plan, hints, captured_at, generic)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (normalized_sql) DO UPDATE SET operation = excluded.operation,
		    table_name = excluded.table_name, duration_ns = excluded.duration_ns,
		    plan = excluded.plan, hints = excluded.hints, captured_at = excluded.captured_at,
		    generic = excluded.generic`,
		p.NormalizedSQL, p.Operation, p.Table, int64(p.Duration), p.Plan,
		strings.Join(p.Hints, "\n"), p.CapturedAt.UnixNano(), boolToInt(p.Generic)); err != nil {
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
		captured_at, generic FROM query_plans ORDER BY captured_at DESC LIMIT ?`, limit)
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
			generic  int
		)
		if err := rows.Scan(&plan.NormalizedSQL, &plan.Operation, &plan.Table, &duration,
			&plan.Plan, &hints, &captured, &generic); err != nil {
			return nil, err
		}
		plan.Duration = time.Duration(duration)
		plan.CapturedAt = time.Unix(0, captured)
		plan.Generic = generic != 0
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
		// with — those are the application's data. A parameterised
		// statement is planned generically instead, so this answers now
		// rather than waiting for the query to come round again.
		e.explain(c.Request.Context(), explainJob{metric: *pick, sql: pick.SQL})

		generic := placeholderCount(pick.SQL) > 0
		resp := gin.H{
			"explained": true,
			"query":     pick.NormalizedSQL,
			"duration":  pick.Duration,
			"generic":   generic,
		}
		if generic {
			resp["message"] = "Planned without the values it ran with, which Pulse does not keep. " +
				"The planner can choose differently with real values."
		}
		c.JSON(http.StatusOK, resp)
	}
}
