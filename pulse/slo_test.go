package pulse

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- SLI classification ---

func TestSLIErrorRate_Classify(t *testing.T) {
	t.Parallel()
	sli := SLIErrorRate{Routes: []string{"/api/*"}}
	tests := []struct {
		name     string
		req      RequestMetric
		wantGood int64
		wantTot  int64
	}{
		{"in-scope 200", RequestMetric{Path: "/api/users", StatusCode: 200}, 1, 1},
		{"in-scope 404 is good", RequestMetric{Path: "/api/users", StatusCode: 404}, 1, 1},
		{"in-scope 500 is bad", RequestMetric{Path: "/api/users", StatusCode: 500}, 0, 1},
		{"out-of-scope ignored", RequestMetric{Path: "/healthz", StatusCode: 500}, 0, 0},
		{"glob prefix matches nested", RequestMetric{Path: "/api/users/1/posts", StatusCode: 200}, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, total := sli.classify(tt.req)
			if g != tt.wantGood || total != tt.wantTot {
				t.Fatalf("got (%d,%d), want (%d,%d)", g, total, tt.wantGood, tt.wantTot)
			}
		})
	}
}

func TestSLILatency_Classify(t *testing.T) {
	t.Parallel()
	sli := SLILatency{Routes: []string{"/api/*"}, Threshold: 500 * time.Millisecond}

	cases := []struct {
		name     string
		req      RequestMetric
		wantGood int64
		wantTot  int64
	}{
		{"fast 200", RequestMetric{Path: "/api/x", StatusCode: 200, Latency: 100 * time.Millisecond}, 1, 1},
		{"slow 200", RequestMetric{Path: "/api/x", StatusCode: 200, Latency: 800 * time.Millisecond}, 0, 1},
		{"fast 5xx still bad", RequestMetric{Path: "/api/x", StatusCode: 500, Latency: 1 * time.Millisecond}, 0, 1},
		{"out of scope", RequestMetric{Path: "/metrics", StatusCode: 200, Latency: 1 * time.Millisecond}, 0, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			g, total := sli.classify(tt.req)
			if g != tt.wantGood || total != tt.wantTot {
				t.Fatalf("got (%d,%d), want (%d,%d)", g, total, tt.wantGood, tt.wantTot)
			}
		})
	}
}

// --- Burn-rate math ---

func TestBudgetConsumed(t *testing.T) {
	t.Parallel()
	// 99.9% target, observed 99.85% → consumed = 0.0015 / 0.001 = 1.5 (over budget)
	got := budgetConsumed(0.9985, 0.999)
	if got < 1.49 || got > 1.51 {
		t.Fatalf("over-budget case: got %.3f, want ~1.5", got)
	}

	// 99.9% target, observed 100% → consumed = 0
	if got := budgetConsumed(1.0, 0.999); got != 0 {
		t.Fatalf("perfect compliance: got %.3f, want 0", got)
	}

	// 99.0% target, observed 99.5% → consumed = 0.005 / 0.01 = 0.5
	if got := budgetConsumed(0.995, 0.99); got < 0.49 || got > 0.51 {
		t.Fatalf("half-budget case: got %.3f, want ~0.5", got)
	}
}

// --- End-to-end evaluator behaviour ---

// TestSLOEvaluator_ComputesCompliance feeds a known mix of good and bad
// requests, runs evaluate(), and checks the resulting snapshot.
func TestSLOEvaluator_ComputesCompliance(t *testing.T) {
	p, clock := newClockedPulse(t)

	slo := SLO{
		Name:      "API availability",
		Target:    0.99,
		Window:    time.Hour,
		Indicator: SLIErrorRate{Routes: []string{"/api/*"}},
		// 95% compliance against 99% target → 5× burn rate. Set the
		// threshold below 5 so the firing branch exercises.
		BurnRateAlerts: []BurnRateAlert{{Name: "fast-burn", Window: 5 * time.Minute, BurnRateMultiple: 2.0, Severity: "critical"}},
	}
	ev := newSLOEvaluator(p, []SLO{slo})

	// 95 good + 5 bad over the last minute → compliance 0.95 < target 0.99
	observeN(p, 95, "/api/users", 200, clock.Add(-30*time.Second))
	observeN(p, 5, "/api/users", 500, clock.Add(-30*time.Second))
	observeN(p, 50, "/healthz", 500, clock.Add(-30*time.Second)) // out of scope

	ev.evaluate()

	snaps := ev.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snaps))
	}
	got := snaps[0]
	if got.TotalEvents != 100 || got.GoodEvents != 95 {
		t.Fatalf("event counts = (good=%d, total=%d), want (95, 100)", got.GoodEvents, got.TotalEvents)
	}
	if got.Compliance < 0.949 || got.Compliance > 0.951 {
		t.Fatalf("compliance = %.4f, want ~0.95", got.Compliance)
	}
	if got.BudgetConsumedPct < 400 { // 5% errors / 1% budget = 5× = 500%
		t.Fatalf("budget consumed = %.1f%%, want > 400", got.BudgetConsumedPct)
	}
	if got.Status == "ok" {
		t.Fatalf("status = %q, want a burn state", got.Status)
	}

	// The burn-rate sub-window should also be firing.
	if len(got.BurnWindows) != 1 || !got.BurnWindows[0].Firing {
		t.Fatalf("expected fast-burn window to be firing, got %+v", got.BurnWindows)
	}
}

