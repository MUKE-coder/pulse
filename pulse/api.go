package pulse

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// --- JWT helpers (HS256, no external dependency) ---

type jwtClaims struct {
	Username string `json:"username"`
	Exp      int64  `json:"exp"`
	Iat      int64  `json:"iat"`
}

func b64Encode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

func b64Decode(s string) ([]byte, error) {
	// Add padding back
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

func signJWT(claims jwtClaims, secret string) string {
	header := b64Encode([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	payloadB64 := b64Encode(payload)
	signingInput := header + "." + payloadB64

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := b64Encode(mac.Sum(nil))

	return signingInput + "." + sig
}

func verifyJWT(tokenStr, secret string) (*jwtClaims, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	expectedSig := b64Encode(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, fmt.Errorf("invalid signature")
	}

	payload, err := b64Decode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("invalid claims: %w", err)
	}

	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	return &claims, nil
}

// --- Auth middleware ---

func authMiddleware(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid authorization header"})
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		claims, err := verifyJWT(token, p.config.Dashboard.SecretKey)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		c.Set("pulse_user", claims.Username)
		c.Next()
	}
}

// --- Route registration ---

func registerAPIRoutes(router *gin.Engine, p *Pulse) {
	prefix := p.config.Prefix
	api := router.Group(prefix + "/api")

	limiter := newLoginLimiter(p.config.Dashboard.LoginRateLimit)

	// Public: auth endpoints
	api.POST("/auth/login", loginHandler(p, limiter))
	api.GET("/auth/verify", authMiddleware(p), verifyHandler())

	// Protected: all other endpoints
	protected := api.Group("")
	protected.Use(authMiddleware(p))

	// Overview
	protected.GET("/overview", overviewHandler(p))

	// Routes
	protected.GET("/routes", routesListHandler(p))
	protected.GET("/routes/:method/*path", routeDetailHandler(p))

	// Database
	protected.GET("/database/overview", dbOverviewHandler(p))
	protected.GET("/database/slow-queries", dbSlowQueriesHandler(p))
	protected.GET("/database/patterns", dbPatternsHandler(p))
	protected.GET("/database/n1", dbN1Handler(p))
	protected.GET("/database/n1/ranked", dbN1RankedHandler(p))
	protected.GET("/database/pool", dbPoolHandler(p))

	// Errors
	protected.GET("/errors", errorsListHandler(p))
	protected.GET("/errors/:id", errorDetailHandler(p))
	protected.POST("/errors/:id/mute", errorMuteHandler(p))
	protected.POST("/errors/:id/resolve", errorResolveHandler(p))
	protected.DELETE("/errors/:id", errorDeleteHandler(p))

	// Runtime
	protected.GET("/runtime/current", runtimeCurrentHandler(p))
	protected.GET("/runtime/history", runtimeHistoryHandler(p))
	protected.GET("/runtime/info", runtimeInfoHandler(p))

	// Health (dashboard version, authed)
	protected.GET("/health/checks", healthChecksHandler(p))
	protected.GET("/health/checks/:name/history", healthCheckHistoryHandler(p))
	protected.POST("/health/checks/:name/run", healthCheckRunHandler(p))

	// Alerts
	protected.GET("/alerts", alertsListHandler(p))

	// SLOs
	protected.GET("/slos", slosListHandler(p))

	// USE method (host U/S/E grid)
	protected.GET("/use", useHandler(p))

	// Test runs (k6 / load-test overlay)
	protected.GET("/test-runs", testRunsListHandler(p))
	protected.POST("/test-runs", testRunsCreateHandler(p))

	// Profiling / flame graph (off by default; double-gated, see profile.go)
	protected.GET("/profile/flamegraph", profileFlameGraphHandler(p))
	protected.GET("/profile/folded", profileFoldedHandler(p))

	// Settings & data
	protected.GET("/settings", settingsHandler(p))
	protected.POST("/data/reset", dataResetHandler(p))

	// Data export
	registerExportRoute(protected, p)
}

// --- Login rate limiter (per-IP, in-memory token bucket) ---

// loginLimiter throttles login attempts per client IP. Buckets refill linearly
// over a one-minute window. Buckets that have been idle for >10m are GC'd.
type loginLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*loginBucket
	capacity int
}

type loginBucket struct {
	tokens   int
	lastSeen time.Time
}

func newLoginLimiter(capacity int) *loginLimiter {
	return &loginLimiter{
		buckets:  make(map[string]*loginBucket),
		capacity: capacity,
	}
}

