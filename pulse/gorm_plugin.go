package pulse

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	pulseCallbackBefore = "pulse:before"
	pulseCallbackAfter  = "pulse:after"
	startTimeKey        = "pulse:start_time"
)

// PulsePlugin implements gorm.Plugin for query tracking.
type PulsePlugin struct {
	pulse *Pulse

	// N+1 detection: tracks query patterns per request trace ID
	n1Tracker   map[string]map[string]int // traceID -> normalizedSQL -> count
	n1TrackerMu sync.Mutex

	// Pool monitoring
	poolDone chan struct{}
}

// Name returns the plugin name as required by gorm.Plugin.
func (p *PulsePlugin) Name() string {
	return "pulse"
}

// Initialize registers callbacks on the GORM DB as required by gorm.Plugin.
func (p *PulsePlugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()

	// Create
	if err := cb.Create().Before("gorm:create").Register(pulseCallbackBefore+"_create", p.beforeCallback); err != nil {
		return err
	}
	if err := cb.Create().After("gorm:create").Register(pulseCallbackAfter+"_create", p.afterCallback); err != nil {
		return err
	}

	// Query
	if err := cb.Query().Before("gorm:query").Register(pulseCallbackBefore+"_query", p.beforeCallback); err != nil {
		return err
	}
	if err := cb.Query().After("gorm:query").Register(pulseCallbackAfter+"_query", p.afterCallback); err != nil {
		return err
	}

	// Update
	if err := cb.Update().Before("gorm:update").Register(pulseCallbackBefore+"_update", p.beforeCallback); err != nil {
		return err
	}
	if err := cb.Update().After("gorm:update").Register(pulseCallbackAfter+"_update", p.afterCallback); err != nil {
		return err
	}

	// Delete
	if err := cb.Delete().Before("gorm:delete").Register(pulseCallbackBefore+"_delete", p.beforeCallback); err != nil {
		return err
	}
	if err := cb.Delete().After("gorm:delete").Register(pulseCallbackAfter+"_delete", p.afterCallback); err != nil {
		return err
	}

	// Row
	if err := cb.Row().Before("gorm:row").Register(pulseCallbackBefore+"_row", p.beforeCallback); err != nil {
		return err
	}
	if err := cb.Row().After("gorm:row").Register(pulseCallbackAfter+"_row", p.afterCallback); err != nil {
		return err
	}

	// Raw
	if err := cb.Raw().Before("gorm:raw").Register(pulseCallbackBefore+"_raw", p.beforeCallback); err != nil {
		return err
	}
	if err := cb.Raw().After("gorm:raw").Register(pulseCallbackAfter+"_raw", p.afterCallback); err != nil {
		return err
	}

	// Start connection pool monitoring
	p.startPoolMonitoring(db)

	return nil
}

// beforeCallback records the start time in the GORM statement context.
func (p *PulsePlugin) beforeCallback(db *gorm.DB) {
	if db == nil || db.Statement == nil {
		return
	}
	db.Set(startTimeKey, time.Now())
}

// afterCallback captures query metrics after execution.
func (p *PulsePlugin) afterCallback(db *gorm.DB) {
	if db == nil || db.Statement == nil {
		return
	}

	cfg := p.pulse.config.Database
	if !boolValue(cfg.Enabled) {
		return
	}

	// Get start time
	val, ok := db.Get(startTimeKey)
	if !ok {
		return
	}
	startTime, ok := val.(time.Time)
	if !ok {
		return
	}

	duration := time.Since(startTime)
	sql := db.Statement.SQL.String()

	// Normalize SQL
	normalized := NormalizeSQL(sql)

	// Get error message
	var errMsg string
	if db.Error != nil && db.Error != gorm.ErrRecordNotFound {
		errMsg = db.Error.Error()
	}

	// Get caller info
	var callerFile string
	var callerLine int
	if boolValue(cfg.TrackCallers) {
		callerFile, callerLine = findCaller()
	}

	// Get trace ID from context
	var traceID string
	if db.Statement.Context != nil {
		traceID = TraceIDFromContext(db.Statement.Context)
	}

	metric := QueryMetric{
		SQL:            sql,
		NormalizedSQL:  normalized.Normalized,
		Duration:       duration,
		RowsAffected:   db.RowsAffected,
		Error:          errMsg,
		Operation:      normalized.Operation,
		Table:          normalized.Table,
		CallerFile:     callerFile,
		CallerLine:     callerLine,
		RequestTraceID: traceID,
		Timestamp:      startTime,
	}

	// Store asynchronously
	go func() {
		if err := p.pulse.storage.StoreQuery(metric); err != nil && p.pulse.config.DevMode {
			p.pulse.logger.Printf("[pulse] failed to store query metric: %v", err)
		}
	}()

	// N+1 detection
	if boolValue(cfg.DetectN1) && traceID != "" && normalized.Normalized != "" {
		p.trackN1(traceID, normalized.Normalized, duration, db)
	}
}