// TestSLOEvaluator_FiresAndResolves walks the firing → resolved transition:
// one alert record, fired and then resolved in place.
func TestSLOEvaluator_FiresAndResolves(t *testing.T) {
	p, clock := newClockedPulse(t)
	slo := SLO{
		Name:      "Test SLO",
		Target:    0.99,
		Window:    time.Hour,
		Indicator: SLIErrorRate{Routes: []string{"/api/*"}},
		BurnRateAlerts: []BurnRateAlert{
			{Name: "fast-burn", Window: 5 * time.Minute, BurnRateMultiple: 2.0, Severity: "critical"},
		},
	}
	ev := newSLOEvaluator(p, []SLO{slo})

	// Bad slice: 70 good / 30 bad → 30% error rate, target budget 1% → 30× burn
	observeN(p, 70, "/api/x", 200, clock.Add(-30*time.Second))
	observeN(p, 30, "/api/x", 500, clock.Add(-30*time.Second))
	ev.evaluate()

	firing, _ := p.storage.GetAlerts(AlertFilter{State: AlertStateFiring})
	if len(firing) != 1 {
		t.Fatalf("expected one firing burn-rate alert, got %d", len(firing))
	}
	if !strings.HasPrefix(firing[0].RuleName, "slo:Test SLO:") {
		t.Fatalf("rule name = %q, want slo:Test SLO:* prefix", firing[0].RuleName)
	}

	// Ten minutes on, the bad slice has left the 5m window and traffic is
	// healthy, so the burn rate falls below threshold.
	*clock = clock.Add(10 * time.Minute)
	observeN(p, 100, "/api/x", 200, clock.Add(-15*time.Second))
	ev.evaluate()

	all, _ := p.storage.GetAlerts(AlertFilter{})
	if len(all) != 1 || all[0].State != AlertStateResolved || all[0].ID != firing[0].ID {
		t.Fatalf("want the firing record resolved in place, got %+v", all)
	}
}

// A few requests right after a deploy or restart must not page, however bad
// their ratio: the burn window needs MinEvents first.
func TestSLOEvaluator_MinEventsPreventsMisfire(t *testing.T) {
	p, clock := newClockedPulse(t)
	burn := BurnRateAlert{Name: "fast-burn", Window: 5 * time.Minute, BurnRateMultiple: 2, Severity: "critical"}
	anyTraffic := burn
	anyTraffic.MinEvents = 1
	ev := newSLOEvaluator(p, []SLO{
		{Name: "default-min", Target: 0.99, Window: time.Hour, Indicator: SLIErrorRate{}, BurnRateAlerts: []BurnRateAlert{burn}},
		{Name: "min-one", Target: 0.99, Window: time.Hour, Indicator: SLIErrorRate{}, BurnRateAlerts: []BurnRateAlert{anyTraffic}},
	})

	// 1 bad in 3 → 33% errors, a 33× burn — but only 3 events.
	observeN(p, 2, "/x", 200, clock.Add(-10*time.Second))
	observeN(p, 1, "/x", 500, clock.Add(-10*time.Second))
	ev.evaluate()

	snaps := ev.Snapshot()
	if w := snaps[0].BurnWindows[0]; w.Firing || w.Events != 3 || w.MinEvents != defaultBurnRateMinEvents {
		t.Errorf("default MinEvents: window = %+v, want not firing with 3 of %d events", w, defaultBurnRateMinEvents)
	}
	if w := snaps[1].BurnWindows[0]; !w.Firing {
		t.Errorf("MinEvents=1: window = %+v, want firing", w)
	}
}

