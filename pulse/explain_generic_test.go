package pulse

import (
	"strings"
	"testing"
)

// A plan Pulse asks for after the fact has no values to bind, and must
// still be answerable. This is the limitation that "armed for its next
// run" used to paper over.
func TestExplain_PlansAParameterisedQueryWithoutItsValues(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	p.config.Database.ExplainSlowQueries = Bool(true)
	p.config.Database.SlowQueryThreshold = 0
	e := newExplainer(p, db)

	for i := 0; i < 40; i++ {
		db.Create(&TestUser{Name: "someone", Age: i})
	}

	// No vars: exactly what the Database page's button has to work with.
	e.explain(t.Context(), explainJob{
		metric: QueryMetric{Operation: "SELECT", NormalizedSQL: "SELECT * FROM test_users WHERE age > ?"},
		sql:    "SELECT * FROM `test_users` WHERE age > ?",
	})

	plans, err := p.storage.(planStore).queryPlans(10)
	if err != nil || len(plans) == 0 {
		t.Fatalf("no plan captured: %v %+v", err, plans)
	}
	if !strings.Contains(strings.ToUpper(plans[0].Plan), "TEST_USERS") {
		t.Errorf("plan does not mention the table: %q", plans[0].Plan)
	}
	if !plans[0].Generic {
		t.Error("a plan made without the query's values should be marked generic")
	}
}

// With the values a live run hands over, the plan is the real one.
func TestExplain_KeepsTheRealPlanWhenItHasTheValues(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	p.config.Database.ExplainSlowQueries = Bool(true)
	e := newExplainer(p, db)
	db.Create(&TestUser{Name: "someone", Age: 30})

	e.explain(t.Context(), explainJob{
		metric: QueryMetric{Operation: "SELECT", NormalizedSQL: "SELECT * FROM test_users WHERE age > ?"},
		sql:    "SELECT * FROM `test_users` WHERE age > ?",
		vars:   []any{5},
	})

	plans, _ := p.storage.(planStore).queryPlans(10)
	if len(plans) == 0 {
		t.Fatal("no plan captured")
	}
	if plans[0].Generic {
		t.Error("a plan made with the query's own values is not generic")
	}
}

// EXPLAIN takes locks and reads statistics, so it runs inside a
// transaction that is always rolled back — nothing a plan touches may
// outlive the request that asked for it.
func TestExplain_RollsBackAndLeavesNothingBehind(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	p.config.Database.ExplainSlowQueries = Bool(true)
	e := newExplainer(p, db)
	db.Create(&TestUser{Name: "before", Age: 1})

	var before int64
	db.Model(&TestUser{}).Count(&before)
	e.explain(t.Context(), explainJob{
		metric: QueryMetric{Operation: "SELECT", NormalizedSQL: "SELECT * FROM test_users"},
		sql:    "SELECT * FROM `test_users`",
	})
	var after int64
	db.Model(&TestUser{}).Count(&after)
	if before != after {
		t.Errorf("rows went from %d to %d: explaining must not write", before, after)
	}

	// The connection is usable afterwards, which a leaked transaction
	// would break.
	var users []TestUser
	if err := db.Find(&users).Error; err != nil {
		t.Errorf("the pool is unusable after an explain: %v", err)
	}
}

// The JSON plan is what lets a hint say more than "there is a seq scan
// here": the node tree carries row estimates, filters and sort methods,
// and knows when a LIMIT stops a scan early.
func TestPostgresHints_ReadTheTree(t *testing.T) {
	cases := []struct {
		name string
		plan string
		want string
	}{
		{
			name: "seq scan under a join names the index to add",
			plan: `[{"Plan":{"Node Type":"Hash Join","Plan Rows":500,"Total Cost":900,
				"Plans":[{"Node Type":"Seq Scan","Relation Name":"orders","Plan Rows":1200000,
				"Total Cost":800,"Filter":"((user_id = $1) AND (status = 'paid'::text))"}]}}]`,
			want: "index on orders(user_id, status)",
		},
		{
			name: "a scan a LIMIT stops early is not a finding",
			plan: `[{"Plan":{"Node Type":"Limit","Plan Rows":10,
				"Plans":[{"Node Type":"Seq Scan","Relation Name":"orders","Plan Rows":1200000}]}}]`,
			want: "",
		},
		{
			name: "a small table is the planner's business",
			plan: `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"settings","Plan Rows":40,"Total Cost":12}}]`,
			want: "",
		},
		{
			// The case the live check caught: a selective filter with no
			// index reads the whole table to return one row. Gating on
			// rows returned would hide the most actionable finding there
			// is, so the gate is the scan's cost — a proxy for the table.
			name: "a selective filter with no index is the point",
			plan: `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"orders","Plan Rows":1,
				"Total Cost":1192,"Filter":"(note = $1)"}}]`,
			want: "consider an index on orders(note)",
		},
		{
			name: "a sort that spilled",
			plan: `[{"Plan":{"Node Type":"Sort","Plan Rows":900,"Sort Method":"external merge",
				"Sort Space Type":"Disk","Sort Space Used":4280}}]`,
			want: "spilled to disk",
		},
		{
			name: "an unfiltered scan reads the table",
			plan: `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"events","Plan Rows":2000000}}]`,
			want: "reads all of events",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node, err := parsePostgresPlan(c.plan)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			hints := strings.Join(postgresHints(node), " | ")
			if c.want == "" {
				if hints != "" {
					t.Errorf("want no hint, got %q", hints)
				}
				return
			}
			if !strings.Contains(hints, c.want) {
				t.Errorf("hints %q should mention %q", hints, c.want)
			}
		})
	}
}

// A parallel scan's rows are split between workers and the leader; counting
// one worker's share would under-report the scan.
func TestParallelRowDivisor(t *testing.T) {
	if got := parallelRowDivisor(0); got != 1 {
		t.Errorf("no workers → %v, want 1", got)
	}
	if got := parallelRowDivisor(2); got < 2.4 || got > 2.5 {
		t.Errorf("two workers → %v, want the planner's 2.4", got)
	}
}

func TestFilterColumns(t *testing.T) {
	cases := map[string][]string{
		"((user_id = $1) AND (status = 'paid'::text))": {"user_id", "status"},
		"(o.created_at > $1)":                          {"created_at"},
		"(total IS NULL)":                              {"total"},
		// A column inside a function call yields nothing on purpose: a
		// plain index on email would not serve lower(email), so naming it
		// would be advice that does not work. The scan is still reported,
		// just without a column to index.
		"(lower(email) ~~ $1)": nil,
		"":                     nil,
	}
	for filter, want := range cases {
		got := filterColumns(filter)
		if len(got) != len(want) {
			t.Errorf("%q → %v, want %v", filter, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q → %v, want %v", filter, got, want)
				break
			}
		}
	}
}

// The rendered tree is what someone reads on the page, so it should look
// like the plan psql prints.
func TestRenderPlanTree(t *testing.T) {
	node, err := parsePostgresPlan(`[{"Plan":{"Node Type":"Hash Join","Plan Rows":500,"Total Cost":900,
		"Plans":[{"Node Type":"Index Scan","Relation Name":"users","Index Name":"users_pkey",
		"Plan Rows":1,"Total Cost":8,"Index Cond":"(id = $1)"}]}}]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	text := renderPlanTree(node)
	for _, want := range []string{"Hash Join", "-> Index Scan on users using users_pkey", "Index Cond: (id = $1)"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered plan is missing %q:\n%s", want, text)
		}
	}
}