// allow returns true if the IP has remaining tokens. It refills tokens
// proportionally to the elapsed wall-clock time since the last attempt.
func (l *loginLimiter) allow(ip string) bool {
	if l == nil || l.capacity <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &loginBucket{tokens: l.capacity, lastSeen: now}
		l.buckets[ip] = b
	}

	// Refill: full bucket every minute.
	elapsed := now.Sub(b.lastSeen)
	refill := int(elapsed / (time.Minute / time.Duration(l.capacity)))
	if refill > 0 {
		b.tokens += refill
		if b.tokens > l.capacity {
			b.tokens = l.capacity
		}
		b.lastSeen = now
	}

	// Opportunistic GC: drop buckets idle >10m to keep the map bounded.
	if len(l.buckets) > 1024 {
		for k, v := range l.buckets {
			if now.Sub(v.lastSeen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// --- Auth handlers ---

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func loginHandler(p *Pulse, limiter *loginLimiter) gin.HandlerFunc {
	cfgUser := p.config.Dashboard.Username
	cfgPass := p.config.Dashboard.Password
	return func(c *gin.Context) {
		if !limiter.allow(c.ClientIP()) {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many login attempts, try again later"})
			return
		}

		var req loginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "username and password required"})
			return
		}

		// Constant-time comparison to prevent timing side-channel on credentials.
		userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfgUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(cfgPass)) == 1
		if !(userOK && passOK) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}

		claims := jwtClaims{
			Username: req.Username,
			Iat:      time.Now().Unix(),
			Exp:      time.Now().Add(24 * time.Hour).Unix(),
		}
		token := signJWT(claims, p.config.Dashboard.SecretKey)
		c.JSON(http.StatusOK, gin.H{"token": token, "expires_in": 86400})
	}
}

func verifyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		username, _ := c.Get("pulse_user")
		c.JSON(http.StatusOK, gin.H{"valid": true, "username": username})
	}
}

// --- Overview ---

func overviewHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)

		if p.aggregator != nil {
			overview := p.aggregator.GetCachedOverview()
			if overview != nil {
				c.JSON(http.StatusOK, overview)
				return
			}
		}

		overview, err := p.storage.GetOverview(tr)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, overview)
	}
}

// --- Routes ---

func routesListHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)

		var stats []RouteStats
		if p.aggregator != nil {
			stats = p.aggregator.GetCachedRouteStats()
		}
		if len(stats) == 0 {
			stats, _ = p.storage.GetRouteStats(tr)
		}

		// Filter by search term
		if search := c.Query("search"); search != "" {
			search = strings.ToLower(search)
			var filtered []RouteStats
			for _, s := range stats {
				if strings.Contains(strings.ToLower(s.Path), search) {
					filtered = append(filtered, s)
				}
			}
			stats = filtered
		}

		c.JSON(http.StatusOK, stats)
	}
}

func routeDetailHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := strings.ToUpper(c.Param("method"))
		path := c.Param("path")
		tr := parseTimeRangeParam(c)

		detail, err := p.storage.GetRouteDetail(method, path, tr)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if detail == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "route not found"})
			return
		}
		c.JSON(http.StatusOK, detail)
	}
}

// --- Database ---

func dbOverviewHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		patterns, _ := p.storage.GetQueryPatterns(tr)
		pool, _ := p.storage.GetConnectionPoolStats()
		slow, _ := p.storage.GetSlowQueries(p.config.Database.SlowQueryThreshold, 0)
		n1, _ := p.storage.GetN1Detections(tr)

		var totalQueries int64
		for _, pat := range patterns {
			totalQueries += pat.Count
		}

		c.JSON(http.StatusOK, gin.H{
			"total_queries":    totalQueries,
			"pattern_count":    len(patterns),
			"slow_query_count": len(slow),
			"n1_count":         len(n1),
			"pool":             pool,
		})
	}
}

func dbSlowQueriesHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		threshold := p.config.Database.SlowQueryThreshold
		if t := c.Query("threshold"); t != "" {
			if d, err := time.ParseDuration(t); err == nil {
				threshold = d
			}
		}
		limit := queryInt(c, "limit", 50)
		queries, _ := p.storage.GetSlowQueries(threshold, limit)
		c.JSON(http.StatusOK, queries)
	}
}

func dbPatternsHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		patterns, _ := p.storage.GetQueryPatterns(tr)
		c.JSON(http.StatusOK, patterns)
	}
}

func dbN1Handler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		detections, _ := p.storage.GetN1Detections(tr)
		c.JSON(http.StatusOK, detections)
	}
}

