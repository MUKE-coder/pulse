package pulse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupPromPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{
		Prometheus: PrometheusConfig{Enabled: true, Path: "/pulse/metrics"},
		Health:     HealthConfig{Enabled: boolPtr(false)},
		Runtime:    RuntimeConfig{Enabled: boolPtr(false)},
		Tracing:    TracingConfig{Enabled: boolPtr(false)},
		Errors:     ErrorConfig{Enabled: boolPtr(false)},
		Alerts:     AlertConfig{Enabled: boolPtr(false)},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })
	return p
}

func TestPrometheus_Endpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupPromPulse(t)

	router := gin.New()
	registerPrometheusRoute(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/metrics", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", contentType)
	}

	body := w.Body.String()
	if !strings.Contains(body, "pulse_uptime_seconds") {
		t.Error("expected pulse_uptime_seconds metric")
	}
}

func TestPrometheus_RequestMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupPromPulse(t)

	// Seed request data
	for i := 0; i < 10; i++ {
		status := 200
		if i < 3 {
			status = 500
		}
		p.storage.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       "/api/users",
			StatusCode: status,
			Latency:    50 * time.Millisecond,
			Timestamp:  time.Now(),
		})
	}

	router := gin.New()
	registerPrometheusRoute(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/metrics", nil)
	router.ServeHTTP(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "pulse_http_requests_total") {
		t.Error("expected pulse_http_requests_total metric")
	}
	if !strings.Contains(body, "pulse_http_request_duration_seconds") {
		t.Error("expected pulse_http_request_duration_seconds metric")
	}
	if !strings.Contains(body, "pulse_http_error_rate") {
		t.Error("expected pulse_http_error_rate metric")
	}
	if !strings.Contains(body, `/api/users`) {
		t.Error("expected path '/api/users' in metrics")
	}
}

func TestPrometheus_RuntimeMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupPromPulse(t)

	// Seed runtime data
	p.storage.StoreRuntime(RuntimeMetric{
		HeapAlloc:    10 * 1024 * 1024,
		HeapInUse:    8 * 1024 * 1024,
		Sys:          20 * 1024 * 1024,
		NumGoroutine: 42,
		GCPauseNs:    500000,
		NumGC:        15,
		Timestamp:    time.Now(),
	})

	router := gin.New()
	registerPrometheusRoute(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/metrics", nil)
	router.ServeHTTP(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "pulse_runtime_goroutines 42") {
		t.Error("expected goroutines gauge")
	}
	if !strings.Contains(body, "pulse_runtime_heap_bytes") {
		t.Error("expected heap bytes gauge")
	}
	if !strings.Contains(body, "pulse_runtime_gc_total 15") {
		t.Error("expected GC total counter")
	}
}

func TestPrometheus_HealthMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupPromPulse(t)

	p.AddHealthCheck(HealthCheck{Name: "db", Type: "database", Critical: true})
	p.storage.StoreHealthResult(HealthCheckResult{
		Name:    "db",
		Status:  "healthy",
		Latency: 5 * time.Millisecond,
	})

	router := gin.New()
	registerPrometheusRoute(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/metrics", nil)
	router.ServeHTTP(w, req)

	body := w.Body.String()

	if !strings.Contains(body, `pulse_health_check_status{name="db"} 1`) {
		t.Error("expected healthy status = 1")
	}
	if !strings.Contains(body, `pulse_health_check_duration_seconds{name="db"}`) {
		t.Error("expected health check duration")
	}
}

func TestPrometheus_ErrorMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupPromPulse(t)

	p.storage.StoreError(ErrorRecord{
		ID: "err1", Fingerprint: "fp1", Method: "GET", Route: "/test",
		ErrorMessage: "test error", ErrorType: ErrorTypeDatabase,
		Count: 5, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	router := gin.New()
	registerPrometheusRoute(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/metrics", nil)
	router.ServeHTTP(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "pulse_errors_total") {
		t.Error("expected pulse_errors_total metric")
	}
	if !strings.Contains(body, `type="database"`) {
		t.Error("expected database error type")
	}
}

func TestPrometheus_EmptyMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := setupPromPulse(t)

	router := gin.New()
	registerPrometheusRoute(router, p)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/metrics", nil)
	router.ServeHTTP(w, req)

	body := w.Body.String()

	// Should still have uptime even with no data
	if !strings.Contains(body, "pulse_uptime_seconds") {
		t.Error("expected uptime metric even with empty storage")
	}
}
