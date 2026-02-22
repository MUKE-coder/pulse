package pulse

import (
	"testing"
	"time"
)

func setupAggregatorPulse(t *testing.T) *Pulse {
	t.Helper()
	cfg := applyDefaults(Config{})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })
	return p
}

// injectRequests adds request metrics with controlled timestamps.
func injectRequests(p *Pulse, count int, method, path string, baseTime time.Time, interval time.Duration, statusCode int, latency time.Duration) {
	for i := 0; i < count; i++ {
		p.storage.StoreRequest(RequestMetric{
			Method:     method,
			Path:       path,
			StatusCode: statusCode,
			Latency:    latency,
			Timestamp:  baseTime.Add(time.Duration(i) * interval),
		})
	}
}

func TestAggregator_ComputeRouteStatsWithTrend(t *testing.T) {
	p := setupAggregatorPulse(t)
	agg := &Aggregator{pulse: p}

	now := time.Now()

	// Inject requests for a route across the last hour
	injectRequests(p, 50, "GET", "/api/users", now.Add(-30*time.Minute), 30*time.Second, 200, 100*time.Millisecond)

	stats := agg.computeRouteStatsWithTrend(Last1h())
	if len(stats) == 0 {
		t.Fatal("expected at least 1 route stat")
	}

	found := false
	for _, s := range stats {
		if s.Method == "GET" && s.Path == "/api/users" {
			found = true
			if s.RequestCount != 50 {
				t.Errorf("expected 50 requests, got %d", s.RequestCount)
			}
			// Trend should be "stable" since data is consistent
			if s.Trend != "stable" {
				t.Errorf("expected trend 'stable', got %q", s.Trend)
			}
		}
	}
	if !found {
		t.Error("expected to find GET /api/users in route stats")
	}
}

func TestDetectTrend_Stable(t *testing.T) {
	current := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    2.0,
		RPM:          50.0,
	}
	previous := RouteStats{
		RequestCount: 100,
		P95Latency:   210 * time.Millisecond,
		ErrorRate:    1.8,
		RPM:          48.0,
	}

	trend := detectTrend(current, previous)
	if trend != "stable" {
		t.Errorf("expected 'stable', got %q", trend)
	}
}

func TestDetectTrend_Degrading_LatencySpike(t *testing.T) {
	current := RouteStats{
		RequestCount: 100,
		P95Latency:   600 * time.Millisecond, // 200% increase
		ErrorRate:    2.0,
		RPM:          50.0,
	}
	previous := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    1.5,
		RPM:          50.0,
	}

	trend := detectTrend(current, previous)
	if trend != "degrading" {
		t.Errorf("expected 'degrading' with latency spike, got %q", trend)
	}
}

func TestDetectTrend_Degrading_ErrorSpike(t *testing.T) {
	current := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    10.0, // >100% increase
		RPM:          50.0,
	}
	previous := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    3.0,
		RPM:          50.0,
	}

	trend := detectTrend(current, previous)
	if trend != "degrading" {
		t.Errorf("expected 'degrading' with error spike, got %q", trend)
	}
}

func TestDetectTrend_Improving(t *testing.T) {
	current := RouteStats{
		RequestCount: 100,
		P95Latency:   100 * time.Millisecond, // 50% decrease
		ErrorRate:    0.5,                     // 75% decrease
		RPM:          60.0,
	}
	previous := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    2.0,
		RPM:          50.0,
	}

	trend := detectTrend(current, previous)
	if trend != "improving" {
		t.Errorf("expected 'improving', got %q", trend)
	}
}

func TestDetectTrend_InsufficientData(t *testing.T) {
	current := RouteStats{
		RequestCount: 2, // too few
		P95Latency:   500 * time.Millisecond,
		ErrorRate:    50.0,
		RPM:          1.0,
	}
	previous := RouteStats{
		RequestCount: 3,
		P95Latency:   100 * time.Millisecond,
		ErrorRate:    1.0,
		RPM:          10.0,
	}

	trend := detectTrend(current, previous)
	if trend != "stable" {
		t.Errorf("expected 'stable' with insufficient data, got %q", trend)
	}
}

func TestDetectTrend_NewErrors(t *testing.T) {
	current := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    10.0, // new errors where there were none
		RPM:          50.0,
	}
	previous := RouteStats{
		RequestCount: 100,
		P95Latency:   200 * time.Millisecond,
		ErrorRate:    0.0, // no errors before
		RPM:          50.0,
	}

	trend := detectTrend(current, previous)
	if trend != "degrading" {
		t.Errorf("expected 'degrading' with new errors, got %q", trend)
	}
}