// dbN1RankedHandler returns N+1 findings grouped by (route, pattern) and
// sorted by impact score (occurrences × queries/occurrence × avg duration),
// so operators see the highest-leverage fix first. Optional ?limit=N.
func dbN1RankedHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		detections, _ := p.storage.GetN1Detections(tr)
		ranked := rankN1Detections(detections, queryInt(c, "limit", 50))
		c.JSON(http.StatusOK, ranked)
	}
}

func dbPoolHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		pool, _ := p.storage.GetConnectionPoolStats()
		if pool == nil {
			c.JSON(http.StatusOK, gin.H{"message": "no pool stats available"})
			return
		}
		c.JSON(http.StatusOK, pool)
	}
}

// --- Errors ---

func errorsListHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		filter := ErrorFilter{
			TimeRange: tr,
			ErrorType: c.Query("type"),
			Route:     c.Query("route"),
			Limit:     queryInt(c, "limit", 50),
			Offset:    queryInt(c, "offset", 0),
		}
		if v := c.Query("muted"); v == "true" || v == "false" {
			b := v == "true"
			filter.Muted = &b
		}
		if v := c.Query("resolved"); v == "true" || v == "false" {
			b := v == "true"
			filter.Resolved = &b
		}

		errors, _ := p.storage.GetErrors(filter)
		c.JSON(http.StatusOK, errors)
	}
}

func errorDetailHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		record, err := p.storage.GetErrorByID(id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "error not found"})
			return
		}
		c.JSON(http.StatusOK, record)
	}
}

func errorMuteHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := p.storage.UpdateError(id, map[string]interface{}{"muted": true}); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "muted"})
	}
}

func errorResolveHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := p.storage.UpdateError(id, map[string]interface{}{"resolved": true}); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "resolved"})
	}
}

func errorDeleteHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if err := p.storage.DeleteError(id); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
	}
}

// --- Runtime ---

func runtimeCurrentHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		history, _ := p.storage.GetRuntimeHistory(TimeRange{
			Start: time.Now().Add(-1 * time.Minute),
			End:   time.Now().Add(1 * time.Minute),
		})
		if len(history) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "no runtime data yet"})
			return
		}
		c.JSON(http.StatusOK, history[len(history)-1])
	}
}

func runtimeHistoryHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		resolution := ResolutionForRange(tr)
		history := RollupRuntime(p.storage, tr, resolution)
		c.JSON(http.StatusOK, history)
	}
}

func runtimeInfoHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := collectSystemInfo()
		c.JSON(http.StatusOK, gin.H{
			"system": info,
			"uptime": formatDuration(p.Uptime()),
		})
	}
}

// --- Health (authed dashboard version) ---

func healthChecksHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		resp := buildHealthResponse(p)
		c.JSON(http.StatusOK, resp)
	}
}

func healthCheckHistoryHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		limit := queryInt(c, "limit", 100)
		history, _ := p.storage.GetHealthHistory(name, limit)
		c.JSON(http.StatusOK, history)
	}
}

func healthCheckRunHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		if p.healthRunner == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "health runner not started"})
			return
		}
		result, err := p.healthRunner.RunCheckByName(name)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

// --- Alerts ---

func alertsListHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		filter := AlertFilter{
			TimeRange: tr,
			Severity:  c.Query("severity"),
			Limit:     queryInt(c, "limit", 50),
		}
		if s := c.Query("state"); s != "" {
			filter.State = AlertState(s)
		}
		alerts, _ := p.storage.GetAlerts(filter)
		c.JSON(http.StatusOK, alerts)
	}
}

// --- SLOs ---

func slosListHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if p.sloEvaluator == nil {
			c.JSON(http.StatusOK, []SLOStatus{})
			return
		}
		c.JSON(http.StatusOK, p.sloEvaluator.Snapshot())
	}
}

// --- USE method ---

// useHandler returns the most recent USE snapshot. When the sampler is
// disabled, returns an empty snapshot rather than 404 so the dashboard can
// render a single "USE disabled" state.
func useHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if p.useSampler == nil {
			c.JSON(http.StatusOK, USESnapshot{Timestamp: time.Now()})
			return
		}
		c.JSON(http.StatusOK, p.useSampler.Snapshot())
	}
}

// --- Test runs (k6 / load-test overlay) ---

// testRunsListHandler returns recorded test runs in the requested window.
// Used by the dashboard to render vertical bands on the timeline charts.
func testRunsListHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		tr := parseTimeRangeParam(c)
		runs, _ := p.storage.GetTestRuns(tr)
		c.JSON(http.StatusOK, runs)
	}
}

// --- Profiling (flame graph) ---