// Multiwindow alerting: a burst that has passed keeps the long window hot,
// but the short window has recovered, so the alert must not fire.
func TestSLOEvaluator_ShortWindowResolvesAfterRecovery(t *testing.T) {
	p, clock := newClockedPulse(t)
	ev := newSLOEvaluator(p, []SLO{{
		Name: "availability", Target: 0.99, Window: time.Hour, Indicator: SLIErrorRate{},
		// nil → defaults: fast-burn 1h confirmed over 5m at 14.4×.
	}})

	observeN(p, 200, "/x", 500, clock.Add(-50*time.Minute)) // burst, long ago
	observeN(p, 400, "/x", 200, clock.Add(-2*time.Minute))  // healthy since
	ev.evaluate()

	fast := ev.Snapshot()[0].BurnWindows[0]
	if fast.BurnRate <= 14.4 || fast.ShortBurnRate != 0 || fast.Firing {
		t.Fatalf("fast-burn = %+v, want long burn > 14.4, short burn 0, not firing", fast)
	}

	// The same burst inside the short window does fire.
	observeN(p, 200, "/x", 500, clock.Add(-time.Minute))
	ev.evaluate()
	if fast := ev.Snapshot()[0].BurnWindows[0]; !fast.Firing {
		t.Fatalf("fast-burn = %+v, want firing once the short window burns too", fast)
	}
}

// Compliance counts every request, not the sampled ones in storage. With
// 10% sampling, stored requests over-represent errors (always kept) about
// tenfold; the SLO must still see exactly 1% errors.
func TestSLOEvaluator_SamplingDoesNotSkewCompliance(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil,
		WithDevMode(),
		WithSampleRate(0.1),
		WithSLO(SLO{Name: "availability", Target: 0.99, Window: time.Hour,
			Indicator: SLIErrorRate{}, BurnRateAlerts: []BurnRateAlert{}}),
	)
	t.Cleanup(func() { _ = p.Shutdown() })
	router.GET("/work/:n", func(c *gin.Context) {
		if c.Param("n") == "0" {
			c.Status(500)
			return
		}
		c.Status(200)
	})

	for i := 0; i < 2000; i++ {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/work/%d", i%100), nil))
	}

	stored, _ := p.storage.GetRequests(RequestFilter{TimeRange: Last1h()})
	if len(stored) > 1500 {
		t.Fatalf("stored %d of 2000 requests; sampling at 0.1 should keep far fewer", len(stored))
	}

	p.sloEvaluator.evaluate()
	s := p.sloEvaluator.Snapshot()[0]
	if s.TotalEvents != 2000 || s.GoodEvents != 1980 {
		t.Fatalf("events = (good=%d, total=%d), want (1980, 2000)", s.GoodEvents, s.TotalEvents)
	}
}

// After a restart (SQLite), a still-burning SLO must not page again, and
// once it recovers the original alert resolves.
func TestSLOEvaluator_RestoresFiringAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slo.db")
	slo := SLO{Name: "restart", Target: 0.99, Window: time.Hour, Indicator: SLIErrorRate{},
		BurnRateAlerts: []BurnRateAlert{{Name: "fast-burn", Window: 10 * time.Minute, BurnRateMultiple: 2, Severity: "critical"}}}
	clock := sloTestTime

	open := func() (*Pulse, *sloEvaluator) {
		p := newPulse(context.Background(), applyDefaults(Config{DevMode: true, SLOs: []SLO{slo}}))
		p.now = func() time.Time { return clock }
		p.rollups = newRollups(24*time.Hour, []SLO{slo}, clock)
		s, err := NewSQLiteStorage(path, "test")
		if err != nil {
			t.Fatalf("open storage: %v", err)
		}
		p.storage = s
		restoreRollups(p)
		return p, newSLOEvaluator(p, []SLO{slo})
	}

	// First run: the SLO burns and pages; rollups are saved on shutdown.
	p1, ev1 := open()
	observeN(p1, 50, "/x", 500, clock.Add(-30*time.Second))
	ev1.evaluate()
	flushRollups(p1)
	_ = p1.Shutdown()

	// Restarted a minute later, still burning.
	clock = clock.Add(time.Minute)
	p2, ev2 := open()
	t.Cleanup(func() { _ = p2.Shutdown() })
	ev2.evaluate()
	all, _ := p2.storage.GetAlerts(AlertFilter{})
	if len(all) != 1 || all[0].State != AlertStateFiring {
		t.Fatalf("after restart: %+v, want the one original firing alert (no second page)", all)
	}
	firedID := all[0].ID

	// The burst leaves the window: the original alert resolves in place.
	clock = clock.Add(time.Hour)
	ev2.evaluate()
	all, _ = p2.storage.GetAlerts(AlertFilter{})
	if len(all) != 1 || all[0].ID != firedID || all[0].State != AlertStateResolved {
		t.Fatalf("after recovery: %+v, want alert %s resolved in place", all, firedID)
	}
}

