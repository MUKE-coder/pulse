package pulse

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Aggregator periodically computes route stats, time-series rollups, trend
// detection, and overview snapshots from raw metrics. Dashboard API endpoints
// read from the cached aggregation rather than scanning raw ring buffers on
// every request.
type Aggregator struct {
	pulse *Pulse

	mu            sync.RWMutex
	routeStats    []RouteStats
	overview      *Overview
	throughputTS  []TimeSeriesPoint // request count per bucket
	errorTS       []TimeSeriesPoint // error count per bucket
	latencyTS     []TimeSeriesPoint // avg latency per bucket
}

// newAggregator creates and starts the aggregation background loop.
func newAggregator(p *Pulse) *Aggregator {
	agg := &Aggregator{
		pulse: p,
	}

	interval := 10 * time.Second
	if p.config.DevMode {
		interval = 5 * time.Second
	}

	p.startBackground("aggregator", func(ctx context.Context) {
		// Run initial aggregation immediately
		agg.run()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				agg.run()
			}
		}
	})

	return agg
}

// run performs a single aggregation pass.
func (agg *Aggregator) run() {
	tr := Last1h()

	// 1. Compute per-route stats with trend detection
	routeStats := agg.computeRouteStatsWithTrend(tr)

	// 2. Compute time-series rollups
	throughput, errors, latency := agg.computeTimeSeries(tr)

	// 3. Compute overview
	overview := agg.computeOverview(tr, routeStats, throughput, errors)

	// Swap in new data
	agg.mu.Lock()
	agg.routeStats = routeStats
	agg.overview = overview
	agg.throughputTS = throughput
	agg.errorTS = errors
	agg.latencyTS = latency
	agg.mu.Unlock()

	// Broadcast overview to connected WebSocket clients
	agg.pulse.BroadcastOverview(overview)
}

// --- Cached Getters (used by API endpoints) ---

// GetCachedRouteStats returns the most recently computed route stats.
func (agg *Aggregator) GetCachedRouteStats() []RouteStats {
	agg.mu.RLock()
	defer agg.mu.RUnlock()
	cp := make([]RouteStats, len(agg.routeStats))
	copy(cp, agg.routeStats)
	return cp
}

// GetCachedOverview returns the most recently computed overview.
func (agg *Aggregator) GetCachedOverview() *Overview {
	agg.mu.RLock()
	defer agg.mu.RUnlock()
	if agg.overview == nil {
		return nil
	}
	cp := *agg.overview
	return &cp
}

// GetCachedTimeSeries returns throughput, error, and latency time-series.
func (agg *Aggregator) GetCachedTimeSeries() (throughput, errors, latency []TimeSeriesPoint) {
	agg.mu.RLock()
	defer agg.mu.RUnlock()

	throughput = make([]TimeSeriesPoint, len(agg.throughputTS))
	copy(throughput, agg.throughputTS)
	errors = make([]TimeSeriesPoint, len(agg.errorTS))
	copy(errors, agg.errorTS)
	latency = make([]TimeSeriesPoint, len(agg.latencyTS))
	copy(latency, agg.latencyTS)
	return
}

// --- Per-Route Aggregation with Trend Detection ---

func (agg *Aggregator) computeRouteStatsWithTrend(tr TimeRange) []RouteStats {
	stats, _ := agg.pulse.storage.GetRouteStats(tr)

	// Compute trend for each route by comparing last 5m vs previous 5m
	now := time.Now()
	currentWindow := TimeRange{Start: now.Add(-5 * time.Minute), End: now}
	previousWindow := TimeRange{Start: now.Add(-10 * time.Minute), End: now.Add(-5 * time.Minute)}

	currentStats, _ := agg.pulse.storage.GetRouteStats(currentWindow)
	previousStats, _ := agg.pulse.storage.GetRouteStats(previousWindow)

	// Build lookups by method+path
	currentMap := make(map[string]RouteStats, len(currentStats))
	for _, s := range currentStats {
		currentMap[s.Method+"|"+s.Path] = s
	}
	previousMap := make(map[string]RouteStats, len(previousStats))
	for _, s := range previousStats {
		previousMap[s.Method+"|"+s.Path] = s
	}

	// Apply trend to the full stats
	for i, s := range stats {
		key := s.Method + "|" + s.Path
		cur, hasCur := currentMap[key]
		prev, hasPrev := previousMap[key]

		if hasCur && hasPrev {
			stats[i].Trend = detectTrend(cur, prev)
		} else {
			stats[i].Trend = "stable"
		}
	}

	return stats
}

