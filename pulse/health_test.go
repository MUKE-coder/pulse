package pulse

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupHealthPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{
		Health: HealthConfig{
			Enabled:       boolPtr(true),
			CheckInterval: 100 * time.Millisecond,
			Timeout:       5 * time.Second,
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })
	return p
}

func TestHealthRunner_RunsChecks(t *testing.T) {
	p := setupHealthPulse(t)

	p.AddHealthCheck(HealthCheck{
		Name:     "test-check",
		Type:     "custom",
		Critical: false,
		CheckFunc: func(ctx context.Context) error {
			return nil
		},
	})

	hr := newHealthRunner(p)

	// Wait for the runner to execute (initial delay 1s + first run)
	time.Sleep(1500 * time.Millisecond)

	history, _ := p.storage.GetHealthHistory("test-check", 10)
	if len(history) == 0 {
		t.Fatal("expected health check results after runner starts")
	}

	latest := history[len(history)-1]
	if latest.Status != "healthy" {
		t.Errorf("expected status 'healthy', got %q", latest.Status)
	}
	if latest.Name != "test-check" {
		t.Errorf("expected name 'test-check', got %q", latest.Name)
	}

	status := hr.GetCompositeStatus()
	if status != "healthy" {
		t.Errorf("expected composite status 'healthy', got %q", status)
	}
}

func TestHealthRunner_DetectsUnhealthy(t *testing.T) {
	p := setupHealthPulse(t)

	p.AddHealthCheck(HealthCheck{
		Name:     "failing-check",
		Type:     "custom",
		Critical: true,
		CheckFunc: func(ctx context.Context) error {
			return fmt.Errorf("connection refused")
		},
	})

	hr := newHealthRunner(p)
	time.Sleep(1500 * time.Millisecond)

	history, _ := p.storage.GetHealthHistory("failing-check", 10)
	if len(history) == 0 {
		t.Fatal("expected health check results")
	}

	latest := history[len(history)-1]
	if latest.Status != "unhealthy" {
		t.Errorf("expected status 'unhealthy', got %q", latest.Status)
	}
	if latest.Error == "" {
		t.Error("expected non-empty error message")
	}

	status := hr.GetCompositeStatus()
	if status != "unhealthy" {
		t.Errorf("expected composite 'unhealthy' with failing critical check, got %q", status)
	}
}

func TestHealthRunner_CompositeStatus_Degraded(t *testing.T) {
	p := setupHealthPulse(t)

	// Healthy critical check
	p.AddHealthCheck(HealthCheck{
		Name:     "db",
		Type:     "database",
		Critical: true,
		CheckFunc: func(ctx context.Context) error {
			return nil
		},
	})
	// Unhealthy non-critical check
	p.AddHealthCheck(HealthCheck{
		Name:     "cache",
		Type:     "redis",
		Critical: false,
		CheckFunc: func(ctx context.Context) error {
			return fmt.Errorf("redis timeout")
		},
	})

	hr := newHealthRunner(p)
	time.Sleep(1500 * time.Millisecond)

	status := hr.GetCompositeStatus()
	if status != "degraded" {
		t.Errorf("expected 'degraded', got %q", status)
	}
}

func TestHealthRunner_RunCheckByName(t *testing.T) {
	p := setupHealthPulse(t)

	callCount := 0
	p.AddHealthCheck(HealthCheck{
		Name: "on-demand",
		Type: "custom",
		CheckFunc: func(ctx context.Context) error {
			callCount++
			return nil
		},
	})

	hr := &HealthRunner{
		pulse:          p,
		compositeState: "healthy",
		flapping:       make(map[string]bool),
	}

	result, err := hr.RunCheckByName("on-demand")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "healthy" {
		t.Errorf("expected 'healthy', got %q", result.Status)
	}
	if callCount != 1 {
		t.Errorf("expected check to be called once, got %d", callCount)
	}

	// Non-existent check
	_, err = hr.RunCheckByName("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent check")
	}
}

func TestHealthRunner_FlappingDetection(t *testing.T) {
	p := setupHealthPulse(t)

	hr := &HealthRunner{
		pulse:          p,
		compositeState: "healthy",
		flapping:       make(map[string]bool),
	}

	// Simulate alternating results (flapping)
	for i := 0; i < 6; i++ {
		status := "healthy"
		if i%2 == 1 {
			status = "unhealthy"
		}
		p.storage.StoreHealthResult(HealthCheckResult{
			Name:      "flappy",
			Status:    status,
			Timestamp: time.Now(),
		})
	}

	hr.detectFlapping("flappy")
	if !hr.IsFlapping("flappy") {
		t.Error("expected 'flappy' to be detected as flapping")
	}

	// Stable check should not be flapping
	for i := 0; i < 6; i++ {
		p.storage.StoreHealthResult(HealthCheckResult{
			Name:      "stable",
			Status:    "healthy",
			Timestamp: time.Now(),
		})
	}
	hr.detectFlapping("stable")
	if hr.IsFlapping("stable") {
		t.Error("expected 'stable' not to be flapping")
	}
}

