package pulse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Registering a run's start begins a CPU profile; registering its end keeps
// it against the run. That profile is the thing no external tool can give
// you, because it comes from inside the process under load (issue #11).
func TestTestRun_ProfileCapturedOverTheRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := applyDefaults(Config{})
	cfg.Dashboard.Username, cfg.Dashboard.Password = "admin", "testpass"
	cfg.Dashboard.SecretKey = "test-secret-key-for-jwt"
	cfg.Profiling.Enabled = Bool(true)
	cfg.Profiling.MaxTestRunProfile = 10 * time.Second
	t.Setenv(ProfileEnabledEnv, "true")

	p := newPulse(context.Background(), cfg)
	p.storage = NewMemoryStorage("test")
	p.profileSampler = newProfileSampler(p)
	p.runProfiler = newRunProfiler(p)
	t.Cleanup(func() { _ = p.Shutdown() })

	router := gin.New()
	registerAPIRoutes(router, p)
	token := loginAndGetToken(t, router)

	start := time.Now().Add(-time.Second)
	body := fmt.Sprintf(`{"id":"run-1","name":"average-load","type":"k6","started_at":%q}`,
		start.Format(time.RFC3339Nano))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("POST", "/pulse/api/test-runs", token, body))
	if w.Code != http.StatusCreated {
		t.Fatalf("start: status %d: %s", w.Code, w.Body.String())
	}
	var started struct {
		Profiling bool `json:"profiling"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &started)
	if !started.Profiling {
		t.Fatal("the start call should report that the run is being profiled")
	}
	if p.runProfiler.profiling() != "run-1" {
		t.Fatalf("profiling %q, want run-1", p.runProfiler.profiling())
	}

	// Something for the profile to find while the "load test" runs.
	stop := make(chan struct{})
	go burnCPUForTest(stop)
	time.Sleep(250 * time.Millisecond)
	close(stop)

	body = fmt.Sprintf(`{"id":"run-1","name":"average-load","type":"k6","started_at":%q,"ended_at":%q}`,
		start.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("POST", "/pulse/api/test-runs", token, body))
	if w.Code != http.StatusCreated {
		t.Fatalf("end: status %d: %s", w.Code, w.Body.String())
	}
	if p.runProfiler.profiling() != "" {
		t.Error("the profile should have stopped when the run ended")
	}

	// The profile is served as pprof by default, and as a flame graph on ask.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/test-runs/run-1/profile", token, ""))
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("profile: status %d, %d bytes", w.Code, w.Body.Len())
	}
	if _, _, err := foldCPUProfile(w.Body.Bytes()); err != nil {
		t.Errorf("stored bytes are not a pprof profile: %v", err)
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/test-runs/run-1/profile?format=svg", token, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<svg") {
		t.Errorf("svg: status %d, body starts %q", w.Code, firstLines(w.Body.String(), 1))
	}

	// And the comparison says it is there.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/test-runs/run-1/compare", token, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("compare: status %d: %s", w.Code, w.Body.String())
	}
	var cmp struct {
		Profile *struct {
			Available bool `json:"available"`
			Bytes     int  `json:"bytes"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cmp); err != nil {
		t.Fatalf("decode compare: %v", err)
	}
	if cmp.Profile == nil || !cmp.Profile.Available || cmp.Profile.Bytes == 0 {
		t.Errorf("the comparison should carry the profile: %+v", cmp.Profile)
	}
}

// With profiling off, registering a run still works — it just says no
// profile is being taken.
func TestTestRun_ProfilingStaysOptIn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p, router := setupAPIPulse(t)
	p.runProfiler = newRunProfiler(p)
	token := loginAndGetToken(t, router)

	body := `{"id":"run-2","name":"spike","started_at":"2026-10-16T08:00:00Z"}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("POST", "/pulse/api/test-runs", token, body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Profiling bool `json:"profiling"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Profiling {
		t.Error("no profile should be taken while profiling is off")
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/test-runs/run-2/profile", token, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 with a hint: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "profiling") {
		t.Errorf("the 404 should say how to get a profile: %s", w.Body.String())
	}
}