// trackN1 detects N+1 query patterns within a single request.
func (p *PulsePlugin) trackN1(traceID, normalizedSQL string, duration time.Duration, db *gorm.DB) {
	p.n1TrackerMu.Lock()
	defer p.n1TrackerMu.Unlock()

	if p.n1Tracker == nil {
		p.n1Tracker = make(map[string]map[string]int)
	}

	patterns, ok := p.n1Tracker[traceID]
	if !ok {
		patterns = make(map[string]int)
		p.n1Tracker[traceID] = patterns
	}

	patterns[normalizedSQL]++
	count := patterns[normalizedSQL]

	threshold := p.pulse.config.Database.N1Threshold
	if threshold <= 0 {
		threshold = 5
	}

	// Only fire on the exact threshold crossing to avoid duplicate detections
	if count == threshold {
		// Get the route from context if available
		var route string
		if db.Statement.Context != nil {
			if pulse := PulseFromContext(db.Statement.Context); pulse != nil {
				// Route info isn't directly on context; we store it via trace ID correlation
			}
		}

		detection := N1Detection{
			Pattern:        normalizedSQL,
			Count:          count,
			TotalDuration:  duration * time.Duration(count),
			RequestTraceID: traceID,
			Route:          route,
			DetectedAt:     time.Now(),
		}

		// Store via MemoryStorage's N+1 specific method
		if ms, ok := p.pulse.storage.(*MemoryStorage); ok {
			ms.StoreN1Detection(detection)
		}

		if p.pulse.config.DevMode {
			p.pulse.logger.Printf("[pulse] N+1 detected: %q repeated %d times in request %s",
				normalizedSQL, count, traceID)
		}
	}
}

// CleanupTraceN1 removes N+1 tracking data for a completed request.
// Called by the middleware after the request completes.
func (p *PulsePlugin) CleanupTraceN1(traceID string) {
	p.n1TrackerMu.Lock()
	defer p.n1TrackerMu.Unlock()
	delete(p.n1Tracker, traceID)
}

// startPoolMonitoring starts a background goroutine to sample connection pool stats.
func (p *PulsePlugin) startPoolMonitoring(db *gorm.DB) {
	p.poolDone = make(chan struct{})

	p.pulse.startBackground("pool-monitor", func(ctx context.Context) {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sqlDB, err := db.DB()
				if err != nil {
					continue
				}

				stats := sqlDB.Stats()
				poolStats := PoolStats{
					MaxOpenConnections: stats.MaxOpenConnections,
					OpenConnections:    stats.OpenConnections,
					InUse:              stats.InUse,
					Idle:               stats.Idle,
					WaitCount:          stats.WaitCount,
					WaitDuration:       stats.WaitDuration.Milliseconds(),
					MaxIdleClosed:      stats.MaxIdleClosed,
					MaxIdleTimeClosed:  stats.MaxIdleTimeClosed,
					MaxLifetimeClosed:  stats.MaxLifetimeClosed,
				}

				if ms, ok := p.pulse.storage.(*MemoryStorage); ok {
					ms.UpdatePoolStats(poolStats)
				}
			}
		}
	})
}

// findCaller walks the call stack to find the first frame outside of
// GORM internals and the pulse package itself.
func findCaller() (string, int) {
	pcs := make([]uintptr, 15)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	for {
		frame, more := frames.Next()
		// Skip gorm internals, pulse package, and runtime
		if !isInternalFrame(frame.Function) {
			return frame.File, frame.Line
		}
		if !more {
			break
		}
	}

	return "", 0
}

// isInternalFrame checks if a function name belongs to internal packages we should skip.
func isInternalFrame(funcName string) bool {
	skip := []string{
		"gorm.io/gorm",
		"gorm.io/driver",
		"github.com/MUKE-coder/pulse/pulse",
		"runtime.",
		"database/sql",
	}
	for _, prefix := range skip {
		if strings.Contains(funcName, prefix) {
			return true
		}
	}
	return false
}

// Ensure PulsePlugin satisfies gorm.Plugin at compile time.
var _ gorm.Plugin = (*PulsePlugin)(nil)
