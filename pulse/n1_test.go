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
