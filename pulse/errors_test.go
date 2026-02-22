package pulse

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupErrorTestPulse() *Pulse {
	cfg := applyDefaults(Config{
		Errors: ErrorConfig{
			Enabled:            boolPtr(true),
			CaptureStackTrace:  boolPtr(true),
			CaptureRequestBody: boolPtr(true),
			MaxBodySize:        4096,
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	return p
}

func TestErrorMiddleware_CapturesPanic(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panic", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}

	// Give async storage a moment
	time.Sleep(50 * time.Millisecond)

	errors, err := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(errors) == 0 {
		t.Fatal("expected at least 1 error record from panic")
	}

	found := false
	for _, e := range errors {
		if e.ErrorType == ErrorTypePanic && strings.Contains(e.ErrorMessage, "test panic") {
			found = true
			if e.StackTrace == "" {
				t.Error("expected stack trace for panic")
			}
			if e.Method != "GET" {
				t.Errorf("expected method GET, got %q", e.Method)
			}
			if e.Fingerprint == "" {
				t.Error("expected non-empty fingerprint")
			}
		}
	}
	if !found {
		t.Error("expected to find panic error record")
	}
}

func TestErrorMiddleware_CapturesGinError(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.GET("/error", func(c *gin.Context) {
		c.Error(fmt.Errorf("validation failed: field is required"))
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/error", nil)
	router.ServeHTTP(w, req)

	time.Sleep(100 * time.Millisecond)

	errors, _ := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})

	if len(errors) == 0 {
		t.Fatal("expected at least 1 error record from Gin error")
	}

	found := false
	for _, e := range errors {
		if strings.Contains(e.ErrorMessage, "validation failed") {
			found = true
			if e.ErrorType != ErrorTypeValidation {
				t.Errorf("expected error type %q, got %q", ErrorTypeValidation, e.ErrorType)
			}
		}
	}
	if !found {
		t.Error("expected to find validation error record")
	}
}

func TestErrorMiddleware_Captures5xxStatus(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.GET("/server-error", func(c *gin.Context) {
		c.Status(http.StatusInternalServerError)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/server-error", nil)
	router.ServeHTTP(w, req)

	time.Sleep(100 * time.Millisecond)

	errors, _ := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})

	if len(errors) == 0 {
		t.Fatal("expected at least 1 error record from 500 status")
	}

	found := false
	for _, e := range errors {
		if strings.Contains(e.ErrorMessage, "500") {
			found = true
		}
	}
	if !found {
		t.Error("expected to find HTTP 500 error record")
	}
}

func TestErrorMiddleware_NoErrorOn200(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.GET("/ok", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ok", nil)
	router.ServeHTTP(w, req)

	time.Sleep(50 * time.Millisecond)

	errors, _ := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})

	if len(errors) != 0 {
		t.Errorf("expected no errors on 200, got %d", len(errors))
	}
}

func TestErrorMiddleware_CapturesRequestContext(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.POST("/submit", func(c *gin.Context) {
		c.Error(fmt.Errorf("database connection refused"))
		c.Status(http.StatusInternalServerError)
	})

	body := `{"name":"test"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/submit?key=value", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Custom", "custom-value")
	router.ServeHTTP(w, req)

	time.Sleep(100 * time.Millisecond)

	errors, _ := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})

	if len(errors) == 0 {
		t.Fatal("expected error record")
	}

	e := errors[0]
	if e.RequestContext == nil {
		t.Fatal("expected non-nil RequestContext")
	}

	ctx := e.RequestContext
	if ctx.Method != "POST" {
		t.Errorf("expected method POST, got %q", ctx.Method)
	}
	if ctx.Path != "/submit" {
		t.Errorf("expected path /submit, got %q", ctx.Path)
	}
	if ctx.Query != "key=value" {
		t.Errorf("expected query key=value, got %q", ctx.Query)
	}
	if ctx.Body != body {
		t.Errorf("expected body %q, got %q", body, ctx.Body)
	}

	// Authorization header should be redacted
	if auth, ok := ctx.Headers["Authorization"]; ok {
		if auth != "[REDACTED]" {
			t.Errorf("expected Authorization to be [REDACTED], got %q", auth)
		}
	} else {
		t.Error("expected Authorization header in context")
	}

	// Custom header should be preserved
	if custom, ok := ctx.Headers["X-Custom"]; ok {
		if custom != "custom-value" {
			t.Errorf("expected X-Custom to be 'custom-value', got %q", custom)
		}
	} else {
		t.Error("expected X-Custom header in context")
	}
}

func TestErrorMiddleware_DeduplicatesSameError(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.GET("/dup", func(c *gin.Context) {
		c.Error(fmt.Errorf("same error message"))
		c.Status(http.StatusInternalServerError)
	})

	// Hit the same endpoint 3 times
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/dup", nil)
		router.ServeHTTP(w, req)
	}

	time.Sleep(100 * time.Millisecond)

	errors, _ := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})

	// Should be deduplicated into 1 record (from gin errors) + potentially 1 from 500 status
	// but the fingerprint for the same message+method+route is the same, so dedup works
	// The Gin error and 500 status error have different messages, so we may get 2 distinct records
	// But the same Gin error 3 times should be deduped to count >= 3
	foundDeduped := false
	for _, e := range errors {
		if strings.Contains(e.ErrorMessage, "same error message") && e.Count >= 3 {
			foundDeduped = true
		}
	}
	if !foundDeduped {
		t.Error("expected deduplication: same error 3x should have Count >= 3")
		for _, e := range errors {
			t.Logf("  error: %q count=%d type=%s", e.ErrorMessage, e.Count, e.ErrorType)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		errMsg     string
		statusCode int
		expected   string
	}{
		{"panic: nil pointer dereference", 500, ErrorTypePanic},
		{"runtime error: index out of range", 500, ErrorTypePanic},
		{"context deadline exceeded", 504, ErrorTypeTimeout},
		{"request timeout", 408, ErrorTypeTimeout},
		{"context canceled", 500, ErrorTypeTimeout},
		{"unauthorized access", 401, ErrorTypeAuth},
		{"forbidden resource", 403, ErrorTypeAuth},
		{"permission denied", 500, ErrorTypeAuth},
		{"user not found", 404, ErrorTypeNotFound},
		{"no rows in result set", 404, ErrorTypeNotFound},
		{"validation failed", 400, ErrorTypeValidation},
		{"field is required", 422, ErrorTypeValidation},
		{"invalid input", 400, ErrorTypeValidation},
		{"sql: connection refused", 500, ErrorTypeDatabase},
		{"duplicate key value violates unique constraint", 500, ErrorTypeDatabase},
		{"deadlock detected", 500, ErrorTypeDatabase},
		{"something went wrong", 500, ErrorTypeInternal},
		{"unknown error", 503, ErrorTypeInternal},
		// Status code alone
		{"error occurred", 401, ErrorTypeAuth},
		{"error occurred", 403, ErrorTypeAuth},
		{"error occurred", 404, ErrorTypeNotFound},
		{"error occurred", 400, ErrorTypeValidation},
		{"error occurred", 408, ErrorTypeTimeout},
		{"error occurred", 504, ErrorTypeTimeout},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%d", tt.errMsg, tt.statusCode), func(t *testing.T) {
			got := classifyError(tt.errMsg, tt.statusCode)
			if got != tt.expected {
				t.Errorf("classifyError(%q, %d) = %q, want %q", tt.errMsg, tt.statusCode, got, tt.expected)
			}
		})
	}
}

func TestGenerateFingerprint(t *testing.T) {
	// Same inputs should produce the same fingerprint
	fp1 := generateFingerprint("GET", "/api/users", "not found")
	fp2 := generateFingerprint("GET", "/api/users", "not found")
	if fp1 != fp2 {
		t.Errorf("expected same fingerprint, got %q and %q", fp1, fp2)
	}

	// Different inputs should produce different fingerprints
	fp3 := generateFingerprint("POST", "/api/users", "not found")
	if fp1 == fp3 {
		t.Error("expected different fingerprint for different method")
	}

	fp4 := generateFingerprint("GET", "/api/posts", "not found")
	if fp1 == fp4 {
		t.Error("expected different fingerprint for different route")
	}

	fp5 := generateFingerprint("GET", "/api/users", "internal error")
	if fp1 == fp5 {
		t.Error("expected different fingerprint for different error message")
	}

	// Fingerprint should be 16 chars hex
	if len(fp1) != 16 {
		t.Errorf("expected fingerprint length 16, got %d", len(fp1))
	}
}

func TestCaptureStackTrace(t *testing.T) {
	stack := captureStackTrace(1)
	if stack == "" {
		t.Error("expected non-empty stack trace")
	}

	// Should contain this test function
	if !strings.Contains(stack, "TestCaptureStackTrace") {
		t.Errorf("expected stack to contain 'TestCaptureStackTrace', got:\n%s", stack)
	}
}

func TestShouldSkipFrame(t *testing.T) {
	tests := []struct {
		fn       string
		expected bool
	}{
		{"runtime.gopanic", true},
		{"runtime.goexit", true},
		{"net/http.ListenAndServe", true},
		{"github.com/gin-gonic/gin.(*Context).Next", true},
		{"github.com/MUKE-coder/pulse/pulse.TestSomething", false},
		{"main.handler", false},
	}

	for _, tt := range tests {
		got := shouldSkipFrame(tt.fn)
		if got != tt.expected {
			t.Errorf("shouldSkipFrame(%q) = %v, want %v", tt.fn, got, tt.expected)
		}
	}
}

func TestCaptureRequestContext_RedactsHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("GET", "/test?q=hello", nil)
	c.Request.Header.Set("Authorization", "Bearer token123")
	c.Request.Header.Set("Cookie", "session=abc")
	c.Request.Header.Set("X-Api-Key", "key123")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Accept", "text/html")

	ctx := captureRequestContext(c, nil)

	if ctx.Headers["Authorization"] != "[REDACTED]" {
		t.Errorf("expected Authorization redacted, got %q", ctx.Headers["Authorization"])
	}
	if ctx.Headers["Cookie"] != "[REDACTED]" {
		t.Errorf("expected Cookie redacted, got %q", ctx.Headers["Cookie"])
	}
	if ctx.Headers["X-Api-Key"] != "[REDACTED]" {
		t.Errorf("expected X-Api-Key redacted, got %q", ctx.Headers["X-Api-Key"])
	}
	if ctx.Headers["Content-Type"] != "application/json" {
		t.Errorf("expected Content-Type preserved, got %q", ctx.Headers["Content-Type"])
	}
	if ctx.Headers["Accept"] != "text/html" {
		t.Errorf("expected Accept preserved, got %q", ctx.Headers["Accept"])
	}
}

func TestErrorMiddleware_DisabledDoesNothing(t *testing.T) {
	cfg := applyDefaults(Config{
		Errors: ErrorConfig{
			Enabled: boolPtr(false),
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	defer p.Shutdown()

	router := gin.New()
	// Don't register error middleware since it's disabled
	// (In production, Mount() checks cfg.Errors.Enabled)
	router.GET("/ok", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ok", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestErrorMiddleware_PanicWithNilPointer(t *testing.T) {
	p := setupErrorTestPulse()
	defer p.Shutdown()

	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.GET("/nil-panic", func(c *gin.Context) {
		var s *string
		_ = *s // nil pointer dereference
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/nil-panic", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}

	time.Sleep(50 * time.Millisecond)

	errors, _ := p.storage.GetErrors(ErrorFilter{
		TimeRange: TimeRange{
			Start: time.Now().Add(-time.Minute),
			End:   time.Now().Add(time.Minute),
		},
	})

	if len(errors) == 0 {
		t.Fatal("expected error record from nil pointer panic")
	}

	found := false
	for _, e := range errors {
		if e.ErrorType == ErrorTypePanic {
			found = true
			if e.StackTrace == "" {
				t.Error("expected stack trace for nil pointer panic")
			}
		}
	}
	if !found {
		t.Error("expected panic error type for nil pointer dereference")
	}
}

func TestBuildErrorRecord(t *testing.T) {
	record := buildErrorRecord("POST", "/api/users", "test error", ErrorTypeValidation, "stack trace here", nil, "trace-123")

	if record.ID == "" {
		t.Error("expected non-empty ID")
	}
	if record.Fingerprint == "" {
		t.Error("expected non-empty fingerprint")
	}
	if record.Method != "POST" {
		t.Errorf("expected method POST, got %q", record.Method)
	}
	if record.Route != "/api/users" {
		t.Errorf("expected route /api/users, got %q", record.Route)
	}
	if record.ErrorMessage != "test error" {
		t.Errorf("expected error message 'test error', got %q", record.ErrorMessage)
	}
	if record.ErrorType != ErrorTypeValidation {
		t.Errorf("expected type validation, got %q", record.ErrorType)
	}
	if record.StackTrace != "stack trace here" {
		t.Errorf("expected stack trace, got %q", record.StackTrace)
	}
	if record.Count != 1 {
		t.Errorf("expected count 1, got %d", record.Count)
	}
	if record.FirstSeen.IsZero() {
		t.Error("expected non-zero FirstSeen")
	}
	if record.LastSeen.IsZero() {
		t.Error("expected non-zero LastSeen")
	}
}

func BenchmarkClassifyError(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		classifyError("database connection refused", 500)
	}
}

func BenchmarkGenerateFingerprint(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		generateFingerprint("GET", "/api/users/123", "not found")
	}
}
