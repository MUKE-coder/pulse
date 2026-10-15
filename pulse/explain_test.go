package pulse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Only SELECTs are explained, and only where Pulse knows how to ask:
// explaining a write runs it on some databases.
func TestExplainStatement_AsksOnlyWhatIsSafeToAsk(t *testing.T) {
	cases := []struct {
		dialect, query, want string
		ok                   bool
	}{
		{"postgres", "SELECT * FROM users WHERE id = $1", "EXPLAIN SELECT * FROM users WHERE id = $1", true},
		{"pgx", "select 1", "EXPLAIN select 1", true},
		{"mysql", "SELECT * FROM users", "EXPLAIN SELECT * FROM users", true},
		{"sqlite", "  SELECT * FROM users", "EXPLAIN QUERY PLAN   SELECT * FROM users", true},
		{"sqlite3", "SELECT 1", "EXPLAIN QUERY PLAN SELECT 1", true},
		{"postgres", "UPDATE users SET name = $1", "", false},
		{"postgres", "DELETE FROM users", "", false},
		{"postgres", "INSERT INTO users (name) VALUES ($1)", "", false},
		{"sqlserver", "SELECT 1", "", false},
	}
	for _, c := range cases {
		got, ok := explainStatement(c.dialect, c.query)
		if ok != c.ok || got != c.want {
			t.Errorf("%s / %q -> %q, %v; want %q, %v", c.dialect, c.query, got, ok, c.want, c.ok)
		}
	}
}

func TestPlanHints_ReadTheUsualSuspects(t *testing.T) {
	cases := []struct {
		name, dialect, plan, want string
	}{
		{"sqlite full scan", "sqlite", "SCAN TABLE users", "reads the whole table"},
		{"sqlite temp b-tree", "sqlite", "SCAN TABLE users\nUSE TEMP B-TREE FOR ORDER BY", "temporary B-tree"},
		{"postgres seq scan", "postgres", "Seq Scan on users  (cost=0.00..18.50 rows=850 width=36)", "sequential scan on users"},
		{"postgres sort to disk", "postgres", "Sort Method: external merge  Disk: 4280kB", "spilled to disk"},
		{"mysql full scan", "mysql", "1  SIMPLE  users  ALL  NULL", "full table scan"},
		{"mysql filesort", "mysql", "1  SIMPLE  users  ref  Using filesort", "index the ORDER BY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hints := planHints(c.dialect, c.plan)
			if len(hints) == 0 {
				t.Fatalf("no hint read from %q", c.plan)
			}
			if !strings.Contains(strings.Join(hints, " | "), c.want) {
				t.Errorf("hints %v should mention %q", hints, c.want)
			}
		})
	}

	// An index-backed lookup is not worth a hint, and a wrong guess is
	// worse than none.
	for dialect, plan := range map[string]string{
		"sqlite":   "SEARCH TABLE users USING INDEX idx_users_email (email=?)",
		"postgres": "Index Scan using users_pkey on users  (cost=0.15..8.17 rows=1 width=36)",
	} {
		if hints := planHints(dialect, plan); len(hints) != 0 {
			t.Errorf("%s: %v for an indexed lookup", dialect, hints)
		}
	}
}