// profileFlameGraphHandler runs a sampling window and returns either an
// interactive SVG flame graph (default) or a JSON representation
// (?format=json) of the folded tree.
//
// Query params:
//
//	duration  — sample window, e.g. "5s". Default Config.Profiling.DefaultDuration.
//	hz        — samples per second. Default Config.Profiling.SampleHz.
//	width     — SVG width in pixels (svg format only). Default 1400.
//	format    — "svg" (default) or "json".
//	cache     — "true" to serve the most recent cached profile if one exists
//	            and is less than 60s old. Default false (always re-sample).
//
// Returns 503 with a remediation hint when profiling is not opted in.
func profileFlameGraphHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !profilingPermitted(p.config) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "profiling is disabled",
				"hint":  "set pulse.WithProfiling() AND export " + ProfileEnabledEnv + "=true",
			})
			return
		}

		root, status, err := runOrCacheProfile(c, p)
		if err != nil {
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}

		switch c.DefaultQuery("format", "svg") {
		case "json":
			c.JSON(http.StatusOK, root)
		default:
			width := queryInt(c, "width", 1400)
			svg := FlameGraphSVG(root, width, 16)
			c.Header("Content-Disposition", "inline; filename=\"pulse-flamegraph.svg\"")
			c.Data(http.StatusOK, "image/svg+xml", []byte(svg))
		}
	}
}

// profileFoldedHandler returns the Brendan Gregg "folded stack" format so
// callers can pipe Pulse profiles into flamegraph.pl or differential-
// flamegraph tools.
func profileFoldedHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !profilingPermitted(p.config) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "profiling is disabled",
				"hint":  "set pulse.WithProfiling() AND export " + ProfileEnabledEnv + "=true",
			})
			return
		}
		root, status, err := runOrCacheProfile(c, p)
		if err != nil {
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(Folded(root)))
	}
}

// runOrCacheProfile honours the ?cache= query param, picking between a
// fresh sample window and the most-recent cached tree.
func runOrCacheProfile(c *gin.Context, p *Pulse) (*FlameNode, int, error) {
	if c.Query("cache") == "true" {
		if cached := p.profileSampler.Cached(60 * time.Second); cached != nil {
			return cached, http.StatusOK, nil
		}
	}

	dur, _ := time.ParseDuration(c.Query("duration"))
	hz := queryInt(c, "hz", 0)

	root, err := p.profileSampler.Sample(c.Request.Context(), dur, hz)
	if err != nil {
		// Already-running is a 409, anything else (config flip mid-flight
		// etc.) is a 503.
		if strings.Contains(err.Error(), "already") {
			return nil, http.StatusConflict, err
		}
		return nil, http.StatusServiceUnavailable, err
	}
	return root, http.StatusOK, nil
}

// testRunsCreateHandler accepts a test-run record. Intended for k6 (or any
// load-test harness) to call at run start and run end. Both timestamps may
// be supplied by the caller — Pulse does not infer them.
//
// The request shape matches the example in issue #4:
//
//	{ "name": "average-load",
//	  "type": "k6.average-load",
//	  "started_at": "2026-05-28T14:30:00Z",
//	  "ended_at":   "2026-05-28T14:39:00Z",
//	  "metadata":   { "vus_peak": 100, "thresholds_passed": true } }
//
// If ID is omitted, Pulse assigns one.
func testRunsCreateHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		var run TestRun
		if err := c.ShouldBindJSON(&run); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid test-run payload: " + err.Error()})
			return
		}
		if run.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "test-run name is required"})
			return
		}
		if run.StartedAt.IsZero() {
			run.StartedAt = time.Now()
		}
		if run.ID == "" {
			run.ID = GenerateTraceID()
		}
		if err := p.storage.StoreTestRun(run); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, run)
	}
}

// --- Settings & Data ---

func settingsHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := p.config
		// Sanitize secrets
		cfg.Dashboard.SecretKey = "[REDACTED]"
		cfg.Dashboard.Password = "[REDACTED]"
		c.JSON(http.StatusOK, cfg)
	}
}

func dataResetHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || !req.Confirm {
			c.JSON(http.StatusBadRequest, gin.H{"error": "send {\"confirm\": true} to reset all data"})
			return
		}
		if err := p.storage.Reset(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "data reset complete"})
	}
}

// --- Query helpers ---

func parseTimeRangeParam(c *gin.Context) TimeRange {
	if r := c.Query("range"); r != "" {
		return ParseTimeRange(r)
	}
	return Last1h()
}

func queryInt(c *gin.Context, key string, defaultVal int) int {
	if v := c.Query(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			return n
		}
	}
	return defaultVal
}
