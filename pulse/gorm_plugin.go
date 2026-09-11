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

// n1TraceIdleTTL is how long an N+1 tally may sit untouched before the
// sweeper finalizes it. Requests that pass through the tracing middleware
// are finalized as soon as they complete; the sweeper only catches traces
// nothing finalizes (queries run under a hand-built ContextWithTraceID, or
// by goroutines that outlive their request).
const n1TraceIdleTTL = 5 * time.Minute

// PulsePlugin implements gorm.Plugin for query tracking.
type PulsePlugin struct {
	pulse *Pulse

	// N+1 detection: per-request tallies of repeated query patterns, keyed
	// by trace ID. Entries are removed when the trace is finalized.
	n1Tracker   map[string]*n1Trace
	n1TrackerMu sync.Mutex

	// Pool monitoring
	poolDone chan struct{}
}

// n1Trace tallies the queries run under one trace ID.
type n1Trace struct {
	route    string // "METHOD /pattern", from the request context when known
	lastSeen time.Time
	patterns map[string]*n1Pattern // normalized SQL → tally
}

type n1Pattern struct {
	count int
	total time.Duration
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

	// Finalize N+1 tallies nothing else finalizes, so the tracker stays bounded.
	p.startN1Sweeper()

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

	// Get trace ID and matched route from context
	var traceID, route string
	if db.Statement.Context != nil {
		traceID = TraceIDFromContext(db.Statement.Context)
		route = RouteFromContext(db.Statement.Context)
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
		p.trackN1(traceID, route, normalized.Normalized, duration)
	}
}

// trackN1 tallies one execution of normalizedSQL under traceID. Detections
// are emitted when the trace is finalized, so they carry the real repeat
// count and summed duration rather than a snapshot at the threshold.
func (p *PulsePlugin) trackN1(traceID, route, normalizedSQL string, duration time.Duration) {
	p.n1TrackerMu.Lock()
	defer p.n1TrackerMu.Unlock()

	if p.n1Tracker == nil {
		p.n1Tracker = make(map[string]*n1Trace)
	}

	tr, ok := p.n1Tracker[traceID]
	if !ok {
		tr = &n1Trace{route: route, patterns: make(map[string]*n1Pattern)}
		p.n1Tracker[traceID] = tr
	}
	tr.lastSeen = time.Now()

	pat, ok := tr.patterns[normalizedSQL]
	if !ok {
		pat = &n1Pattern{}
		tr.patterns[normalizedSQL] = pat
	}
	pat.count++
	pat.total += duration
}

// finalizeTrace records an N1Detection for every query pattern that repeated
// at least N1Threshold times under traceID, then forgets the trace. The
// tracing middleware calls it when a request completes; route overrides the
// route captured from the query context when non-empty.
func (p *PulsePlugin) finalizeTrace(traceID, route string) {
	p.n1TrackerMu.Lock()
	tr := p.n1Tracker[traceID]
	delete(p.n1Tracker, traceID)
	p.n1TrackerMu.Unlock()

	if tr == nil {
		return
	}
	if route == "" {
		route = tr.route
	}
	p.emitN1(traceID, route, tr)
}

// CleanupTraceN1 finalizes N+1 tracking for a completed request: detections
// for patterns that crossed the threshold are recorded, then the trace's
// tracking data is removed. The tracing middleware does this automatically;
// call it yourself only for queries run under a hand-built
// [ContextWithTraceID].
func (p *PulsePlugin) CleanupTraceN1(traceID string) {
	p.finalizeTrace(traceID, "")
}

// emitN1 stores one detection per pattern in tr at or above the threshold.
func (p *PulsePlugin) emitN1(traceID, route string, tr *n1Trace) {
	threshold := p.pulse.config.Database.N1Threshold
	if threshold <= 0 {
		threshold = 5
	}

	now := time.Now()
	for sql, pat := range tr.patterns {
		if pat.count < threshold {
			continue
		}
		detection := N1Detection{
			Pattern:        sql,
			Count:          pat.count,
			TotalDuration:  pat.total,
			AvgDuration:    pat.total / time.Duration(pat.count),
			RequestTraceID: traceID,
			Route:          route,
			SuggestedFix:   suggestN1Fix(sql),
			DetectedAt:     now,
		}

		if err := p.pulse.storage.StoreN1Detection(detection); err != nil && p.pulse.config.DevMode {
			p.pulse.logger.Printf("[pulse] failed to store N+1 detection: %v", err)
		}

		if p.pulse.config.DevMode {
			if route != "" {
				p.pulse.logger.Printf("[pulse] N+1 detected in %s (sampled %s): %d× %q",
					route, traceID, pat.count, sql)
			} else {
				p.pulse.logger.Printf("[pulse] N+1 detected: %q repeated %d times in request %s",
					sql, pat.count, traceID)
			}
			if detection.SuggestedFix != "" {
				p.pulse.logger.Printf("[pulse]   suggestion: %s", detection.SuggestedFix)
			}
		}
	}
}

// sweepIdleN1Traces finalizes every trace untouched for longer than
// n1TraceIdleTTL as of now.
func (p *PulsePlugin) sweepIdleN1Traces(now time.Time) {
	p.n1TrackerMu.Lock()
	stale := make(map[string]*n1Trace)
	for id, tr := range p.n1Tracker {
		if now.Sub(tr.lastSeen) > n1TraceIdleTTL {
			stale[id] = tr
			delete(p.n1Tracker, id)
		}
	}
	p.n1TrackerMu.Unlock()

	for id, tr := range stale {
		p.emitN1(id, tr.route, tr)
	}
}

// startN1Sweeper periodically finalizes idle N+1 traces.
func (p *PulsePlugin) startN1Sweeper() {
	p.pulse.startBackground("n1-sweeper", func(ctx context.Context) {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				p.sweepIdleN1Traces(now)
			}
		}
	})
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

				if err := p.pulse.storage.UpdatePoolStats(poolStats); err != nil && p.pulse.config.DevMode {
					p.pulse.logger.Printf("[pulse] failed to update pool stats: %v", err)
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