// detectTrend compares current window stats against previous window to
// determine if a route is improving, stable, or degrading.
func detectTrend(current, previous RouteStats) string {
	// Need meaningful data in both windows
	if previous.RequestCount < 5 || current.RequestCount < 5 {
		return "stable"
	}

	degradingSignals := 0
	improvingSignals := 0

	// Compare p95 latency: >50% increase → degrading, >30% decrease → improving
	if previous.P95Latency > 0 {
		latencyChange := float64(current.P95Latency-previous.P95Latency) / float64(previous.P95Latency)
		if latencyChange > 0.5 {
			degradingSignals++
		} else if latencyChange < -0.3 {
			improvingSignals++
		}
	}

	// Compare error rate: >100% increase → degrading, >50% decrease → improving
	if previous.ErrorRate > 0 {
		errorChange := (current.ErrorRate - previous.ErrorRate) / previous.ErrorRate
		if errorChange > 1.0 {
			degradingSignals++
		} else if errorChange < -0.5 {
			improvingSignals++
		}
	} else if current.ErrorRate > 5.0 {
		// New errors appearing where there were none
		degradingSignals++
	}

	// Compare throughput: >50% drop → degrading (potential issue)
	if previous.RPM > 0 {
		rpmChange := (current.RPM - previous.RPM) / previous.RPM
		if rpmChange < -0.5 {
			degradingSignals++
		}
	}

	if degradingSignals >= 2 {
		return "degrading"
	}
	if degradingSignals >= 1 && improvingSignals == 0 {
		return "degrading"
	}
	if improvingSignals >= 2 {
		return "improving"
	}
	if improvingSignals >= 1 && degradingSignals == 0 {
		return "improving"
	}
	return "stable"
}

// --- Time-Series Rollup ---

// ResolutionForRange returns the appropriate bucket duration for a given time range.
func ResolutionForRange(tr TimeRange) time.Duration {
	span := tr.End.Sub(tr.Start)
	switch {
	case span <= 5*time.Minute:
		return 5 * time.Second
	case span <= 15*time.Minute:
		return 10 * time.Second
	case span <= 1*time.Hour:
		return 30 * time.Second
	case span <= 6*time.Hour:
		return 1 * time.Minute
	case span <= 24*time.Hour:
		return 5 * time.Minute
	default:
		return 1 * time.Hour
	}
}

// TimeSeriesBucket holds raw data for a single time bucket during rollup.
type TimeSeriesBucket struct {
	Timestamp  time.Time
	Count      int64
	ErrorCount int64
	Latencies  []time.Duration
}

func (agg *Aggregator) computeTimeSeries(tr TimeRange) (throughput, errors, latency []TimeSeriesPoint) {
	resolution := ResolutionForRange(tr)
	buckets := rollupRequests(agg.pulse.storage, tr, resolution)

	throughput = make([]TimeSeriesPoint, 0, len(buckets))
	errors = make([]TimeSeriesPoint, 0, len(buckets))
	latency = make([]TimeSeriesPoint, 0, len(buckets))

	for _, b := range buckets {
		throughput = append(throughput, TimeSeriesPoint{
			Timestamp: b.Timestamp,
			Value:     float64(b.Count),
		})
		errors = append(errors, TimeSeriesPoint{
			Timestamp: b.Timestamp,
			Value:     float64(b.ErrorCount),
		})

		var avgLatency float64
		if len(b.Latencies) > 0 {
			avgLatency = float64(ComputeAvg(b.Latencies)) / float64(time.Millisecond)
		}
		latency = append(latency, TimeSeriesPoint{
			Timestamp: b.Timestamp,
			Value:     avgLatency,
		})
	}

	return
}