// One plan per pattern per interval, and never a blocked caller: the queue
// is drained by a background goroutine, and dropping is the right answer
// when it is full.
func TestExplainer_RateLimitsAndNeverBlocks(t *testing.T) {
	p := newTestPulse(t)
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.config.Database.ExplainInterval = time.Hour

	e := newExplainer(p, nil)
	slow := QueryMetric{Operation: "SELECT", NormalizedSQL: "SELECT * FROM users WHERE id = ?",
		Duration: time.Second}

	e.consider(explainJob{metric: slow})
	if len(e.queue) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(e.queue))
	}
	e.consider(explainJob{metric: slow})
	if len(e.queue) != 1 {
		t.Errorf("the same pattern within the interval should be skipped, queue has %d", len(e.queue))
	}
	now = now.Add(time.Hour + time.Minute)
	e.consider(explainJob{metric: slow})
	if len(e.queue) != 2 {
		t.Errorf("after the interval the pattern is explained again, queue has %d", len(e.queue))
	}

	// Writes, failures and unnamed patterns are never asked about.
	for _, m := range []QueryMetric{
		{Operation: "UPDATE", NormalizedSQL: "UPDATE users SET name = ?"},
		{Operation: "SELECT", NormalizedSQL: "SELECT * FROM missing", Error: "no such table"},
		{Operation: "SELECT", NormalizedSQL: ""},
	} {
		before := len(e.queue)
		e.consider(explainJob{metric: m})
		if len(e.queue) != before {
			t.Errorf("%+v should not have been queued", m)
		}
	}

	// A full queue drops rather than waits: nothing here may block a query.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < explainQueueSize*2; i++ {
			now = now.Add(2 * time.Hour)
			e.consider(explainJob{metric: slow})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consider blocked on a full queue")
	}
}

// End to end against a real database: a slow SELECT with no usable index
// gets a plan, and the plan carries the hint.
func TestExplainer_CapturesAPlanForASlowQuery(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	p.config.Database.ExplainSlowQueries = Bool(true)
	p.config.Database.SlowQueryThreshold = 0 // every query counts as slow here
	p.explainer = newExplainer(p, db)
	startExplainer(p)

	for i := 0; i < 50; i++ {
		db.Create(&TestUser{Name: "user", Age: i})
	}
	var found []TestUser
	if err := db.Where("age > ?", 10).Find(&found).Error; err != nil {
		t.Fatalf("query: %v", err)
	}

	store := p.storage.(planStore)
	var plans []QueryPlan
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		plans, _ = store.queryPlans(10)
		if len(plans) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(plans) == 0 {
		t.Fatal("no plan was captured for a slow SELECT")
	}

	var plan *QueryPlan
	for i := range plans {
		if strings.Contains(plans[i].NormalizedSQL, "test_users") {
			plan = &plans[i]
		}
	}
	if plan == nil {
		t.Fatalf("no plan for the table queried: %+v", plans)
	}
	if !strings.Contains(strings.ToUpper(plan.Plan), "TEST_USERS") {
		t.Errorf("plan doesn't look like SQLite's answer: %q", plan.Plan)
	}
	if len(plan.Hints) == 0 || !strings.Contains(plan.Hints[0], "whole table") {
		t.Errorf("an unindexed filter should be hinted, got %v (plan %q)", plan.Hints, plan.Plan)
	}
	if plan.Operation != "SELECT" || plan.CapturedAt.IsZero() {
		t.Errorf("plan is missing its context: %+v", plan)
	}

	// Pulse's own EXPLAIN runs outside GORM, so it is not recorded as one
	// of the application's queries.
	queries, _ := p.storage.GetSlowQueries(0, 200)
	if len(queries) == 0 {
		t.Fatal("the application's own queries should have been recorded")
	}
	for _, q := range queries {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q.SQL)), "EXPLAIN") {
			t.Errorf("Pulse recorded its own EXPLAIN as a query: %q", q.SQL)
		}
	}
}

