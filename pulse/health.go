package pulse

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// HealthRunner periodically executes registered health checks and stores results.
//
// Each check runs in its own goroutine on its own interval, with its
// timeout enforced by the runner — so one slow or hung dependency can't
// delay the other checks or freeze the overall status during an outage.
type HealthRunner struct {
	pulse *Pulse

	mu             sync.RWMutex
	compositeState string // "healthy", "degraded", "unhealthy"
	flapping       map[string]bool
	latest         map[string]HealthCheckResult // most recent result per check
	running        map[string]bool              // checks whose CheckFunc hasn't returned yet
}

// HealthResponse is the JSON structure returned by the public health endpoint.
type HealthResponse struct {
	Status    string                         `json:"status"`
	Timestamp time.Time                      `json:"timestamp"`
	Uptime    string                         `json:"uptime"`
	Checks    map[string]HealthCheckResponse `json:"checks,omitempty"`
}

// HealthCheckResponse is one check's status in the health endpoint.
type HealthCheckResponse struct {
	Status    string  `json:"status"`
	LatencyMs float64 `json:"latency_ms"`
	Error     string  `json:"error,omitempty"`
}

// newHealthRunner creates and starts the health check runner.
func newHealthRunner(p *Pulse) *HealthRunner {
	hr := &HealthRunner{
		pulse:          p,
		compositeState: "healthy",
		flapping:       make(map[string]bool),
	}

	p.startBackground("health-runner", func(ctx context.Context) {
		// Let the app finish starting before the first checks.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}

		next := make(map[string]time.Time) // check name → when it is next due
		for {
			hr.runDue(next, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-time.After(hr.pollInterval()):
			}
		}
	})

	return hr
}

// checks returns a snapshot of the registered checks.
func (hr *HealthRunner) checks() []HealthCheck {
	hr.pulse.healthMu.RLock()
	defer hr.pulse.healthMu.RUnlock()
	return append([]HealthCheck(nil), hr.pulse.healthChecks...)
}

// intervalFor is the check's own Interval, or the global one.
func (hr *HealthRunner) intervalFor(check HealthCheck) time.Duration {
	if check.Interval > 0 {
		return check.Interval
	}
	if hr.pulse.config.Health.CheckInterval > 0 {
		return hr.pulse.config.Health.CheckInterval
	}
	return 30 * time.Second
}

// pollInterval is how often the scheduler looks for due checks: the
// shortest check interval, between 50ms and 1s.
func (hr *HealthRunner) pollInterval() time.Duration {
	poll := time.Second
	for _, c := range hr.checks() {
		if iv := hr.intervalFor(c); iv < poll {
			poll = iv
		}
	}
	if iv := hr.intervalFor(HealthCheck{}); iv < poll {
		poll = iv
	}
	return max(poll, 50*time.Millisecond)
}

// runDue starts every check whose interval has elapsed, each in its own
// goroutine.
func (hr *HealthRunner) runDue(next map[string]time.Time, now time.Time) {
	for _, check := range hr.checks() {
		if due, ok := next[check.Name]; ok && now.Before(due) {
			continue
		}
		next[check.Name] = now.Add(hr.intervalFor(check))
		hr.pulse.wg.Add(1)
		go func(check HealthCheck) {
			defer hr.pulse.wg.Done()
			hr.runCheck(check)
		}(check)
	}
}

// runCheck executes a single health check with its timeout, then stores,
// caches and broadcasts the result.
func (hr *HealthRunner) runCheck(check HealthCheck) HealthCheckResult {
	timeout := check.Timeout
	if timeout <= 0 {
		timeout = hr.pulse.config.Health.Timeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	start := time.Now()
	err := hr.invoke(check, timeout)
	latency := time.Since(start)

	status := "healthy"
	errMsg := ""
	if err != nil {
		status = "unhealthy"
		errMsg = err.Error()
	}

	result := HealthCheckResult{
		Name:      check.Name,
		Type:      check.Type,
		Status:    status,
		Latency:   latency,
		Error:     errMsg,
		Timestamp: time.Now(),
	}
	if hr.pulse.ctx.Err() != nil {
		return result // shutting down: the check was cut short, not failed
	}

	hr.pulse.internalError("storage: health results", hr.pulse.storage.StoreHealthResult(result))
	hr.record(result)
	hr.pulse.BroadcastHealthResult(result)
	hr.detectFlapping(check.Name)

	return result
}

// invoke runs check.CheckFunc, enforcing the timeout even when the check
// ignores its context: the runner stops waiting, and the check's goroutine
// is abandoned. A check whose previous run is still stuck is not started
// again — it fails straight away — so a hung dependency can't pile up
// goroutines. A panicking check fails rather than crashing the process.
func (hr *HealthRunner) invoke(check HealthCheck, timeout time.Duration) error {
	hr.mu.Lock()
	if hr.running == nil {
		hr.running = make(map[string]bool)
	}
	if hr.running[check.Name] {
		hr.mu.Unlock()
		return fmt.Errorf("previous run is still in progress after its %s timeout (the check does not honor its context)", timeout)
	}
	hr.running[check.Name] = true
	hr.mu.Unlock()

	ctx, cancel := context.WithTimeout(hr.pulse.ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("check panicked: %v", r)
			}
			hr.mu.Lock()
			delete(hr.running, check.Name)
			hr.mu.Unlock()
			done <- err
		}()
		err = check.CheckFunc(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		select {
		case err := <-done: // finished at the deadline
			return err
		default:
			return fmt.Errorf("timed out after %s", timeout)
		}
	}
}