// Every backend must keep a run's profile, or the Test Runs page offers a
// flame graph on Memory and a dash on SQLite.
func TestConformance_TestRunProfilesRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		store, ok := s.(testRunProfileStore)
		if !ok {
			t.Fatalf("%T keeps no run profiles", s)
		}
		captured := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
		data := []byte{0x1f, 0x8b, 0x08, 0x00, 'p', 'u', 'l', 's', 'e'}

		if err := store.saveTestRunProfile("run-x", captured, data); err != nil {
			t.Fatalf("save: %v", err)
		}
		got, at, err := store.testRunProfile("run-x")
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != string(data) {
			t.Errorf("profile came back as %v", got)
		}
		if !at.Equal(captured) {
			t.Errorf("captured at %v, want %v", at, captured)
		}

		// A re-registered run replaces its profile rather than adding one.
		if err := store.saveTestRunProfile("run-x", captured.Add(time.Minute), []byte("second")); err != nil {
			t.Fatalf("re-save: %v", err)
		}
		if got, _, _ = store.testRunProfile("run-x"); string(got) != "second" {
			t.Errorf("the newer profile should have replaced the older: %q", got)
		}

		// An unknown run is empty, not an error.
		if got, _, err = store.testRunProfile("nope"); err != nil || len(got) != 0 {
			t.Errorf("unknown run: %v %q", err, got)
		}
	})
}

// A load test hammers a couple of endpoints; the global average hides the
// one that fell over.
func TestCompareTestRun_BreaksDownPerRoute(t *testing.T) {
	now := time.Date(2026, 3, 8, 12, 30, 0, 0, time.UTC)
	r := newRollups(24*time.Hour, nil, now.Add(-time.Hour))

	runStart := now.Add(-10 * time.Minute)
	runEnd := now.Add(-5 * time.Minute)

	// Before: both routes quick. During: /checkout falls over, /health doesn't.
	for i := 0; i < 60; i++ {
		at := runStart.Add(-time.Duration(i+1) * 5 * time.Second)
		r.observe("GET", "/checkout", "/checkout", 200, 50*time.Millisecond, at)
		r.observe("GET", "/health", "/health", 200, 5*time.Millisecond, at)
	}
	for i := 0; i < 60; i++ {
		at := runStart.Add(time.Duration(i) * 5 * time.Second)
		if at.After(runEnd) {
			break
		}
		r.observe("GET", "/checkout", "/checkout", 500, 400*time.Millisecond, at)
		r.observe("GET", "/health", "/health", 200, 6*time.Millisecond, at)
	}

	cmp := compareTestRun(r, TestRun{ID: "r1", Name: "spike", StartedAt: runStart, EndedAt: runEnd}, now)
	if len(cmp.Routes) == 0 {
		t.Fatal("no per-route breakdown")
	}
	byRoute := map[string]testRunRoute{}
	for _, row := range cmp.Routes {
		byRoute[row.Route] = row
	}
	checkout, health := byRoute["/checkout"], byRoute["/health"]
	if checkout.During.Requests == 0 || health.During.Requests == 0 {
		t.Fatalf("both routes should appear: %+v", cmp.Routes)
	}
	if checkout.SlowdownRatio < 5 {
		t.Errorf("/checkout went 50ms → 400ms, ratio reads %.1f", checkout.SlowdownRatio)
	}
	if health.SlowdownRatio > 2 {
		t.Errorf("/health barely moved, ratio reads %.1f", health.SlowdownRatio)
	}
	if checkout.During.ErrorRate < 99 {
		t.Errorf("/checkout returned 500s during the run, error rate reads %.1f", checkout.During.ErrorRate)
	}
	if health.During.ErrorRate != 0 {
		t.Errorf("/health had no errors, reads %.1f", health.During.ErrorRate)
	}
}
