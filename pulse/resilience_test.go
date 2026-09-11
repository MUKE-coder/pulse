package pulse

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// A check that ignores its context must not block the other checks, must
// be reported unhealthy at its timeout, and must not be started again while
// its previous run is stuck.
func TestHealthRunner_HungCheckIsIsolated(t *testing.T) {
	p := setupHealthPulse(t) // 100ms interval
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	var calls atomic.Int32
	p.AddHealthCheck(HealthCheck{
		Name: "hung", Type: "custom", Critical: true, Timeout: 100 * time.Millisecond,
		CheckFunc: func(context.Context) error { // ignores ctx
			calls.Add(1)
			<-release
			return nil
		},
	})
	p.AddHealthCheck(HealthCheck{
		Name: "fine", Type: "custom", CheckFunc: func(context.Context) error { return nil },
	})

	hr := newHealthRunner(p)
	time.Sleep(1800 * time.Millisecond)

	results := hr.latestResults()
	if r := results["hung"]; r.Status != "unhealthy" || r.Error == "" {
		t.Errorf("hung check = %+v, want unhealthy with an error", r)
	}
	if r := results["fine"]; r.Status != "healthy" {
		t.Errorf("fine check = %+v, want healthy despite the hung one", r)
	}
	if hr.GetCompositeStatus() != "unhealthy" {
		t.Errorf("composite = %s, want unhealthy (the hung check is critical)", hr.GetCompositeStatus())
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("hung CheckFunc started %d times, want 1 (no pile-up while it is stuck)", n)
	}
}

// Each check runs on its own Interval rather than only the global one.
func TestHealthRunner_HonorsPerCheckInterval(t *testing.T) {
	cfg := applyDefaults(Config{Health: HealthConfig{Enabled: boolPtr(true), CheckInterval: time.Hour}})
	p := newPulse(context.Background(), cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = p.Shutdown() })

	var fast, slow atomic.Int32
	p.AddHealthCheck(HealthCheck{Name: "fast", Interval: 200 * time.Millisecond,
		CheckFunc: func(context.Context) error { fast.Add(1); return nil }})
	p.AddHealthCheck(HealthCheck{Name: "slow",
		CheckFunc: func(context.Context) error { slow.Add(1); return nil }})

	newHealthRunner(p)
	time.Sleep(2 * time.Second)

	if n := fast.Load(); n < 3 {
		t.Errorf("fast check ran %d times in ~1s of scheduling, want at least 3 (Interval 200ms)", n)
	}
	if n := slow.Load(); n != 1 {
		t.Errorf("slow check ran %d times, want 1 (global interval 1h)", n)
	}
}

func TestHealthRunner_PanickingCheckFails(t *testing.T) {
	p := setupHealthPulse(t)
	p.AddHealthCheck(HealthCheck{Name: "boom", CheckFunc: func(context.Context) error { panic("kaboom") }})
	hr := &HealthRunner{pulse: p, compositeState: "healthy", flapping: map[string]bool{}}
	if r := hr.runCheck(p.healthChecks[0]); r.Status != "unhealthy" || !strings.Contains(r.Error, "kaboom") {
		t.Fatalf("result = %+v, want unhealthy mentioning the panic", r)
	}
}

// The health endpoint answers from the runner's cache, without storage.
func TestHealthEndpoint_ServesCachedResults(t *testing.T) {
	p := setupHealthPulse(t)
	p.AddHealthCheck(HealthCheck{Name: "db", Critical: true})
	hr := &HealthRunner{pulse: p, compositeState: "healthy", flapping: map[string]bool{}}
	hr.record(HealthCheckResult{Name: "db", Status: "unhealthy", Error: "down", Timestamp: time.Now()})
	p.healthRunner = hr

	router := gin.New()
	registerHealthRoutes(router, p)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/pulse/health", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"error":"down"`) {
		t.Fatalf("status %d body %s, want 503 from the cached result (storage holds nothing)", w.Code, w.Body.String())
	}
}

