package pulse

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v4/disk"
)

// Storage capacity reporting.
//
// Both backends silently age data out: the Memory ring buffers overwrite
// their oldest records once full, and the SQLite write queue drops records
// when it can't keep up. Operators should be told when that — rather than
// Storage.RetentionHours — decides how much history they have. Backends
// report per-kind capacity; the storage_retention_limited and
// storage_writes_dropped built-in alerts fire on it.

// capacityStat describes one kind of stored data.
type capacityStat struct {
	Kind     string `json:"kind"`
	Stored   int64  `json:"stored"`
	Capacity int64  `json:"capacity,omitempty"` // 0: not bounded by record count
	Full     bool   `json:"full"`

	// For a full, count-bounded kind: the oldest record held, and how far
	// back that reaches.
	OldestAt                  *time.Time `json:"oldest_at,omitempty"`
	EffectiveRetentionSeconds float64    `json:"effective_retention_seconds,omitempty"`

	Dropped   int64 `json:"dropped,omitempty"` // writes dropped because a queue was full
	Failed    int64 `json:"failed,omitempty"`  // writes that reached the database but failed
	Bytes     int64 `json:"bytes,omitempty"`
	FreeBytes int64 `json:"free_bytes,omitempty"`
}

// capacityReporter is implemented by storage backends that can describe
// their capacity. Unexported for the same reason as rollupStore.
type capacityReporter interface {
	capacityStats(now time.Time) []capacityStat
}

// retentionAlertKinds are the kinds whose capacity drives the
// storage_retention_limited alert: the core telemetry. Capped side lists
// (N+1 detections, alerts, test runs) are reported but don't alert.
var retentionAlertKinds = map[string]bool{
	"requests": true, "queries": true, "dependencies": true, "runtime": true,
}

// retentionCoverage returns the smallest fraction of Storage.RetentionHours
// that any full core kind actually holds — 1 when nothing is limiting — and
// the kind responsible. ok is false when the backend can't report capacity.
func retentionCoverage(p *Pulse, now time.Time) (coverage float64, limiting *capacityStat, ok bool) {
	cr, ok := p.storage.(capacityReporter)
	if !ok {
		return 1, nil, false
	}
	retention := time.Duration(p.config.Storage.RetentionHours) * time.Hour
	coverage = 1
	if retention <= 0 {
		return coverage, nil, true
	}
	stats := cr.capacityStats(now)
	for i := range stats {
		st := &stats[i]
		if !retentionAlertKinds[st.Kind] || !st.Full || st.OldestAt == nil {
			continue
		}
		if c := st.EffectiveRetentionSeconds / retention.Seconds(); c < coverage {
			coverage, limiting = c, st
		}
	}
	return coverage, limiting, true
}

// retentionDetail explains, for alert messages, which kind limits retention.
func retentionDetail(p *Pulse) string {
	_, st, _ := retentionCoverage(p, time.Now())
	if st == nil {
		return ""
	}
	held := time.Duration(st.EffectiveRetentionSeconds * float64(time.Second)).Round(time.Minute)
	return fmt.Sprintf("%s: holding %s of %dh retention (buffer full at %d records). "+
		"Raise Storage.MemoryCapacity or switch to the SQLite backend.",
		st.Kind, held, p.config.Storage.RetentionHours, st.Capacity)
}

// droppedWrites is the total writes the backend has dropped so far.
func droppedWrites(p *Pulse) (int64, bool) {
	cr, ok := p.storage.(capacityReporter)
	if !ok {
		return 0, false
	}
	var total int64
	for _, st := range cr.capacityStats(time.Now()) {
		total += st.Dropped
	}
	return total, true
}

// memoryCapacityFor resolves the Memory backend's buffer sizes. The runtime
// buffer defaults to whatever holds RetentionHours of samples: at one sample
// per 5s, the fixed 10,000 covered under 14 of the default 24 hours.
func memoryCapacityFor(cfg Config) MemoryCapacity {
	c := cfg.Storage.MemoryCapacity
	if c.Runtime == 0 && cfg.Runtime.SampleInterval > 0 {
		need := int(time.Duration(cfg.Storage.RetentionHours)*time.Hour/cfg.Runtime.SampleInterval) + 1
		c.Runtime = max(need, defaultRuntimeCapacity)
	}
	return c
}

// storageHandler serves GET /pulse/api/storage.
func storageHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()
		kinds := []capacityStat{}
		if cr, ok := p.storage.(capacityReporter); ok {
			kinds = cr.capacityStats(now)
		}
		coverage, limiting, _ := retentionCoverage(p, now)
		resp := gin.H{
			"driver":             storageDriverName(p.config.Storage.Driver),
			"retention_hours":    p.config.Storage.RetentionHours,
			"retention_coverage": coverage,
			"kinds":              kinds,
		}
		if limiting != nil {
			resp["limited_by"] = limiting.Kind
		}
		c.JSON(http.StatusOK, resp)
	}
}

