package pulse

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestRouter(cfg ...Config) (*gin.Engine, *Pulse) {
	router := gin.New()
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}
	p := Mount(router, nil, c)

	// Add test routes
	router.GET("/users", func(c *gin.Context) {
		c.JSON(200, gin.H{"users": []string{}})
	})
	router.GET("/users/:id", func(c *gin.Context) {
		c.JSON(200, gin.H{"id": c.Param("id")})
	})
	router.POST("/users", func(c *gin.Context) {
		c.JSON(201, gin.H{"created": true})
	})
	router.GET("/slow", func(c *gin.Context) {
		time.Sleep(50 * time.Millisecond)
		c.JSON(200, gin.H{"slow": true})
	})
	router.GET("/error", func(c *gin.Context) {
		c.JSON(500, gin.H{"error": "internal"})
	})

	return router, p
}

func TestMiddleware_TracesRequests(t *testing.T) {
	router, p := setupTestRouter()

	req := httptest.NewRequest("GET", "/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Check trace ID header
	traceID := w.Header().Get(TraceIDHeader)
	if traceID == "" {
		t.Fatal("expected trace ID header")
	}
	if len(traceID) != 32 {
		t.Fatalf("expected 32 char trace ID, got %d: %s", len(traceID), traceID)
	}

	// Wait for async storage
	time.Sleep(50 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 1 {
		t.Fatalf("expected 1 stored request, got %d", len(reqs))
	}

	m := reqs[0]
	if m.Method != "GET" {
		t.Errorf("expected GET, got %s", m.Method)
	}
	if m.Path != "/users" {
		t.Errorf("expected /users, got %s", m.Path)
	}
	if m.StatusCode != 200 {
		t.Errorf("expected 200, got %d", m.StatusCode)
	}
	if m.TraceID != traceID {
		t.Errorf("trace ID mismatch: %s vs %s", m.TraceID, traceID)
	}
	if m.Latency < 0 {
		t.Errorf("expected non-negative latency, got %v", m.Latency)
	}
}

func TestMiddleware_CapturesRoutePattern(t *testing.T) {
	router, p := setupTestRouter()

	req := httptest.NewRequest("GET", "/users/42", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	time.Sleep(50 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	// Should use route pattern, not actual path
	if reqs[0].Path != "/users/:id" {
		t.Errorf("expected route pattern /users/:id, got %s", reqs[0].Path)
	}
}

func TestMiddleware_ExcludesPulsePaths(t *testing.T) {
	router, p := setupTestRouter()

	// Request to /pulse/ should be excluded
	req := httptest.NewRequest("GET", "/pulse/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	time.Sleep(50 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 0 {
		t.Fatalf("expected 0 requests for excluded path, got %d", len(reqs))
	}
}

func TestMiddleware_ExcludesCustomPaths(t *testing.T) {
	router, p := setupTestRouter(Config{
		Tracing: TracingConfig{
			Enabled:      boolPtr(true),
			SampleRate:   float64Ptr(1.0),
			ExcludePaths: []string{"/users"},
		},
	})

	req := httptest.NewRequest("GET", "/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	time.Sleep(50 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 0 {
		t.Fatalf("expected 0 requests for excluded path, got %d", len(reqs))
	}
}

func TestMiddleware_CapturesStatusCodes(t *testing.T) {
	router, p := setupTestRouter()

	tests := []struct {
		path   string
		method string
		status int
	}{
		{"/users", "GET", 200},
		{"/users", "POST", 201},
		{"/error", "GET", 500},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != tt.status {
			t.Errorf("%s %s: expected %d, got %d", tt.method, tt.path, tt.status, w.Code)
		}
	}

	time.Sleep(100 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(reqs))
	}
}

func TestMiddleware_Sampling(t *testing.T) {
	router, p := setupTestRouter(Config{
		Tracing: TracingConfig{
			Enabled:    boolPtr(true),
			SampleRate: float64Ptr(0.0), // Sample nothing (except errors/slow)
		},
	})

	// Normal request should be skipped
	req := httptest.NewRequest("GET", "/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	time.Sleep(50 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 0 {
		t.Fatalf("expected 0 requests with 0 sample rate, got %d", len(reqs))
	}

	// Error request should still be recorded
	req = httptest.NewRequest("GET", "/error", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	time.Sleep(50 * time.Millisecond)

	reqs, _ = p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request (error always recorded), got %d", len(reqs))
	}
}

func TestMiddleware_SlowRequestAlwaysRecorded(t *testing.T) {
	router, p := setupTestRouter(Config{
		Tracing: TracingConfig{
			Enabled:              boolPtr(true),
			SampleRate:           float64Ptr(0.0),
			SlowRequestThreshold: 10 * time.Millisecond, // 10ms threshold
		},
	})

	req := httptest.NewRequest("GET", "/slow", nil) // sleeps 50ms
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	time.Sleep(100 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 1 {
		t.Fatalf("expected 1 slow request recorded, got %d", len(reqs))
	}
}

func TestMiddleware_TracingDisabled(t *testing.T) {
	router, p := setupTestRouter(Config{
		Tracing: TracingConfig{
			Enabled: boolPtr(false),
		},
	})

	req := httptest.NewRequest("GET", "/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// No trace header when disabled
	traceID := w.Header().Get(TraceIDHeader)
	if traceID != "" {
		t.Fatal("expected no trace ID when tracing disabled")
	}

	time.Sleep(50 * time.Millisecond)

	reqs, _ := p.storage.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute)},
	})
	if len(reqs) != 0 {
		t.Fatalf("expected 0 requests when tracing disabled, got %d", len(reqs))
	}
}

func TestShouldExclude(t *testing.T) {
	patterns := []string{"/pulse/*", "/favicon.ico", "/health"}

	tests := []struct {
		path    string
		exclude bool
	}{
		{"/pulse/", true},
		{"/pulse/api/overview", true},
		{"/pulse", true},
		{"/favicon.ico", true},
		{"/health", true},
		{"/users", false},
		{"/api/data", false},
	}

	for _, tt := range tests {
		if got := shouldExclude(tt.path, patterns); got != tt.exclude {
			t.Errorf("shouldExclude(%q) = %v, want %v", tt.path, got, tt.exclude)
		}
	}
}

func TestShouldSample(t *testing.T) {
	if !shouldSample(1.0) {
		t.Error("expected true for rate 1.0")
	}
	if shouldSample(0.0) {
		t.Error("expected false for rate 0.0")
	}

	// Statistical test for 50% rate
	hits := 0
	for i := 0; i < 10000; i++ {
		if shouldSample(0.5) {
			hits++
		}
	}
	// Should be roughly 50% (allow 40-60% range)
	rate := float64(hits) / 10000.0
	if rate < 0.4 || rate > 0.6 {
		t.Errorf("expected ~50%% sample rate, got %.1f%%", rate*100)
	}
}

func TestGenerateTraceID(t *testing.T) {
	id1 := GenerateTraceID()
	id2 := GenerateTraceID()

	if len(id1) != 32 {
		t.Fatalf("expected 32 char ID, got %d", len(id1))
	}
	if id1 == id2 {
		t.Fatal("expected unique trace IDs")
	}
}

func BenchmarkMiddleware(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	p := &Pulse{
		config:  applyDefaults(Config{}),
		storage: NewMemoryStorage("bench"),
	}
	router.Use(newTracingMiddleware(p))
	router.GET("/bench", func(c *gin.Context) {
		c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/bench", nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

func BenchmarkMiddleware_ExcludedPath(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	p := &Pulse{
		config:  applyDefaults(Config{}),
		storage: NewMemoryStorage("bench"),
	}
	router.Use(newTracingMiddleware(p))
	router.GET("/pulse/api/test", func(c *gin.Context) {
		c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/pulse/api/test", nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

func BenchmarkGenerateTraceID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		GenerateTraceID()
	}
}

// Verify responseWriter satisfies http.Hijacker
var _ http.Hijacker = (*responseWriter)(nil)