func TestInternalErrors_RateLimitedLogging(t *testing.T) {
	t.Parallel()
	var ie internalErrors
	start := time.Now()
	logged := 0
	for i := 0; i < 5; i++ {
		if log, _ := ie.record("storage: requests", errors.New("disk full"), start.Add(time.Duration(i)*time.Second), false); log {
			logged++
		}
	}
	if logged != 1 {
		t.Errorf("logged %d of 5 errors within a minute, want 1", logged)
	}
	log, skipped := ie.record("storage: requests", errors.New("disk full"), start.Add(2*time.Minute), false)
	if !log || skipped != 4 {
		t.Errorf("after a minute: log=%v skipped=%d, want a log line reporting 4 skipped", log, skipped)
	}
	if n := ie.snapshot()["storage: requests"]; n != 6 {
		t.Errorf("count = %d, want 6", n)
	}
}

// With SQLite, dropped writes turn the pulse_storage check unhealthy until
// a check passes with no new failures.
func TestStorageHealthCheck_ReportsNewFailures(t *testing.T) {
	s, err := openSQLiteStorage(":memory:", "test", 4)
	if err != nil {
		t.Fatal(err)
	}
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = s
	t.Cleanup(func() { _ = p.Shutdown() })
	check := storageHealthCheck(p)

	if err := check.CheckFunc(context.Background()); err != nil {
		t.Fatalf("fresh storage: %v", err)
	}
	s.writeMu.Lock()
	for i := 0; i < 100; i++ {
		p.internalError("storage: requests", s.StoreRequest(RequestMetric{Timestamp: time.Now()}))
	}
	s.writeMu.Unlock()

	if err := check.CheckFunc(context.Background()); err == nil || !strings.Contains(err.Error(), "failed since the last check") {
		t.Fatalf("after drops: %v, want a failure report", err)
	}
	if err := check.CheckFunc(context.Background()); err != nil {
		t.Fatalf("no new failures: %v, want healthy again", err)
	}
	if !strings.Contains(buildPrometheusMetrics(p), `pulse_internal_errors_total{component="storage: requests"}`) {
		t.Error("internal errors missing from Prometheus output")
	}
}

// A full notification queue drops and counts rather than blocking the
// alert engine.
func TestAlertEngine_NotificationQueueFullDrops(t *testing.T) {
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = p.Shutdown() })
	ae := &AlertEngine{pulse: p, ruleStates: map[string]*ruleState{}, notifyQ: make(chan AlertRecord, 1)}

	ae.notify(AlertRecord{RuleName: "a", State: AlertStateFiring})
	ae.notify(AlertRecord{RuleName: "b", State: AlertStateFiring}) // queue full, no workers
	if n := p.internal.snapshot()["notifications"]; n != 1 {
		t.Fatalf("dropped notifications counted = %d, want 1", n)
	}
}

// An SMTP server that accepts the connection but never speaks must not hang
// the sender.
func TestSendMail_TimesOutOnSilentServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // hold it open, say nothing
		}
	}()

	old := smtpTimeout
	smtpTimeout = 300 * time.Millisecond
	t.Cleanup(func() { smtpTimeout = old })

	start := time.Now()
	err = sendMail(ln.Addr().String(), nil, "pulse@example.com", []string{"ops@example.com"}, []byte("hi"))
	if err == nil {
		t.Fatal("expected an error from a silent server")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("sendMail took %s against a silent server, want it bounded by the timeout", elapsed)
	}
	if err := sendMail("x:25", nil, "a@b.c\r\nBcc: evil@x", []string{"o@x"}, nil); err == nil {
		t.Fatal("expected header injection to be refused")
	}
}

// Webhook failures are logged without the URL — chat webhook URLs embed
// their credentials.
func TestNotificationError_OmitsWebhookURL(t *testing.T) {
	t.Parallel()
	// http.Client errors are *url.Error, whose message embeds the URL.
	err := &url.Error{Op: "Post", URL: "https://hooks.slack.com/services/T0/B0/SECRET", Err: errors.New("connection refused")}
	if !strings.Contains(err.Error(), "SECRET") {
		t.Fatal("test setup: the raw error should contain the secret")
	}
	got := notificationError("slack", fmt.Errorf("delivering: %w", err)).Error()
	if strings.Contains(got, "SECRET") || !strings.Contains(got, "connection refused") {
		t.Fatalf("notification error = %q, want the cause without the webhook URL", got)
	}
}
