package pulse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The login screen's status cards read this endpoint. It is the only
// unauthenticated view of live data Pulse serves, so it stays off until
// asked for.
func TestAPI_PublicStatus_IsOptIn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	off := newPulse(context.Background(), applyDefaults(Config{}))
	off.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = off.Shutdown() })
	router := gin.New()
	registerAPIRoutes(router, off)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/pulse/api/public/status", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d: the endpoint should not exist until Dashboard.PublicStatus is set", w.Code)
	}

	cfg := applyDefaults(Config{})
	WithPublicStatus()(&cfg)
	on := newPulse(context.Background(), cfg)
	on.storage = NewMemoryStorage("test")
	t.Cleanup(func() { _ = on.Shutdown() })
	on.storage.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200,
		Latency: 40 * time.Millisecond, Timestamp: time.Now().Add(-time.Minute)})
	router = gin.New()
	registerAPIRoutes(router, on)

	w = httptest.NewRecorder()
	// No Authorization header: that is the point of this endpoint.
	router.ServeHTTP(w, httptest.NewRequest("GET", "/pulse/api/public/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Status  string  `json:"status"`
		Uptime  string  `json:"uptime"`
		Seconds int64   `json:"uptime_seconds"`
		Version string  `json:"version"`
		P95     float64 `json:"p95_latency_ms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status == "" || resp.Uptime == "" || resp.Version != Version {
		t.Errorf("unexpected payload: %+v", resp)
	}
	if resp.P95 <= 0 {
		t.Errorf("p95 should come from the recorded request, got %v", resp.P95)
	}

	// It carries nothing route-specific: a status card, not a dashboard.
	body := strings.ToLower(w.Body.String())
	for _, leak := range []string{"/x", "route", "error_rate"} {
		if strings.Contains(body, leak) {
			t.Errorf("public status should not mention %q: %s", leak, w.Body.String())
		}
	}
}

// The dashboard asks for the window before this one to draw an honest
// delta, by sliding the range back with ?end=.
func TestParseTimeRangeParam_SlidesTheWindowBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ask := func(url string) TimeRange {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", url, nil)
		return parseTimeRangeParam(c)
	}

	now := ask("/x?range=15m")
	if span := now.End.Sub(now.Start); span < 14*time.Minute || span > 16*time.Minute {
		t.Errorf("15m window spans %v", span)
	}

	end := time.Now().Add(-15 * time.Minute).UTC().Truncate(time.Second)
	prev := ask("/x?range=15m&end=" + end.Format(time.RFC3339))
	if !prev.End.Equal(end) {
		t.Errorf("window should end at %v, got %v", end, prev.End)
	}
	if span := prev.End.Sub(prev.Start); span < 14*time.Minute || span > 16*time.Minute {
		t.Errorf("shifted window spans %v, want the same 15m", span)
	}
	if !prev.End.Before(now.End) {
		t.Error("the previous window should end before the current one")
	}

	// A malformed end is ignored rather than producing a nonsense window.
	bad := ask("/x?range=15m&end=yesterday")
	if span := bad.End.Sub(bad.Start); span < 14*time.Minute || span > 16*time.Minute {
		t.Errorf("a bad end should fall back to the plain range, got %v", span)
	}
}

// The Database page names what it is watching, and says what counts as slow.
func TestAPI_DatabaseOverview_NamesTheMonitoredDatabase(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	db.Create(&TestUser{Name: "someone", Age: 30})

	gin.SetMode(gin.TestMode)
	p.config.Dashboard.Username = "admin"
	p.config.Dashboard.Password = "testpass"
	p.config.Dashboard.SecretKey = "test-secret-key-for-jwt"
	router := gin.New()
	registerAPIRoutes(router, p)

	token := loginAndGetToken(t, router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/database/overview?range=1h", token, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Engine    string  `json:"engine"`
		Database  string  `json:"database"`
		Threshold float64 `json:"slow_threshold_ms"`
		Explain   bool    `json:"explain_enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Engine != "sqlite" {
		t.Errorf("engine %q, want the dialect of the monitored database", resp.Engine)
	}
	if resp.Threshold <= 0 {
		t.Errorf("slow threshold %v, want the configured value in ms", resp.Threshold)
	}
	if resp.Explain {
		t.Error("explain_enabled should be false until ExplainSlowQueries is set")
	}
}