func TestResolutionForRange(t *testing.T) {
	tests := []struct {
		name     string
		span     time.Duration
		expected time.Duration
	}{
		{"5m window", 5 * time.Minute, 5 * time.Second},
		{"15m window", 15 * time.Minute, 10 * time.Second},
		{"1h window", 1 * time.Hour, 30 * time.Second},
		{"6h window", 6 * time.Hour, 1 * time.Minute},
		{"24h window", 24 * time.Hour, 5 * time.Minute},
		{"7d window", 7 * 24 * time.Hour, 1 * time.Hour},
		{"3m window", 3 * time.Minute, 5 * time.Second},
		{"2h window", 2 * time.Hour, 1 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			tr := TimeRange{Start: now.Add(-tt.span), End: now}
			got := ResolutionForRange(tr)
			if got != tt.expected {
				t.Errorf("ResolutionForRange(%v) = %v, want %v", tt.span, got, tt.expected)
			}
		})
	}
}

func TestRollupRequests(t *testing.T) {
	p := setupAggregatorPulse(t)

	now := time.Now()
	// Inject 60 requests over 5 minutes (one per 5 seconds)
	for i := 0; i < 60; i++ {
		status := 200
		if i%10 == 0 {
			status = 500
		}
		p.storage.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       "/api/data",
			StatusCode: status,
			Latency:    time.Duration(50+i) * time.Millisecond,
			Timestamp:  now.Add(-5*time.Minute + time.Duration(i)*5*time.Second),
		})
	}

	tr := TimeRange{Start: now.Add(-5 * time.Minute), End: now}
	resolution := 30 * time.Second
	buckets := rollupRequests(p.storage, tr, resolution)

	if len(buckets) == 0 {
		t.Fatal("expected non-empty rollup buckets")
	}

	// Verify total count across all buckets
	var totalCount int64
	var totalErrors int64
	for _, b := range buckets {
		totalCount += b.Count
		totalErrors += b.ErrorCount
	}
	if totalCount != 60 {
		t.Errorf("expected total count 60, got %d", totalCount)
	}
	if totalErrors != 6 { // every 10th request is an error
		t.Errorf("expected 6 errors, got %d", totalErrors)
	}

	// Buckets should be chronologically ordered
	for i := 1; i < len(buckets); i++ {
		if buckets[i].Timestamp.Before(buckets[i-1].Timestamp) {
			t.Error("expected buckets to be chronologically ordered")
			break
		}
	}
}

func TestRollupRequests_FillsGaps(t *testing.T) {
	p := setupAggregatorPulse(t)

	now := time.Now()
	// Only inject requests at the start and end, leaving a gap in the middle
	p.storage.StoreRequest(RequestMetric{
		Method: "GET", Path: "/gap", StatusCode: 200,
		Latency: 10 * time.Millisecond, Timestamp: now.Add(-5 * time.Minute),
	})
	p.storage.StoreRequest(RequestMetric{
		Method: "GET", Path: "/gap", StatusCode: 200,
		Latency: 10 * time.Millisecond, Timestamp: now.Add(-10 * time.Second),
	})

	tr := TimeRange{Start: now.Add(-5 * time.Minute), End: now}
	buckets := rollupRequests(p.storage, tr, 30*time.Second)

	// Should have buckets for every 30s interval, most with Count=0
	emptyBuckets := 0
	for _, b := range buckets {
		if b.Count == 0 {
			emptyBuckets++
		}
	}
	if emptyBuckets == 0 {
		t.Error("expected some empty (gap-filling) buckets")
	}
}

func TestComputeTimeSeries(t *testing.T) {
	p := setupAggregatorPulse(t)
	agg := &Aggregator{pulse: p}

	now := time.Now()
	// Inject requests
	injectRequests(p, 30, "GET", "/api/ts", now.Add(-10*time.Minute), 20*time.Second, 200, 50*time.Millisecond)

	throughput, errors, latency := agg.computeTimeSeries(Last1h())

	if len(throughput) == 0 {
		t.Fatal("expected non-empty throughput time-series")
	}
	if len(errors) == 0 {
		t.Fatal("expected non-empty errors time-series")
	}
	if len(latency) == 0 {
		t.Fatal("expected non-empty latency time-series")
	}

	// All series should have the same length
	if len(throughput) != len(errors) || len(errors) != len(latency) {
		t.Errorf("time-series lengths differ: throughput=%d, errors=%d, latency=%d",
			len(throughput), len(errors), len(latency))
	}

	// Verify some throughput buckets have data
	hasData := false
	for _, pt := range throughput {
		if pt.Value > 0 {
			hasData = true
			break
		}
	}
	if !hasData {
		t.Error("expected at least one throughput bucket with data")
	}
}