// record caches a result and recomputes the composite status.
func (hr *HealthRunner) record(r HealthCheckResult) {
	checks := hr.checks()
	hr.mu.Lock()
	defer hr.mu.Unlock()
	if hr.latest == nil {
		hr.latest = make(map[string]HealthCheckResult)
	}
	hr.latest[r.Name] = r
	hr.compositeState = compositeFrom(checks, hr.latest)
}

// latestResults returns a copy of the cached results.
func (hr *HealthRunner) latestResults() map[string]HealthCheckResult {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	out := make(map[string]HealthCheckResult, len(hr.latest))
	for k, v := range hr.latest {
		out[k] = v
	}
	return out
}

// compositeFrom is "unhealthy" if a critical check failed, "degraded" if
// only non-critical ones did, else "healthy". Checks without results don't
// count.
func compositeFrom(checks []HealthCheck, results map[string]HealthCheckResult) string {
	state := "healthy"
	for _, check := range checks {
		result, ok := results[check.Name]
		if !ok || result.Status == "healthy" {
			continue
		}
		if check.Critical {
			return "unhealthy"
		}
		state = "degraded"
	}
	return state
}

// RunCheckByName executes a specific health check by name on demand.
func (hr *HealthRunner) RunCheckByName(name string) (*HealthCheckResult, error) {
	for _, c := range hr.checks() {
		if c.Name == name {
			result := hr.runCheck(c)
			return &result, nil
		}
	}
	return nil, fmt.Errorf("health check not found: %s", name)
}

// detectFlapping checks if a health check is alternating between healthy/unhealthy.
func (hr *HealthRunner) detectFlapping(name string) {
	history, _ := hr.pulse.storage.GetHealthHistory(name, 6)
	if len(history) < 4 {
		return
	}

	// Count status transitions in the last 6 results
	transitions := 0
	for i := 1; i < len(history); i++ {
		if history[i].Status != history[i-1].Status {
			transitions++
		}
	}

	hr.mu.Lock()
	hr.flapping[name] = transitions >= 3
	hr.mu.Unlock()
}

// GetCompositeStatus returns the current composite health status.
func (hr *HealthRunner) GetCompositeStatus() string {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	return hr.compositeState
}

// IsFlapping returns whether a check is currently flapping.
func (hr *HealthRunner) IsFlapping(name string) bool {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	return hr.flapping[name]
}

// --- Built-in Health Checks ---

// DatabaseHealthCheck creates a health check for a GORM database.
func DatabaseHealthCheck(db *gorm.DB) HealthCheck {
	return HealthCheck{
		Name:     "database",
		Type:     "database",
		Critical: true,
		Timeout:  5 * time.Second,
		CheckFunc: func(ctx context.Context) error {
			sqlDB, err := db.DB()
			if err != nil {
				return fmt.Errorf("failed to get sql.DB: %w", err)
			}
			if err := sqlDB.PingContext(ctx); err != nil {
				return fmt.Errorf("ping failed: %w", err)
			}
			return nil
		},
	}
}

// --- Health HTTP Endpoints ---

// registerHealthRoutes registers public health endpoints on the router.
func registerHealthRoutes(router *gin.Engine, p *Pulse) {
	prefix := p.config.Prefix

	// GET /pulse/health — full health status (public, no auth)
	router.GET(prefix+"/health", func(c *gin.Context) {
		hr := p.healthRunner
		if hr == nil {
			c.JSON(http.StatusOK, HealthResponse{
				Status:    "healthy",
				Timestamp: time.Now(),
				Uptime:    formatDuration(p.Uptime()),
			})
			return
		}

		resp := buildHealthResponse(p)

		statusCode := http.StatusOK
		switch resp.Status {
		case "unhealthy":
			statusCode = http.StatusServiceUnavailable
		case "degraded":
			statusCode = 207 // Multi-Status
		}

		c.JSON(statusCode, resp)
	})

	// GET /pulse/health/live — Kubernetes liveness probe
	router.GET(prefix+"/health/live", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "alive"})
	})

	// GET /pulse/health/ready — Kubernetes readiness probe
	router.GET(prefix+"/health/ready", func(c *gin.Context) {
		hr := p.healthRunner
		if hr == nil {
			c.JSON(http.StatusOK, gin.H{"status": "ready"})
			return
		}

		status := hr.GetCompositeStatus()
		if status == "unhealthy" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
}

// buildHealthResponse constructs the full health response with per-check
// details. Results come from the runner's in-memory cache, so the health
// endpoints keep answering even when Pulse's own storage is slow or down;
// storage is consulted only for checks that haven't produced a result in
// this process yet.
func buildHealthResponse(p *Pulse) HealthResponse {
	resp := HealthResponse{
		Timestamp: time.Now(),
		Uptime:    formatDuration(p.Uptime()),
		Checks:    make(map[string]HealthCheckResponse),
	}

	p.healthMu.RLock()
	checks := make([]HealthCheck, len(p.healthChecks))
	copy(checks, p.healthChecks)
	p.healthMu.RUnlock()

	results := map[string]HealthCheckResult{}
	if p.healthRunner != nil {
		results = p.healthRunner.latestResults()
	}
	for _, check := range checks {
		if _, ok := results[check.Name]; !ok {
			for name, r := range p.storage.GetLatestHealthResults() {
				if _, cached := results[name]; !cached {
					results[name] = r
				}
			}
			break
		}
	}

	for _, check := range checks {
		result, exists := results[check.Name]
		if !exists {
			resp.Checks[check.Name] = HealthCheckResponse{Status: "unknown"}
			continue
		}
		resp.Checks[check.Name] = HealthCheckResponse{
			Status:    result.Status,
			LatencyMs: float64(result.Latency) / float64(time.Millisecond),
			Error:     result.Error,
		}
	}
	resp.Status = compositeFrom(checks, results)

	return resp
}