// writeStorageMetrics adds storage capacity gauges to the Prometheus output.
func writeStorageMetrics(b *strings.Builder, p *Pulse) {
	cr, ok := p.storage.(capacityReporter)
	if !ok {
		return
	}
	now := time.Now()
	stats := cr.capacityStats(now)

	fmt.Fprintf(b, "# HELP pulse_storage_records Records held, per storage kind\n")
	fmt.Fprintf(b, "# TYPE pulse_storage_records gauge\n")
	for _, st := range stats {
		fmt.Fprintf(b, "pulse_storage_records{kind=%q} %d\n", st.Kind, st.Stored)
	}
	b.WriteString("\n")

	fmt.Fprintf(b, "# HELP pulse_storage_effective_retention_seconds How far back a full storage kind reaches\n")
	fmt.Fprintf(b, "# TYPE pulse_storage_effective_retention_seconds gauge\n")
	for _, st := range stats {
		if st.Full && st.OldestAt != nil {
			fmt.Fprintf(b, "pulse_storage_effective_retention_seconds{kind=%q} %.0f\n", st.Kind, st.EffectiveRetentionSeconds)
		}
	}
	b.WriteString("\n")

	coverage, _, _ := retentionCoverage(p, now)
	fmt.Fprintf(b, "# HELP pulse_storage_retention_coverage_ratio Fraction of the configured retention the fullest storage kind holds\n")
	fmt.Fprintf(b, "# TYPE pulse_storage_retention_coverage_ratio gauge\n")
	fmt.Fprintf(b, "pulse_storage_retention_coverage_ratio %f\n\n", coverage)

	dropped, _ := droppedWrites(p)
	fmt.Fprintf(b, "# HELP pulse_storage_dropped_writes_total Writes dropped because the storage write queue was full\n")
	fmt.Fprintf(b, "# TYPE pulse_storage_dropped_writes_total counter\n")
	fmt.Fprintf(b, "pulse_storage_dropped_writes_total %d\n\n", dropped)
}

// --- MemoryStorage ---

func (s *MemoryStorage) capacityStats(now time.Time) []capacityStat {
	out := []capacityStat{
		ringCapacity("requests", s.requests, now, func(m RequestMetric) time.Time { return m.Timestamp }),
		ringCapacity("queries", s.queries, now, func(m QueryMetric) time.Time { return m.Timestamp }),
		ringCapacity("dependencies", s.dependencies, now, func(m DependencyMetric) time.Time { return m.Timestamp }),
		ringCapacity("runtime", s.runtimeStats, now, func(m RuntimeMetric) time.Time { return m.Timestamp }),
	}

	s.errorsMu.RLock()
	out = append(out, capacityStat{Kind: "errors", Stored: int64(len(s.errors))})
	s.errorsMu.RUnlock()

	s.alertsMu.RLock()
	st := capacityStat{Kind: "alerts", Stored: int64(len(s.alerts)), Capacity: maxStoredAlerts}
	if len(s.alerts) > 0 {
		st = sliceCapacity(st, s.alerts[0].FiredAt, now)
	}
	s.alertsMu.RUnlock()
	out = append(out, st)

	s.n1Mu.RLock()
	st = capacityStat{Kind: "n1_detections", Stored: int64(len(s.n1Detections)), Capacity: maxStoredN1}
	if len(s.n1Detections) > 0 {
		st = sliceCapacity(st, s.n1Detections[0].DetectedAt, now)
	}
	s.n1Mu.RUnlock()
	out = append(out, st)

	s.testRunsMu.RLock()
	st = capacityStat{Kind: "test_runs", Stored: int64(len(s.testRuns)), Capacity: maxStoredTestRuns}
	if len(s.testRuns) > 0 {
		st = sliceCapacity(st, s.testRuns[0].StartedAt, now)
	}
	s.testRunsMu.RUnlock()
	out = append(out, st)

	return out
}

func ringCapacity[T any](kind string, rb *RingBuffer[T], now time.Time, ts func(T) time.Time) capacityStat {
	st := capacityStat{Kind: kind, Stored: int64(rb.Len()), Capacity: rb.capacity}
	if st.Stored < st.Capacity {
		return st
	}
	st.Full = true
	if oldest, ok := rb.oldest(); ok {
		t := ts(oldest)
		st.OldestAt = &t
		st.EffectiveRetentionSeconds = now.Sub(t).Seconds()
	}
	return st
}

// sliceCapacity fills in fullness for a capped, oldest-first slice.
func sliceCapacity(st capacityStat, oldest, now time.Time) capacityStat {
	if st.Stored < st.Capacity {
		return st
	}
	st.Full = true
	st.OldestAt = &oldest
	st.EffectiveRetentionSeconds = now.Sub(oldest).Seconds()
	return st
}

// --- SQLiteStorage ---

func (s *SQLiteStorage) capacityStats(now time.Time) []capacityStat {
	depth, dropped, failed := s.writeQueueStats()
	out := []capacityStat{{
		Kind:     "write_queue",
		Stored:   int64(depth),
		Capacity: int64(cap(s.queue)),
		Full:     depth >= cap(s.queue),
		Dropped:  dropped,
		Failed:   failed,
	}}
	if s.path != "" {
		db := capacityStat{Kind: "database"}
		for _, suffix := range []string{"", "-wal"} {
			if fi, err := os.Stat(s.path + suffix); err == nil {
				db.Bytes += fi.Size()
			}
		}
		if abs, err := filepath.Abs(s.path); err == nil {
			if u, err := disk.Usage(filepath.Dir(abs)); err == nil {
				db.FreeBytes = int64(u.Free)
			}
		}
		out = append(out, db)
	}
	return out
}

// sqliteFilePath returns the database file a DSN refers to, or "" for an
// in-memory database.
func sqliteFilePath(dsn string) string {
	path := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || strings.Contains(path, ":memory:") {
		return ""
	}
	return path
}

var (
	_ capacityReporter = (*MemoryStorage)(nil)
	_ capacityReporter = (*SQLiteStorage)(nil)
)