func TestHealthRunner_Timeout(t *testing.T) {
	cfg := applyDefaults(Config{
		Health: HealthConfig{
			Enabled: boolPtr(true),
			Timeout: 100 * time.Millisecond,
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	p.AddHealthCheck(HealthCheck{
		Name:    "slow",
		Type:    "custom",
		Timeout: 100 * time.Millisecond,
		CheckFunc: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		},
	})

	hr := &HealthRunner{
		pulse:          p,
		compositeState: "healthy",
		flapping:       make(map[string]bool),
	}

	result := hr.runCheck(p.healthChecks[0])
	if result.Status != "unhealthy" {
		t.Errorf("expected 'unhealthy' from timeout, got %q", result.Status)
	}
}

func TestDatabaseHealthCheck(t *testing.T) {
	check := DatabaseHealthCheck(nil)
	if check.Name != "database" {
		t.Errorf("expected name 'database', got %q", check.Name)
	}
	if !check.Critical {
		t.Error("expected database check to be critical")
	}
	if check.Type != "database" {
		t.Errorf("expected type 'database', got %q", check.Type)
	}
}

func TestHealthEndpoint_Healthy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupHealthPulse(t)
	p.healthRunner = &HealthRunner{
		pulse:          p,
		compositeState: "healthy",
		flapping:       make(map[string]bool),
	}

	p.AddHealthCheck(HealthCheck{
		Name: "ok-check", Type: "custom", Critical: true,
		CheckFunc: func(ctx context.Context) error { return nil },
	})

	// Manually store a healthy result
	p.storage.StoreHealthResult(HealthCheckResult{
		Name: "ok-check", Status: "healthy", Latency: 5 * time.Millisecond, Timestamp: time.Now(),
	})

	router := gin.New()
	registerHealthRoutes(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/health", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHealthEndpoint_Unhealthy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupHealthPulse(t)
	p.healthRunner = &HealthRunner{
		pulse:          p,
		compositeState: "unhealthy",
		flapping:       make(map[string]bool),
	}

	p.AddHealthCheck(HealthCheck{
		Name: "bad-check", Type: "custom", Critical: true,
		CheckFunc: func(ctx context.Context) error { return fmt.Errorf("fail") },
	})

	p.storage.StoreHealthResult(HealthCheckResult{
		Name: "bad-check", Status: "unhealthy", Error: "fail", Timestamp: time.Now(),
	})

	router := gin.New()
	registerHealthRoutes(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/health", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHealthEndpoint_LiveProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupHealthPulse(t)

	router := gin.New()
	registerHealthRoutes(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/health/live", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHealthEndpoint_ReadyProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupHealthPulse(t)
	p.healthRunner = &HealthRunner{
		pulse:          p,
		compositeState: "healthy",
		flapping:       make(map[string]bool),
	}

	router := gin.New()
	registerHealthRoutes(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/health/ready", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Unhealthy composite
	p.healthRunner.mu.Lock()
	p.healthRunner.compositeState = "unhealthy"
	p.healthRunner.mu.Unlock()

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/pulse/health/ready", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestBuildHealthResponse(t *testing.T) {
	p := setupHealthPulse(t)

	p.AddHealthCheck(HealthCheck{Name: "db", Type: "database", Critical: true})
	p.AddHealthCheck(HealthCheck{Name: "cache", Type: "redis", Critical: false})

	p.storage.StoreHealthResult(HealthCheckResult{
		Name: "db", Status: "healthy", Latency: 2 * time.Millisecond, Timestamp: time.Now(),
	})
	p.storage.StoreHealthResult(HealthCheckResult{
		Name: "cache", Status: "unhealthy", Error: "timeout", Latency: 100 * time.Millisecond, Timestamp: time.Now(),
	})

	resp := buildHealthResponse(p)

	if resp.Status != "degraded" {
		t.Errorf("expected 'degraded', got %q", resp.Status)
	}
	if len(resp.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(resp.Checks))
	}
	if resp.Checks["db"].Status != "healthy" {
		t.Errorf("expected db healthy, got %q", resp.Checks["db"].Status)
	}
	if resp.Checks["cache"].Status != "unhealthy" {
		t.Errorf("expected cache unhealthy, got %q", resp.Checks["cache"].Status)
	}
	if resp.Uptime == "" {
		t.Error("expected non-empty uptime")
	}
}