// sloTestTime is a fixed "now", 45s into a minute, so events a few seconds
// earlier land in the same rollup minute.
var sloTestTime = time.Date(2026, 1, 15, 12, 30, 45, 0, time.UTC)

// newClockedPulse returns a Pulse whose clock reads *clock (initially
// sloTestTime) and whose rollups already cover the past week.
func newClockedPulse(t *testing.T) (*Pulse, *time.Time) {
	t.Helper()
	p := newTestPulse(t)
	clock := sloTestTime
	p.now = func() time.Time { return clock }
	p.rollups = newRollups(24*time.Hour, nil, sloTestTime.Add(-7*24*time.Hour))
	return p, &clock
}

// observeN records n requests to path with the given status at time at.
func observeN(p *Pulse, n int, path string, status int, at time.Time) {
	for i := 0; i < n; i++ {
		p.rollups.observe("GET", path, path, status, time.Millisecond, at)
	}
}

// TestSLOAPIEndpoint hits /pulse/api/slos and verifies the snapshot is
// returned as JSON. (Auth is checked by the JWT middleware in api_test.go;
// here we just check the route is wired and serializes.)
func TestSLOAPIEndpoint(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil,
		WithDevMode(),
		WithAppName("slo-api-test"),
		WithSLO(SLO{
			Name:      "demo",
			Target:    0.99,
			Window:    time.Hour,
			Indicator: SLIErrorRate{},
		}),
	)

	// Seed a request so the evaluator has something to count, then run a
	// synchronous tick.
	_ = p.storage.StoreRequest(RequestMetric{
		Path: "/x", StatusCode: 200, Timestamp: time.Now(),
	})
	if p.sloEvaluator == nil {
		t.Fatal("expected sloEvaluator to be running")
	}
	p.sloEvaluator.evaluate()

	// Sign a token and call the endpoint.
	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	req := httptest.NewRequest("GET", "/pulse/api/slos", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"demo"`) {
		t.Fatalf("body missing SLO name: %s", w.Body.String())
	}
}

// --- helpers ---

// TestSLOEvaluator_ResolvesInPlace checks a burn-rate firing → resolved cycle
// leaves one record carrying the original firing time and burn rate. v1.0.0
// stamped the resolution with the resolve time instead.
func TestSLOEvaluator_ResolvesInPlace(t *testing.T) {
	p := newTestPulse(t)
	slo := SLO{Name: "in-place", Target: 0.99, Window: time.Hour, Indicator: SLIErrorRate{}}
	ba := BurnRateAlert{Name: "fast-burn", Window: 5 * time.Minute, BurnRateMultiple: 2, Severity: "critical"}
	ev := &sloEvaluator{
		pulse:  p,
		slos:   []SLO{slo},
		status: make(map[string]SLOStatus),
		firing: make(map[string]map[string]sloFiring),
	}

	firedAt := time.Now().Add(-10 * time.Minute)
	ev.reconcileAlert(slo, ba, 30, 0.7, true, firedAt)
	ev.reconcileAlert(slo, ba, 0.5, 0.995, false, time.Now())

	alerts, _ := p.storage.GetAlerts(AlertFilter{})
	if len(alerts) != 1 {
		t.Fatalf("got %d alert records, want 1", len(alerts))
	}
	a := alerts[0]
	if a.State != AlertStateResolved || a.ResolvedAt == nil || !a.FiredAt.Equal(firedAt) || a.Value != 30 {
		t.Errorf("record not resolved in place: %+v", a)
	}
}

func newTestPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{DevMode: true})
	p := newPulse(context.Background(), cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = p.Shutdown() })
	return p
}
