package pulse

import (
	"context"
	"net/http/httptest"
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

// TestSLOEvaluator_ComputesCompliance feeds the storage a known mix of good
// and bad requests, runs evaluate(), and checks the resulting snapshot.
func TestSLOEvaluator_ComputesCompliance(t *testing.T) {
	p := newTestPulse(t)

	slo := SLO{
		Name:      "API availability",
		Target:    0.99,
		Window:    time.Hour,
		Indicator: SLIErrorRate{Routes: []string{"/api/*"}},
		// 95% compliance against 99% target → 5× burn rate. Set the
		// threshold below 5 so the firing branch exercises.
		BurnRateAlerts: []BurnRateAlert{{Name: "fast-burn", Window: 5 * time.Minute, BurnRateMultiple: 2.0, Severity: "critical"}},
	}

	// 95 good + 5 bad over the last minute → compliance 0.95 < target 0.99
	now := time.Now()
	for i := 0; i < 95; i++ {
		_ = p.storage.StoreRequest(RequestMetric{
			Path: "/api/users", StatusCode: 200, Timestamp: now.Add(-30 * time.Second),
		})
	}
	for i := 0; i < 5; i++ {
		_ = p.storage.StoreRequest(RequestMetric{
			Path: "/api/users", StatusCode: 500, Timestamp: now.Add(-30 * time.Second),
		})
	}

	ev := newSLOEvaluator(p, []SLO{slo})
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

// TestSLOEvaluator_FiresAndResolves walks the firing → resolved transition
// and confirms both alerts land in storage.
func TestSLOEvaluator_FiresAndResolves(t *testing.T) {
	p := newTestPulse(t)
	slo := SLO{
		Name:      "Test SLO",
		Target:    0.99,
		Window:    time.Hour,
		Indicator: SLIErrorRate{Routes: []string{"/api/*"}},
		BurnRateAlerts: []BurnRateAlert{
			{Name: "fast-burn", Window: 5 * time.Minute, BurnRateMultiple: 2.0, Severity: "critical"},
		},
	}

	now := time.Now()
	// Bad slice: 70 good / 30 bad → 30% error rate, target budget 1% → 30× burn
	for i := 0; i < 70; i++ {
		_ = p.storage.StoreRequest(RequestMetric{
			Path: "/api/x", StatusCode: 200, Timestamp: now.Add(-30 * time.Second),
		})
	}
	for i := 0; i < 30; i++ {
		_ = p.storage.StoreRequest(RequestMetric{
			Path: "/api/x", StatusCode: 500, Timestamp: now.Add(-30 * time.Second),
		})
	}

	ev := newSLOEvaluator(p, []SLO{slo})
	ev.evaluate()

	firing, _ := p.storage.GetAlerts(AlertFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
		State:     AlertStateFiring,
	})
	if len(firing) != 1 {
		t.Fatalf("expected one firing burn-rate alert, got %d", len(firing))
	}
	if !strings.HasPrefix(firing[0].RuleName, "slo:Test SLO:") {
		t.Fatalf("rule name = %q, want slo:Test SLO:* prefix", firing[0].RuleName)
	}

	// Now reset and feed an all-good window so the burn-rate falls below threshold.
	_ = p.storage.Reset()
	for i := 0; i < 100; i++ {
		_ = p.storage.StoreRequest(RequestMetric{
			Path: "/api/x", StatusCode: 200, Timestamp: now.Add(-15 * time.Second),
		})
	}
	ev.evaluate()

	resolved, _ := p.storage.GetAlerts(AlertFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
		State:     AlertStateResolved,
	})
	if len(resolved) != 1 {
		t.Fatalf("expected one resolved alert, got %d", len(resolved))
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

func newTestPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{DevMode: true})
	p := newPulse(context.Background(), cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = p.Shutdown() })
	return p
}
