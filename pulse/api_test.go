package pulse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupAPIPulse(t *testing.T) (*Pulse, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := applyDefaults(Config{
		Dashboard: DashboardConfig{
			Username:  "admin",
			Password:  "testpass",
			SecretKey: "test-secret-key-for-jwt",
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	p.aggregator = &Aggregator{pulse: p}
	t.Cleanup(func() { p.Shutdown() })

	router := gin.New()
	registerAPIRoutes(router, p)
	return p, router
}

func loginAndGetToken(t *testing.T, router *gin.Engine) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"testpass"}`
	req := httptest.NewRequest("POST", "/pulse/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d - %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	token, ok := resp["token"].(string)
	if !ok || token == "" {
		t.Fatal("expected token in login response")
	}
	return token
}

func authedRequest(method, path, token string, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// --- JWT Tests ---

func TestJWT_SignAndVerify(t *testing.T) {
	secret := "test-secret"
	claims := jwtClaims{
		Username: "admin",
		Iat:      time.Now().Unix(),
		Exp:      time.Now().Add(1 * time.Hour).Unix(),
	}

	token := signJWT(claims, secret)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	parsed, err := verifyJWT(token, secret)
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
	if parsed.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", parsed.Username)
	}
}

func TestJWT_InvalidSignature(t *testing.T) {
	secret := "correct-secret"
	claims := jwtClaims{Username: "admin", Exp: time.Now().Add(1 * time.Hour).Unix()}
	token := signJWT(claims, secret)

	_, err := verifyJWT(token, "wrong-secret")
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

func TestJWT_Expired(t *testing.T) {
	secret := "test"
	claims := jwtClaims{Username: "admin", Exp: time.Now().Add(-1 * time.Hour).Unix()}
	token := signJWT(claims, secret)

	_, err := verifyJWT(token, secret)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestJWT_InvalidFormat(t *testing.T) {
	_, err := verifyJWT("not-a-jwt", "secret")
	if err == nil {
		t.Error("expected error for invalid format")
	}
}

// --- Auth Endpoints ---

func TestAPI_Login_Success(t *testing.T) {
	_, router := setupAPIPulse(t)

	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"testpass"}`
	req := httptest.NewRequest("POST", "/pulse/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["token"]; !ok {
		t.Error("expected 'token' in response")
	}
}

func TestAPI_Login_InvalidCredentials(t *testing.T) {
	_, router := setupAPIPulse(t)

	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"wrong"}`
	req := httptest.NewRequest("POST", "/pulse/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPI_Login_MissingFields(t *testing.T) {
	_, router := setupAPIPulse(t)

	w := httptest.NewRecorder()
	body := `{}`
	req := httptest.NewRequest("POST", "/pulse/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAPI_Verify(t *testing.T) {
	_, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/auth/verify", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_Unauthorized(t *testing.T) {
	_, router := setupAPIPulse(t)

	// No auth header
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/pulse/api/overview", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// Invalid token
	w = httptest.NewRecorder()
	req = authedRequest("GET", "/pulse/api/overview", "invalid-token", "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", w.Code)
	}
}

// --- Overview ---

func TestAPI_Overview(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	// Inject data
	now := time.Now()
	for i := 0; i < 10; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/api/test", StatusCode: 200,
			Latency: 100 * time.Millisecond, Timestamp: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/overview?range=1h", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Routes ---

func TestAPI_RoutesList(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	now := time.Now()
	for i := 0; i < 5; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/users", StatusCode: 200,
			Latency: 50 * time.Millisecond, Timestamp: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	// Run aggregation so cache is populated
	p.aggregator.run()

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/routes", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var stats []RouteStats
	json.Unmarshal(w.Body.Bytes(), &stats)
	if len(stats) == 0 {
		t.Error("expected at least 1 route stat")
	}
}

func TestAPI_RoutesList_Search(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	now := time.Now()
	p.storage.StoreRequest(RequestMetric{Method: "GET", Path: "/users", StatusCode: 200, Latency: 10 * time.Millisecond, Timestamp: now})
	p.storage.StoreRequest(RequestMetric{Method: "GET", Path: "/posts", StatusCode: 200, Latency: 10 * time.Millisecond, Timestamp: now})
	p.aggregator.run()

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/routes?search=user", token, "")
	router.ServeHTTP(w, req)

	var stats []RouteStats
	json.Unmarshal(w.Body.Bytes(), &stats)

	for _, s := range stats {
		if !strings.Contains(strings.ToLower(s.Path), "user") {
			t.Errorf("expected search filter to match 'user', got path %q", s.Path)
		}
	}
}

// --- Database ---

func TestAPI_DatabaseOverview(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreQuery(QueryMetric{
		SQL: "SELECT * FROM users", NormalizedSQL: "select * from users",
		Operation: "SELECT", Table: "users", Duration: 50 * time.Millisecond,
		Timestamp: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/database/overview?range=1h", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_DatabaseSlowQueries(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreQuery(QueryMetric{
		SQL: "SELECT * FROM users", Duration: 500 * time.Millisecond,
		Operation: "SELECT", Table: "users", Timestamp: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/database/slow-queries?threshold=100ms", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_DatabasePatterns(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreQuery(QueryMetric{
		NormalizedSQL: "select * from users where id = ?",
		Operation: "SELECT", Table: "users", Duration: 10 * time.Millisecond,
		Timestamp: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/database/patterns?range=1h", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_DatabasePool(t *testing.T) {
	_, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/database/pool", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- Errors ---

func TestAPI_ErrorsList(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreError(ErrorRecord{
		ID: "err-1", Fingerprint: "fp1", Method: "GET", Route: "/api/test",
		ErrorMessage: "test error", ErrorType: "internal",
		Count: 1, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/errors?range=1h", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var errors []ErrorRecord
	json.Unmarshal(w.Body.Bytes(), &errors)
	if len(errors) == 0 {
		t.Error("expected at least 1 error")
	}
}

func TestAPI_ErrorDetail(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreError(ErrorRecord{
		ID: "err-detail-1", Fingerprint: "fp-detail", Method: "POST", Route: "/submit",
		ErrorMessage: "db error", ErrorType: "database",
		Count: 1, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/errors/err-detail-1", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_ErrorMuteAndResolve(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreError(ErrorRecord{
		ID: "err-action-1", Fingerprint: "fp-action", Method: "GET", Route: "/api",
		ErrorMessage: "timeout", ErrorType: "timeout",
		Count: 1, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	// Mute
	w := httptest.NewRecorder()
	req := authedRequest("POST", "/pulse/api/errors/err-action-1/mute", token, "")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for mute, got %d", w.Code)
	}

	// Resolve
	w = httptest.NewRecorder()
	req = authedRequest("POST", "/pulse/api/errors/err-action-1/resolve", token, "")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for resolve, got %d", w.Code)
	}
}

func TestAPI_ErrorDelete(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreError(ErrorRecord{
		ID: "err-del-1", Fingerprint: "fp-del", Method: "GET", Route: "/api",
		ErrorMessage: "temp error", ErrorType: "internal",
		Count: 1, FirstSeen: time.Now(), LastSeen: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("DELETE", "/pulse/api/errors/err-del-1", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify deleted
	w = httptest.NewRecorder()
	req = authedRequest("GET", "/pulse/api/errors/err-del-1", token, "")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", w.Code)
	}
}

// --- Runtime ---

func TestAPI_RuntimeCurrent(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreRuntime(RuntimeMetric{
		HeapAlloc: 1024 * 1024, NumGoroutine: 10, Timestamp: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/runtime/current", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_RuntimeHistory(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	now := time.Now()
	for i := 0; i < 10; i++ {
		p.storage.StoreRuntime(RuntimeMetric{
			HeapAlloc: uint64(1024 * 1024 * (i + 1)), NumGoroutine: 10 + i,
			Timestamp: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/runtime/history?range=1h", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_RuntimeInfo(t *testing.T) {
	_, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/runtime/info", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- Health (dashboard API) ---

func TestAPI_HealthChecks(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.AddHealthCheck(HealthCheck{Name: "api-check", Type: "custom"})
	p.storage.StoreHealthResult(HealthCheckResult{
		Name: "api-check", Status: "healthy", Timestamp: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/health/checks", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPI_HealthCheckHistory(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	for i := 0; i < 5; i++ {
		p.storage.StoreHealthResult(HealthCheckResult{
			Name: "hist-check", Status: "healthy", Timestamp: time.Now(),
		})
	}

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/health/checks/hist-check/history?limit=10", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- Alerts ---

func TestAPI_Alerts(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	p.storage.StoreAlert(AlertRecord{
		ID: "alert-1", RuleName: "test-rule", Metric: "error_rate",
		Value: 10.0, Threshold: 5.0, Severity: "warning",
		State: AlertStateFiring, FiredAt: time.Now(),
	})

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/alerts?range=1h", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- Settings ---

func TestAPI_Settings(t *testing.T) {
	_, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	w := httptest.NewRecorder()
	req := authedRequest("GET", "/pulse/api/settings", token, "")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "test-secret-key-for-jwt") {
		t.Error("expected secret key to be redacted")
	}
	if strings.Contains(body, "testpass") {
		t.Error("expected password to be redacted")
	}
}

// --- Data Reset ---

func TestAPI_DataReset(t *testing.T) {
	p, router := setupAPIPulse(t)
	token := loginAndGetToken(t, router)

	// Inject data
	p.storage.StoreRequest(RequestMetric{Method: "GET", Path: "/test", StatusCode: 200, Timestamp: time.Now()})

	// Reset without confirm
	w := httptest.NewRecorder()
	req := authedRequest("POST", "/pulse/api/data/reset", token, `{"confirm": false}`)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 without confirm, got %d", w.Code)
	}

	// Reset with confirm
	w = httptest.NewRecorder()
	req = authedRequest("POST", "/pulse/api/data/reset", token, `{"confirm": true}`)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
