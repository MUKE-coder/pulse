package pulse

import (
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Every latency falls inside its bucket's bounds, and indices never go
// backwards as latency grows.
func TestHistIndex_BoundsContainValue(t *testing.T) {
	t.Parallel()
	prev := -1
	for us := uint64(0); us < 5_000_000; us += 1 + us/64 {
		idx := histIndex(time.Duration(us) * time.Microsecond)
		lo, hi := histBounds(idx)
		if us < lo || us >= hi {
			t.Fatalf("%dµs → bucket %d [%d, %d) does not contain it", us, idx, lo, hi)
		}
		if idx < prev {
			t.Fatalf("%dµs → bucket %d, below previous bucket %d", us, idx, prev)
		}
		prev = idx
	}
}

func TestLatencyHistogram_QuantilesWithinBucketError(t *testing.T) {
	t.Parallel()
	var h latencyHistogram
	for ms := 1; ms <= 1000; ms++ {
		h.add(time.Duration(ms) * time.Millisecond)
	}
	for _, tc := range []struct {
		q    float64
		want time.Duration
	}{{0.50, 500 * time.Millisecond}, {0.95, 950 * time.Millisecond}, {0.99, 990 * time.Millisecond}} {
		got := h.quantile(tc.q)
		if rel := math.Abs(float64(got-tc.want)) / float64(tc.want); rel > 0.125 {
			t.Errorf("p%.0f = %s, want %s ±12.5%%", tc.q*100, got, tc.want)
		}
	}

	decoded := decodeLatencyHistogram(h.encode())
	if decoded != h {
		t.Fatal("histogram changed across encode/decode")
	}
	var empty latencyHistogram
	if empty.quantile(0.95) != 0 {
		t.Fatal("empty histogram should report 0")
	}
}

func TestRollups_SummaryRoutesAndUnmatched(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	r := newRollups(24*time.Hour, nil, start)

	r.observe("GET", "/users/:id", "/users/:id", 200, 10*time.Millisecond, start.Add(10*time.Second))
	r.observe("GET", "/users/:id", "/users/:id", 404, 20*time.Millisecond, start.Add(70*time.Second))
	r.observe("POST", "/users", "/users", 503, 30*time.Millisecond, start.Add(80*time.Second))
	for i := 0; i < 5; i++ { // scanner noise: distinct raw paths, one route key
		r.observe("GET", "", fmt.Sprintf("/wp-admin/%d.php", i), 404, time.Millisecond, start.Add(90*time.Second))
	}

	c := r.summary(start, start.Add(2*time.Minute)).counts
	if c.total != 8 || c.status4xx != 6 || c.status5xx != 1 {
		t.Fatalf("summary = %+v, want 8 total, 6 4xx, 1 5xx", c)
	}

	routes := r.routeTotals(start, start.Add(2*time.Minute))
	if got := routes[rollupRouteKey{"GET", "/users/:id"}]; got.total != 2 || got.latency != 30*time.Millisecond {
		t.Errorf("GET /users/:id = %+v, want 2 requests, 30ms total latency", got)
	}
	if got := routes[rollupRouteKey{"GET", unmatchedRoute}]; got.total != 5 {
		t.Errorf("unmatched = %+v, want all 5 scanner requests under one key", got)
	}
	if len(routes) != 3 {
		t.Errorf("got %d route keys, want 3", len(routes))
	}

	// Minute bounds: only the first minute.
	if c := r.summary(start, start.Add(30*time.Second)).counts; c.total != 1 {
		t.Errorf("first-minute summary = %+v, want 1 request", c)
	}
}

func TestRollups_PruneKeepsSLOCountsForTheirWindow(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	slo := SLO{Name: "monthly", Target: 0.99, Window: 30 * 24 * time.Hour, Indicator: SLIErrorRate{}}
	r := newRollups(24*time.Hour, []SLO{slo}, start)

	r.observe("GET", "/x", "/x", 200, time.Millisecond, start.Add(time.Minute))
	now := start.Add(3 * 24 * time.Hour)
	r.observe("GET", "/x", "/x", 200, time.Millisecond, now)
	r.prune(now)

	if c := r.summary(start, now).counts; c.total != 1 {
		t.Errorf("per-route detail after prune = %d requests, want 1 (3-day-old minute past the 24h detail window)", c.total)
	}
	if _, total := r.sloCounts("monthly", now.Add(-slo.Window), now); total != 2 {
		t.Errorf("SLO events after prune = %d, want 2 (both inside the 30-day window)", total)
	}
}

// Rollups saved to SQLite come back identical in a fresh process.
func TestRollups_PersistAndRestore(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	slo := SLO{Name: "latency", Target: 0.95, Window: time.Hour,
		Indicator: SLILatency{Threshold: 50 * time.Millisecond}}
	s := newSQLiteForTest(t)

	r := newRollups(24*time.Hour, []SLO{slo}, start)
	r.persist = true
	for i := 0; i < 100; i++ {
		r.observe("GET", "/x", "/x", 200, time.Duration(i+1)*time.Millisecond, start.Add(time.Duration(i)*time.Second))
	}
	if err := s.saveRollups(r.takeDirty()); err != nil {
		t.Fatalf("saveRollups: %v", err)
	}
	if dirty := r.takeDirty(); len(dirty) != 0 {
		t.Fatalf("takeDirty should clear the dirty set, got %d minutes", len(dirty))
	}

	snaps, err := s.loadRollups(minuteOf(start.Add(-time.Hour)))
	if err != nil {
		t.Fatalf("loadRollups: %v", err)
	}
	restored := newRollups(24*time.Hour, []SLO{slo}, start.Add(time.Hour))
	restored.restore(snaps)

	end := start.Add(5 * time.Minute)
	want, got := r.summary(start, end), restored.summary(start, end)
	if got.counts != want.counts || got.hist != want.hist {
		t.Fatalf("restored summary %+v differs from original %+v", got.counts, want.counts)
	}
	wg, wt := r.sloCounts("latency", start, end)
	gg, gt := restored.sloCounts("latency", start, end)
	if wg != gg || wt != gt || wt != 100 || wg != 50 {
		t.Fatalf("SLO counts restored as (%d/%d), want (%d/%d) = (50/100)", gg, gt, wg, wt)
	}
	if !restored.since.Equal(start) {
		t.Errorf("restored since = %s, want the earliest restored minute %s", restored.since, start)
	}

	if err := s.pruneRollups(minuteOf(end), minuteOf(end)); err != nil {
		t.Fatalf("pruneRollups: %v", err)
	}
	if left, _ := s.loadRollups(0); len(left) != 0 {
		t.Fatalf("after prune %d minutes remain, want 0", len(left))
	}
}

// The overview and route list count every request, not the sampled ones in
// storage.
func TestAggregator_CountsEveryRequestDespiteSampling(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithSampleRate(0.1))
	t.Cleanup(func() { _ = p.Shutdown() })
	router.GET("/work/:n", func(c *gin.Context) {
		if c.Param("n") == "0" {
			c.Status(500)
			return
		}
		c.Status(200)
	})

	for i := 0; i < 1000; i++ {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/work/%d", i%10), nil))
	}
	p.aggregator.run()

	ov := p.aggregator.GetCachedOverview()
	if ov.TotalRequests != 1000 || ov.TotalErrors != 100 || math.Abs(ov.ErrorRate-10) > 1e-9 {
		t.Fatalf("overview = %d requests, %d errors, %.2f%%; want 1000, 100, 10%%",
			ov.TotalRequests, ov.TotalErrors, ov.ErrorRate)
	}
	var found bool
	for _, rs := range p.aggregator.GetCachedRouteStats() {
		if rs.Method == "GET" && rs.Path == "/work/:n" {
			found = true
			if rs.RequestCount != 1000 || rs.ErrorCount != 100 {
				t.Errorf("GET /work/:n = %d requests, %d errors; want 1000, 100", rs.RequestCount, rs.ErrorCount)
			}
		}
	}
	if !found {
		t.Error("GET /work/:n missing from route stats")
	}
}
