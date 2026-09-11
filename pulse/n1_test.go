package pulse

import (
	"strings"
	"testing"
	"time"
)

func TestSuggestN1Fix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		sql        string
		wantSubstr string // expected substring of the suggestion; "" means accept anything non-empty
	}{
		{
			name:       "primary key lookup",
			sql:        "SELECT * FROM `authors` WHERE id = ?",
			wantSubstr: "Preload",
		},
		{
			name:       "foreign key lookup",
			sql:        "SELECT * FROM posts WHERE author_id = ?",
			wantSubstr: "Preload",
		},
		{
			name:       ".First() pattern",
			sql:        "SELECT * FROM authors ORDER BY id LIMIT 1",
			wantSubstr: "First",
		},
		{
			name:       "aggregate per row",
			sql:        "SELECT count(*) FROM posts WHERE author_id = ?",
			wantSubstr: "GROUP BY",
		},
		{
			name:       "existence check",
			sql:        "SELECT 1 FROM posts WHERE author_id = ?",
			wantSubstr: "existence",
		},
		{
			name:       "fallback for unknown shape",
			sql:        "UPDATE counters SET value = value + 1 WHERE id = ?",
			wantSubstr: "", // generic fallback
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestN1Fix(tc.sql)
			if got == "" {
				t.Fatalf("expected a non-empty suggestion for %q", tc.sql)
			}
			if tc.wantSubstr != "" && !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("suggestion for %q = %q, want substring %q", tc.sql, got, tc.wantSubstr)
			}
		})
	}
}

func TestSuggestN1Fix_EmptyInput(t *testing.T) {
	t.Parallel()
	if got := suggestN1Fix(""); got != "" {
		t.Fatalf("expected empty suggestion for empty SQL, got %q", got)
	}
}

func TestRankN1Detections_GroupsAndOrders(t *testing.T) {
	t.Parallel()

	now := time.Now()
	in := []N1Detection{
		// Route A — 2 occurrences, cheap.
		{Route: "GET /api/users", Pattern: "select * from accounts where id = ?", Count: 5, TotalDuration: 10 * time.Millisecond, DetectedAt: now.Add(-2 * time.Minute)},
		{Route: "GET /api/users", Pattern: "select * from accounts where id = ?", Count: 8, TotalDuration: 15 * time.Millisecond, DetectedAt: now.Add(-1 * time.Minute)},

		// Route B — 1 occurrence, expensive — should rank highest.
		{Route: "GET /api/orders", Pattern: "select * from items where order_id = ?", Count: 50, TotalDuration: 800 * time.Millisecond, DetectedAt: now.Add(-90 * time.Second), SuggestedFix: "Preload Items"},

		// Route A again with different pattern.
		{Route: "GET /api/users", Pattern: "select 1 from posts where author_id = ?", Count: 3, TotalDuration: 5 * time.Millisecond, DetectedAt: now.Add(-30 * time.Second)},
	}

	out := rankN1Detections(in, 0)
	if len(out) != 3 {
		t.Fatalf("expected 3 groups (2 patterns on /users + 1 on /orders), got %d", len(out))
	}

	// Highest impact first.
	if out[0].Route != "GET /api/orders" {
		t.Fatalf("top entry by impact = %q, want %q", out[0].Route, "GET /api/orders")
	}

	// Aggregation of route A's first pattern.
	var found *N1Ranking
	for i := range out {
		if out[i].Route == "GET /api/users" && strings.Contains(out[i].Pattern, "accounts") {
			found = &out[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected an aggregated entry for /api/users + accounts pattern")
	}
	if found.Occurrences != 2 {
		t.Fatalf("Occurrences = %d, want 2", found.Occurrences)
	}
	if found.TotalDuration != 25*time.Millisecond {
		t.Fatalf("TotalDuration = %s, want 25ms", found.TotalDuration)
	}
	if found.AvgQueriesPerHit != 6.5 {
		t.Fatalf("AvgQueriesPerHit = %.2f, want 6.5", found.AvgQueriesPerHit)
	}

	// Suggestion is carried through.
	if out[0].SuggestedFix == "" {
		t.Fatal("expected SuggestedFix on the top ranking entry")
	}
}

func TestRankN1Detections_LimitTrims(t *testing.T) {
	t.Parallel()
	in := []N1Detection{
		{Route: "a", Pattern: "x", Count: 1, TotalDuration: 30 * time.Millisecond, DetectedAt: time.Now()},
		{Route: "b", Pattern: "x", Count: 1, TotalDuration: 20 * time.Millisecond, DetectedAt: time.Now()},
		{Route: "c", Pattern: "x", Count: 1, TotalDuration: 10 * time.Millisecond, DetectedAt: time.Now()},
	}
	out := rankN1Detections(in, 2)
	if len(out) != 2 {
		t.Fatalf("expected limit=2 to trim to 2, got %d", len(out))
	}
	if out[0].Route != "a" || out[1].Route != "b" {
		t.Fatalf("expected the two highest-impact routes (a, b), got (%q, %q)", out[0].Route, out[1].Route)
	}
}

// TestRankN1Detections_ReviewScenario seeds three N+1 patterns of very
// different shapes — as the plugin now records them, with real per-request
// counts — and checks they rank by total wall-clock cost, the order someone
// paying for the latency would fix them in:
//
//	GET /orders: 100 requests × 50 queries × 2ms  = 10s
//	GET /users: 1000 requests ×  5 queries × 1ms  =  5s
//	GET /feed:    10 requests ×  6 queries × 50ms =  3s
func TestRankN1Detections_ReviewScenario(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var in []N1Detection
	add := func(route, pattern string, requests, perRequest int, each time.Duration) {
		for i := 0; i < requests; i++ {
			in = append(in, N1Detection{
				Route: route, Pattern: pattern, Count: perRequest,
				TotalDuration: time.Duration(perRequest) * each, AvgDuration: each,
				DetectedAt: now,
			})
		}
	}
	add("GET /orders", "select * from items where order_id = ?", 100, 50, 2*time.Millisecond)
	add("GET /feed", "select * from authors where id = ?", 10, 6, 50*time.Millisecond)
	add("GET /users", "select count(*) from posts where author_id = ?", 1000, 5, time.Millisecond)

	out := rankN1Detections(in, 0)
	want := []string{"GET /orders", "GET /users", "GET /feed"}
	if len(out) != len(want) {
		t.Fatalf("got %d groups, want %d", len(out), len(want))
	}
	for i, route := range want {
		if out[i].Route != route {
			t.Fatalf("rank %d = %q, want %q (full order: %v)", i, out[i].Route, route, out)
		}
	}
	if out[0].AvgQueriesPerHit != 50 || out[0].TotalDuration != 10*time.Second {
		t.Errorf("GET /orders = %.0f queries/hit, %s total; want 50 and 10s",
			out[0].AvgQueriesPerHit, out[0].TotalDuration)
	}
}
