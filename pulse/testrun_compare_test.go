package pulse

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The review's scenario: p95 climbs from ~80ms to ~1100ms during a spike
// test, then recovers. The comparison must show both, and when recovery
// happened.
func TestCompareTestRun_SpikeAndRecovery(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	r := newRollups(24*time.Hour, nil, start.Add(-time.Hour))

	perMinute := func(from time.Time, minutes int, latency time.Duration, errorsEvery int) {
		for m := 0; m < minutes; m++ {
			for i := 0; i < 60; i++ {
				status := 200
				if errorsEvery > 0 && i%errorsEvery == 0 {
					status = 500
				}
				r.observe("GET", "/api/x", "/api/x", status, latency, from.Add(time.Duration(m)*time.Minute+time.Duration(i)*time.Second))
			}
		}
	}
	perMinute(start.Add(-5*time.Minute), 5, 80*time.Millisecond, 0) // baseline
	perMinute(start, 5, 1100*time.Millisecond, 10)                  // spike: 10% errors
	perMinute(end, 2, 900*time.Millisecond, 0)                      // still slow
	perMinute(end.Add(2*time.Minute), 8, 82*time.Millisecond, 0)    // recovered

	cmp := compareTestRun(r, TestRun{ID: "spike", Name: "spike", StartedAt: start, EndedAt: end}, end.Add(10*time.Minute))

	within := func(got, want float64) bool { return math.Abs(got-want)/want <= 0.125 }
	if !within(cmp.Before.P95Ms, 80) || !within(cmp.During.P95Ms, 1100) {
		t.Errorf("p95 before/during = %.0fms / %.0fms, want ~80 / ~1100", cmp.Before.P95Ms, cmp.During.P95Ms)
	}
	if cmp.Before.Requests != 300 || cmp.During.Requests != 300 || cmp.During.RPS != 1 {
		t.Errorf("requests before/during = %d / %d at %.1f rps, want 300 / 300 at 1 rps",
			cmp.Before.Requests, cmp.During.Requests, cmp.During.RPS)
	}
	if cmp.During.ErrorRate != 10 || cmp.Before.ErrorRate != 0 {
		t.Errorf("error rate before/during = %.1f%% / %.1f%%, want 0 / 10", cmp.Before.ErrorRate, cmp.During.ErrorRate)
	}
	if !cmp.Degraded {
		t.Error("expected Degraded")
	}
	if cmp.RecoverySeconds == nil || *cmp.RecoverySeconds != 120 {
		t.Fatalf("recovery = %v, want 120s (p95 back to baseline two minutes after the run)", cmp.RecoverySeconds)
	}
	if !cmp.After.From.Equal(end) || !cmp.After.To.Equal(end.Add(5*time.Minute)) {
		t.Errorf("after window = %s–%s, want the 5 minutes after the run", cmp.After.From, cmp.After.To)
	}
}

// No recovery yet, and nothing to compare against, both yield nil.
func TestCompareTestRun_NoRecoveryYet(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	r := newRollups(24*time.Hour, nil, start.Add(-time.Hour))
	for i := 0; i < 120; i++ {
		r.observe("GET", "/x", "/x", 200, 50*time.Millisecond, start.Add(-2*time.Minute+time.Duration(i)*time.Second))
		r.observe("GET", "/x", "/x", 200, 900*time.Millisecond, start.Add(time.Duration(i)*time.Second))
		r.observe("GET", "/x", "/x", 200, 900*time.Millisecond, end.Add(time.Duration(i)*time.Second))
	}
	cmp := compareTestRun(r, TestRun{StartedAt: start, EndedAt: end}, end.Add(2*time.Minute))
	if cmp.RecoverySeconds != nil {
		t.Errorf("recovery = %v, want nil while p95 is still elevated", *cmp.RecoverySeconds)
	}

	quiet := newRollups(24*time.Hour, nil, start.Add(-time.Hour))
	if cmp := compareTestRun(quiet, TestRun{StartedAt: start, EndedAt: end}, end.Add(time.Hour)); cmp.RecoverySeconds != nil || cmp.Degraded {
		t.Errorf("no traffic: %+v, want no recovery and not degraded", cmp)
	}
}

func TestTestRunCompareAPI(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode())
	t.Cleanup(func() { _ = p.Shutdown() })
	token := signJWT(jwtClaims{Username: "t", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix()},
		p.config.Dashboard.SecretKey)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	started := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	if w := do("POST", "/pulse/api/test-runs", `{"id":"run-1","name":"spike","started_at":"`+started+`"}`); w.Code != 201 {
		t.Fatalf("create run: %d %s", w.Code, w.Body.String())
	}

	w := do("GET", "/pulse/api/test-runs/run-1/compare", "")
	if w.Code != 200 {
		t.Fatalf("compare: %d %s", w.Code, w.Body.String())
	}
	var cmp testRunComparison
	if err := json.Unmarshal(w.Body.Bytes(), &cmp); err != nil || cmp.Run.ID != "run-1" || cmp.Resolution != "1m" {
		t.Fatalf("compare body = %s (err %v)", w.Body.String(), err)
	}

	if w := do("GET", "/pulse/api/test-runs/nope/compare", ""); w.Code != 404 {
		t.Fatalf("unknown run: status %d, want 404", w.Code)
	}
}
