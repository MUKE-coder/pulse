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
type HealthRunner struct {
	pulse *Pulse

	mu             sync.RWMutex
	compositeState string // "healthy", "degraded", "unhealthy"
	flapping       map[string]bool
}

// HealthResponse is the JSON structure returned by the public health endpoint.
type HealthResponse struct {
	Status    string                         `json:"status"`
	Timestamp time.Time                      `json:"timestamp"`
	Uptime    string                         `json:"uptime"`
	Checks   map[string]HealthCheckResponse  `json:"checks,omitempty"`
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

	interval := p.config.Health.CheckInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	p.startBackground("health-runner", func(ctx context.Context) {
		// Run initial checks after a short delay to let the app start
		time.Sleep(1 * time.Second)
		hr.runAll()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hr.runAll()
			}
		}
	})

	return hr
}

// runAll executes all registered health checks.
func (hr *HealthRunner) runAll() {
	hr.pulse.healthMu.RLock()
	checks := make([]HealthCheck, len(hr.pulse.healthChecks))
	copy(checks, hr.pulse.healthChecks)
	hr.pulse.healthMu.RUnlock()

	if len(checks) == 0 {
		hr.mu.Lock()
		hr.compositeState = "healthy"
		hr.mu.Unlock()
		return
	}

	for _, check := range checks {
		hr.runCheck(check)
	}

	hr.updateComposite()
}

// runCheck executes a single health check with timeout.
func (hr *HealthRunner) runCheck(check HealthCheck) HealthCheckResult {
	timeout := check.Timeout
	if timeout <= 0 {
		timeout = hr.pulse.config.Health.Timeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(hr.pulse.ctx, timeout)
	defer cancel()

	start := time.Now()
	err := check.CheckFunc(ctx)
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

	// Store result
	if storeErr := hr.pulse.storage.StoreHealthResult(result); storeErr != nil && hr.pulse.config.DevMode {
		hr.pulse.logger.Printf("[pulse] failed to store health result: %v", storeErr)
	}

	// Broadcast health result to WebSocket clients
	hr.pulse.BroadcastHealthResult(result)

	// Detect flapping
	hr.detectFlapping(check.Name)

	return result
}

// RunCheckByName executes a specific health check by name on demand.
func (hr *HealthRunner) RunCheckByName(name string) (*HealthCheckResult, error) {
	hr.pulse.healthMu.RLock()
	var found *HealthCheck
	for _, c := range hr.pulse.healthChecks {
		if c.Name == name {
			cc := c
			found = &cc
			break
		}
	}
	hr.pulse.healthMu.RUnlock()

	if found == nil {
		return nil, fmt.Errorf("health check not found: %s", name)
	}

	result := hr.runCheck(*found)
	return &result, nil
}

// updateComposite recalculates the overall health status.
func (hr *HealthRunner) updateComposite() {
	hr.pulse.healthMu.RLock()
	checks := make([]HealthCheck, len(hr.pulse.healthChecks))
	copy(checks, hr.pulse.healthChecks)
	hr.pulse.healthMu.RUnlock()

	if ms, ok := hr.pulse.storage.(*MemoryStorage); ok {
		state := computeCompositeHealth(hr.pulse, ms)
		hr.mu.Lock()
		hr.compositeState = state
		hr.mu.Unlock()
	}
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

// buildHealthResponse constructs the full health response with per-check details.
func buildHealthResponse(p *Pulse) HealthResponse {
	resp := HealthResponse{
		Timestamp: time.Now(),
		Uptime:    formatDuration(p.Uptime()),
		Checks:    make(map[string]HealthCheckResponse),
	}

	ms, ok := p.storage.(*MemoryStorage)
	if !ok {
		resp.Status = "healthy"
		return resp
	}

	latestResults := ms.getLatestHealthResults()

	p.healthMu.RLock()
	checks := make([]HealthCheck, len(p.healthChecks))
	copy(checks, p.healthChecks)
	p.healthMu.RUnlock()

	hasCriticalFail := false
	hasNonCriticalFail := false

	for _, check := range checks {
		result, exists := latestResults[check.Name]
		if !exists {
			resp.Checks[check.Name] = HealthCheckResponse{Status: "unknown"}
			continue
		}

		checkResp := HealthCheckResponse{
			Status:    result.Status,
			LatencyMs: float64(result.Latency) / float64(time.Millisecond),
			Error:     result.Error,
		}
		resp.Checks[check.Name] = checkResp

		if result.Status != "healthy" {
			if check.Critical {
				hasCriticalFail = true
			} else {
				hasNonCriticalFail = true
			}
		}
	}

	if hasCriticalFail {
		resp.Status = "unhealthy"
	} else if hasNonCriticalFail {
		resp.Status = "degraded"
	} else {
		resp.Status = "healthy"
	}

	return resp
}
