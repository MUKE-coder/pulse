package pulse

import (
	"context"
	"log"
	"sync"
	"time"
)

// HealthCheck defines a health check that can be registered with Pulse.
type HealthCheck struct {
	// Name is the unique identifier for this health check.
	Name string
	// Type categorizes the check (database, redis, http, disk, custom).
	Type string
	// CheckFunc is the function that performs the check.
	CheckFunc func(ctx context.Context) error
	// Interval overrides the global check interval for this check.
	Interval time.Duration
	// Timeout overrides the global timeout for this check.
	Timeout time.Duration
	// Critical marks this check as critical — failure means system "unhealthy".
	Critical bool
}

// Pulse is the main engine that orchestrates all observability subsystems.
type Pulse struct {
	config    Config
	storage   Storage
	startTime time.Time

	// GORM plugin
	gormPlugin *PulsePlugin

	// Runtime sampler
	runtimeSampler *RuntimeSampler

	// Aggregator
	aggregator *Aggregator

	// Health check system
	healthChecks []HealthCheck
	healthMu     sync.RWMutex
	healthRunner *HealthRunner

	// WebSocket hub
	wsHub *WebSocketHub

	// Alert engine
	alertEngine *AlertEngine

	// Lifecycle management
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Logger
	logger *log.Logger
}

// newPulse creates a new Pulse engine with the given config.
func newPulse(cfg Config) *Pulse {
	ctx, cancel := context.WithCancel(context.Background())

	p := &Pulse{
		config:       cfg,
		startTime:    time.Now(),
		healthChecks: make([]HealthCheck, 0),
		ctx:          ctx,
		cancel:       cancel,
		logger:       log.Default(),
	}

	return p
}

// AddHealthCheck registers a new health check with Pulse.
func (p *Pulse) AddHealthCheck(check HealthCheck) {
	p.healthMu.Lock()
	defer p.healthMu.Unlock()
	p.healthChecks = append(p.healthChecks, check)
	if p.config.DevMode {
		p.logger.Printf("[pulse] registered health check: %s (%s)", check.Name, check.Type)
	}
}

// GetConfig returns the current Pulse configuration.
func (p *Pulse) GetConfig() Config {
	return p.config
}

// GetStorage returns the storage backend.
func (p *Pulse) GetStorage() Storage {
	return p.storage
}

// Uptime returns the duration since Pulse was started.
func (p *Pulse) Uptime() time.Duration {
	return time.Since(p.startTime)
}

// Shutdown gracefully stops all background goroutines and closes resources.
func (p *Pulse) Shutdown() error {
	p.cancel()
	p.wg.Wait()

	if p.storage != nil {
		return p.storage.Close()
	}
	return nil
}

// startBackground launches a background goroutine managed by the Pulse lifecycle.
func (p *Pulse) startBackground(name string, fn func(ctx context.Context)) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if p.config.DevMode {
			p.logger.Printf("[pulse] starting background: %s", name)
		}
		fn(p.ctx)
		if p.config.DevMode {
			p.logger.Printf("[pulse] stopped background: %s", name)
		}
	}()
}