// The Database page's primary action explains the slowest SELECT on
// demand. With plans off it says so, with a hint rather than a dead button.
func TestAPI_ExplainNow(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	gin.SetMode(gin.TestMode)
	p.config.Dashboard.Username = "admin"
	p.config.Dashboard.Password = "testpass"
	p.config.Dashboard.SecretKey = "test-secret-key-for-jwt"
	router := gin.New()
	registerAPIRoutes(router, p)
	token := loginAndGetToken(t, router)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("POST", "/pulse/api/database/explain", token, ""))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409 while query plans are off: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "WithExplainSlowQueries") {
		t.Errorf("the refusal should say how to turn plans on: %s", w.Body.String())
	}

	// Now turn it on and run a statement with no bind parameters: that one
	// can be explained on the spot.
	p.config.Database.ExplainSlowQueries = Bool(true)
	p.config.Database.SlowQueryThreshold = 0
	p.explainer = newExplainer(p, db)
	for i := 0; i < 20; i++ {
		db.Create(&TestUser{Name: "someone", Age: i})
	}
	var found []TestUser
	db.Where("age > 5").Find(&found)

	w = httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("POST", "/pulse/api/database/explain", token, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Explained bool   `json:"explained"`
		Armed     bool   `json:"armed"`
		Query     string `json:"query"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Explained || resp.Query == "" {
		t.Fatalf("nothing was explained: %+v", resp)
	}

	plans, err := p.storage.(planStore).queryPlans(10)
	if err != nil || len(plans) == 0 {
		t.Fatalf("the plan should have been stored: %v %+v", err, plans)
	}
	if !strings.Contains(strings.ToUpper(plans[0].Plan), "TEST_USERS") {
		t.Errorf("plan does not look like SQLite's answer: %q", plans[0].Plan)
	}
}

// Pulse keeps the statement but not the values a query ran with, so a
// parameterised query is planned generically rather than waiting for its
// next run. The response says which kind of plan came back.
func TestExplainNow_PlansAParameterisedQueryGenerically(t *testing.T) {
	db, p := setupTestPulseWithDB(t)
	gin.SetMode(gin.TestMode)
	p.config.Dashboard.Username = "admin"
	p.config.Dashboard.Password = "testpass"
	p.config.Dashboard.SecretKey = "test-secret-key-for-jwt"
	p.config.Database.ExplainSlowQueries = Bool(true)
	p.config.Database.SlowQueryThreshold = 0
	p.explainer = newExplainer(p, db)
	router := gin.New()
	registerAPIRoutes(router, p)
	token := loginAndGetToken(t, router)

	db.Create(&TestUser{Name: "someone", Age: 30})
	var found []TestUser
	db.Where("age > ?", 5).Find(&found)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("POST", "/pulse/api/database/explain", token, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Explained bool   `json:"explained"`
		Generic   bool   `json:"generic"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Explained || !resp.Generic {
		t.Fatalf("a parameterised query should be explained generically: %+v", resp)
	}
	if !strings.Contains(resp.Message, "without the values") {
		t.Errorf("the message should say the plan is generic: %q", resp.Message)
	}

	plans, err := p.storage.(planStore).queryPlans(10)
	if err != nil || len(plans) == 0 {
		t.Fatalf("the plan should have been stored: %v %+v", err, plans)
	}
	if !plans[0].Generic {
		t.Error("the stored plan should be marked generic")
	}
}

func TestPlaceholderCount(t *testing.T) {
	cases := map[string]int{
		"SELECT * FROM users":                       0,
		"SELECT * FROM users WHERE id = ?":          1,
		"SELECT * FROM users WHERE a = ? AND b = ?": 2,
		"SELECT * FROM users WHERE id = $1":         1,
		"SELECT * FROM t WHERE a = $1 AND b = $2":   2,
		// A $ inside a literal counts too. The question this answers is
		// "can Pulse run this with no values?", and erring towards waiting
		// for the real ones is the safe way to be wrong.
		"SELECT 'price $100' FROM t": 1,
	}
	for query, want := range cases {
		if got := placeholderCount(query); got != want {
			t.Errorf("%q → %d, want %d", query, got, want)
		}
	}
}