func TestConformance_QueryPlansKeepOnePerPattern(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		store, ok := s.(planStore)
		if !ok {
			t.Fatalf("%T keeps no query plans", s)
		}
		now := time.Now().Truncate(time.Millisecond)

		first := QueryPlan{NormalizedSQL: "SELECT * FROM users WHERE id = ?", Operation: "SELECT",
			Table: "users", Duration: 300 * time.Millisecond, Plan: "SCAN TABLE users",
			Hints: []string{"reads the whole table: SCAN TABLE users"}, CapturedAt: now.Add(-time.Hour)}
		if err := store.storeQueryPlan(first); err != nil {
			t.Fatalf("store: %v", err)
		}
		if err := store.storeQueryPlan(QueryPlan{NormalizedSQL: "SELECT name FROM users", Operation: "SELECT",
			Table: "users", Duration: time.Second, Plan: "SCAN TABLE users",
			CapturedAt: now.Add(-2 * time.Hour)}); err != nil {
			t.Fatalf("store second: %v", err)
		}

		plans, err := store.queryPlans(10)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if len(plans) != 2 {
			t.Fatalf("got %d plans, want 2: %+v", len(plans), plans)
		}
		if plans[0].NormalizedSQL != first.NormalizedSQL {
			t.Errorf("plans should come back newest first, got %q", plans[0].NormalizedSQL)
		}
		if got := plans[0]; got.Operation != "SELECT" || got.Table != "users" ||
			got.Duration != 300*time.Millisecond || got.Plan != "SCAN TABLE users" ||
			len(got.Hints) != 1 || !got.CapturedAt.Equal(first.CapturedAt) {
			t.Errorf("plan came back wrong: %+v", got)
		}

		// Explaining the same pattern again replaces its plan: one per
		// pattern, always the latest.
		again := first
		again.Plan = "SEARCH TABLE users USING INDEX idx_users_id (id=?)"
		again.Hints = nil
		again.CapturedAt = now
		if err := store.storeQueryPlan(again); err != nil {
			t.Fatalf("re-store: %v", err)
		}
		plans, _ = store.queryPlans(10)
		if len(plans) != 2 {
			t.Fatalf("a re-explained pattern should replace, not add: %+v", plans)
		}
		for _, got := range plans {
			if got.NormalizedSQL == again.NormalizedSQL && (got.Plan != again.Plan || len(got.Hints) != 0) {
				t.Errorf("the newer plan should have replaced the older: %+v", got)
			}
		}

		if plans, _ = store.queryPlans(1); len(plans) != 1 {
			t.Errorf("limit ignored: got %d plans", len(plans))
		}
	})
}

func TestAPI_DatabasePlans(t *testing.T) {
	p, router := setupAPIPulse(t)
	if err := p.storage.(planStore).storeQueryPlan(QueryPlan{
		NormalizedSQL: "SELECT * FROM orders WHERE user_id = ?", Operation: "SELECT", Table: "orders",
		Duration: 450 * time.Millisecond, Plan: "SCAN TABLE orders",
		Hints: []string{"reads the whole table: SCAN TABLE orders"}, CapturedAt: time.Now(),
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	token := loginAndGetToken(t, router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/database/plans", token, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Plans     []QueryPlan `json:"plans"`
		Capturing bool        `json:"capturing"`
		Supported bool        `json:"supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Supported {
		t.Error("the Memory backend keeps plans, so the endpoint should say so")
	}
	if resp.Capturing {
		t.Error("capturing should be false until ExplainSlowQueries is turned on")
	}
	if len(resp.Plans) != 1 || resp.Plans[0].Table != "orders" || len(resp.Plans[0].Hints) != 1 {
		t.Fatalf("unexpected plans: %+v", resp.Plans)
	}
}

// The explainer is only built when it is asked for.
func TestExplainer_IsOptIn(t *testing.T) {
	db := setupTestDB(t)
	off := newPulse(context.Background(), applyDefaults(Config{}))
	if off.explainer != nil {
		t.Error("the explainer should not exist until configured")
	}
	if boolValue(off.config.Database.ExplainSlowQueries) {
		t.Error("ExplainSlowQueries should default to off: it queries the user's database")
	}
	_ = off.Shutdown()

	cfg := applyDefaults(Config{})
	WithExplainSlowQueries(0)(&cfg)
	if !boolValue(cfg.Database.ExplainSlowQueries) {
		t.Fatal("WithExplainSlowQueries should turn it on")
	}
	p := newPulse(context.Background(), cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = p.Shutdown() })
	p.explainer = newExplainer(p, db)
	if got := p.explainer.interval(); got != defaultExplainInterval {
		t.Errorf("interval %v, want the default %v", got, defaultExplainInterval)
	}
}