// rollupRequests buckets request metrics into time intervals of the given resolution.
func rollupRequests(storage Storage, tr TimeRange, resolution time.Duration) []TimeSeriesBucket {
	// Calculate bucket boundaries
	start := tr.Start.Truncate(resolution)
	end := tr.End

	numBuckets := int(end.Sub(start)/resolution) + 1
	if numBuckets > 10000 {
		numBuckets = 10000
	}

	bucketMap := make(map[int64]*TimeSeriesBucket, numBuckets)

	// Iterate over raw requests and assign to buckets
	requests, _ := storage.GetRequests(RequestFilter{TimeRange: tr, Limit: 0})
	for _, r := range requests {
		bucketTS := r.Timestamp.Truncate(resolution)
		key := bucketTS.Unix()

		b, ok := bucketMap[key]
		if !ok {
			b = &TimeSeriesBucket{Timestamp: bucketTS}
			bucketMap[key] = b
		}
		b.Count++
		if r.StatusCode >= 400 {
			b.ErrorCount++
		}
		b.Latencies = append(b.Latencies, r.Latency)
	}

	// Convert map to sorted slice, filling gaps with zero-value buckets
	result := make([]TimeSeriesBucket, 0, numBuckets)
	for ts := start; !ts.After(end); ts = ts.Add(resolution) {
		key := ts.Unix()
		if b, ok := bucketMap[key]; ok {
			result = append(result, *b)
		} else {
			result = append(result, TimeSeriesBucket{Timestamp: ts})
		}
	}

	return result
}

// RollupRuntime buckets runtime metrics into time intervals. Exported for API use.
func RollupRuntime(storage Storage, tr TimeRange, resolution time.Duration) []RuntimeMetric {
	history, _ := storage.GetRuntimeHistory(tr)
	if len(history) == 0 {
		return nil
	}

	// Group by bucket timestamp, take the latest sample per bucket
	type bucket struct {
		metric RuntimeMetric
		ts     time.Time
	}
	bucketMap := make(map[int64]bucket)
	for _, m := range history {
		bucketTS := m.Timestamp.Truncate(resolution)
		key := bucketTS.Unix()
		existing, ok := bucketMap[key]
		if !ok || m.Timestamp.After(existing.ts) {
			bucketMap[key] = bucket{metric: m, ts: m.Timestamp}
		}
	}

	// Convert to sorted slice
	result := make([]RuntimeMetric, 0, len(bucketMap))
	for _, b := range bucketMap {
		result = append(result, b.metric)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})

	return result
}

// --- Overview Snapshot ---

func (agg *Aggregator) computeOverview(tr TimeRange, routeStats []RouteStats, throughputTS, errorTS []TimeSeriesPoint) *Overview {
	overview, _ := agg.pulse.storage.GetOverview(tr)
	if overview == nil {
		return nil
	}

	// Enrich with trend-aware route stats
	topRoutes := routeStats
	if len(topRoutes) > 10 {
		topRoutes = topRoutes[:10]
	}
	overview.TopRoutes = topRoutes

	// Attach time-series data
	overview.ThroughputSeries = throughputTS
	overview.ErrorSeries = errorTS

	// Compute health status from stored health results
	if ms, ok := agg.pulse.storage.(*MemoryStorage); ok {
		overview.HealthStatus = computeCompositeHealth(agg.pulse, ms)
	}

	return overview
}

// computeCompositeHealth determines the overall health status.
func computeCompositeHealth(p *Pulse, ms *MemoryStorage) string {
	latestResults := ms.getLatestHealthResults()
	if len(latestResults) == 0 {
		return "healthy" // no checks registered → healthy by default
	}

	p.healthMu.RLock()
	checks := make([]HealthCheck, len(p.healthChecks))
	copy(checks, p.healthChecks)
	p.healthMu.RUnlock()

	hasCriticalFailure := false
	hasNonCriticalFailure := false

	for _, check := range checks {
		result, ok := latestResults[check.Name]
		if !ok {
			continue
		}
		if result.Status != "healthy" {
			if check.Critical {
				hasCriticalFailure = true
			} else {
				hasNonCriticalFailure = true
			}
		}
	}

	if hasCriticalFailure {
		return "unhealthy"
	}
	if hasNonCriticalFailure {
		return "degraded"
	}
	return "healthy"
}