func TestRollupRuntime(t *testing.T) {
	p := setupAggregatorPulse(t)

	now := time.Now()
	// Inject runtime metrics
	for i := 0; i < 60; i++ {
		p.storage.StoreRuntime(RuntimeMetric{
			HeapAlloc:    uint64(1024 * 1024 * (10 + i)),
			NumGoroutine: 50 + i,
			Timestamp:    now.Add(-30*time.Minute + time.Duration(i)*30*time.Second),
		})
	}

	tr := TimeRange{Start: now.Add(-30 * time.Minute), End: now}
	result := RollupRuntime(p.storage, tr, 5*time.Minute)

	if len(result) == 0 {
		t.Fatal("expected non-empty runtime rollup")
	}

	// Should have fewer buckets than raw samples (downsampled)
	if len(result) >= 60 {
		t.Errorf("expected fewer buckets than raw samples (60), got %d", len(result))
	}

	// Should be sorted by timestamp
	for i := 1; i < len(result); i++ {
		if result[i].Timestamp.Before(result[i-1].Timestamp) {
			t.Error("expected runtime rollup to be sorted by timestamp")
			break
		}
	}
}

func TestAggregator_Run(t *testing.T) {
	p := setupAggregatorPulse(t)
	agg := &Aggregator{pulse: p}

	now := time.Now()
	// Inject some data
	injectRequests(p, 20, "GET", "/api/test", now.Add(-5*time.Minute), 15*time.Second, 200, 100*time.Millisecond)
	injectRequests(p, 5, "POST", "/api/test", now.Add(-3*time.Minute), 30*time.Second, 500, 500*time.Millisecond)

	// Run aggregation
	agg.run()

	// Check cached route stats
	stats := agg.GetCachedRouteStats()
	if len(stats) == 0 {
		t.Fatal("expected cached route stats after aggregation")
	}

	// Check cached overview
	overview := agg.GetCachedOverview()
	if overview == nil {
		t.Fatal("expected cached overview after aggregation")
	}
	if overview.TotalRequests != 25 {
		t.Errorf("expected 25 total requests, got %d", overview.TotalRequests)
	}

	// Check cached time-series
	throughput, errors, latency := agg.GetCachedTimeSeries()
	if len(throughput) == 0 {
		t.Error("expected cached throughput time-series")
	}
	if len(errors) == 0 {
		t.Error("expected cached errors time-series")
	}
	if len(latency) == 0 {
		t.Error("expected cached latency time-series")
	}
}

func TestAggregator_BackgroundLoop(t *testing.T) {
	cfg := applyDefaults(Config{DevMode: true})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	now := time.Now()
	injectRequests(p, 10, "GET", "/bg", now.Add(-2*time.Minute), 10*time.Second, 200, 50*time.Millisecond)

	agg := newAggregator(p)

	// Wait for at least one aggregation tick
	time.Sleep(200 * time.Millisecond)

	overview := agg.GetCachedOverview()
	if overview == nil {
		t.Fatal("expected aggregator to produce cached overview")
	}
	if overview.TotalRequests < 10 {
		t.Errorf("expected at least 10 requests in overview, got %d", overview.TotalRequests)
	}
}

func TestComputeCompositeHealth(t *testing.T) {
	p := setupAggregatorPulse(t)
	ms := p.storage.(*MemoryStorage)

	// No checks → healthy
	status := computeCompositeHealth(p, ms)
	if status != "healthy" {
		t.Errorf("expected 'healthy' with no checks, got %q", status)
	}

	// Add a critical check
	p.AddHealthCheck(HealthCheck{Name: "db", Type: "database", Critical: true})
	ms.StoreHealthResult(HealthCheckResult{Name: "db", Status: "healthy", Timestamp: time.Now()})

	status = computeCompositeHealth(p, ms)
	if status != "healthy" {
		t.Errorf("expected 'healthy' with passing critical check, got %q", status)
	}

	// Fail the critical check
	ms.StoreHealthResult(HealthCheckResult{Name: "db", Status: "unhealthy", Timestamp: time.Now()})
	status = computeCompositeHealth(p, ms)
	if status != "unhealthy" {
		t.Errorf("expected 'unhealthy' with failing critical check, got %q", status)
	}

	// Add a non-critical check that's unhealthy, critical check is healthy
	ms.StoreHealthResult(HealthCheckResult{Name: "db", Status: "healthy", Timestamp: time.Now()})
	p.AddHealthCheck(HealthCheck{Name: "cache", Type: "redis", Critical: false})
	ms.StoreHealthResult(HealthCheckResult{Name: "cache", Status: "unhealthy", Timestamp: time.Now()})

	status = computeCompositeHealth(p, ms)
	if status != "degraded" {
		t.Errorf("expected 'degraded' with failing non-critical check, got %q", status)
	}
}

func BenchmarkAggregatorRun(b *testing.B) {
	cfg := applyDefaults(Config{})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("bench")
	defer p.Shutdown()

	now := time.Now()
	// Inject 10K requests across 50 routes
	for i := 0; i < 10000; i++ {
		route := "/api/route-" + string(rune('a'+i%50))
		status := 200
		if i%20 == 0 {
			status = 500
		}
		p.storage.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       route,
			StatusCode: status,
			Latency:    time.Duration(10+i%500) * time.Millisecond,
			Timestamp:  now.Add(-time.Duration(i) * 360 * time.Millisecond),
		})
	}

	agg := &Aggregator{pulse: p}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		agg.run()
	}
}
