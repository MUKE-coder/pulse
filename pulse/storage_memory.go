package pulse

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	defaultRequestCapacity  = 100000
	defaultQueryCapacity    = 50000
	defaultRuntimeCapacity  = 10000
	defaultHealthCapacity   = 1000
	defaultDependencyCapacity = 50000
)

// MemoryStorage is an in-memory Storage implementation backed by ring buffers.
type MemoryStorage struct {
	requests     *RingBuffer[RequestMetric]
	queries      *RingBuffer[QueryMetric]
	runtimeStats *RingBuffer[RuntimeMetric]
	dependencies *RingBuffer[DependencyMetric]

	// Errors use a map keyed by fingerprint for deduplication
	errors   map[string]*ErrorRecord
	errorsMu sync.RWMutex

	// Health check results per check name
	healthResults map[string]*RingBuffer[HealthCheckResult]
	healthMu      sync.RWMutex

	// Alerts
	alerts   []AlertRecord
	alertsMu sync.RWMutex

	// N+1 detections
	n1Detections []N1Detection
	n1Mu         sync.RWMutex

	// Connection pool stats (updated periodically)
	poolStats *PoolStats
	poolMu    sync.RWMutex

	// Config
	appName   string
	startTime time.Time
}

// NewMemoryStorage creates a new in-memory storage with default ring buffer capacities.
func NewMemoryStorage(appName string) *MemoryStorage {
	return &MemoryStorage{
		requests:      NewRingBuffer[RequestMetric](defaultRequestCapacity),
		queries:       NewRingBuffer[QueryMetric](defaultQueryCapacity),
		runtimeStats:  NewRingBuffer[RuntimeMetric](defaultRuntimeCapacity),
		dependencies:  NewRingBuffer[DependencyMetric](defaultDependencyCapacity),
		errors:        make(map[string]*ErrorRecord),
		healthResults: make(map[string]*RingBuffer[HealthCheckResult]),
		alerts:        make([]AlertRecord, 0),
		n1Detections:  make([]N1Detection, 0),
		appName:       appName,
		startTime:     time.Now(),
	}
}

// --- Request Metrics ---

// StoreRequest stores a request metric.
func (s *MemoryStorage) StoreRequest(m RequestMetric) error {
	s.requests.Push(m)
	return nil
}

// GetRequests returns requests matching the filter.
func (s *MemoryStorage) GetRequests(filter RequestFilter) ([]RequestMetric, error) {
	all := s.requests.Filter(func(m RequestMetric) bool {
		if !filter.TimeRange.Start.IsZero() && m.Timestamp.Before(filter.TimeRange.Start) {
			return false
		}
		if !filter.TimeRange.End.IsZero() && m.Timestamp.After(filter.TimeRange.End) {
			return false
		}
		if filter.Method != "" && m.Method != filter.Method {
			return false
		}
		if filter.Path != "" && m.Path != filter.Path {
			return false
		}
		if filter.StatusCode != 0 && m.StatusCode != filter.StatusCode {
			return false
		}
		if filter.MinLatency > 0 && m.Latency < filter.MinLatency {
			return false
		}
		return true
	})

	// Apply offset and limit
	if filter.Offset > 0 && filter.Offset < len(all) {
		all = all[filter.Offset:]
	} else if filter.Offset >= len(all) {
		return nil, nil
	}
	if filter.Limit > 0 && filter.Limit < len(all) {
		all = all[:filter.Limit]
	}

	return all, nil
}

// GetRouteStats returns aggregated stats per route within the time range.
func (s *MemoryStorage) GetRouteStats(timeRange TimeRange) ([]RouteStats, error) {
	// Group requests by method+path
	type routeKey struct{ method, path string }
	groups := make(map[routeKey][]RequestMetric)

	s.requests.ForEach(func(m RequestMetric) bool {
		if m.Timestamp.Before(timeRange.Start) || m.Timestamp.After(timeRange.End) {
			return true
		}
		key := routeKey{m.Method, m.Path}
		groups[key] = append(groups[key], m)
		return true
	})

	duration := timeRange.End.Sub(timeRange.Start)
	stats := make([]RouteStats, 0, len(groups))
	for key, reqs := range groups {
		stats = append(stats, computeRouteStats(key.method, key.path, reqs, duration))
	}

	// Sort by request count descending
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].RequestCount > stats[j].RequestCount
	})

	return stats, nil
}

// GetRouteDetail returns detailed stats for a specific route.
func (s *MemoryStorage) GetRouteDetail(method, path string, timeRange TimeRange) (*RouteDetail, error) {
	var reqs []RequestMetric
	s.requests.ForEach(func(m RequestMetric) bool {
		if m.Method == method && m.Path == path &&
			!m.Timestamp.Before(timeRange.Start) && !m.Timestamp.After(timeRange.End) {
			reqs = append(reqs, m)
		}
		return true
	})

	if len(reqs) == 0 {
		return nil, nil
	}

	duration := timeRange.End.Sub(timeRange.Start)
	rs := computeRouteStats(method, path, reqs, duration)

	// Get recent requests (last 50)
	recent := reqs
	if len(recent) > 50 {
		recent = recent[len(recent)-50:]
	}

	// Get related errors
	errors, _ := s.GetErrors(ErrorFilter{
		TimeRange: timeRange,
		Route:     fmt.Sprintf("%s %s", method, path),
		Limit:     20,
	})

	// Get related queries
	patterns, _ := s.GetQueryPatterns(timeRange)

	detail := &RouteDetail{
		RouteStats:     rs,
		RecentRequests: recent,
		RecentErrors:   errors,
		TopQueries:     patterns,
	}

	return detail, nil
}

// --- Query Metrics ---

// StoreQuery stores a query metric.
func (s *MemoryStorage) StoreQuery(m QueryMetric) error {
	s.queries.Push(m)
	return nil
}

// GetSlowQueries returns queries slower than the threshold.
func (s *MemoryStorage) GetSlowQueries(threshold time.Duration, limit int) ([]QueryMetric, error) {
	slow := s.queries.Filter(func(m QueryMetric) bool {
		return m.Duration >= threshold
	})

	// Sort by duration descending
	sort.Slice(slow, func(i, j int) bool {
		return slow[i].Duration > slow[j].Duration
	})

	if limit > 0 && limit < len(slow) {
		slow = slow[:limit]
	}

	return slow, nil
}

// GetQueryPatterns returns aggregated query patterns.
func (s *MemoryStorage) GetQueryPatterns(timeRange TimeRange) ([]QueryPattern, error) {
	type patternAgg struct {
		normalized string
		operation  string
		table      string
		durations  []time.Duration
		errCount   int64
	}

	patterns := make(map[string]*patternAgg)

	s.queries.ForEach(func(m QueryMetric) bool {
		if m.Timestamp.Before(timeRange.Start) || m.Timestamp.After(timeRange.End) {
			return true
		}
		key := m.NormalizedSQL
		if key == "" {
			key = m.SQL
		}
		p, ok := patterns[key]
		if !ok {
			p = &patternAgg{
				normalized: key,
				operation:  m.Operation,
				table:      m.Table,
			}
			patterns[key] = p
		}
		p.durations = append(p.durations, m.Duration)
		if m.Error != "" {
			p.errCount++
		}
		return true
	})

	result := make([]QueryPattern, 0, len(patterns))
	for _, p := range patterns {
		var total, max time.Duration
		for _, d := range p.durations {
			total += d
			if d > max {
				max = d
			}
		}
		avg := total / time.Duration(len(p.durations))
		result = append(result, QueryPattern{
			NormalizedSQL: p.normalized,
			Operation:     p.operation,
			Table:         p.table,
			Count:         int64(len(p.durations)),
			AvgDuration:   avg,
			MaxDuration:   max,
			TotalDuration: total,
			ErrorCount:    p.errCount,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalDuration > result[j].TotalDuration
	})

	return result, nil
}

// GetN1Detections returns detected N+1 query issues.
func (s *MemoryStorage) GetN1Detections(timeRange TimeRange) ([]N1Detection, error) {
	s.n1Mu.RLock()
	defer s.n1Mu.RUnlock()

	var result []N1Detection
	for _, d := range s.n1Detections {
		if !d.DetectedAt.Before(timeRange.Start) && !d.DetectedAt.After(timeRange.End) {
			result = append(result, d)
		}
	}
	return result, nil
}

// StoreN1Detection stores an N+1 detection. (Not part of Storage interface, used internally.)
func (s *MemoryStorage) StoreN1Detection(d N1Detection) {
	s.n1Mu.Lock()
	defer s.n1Mu.Unlock()
	s.n1Detections = append(s.n1Detections, d)
	// Cap at 1000 detections
	if len(s.n1Detections) > 1000 {
		s.n1Detections = s.n1Detections[len(s.n1Detections)-1000:]
	}
}

// GetConnectionPoolStats returns the latest connection pool stats.
func (s *MemoryStorage) GetConnectionPoolStats() (*PoolStats, error) {
	s.poolMu.RLock()
	defer s.poolMu.RUnlock()
	if s.poolStats == nil {
		return nil, nil
	}
	cp := *s.poolStats
	return &cp, nil
}

// UpdatePoolStats updates the connection pool stats. (Not part of Storage interface.)
func (s *MemoryStorage) UpdatePoolStats(stats PoolStats) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	s.poolStats = &stats
}

// --- Runtime Metrics ---

// StoreRuntime stores a runtime metric snapshot.
func (s *MemoryStorage) StoreRuntime(m RuntimeMetric) error {
	s.runtimeStats.Push(m)
	return nil
}

// GetRuntimeHistory returns runtime metrics within the time range.
func (s *MemoryStorage) GetRuntimeHistory(timeRange TimeRange) ([]RuntimeMetric, error) {
	return s.runtimeStats.Filter(func(m RuntimeMetric) bool {
		return !m.Timestamp.Before(timeRange.Start) && !m.Timestamp.After(timeRange.End)
	}), nil
}

// --- Error Records ---

// StoreError stores or deduplicates an error record.
func (s *MemoryStorage) StoreError(e ErrorRecord) error {
	s.errorsMu.Lock()
	defer s.errorsMu.Unlock()

	existing, ok := s.errors[e.Fingerprint]
	if ok {
		// Deduplicate: increment count and update LastSeen
		existing.Count++
		existing.LastSeen = e.LastSeen
		if e.StackTrace != "" {
			existing.StackTrace = e.StackTrace
		}
		if e.RequestContext != nil {
			existing.RequestContext = e.RequestContext
		}
	} else {
		cp := e
		s.errors[e.Fingerprint] = &cp
	}
	return nil
}

// GetErrors returns errors matching the filter.
func (s *MemoryStorage) GetErrors(filter ErrorFilter) ([]ErrorRecord, error) {
	s.errorsMu.RLock()
	defer s.errorsMu.RUnlock()

	var result []ErrorRecord
	for _, e := range s.errors {
		if !filter.TimeRange.Start.IsZero() && e.LastSeen.Before(filter.TimeRange.Start) {
			continue
		}
		if !filter.TimeRange.End.IsZero() && e.FirstSeen.After(filter.TimeRange.End) {
			continue
		}
		if filter.ErrorType != "" && e.ErrorType != filter.ErrorType {
			continue
		}
		if filter.Route != "" && e.Route != filter.Route {
			continue
		}
		if filter.Muted != nil && e.Muted != *filter.Muted {
			continue
		}
		if filter.Resolved != nil && e.Resolved != *filter.Resolved {
			continue
		}
		result = append(result, *e)
	}

	// Sort by last seen descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastSeen.After(result[j].LastSeen)
	})

	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	} else if filter.Offset >= len(result) && filter.Offset > 0 {
		return nil, nil
	}
	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}

	return result, nil
}

// GetErrorGroups returns error groups for the dashboard.
func (s *MemoryStorage) GetErrorGroups(timeRange TimeRange) ([]ErrorGroup, error) {
	s.errorsMu.RLock()
	defer s.errorsMu.RUnlock()

	var groups []ErrorGroup
	for _, e := range s.errors {
		if e.LastSeen.Before(timeRange.Start) || e.FirstSeen.After(timeRange.End) {
			continue
		}
		groups = append(groups, ErrorGroup{
			Fingerprint:  e.Fingerprint,
			ErrorMessage: e.ErrorMessage,
			ErrorType:    e.ErrorType,
			Route:        e.Route,
			Method:       e.Method,
			Count:        e.Count,
			FirstSeen:    e.FirstSeen,
			LastSeen:     e.LastSeen,
			Muted:        e.Muted,
			Resolved:     e.Resolved,
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Count > groups[j].Count
	})

	return groups, nil
}

// UpdateError updates specific fields on an error record.
func (s *MemoryStorage) UpdateError(id string, updates map[string]interface{}) error {
	s.errorsMu.Lock()
	defer s.errorsMu.Unlock()

	// Find by ID
	for _, e := range s.errors {
		if e.ID == id {
			if v, ok := updates["muted"]; ok {
				if b, ok := v.(bool); ok {
					e.Muted = b
				}
			}
			if v, ok := updates["resolved"]; ok {
				if b, ok := v.(bool); ok {
					e.Resolved = b
				}
			}
			return nil
		}
	}

	return fmt.Errorf("error record not found: %s", id)
}

// --- Health Results ---

// StoreHealthResult stores a health check result.
func (s *MemoryStorage) StoreHealthResult(r HealthCheckResult) error {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()

	buf, ok := s.healthResults[r.Name]
	if !ok {
		buf = NewRingBuffer[HealthCheckResult](defaultHealthCapacity)
		s.healthResults[r.Name] = buf
	}
	buf.Push(r)
	return nil
}

// GetHealthHistory returns the history for a specific health check.
func (s *MemoryStorage) GetHealthHistory(name string, limit int) ([]HealthCheckResult, error) {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()

	buf, ok := s.healthResults[name]
	if !ok {
		return nil, nil
	}

	if limit > 0 {
		return buf.GetLast(limit), nil
	}
	return buf.GetAll(), nil
}

// --- Alerts ---

// StoreAlert stores an alert record.
func (s *MemoryStorage) StoreAlert(a AlertRecord) error {
	s.alertsMu.Lock()
	defer s.alertsMu.Unlock()

	s.alerts = append(s.alerts, a)
	// Cap at 10000
	if len(s.alerts) > 10000 {
		s.alerts = s.alerts[len(s.alerts)-10000:]
	}
	return nil
}

// GetAlerts returns alerts matching the filter.
func (s *MemoryStorage) GetAlerts(filter AlertFilter) ([]AlertRecord, error) {
	s.alertsMu.RLock()
	defer s.alertsMu.RUnlock()

	var result []AlertRecord
	for _, a := range s.alerts {
		if !filter.TimeRange.Start.IsZero() && a.FiredAt.Before(filter.TimeRange.Start) {
			continue
		}
		if !filter.TimeRange.End.IsZero() && a.FiredAt.After(filter.TimeRange.End) {
			continue
		}
		if filter.State != "" && a.State != filter.State {
			continue
		}
		if filter.Severity != "" && a.Severity != filter.Severity {
			continue
		}
		result = append(result, a)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].FiredAt.After(result[j].FiredAt)
	})

	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}

	return result, nil
}

// --- Dependencies ---

// StoreDependencyMetric stores a dependency metric.
func (s *MemoryStorage) StoreDependencyMetric(m DependencyMetric) error {
	s.dependencies.Push(m)
	return nil
}

// GetDependencyStats returns aggregated stats per dependency.
func (s *MemoryStorage) GetDependencyStats(timeRange TimeRange) ([]DependencyStats, error) {
	type depAgg struct {
		latencies []time.Duration
		errCount  int64
		total     int64
		lastTS    time.Time
		lastCode  int
	}

	groups := make(map[string]*depAgg)

	s.dependencies.ForEach(func(m DependencyMetric) bool {
		if m.Timestamp.Before(timeRange.Start) || m.Timestamp.After(timeRange.End) {
			return true
		}
		d, ok := groups[m.Name]
		if !ok {
			d = &depAgg{}
			groups[m.Name] = d
		}
		d.latencies = append(d.latencies, m.Latency)
		d.total++
		if m.Error != "" || m.StatusCode >= 500 {
			d.errCount++
		}
		if m.Timestamp.After(d.lastTS) {
			d.lastTS = m.Timestamp
			d.lastCode = m.StatusCode
		}
		return true
	})

	duration := timeRange.End.Sub(timeRange.Start)
	result := make([]DependencyStats, 0, len(groups))
	for name, d := range groups {
		sort.Slice(d.latencies, func(i, j int) bool { return d.latencies[i] < d.latencies[j] })

		errRate := float64(0)
		if d.total > 0 {
			errRate = float64(d.errCount) / float64(d.total) * 100
		}

		lastStatus := "healthy"
		if d.lastCode >= 500 {
			lastStatus = "unhealthy"
		}

		rpm := float64(0)
		if duration.Minutes() > 0 {
			rpm = float64(d.total) / duration.Minutes()
		}

		result = append(result, DependencyStats{
			Name:         name,
			RequestCount: d.total,
			ErrorCount:   d.errCount,
			ErrorRate:    errRate,
			AvgLatency:   ComputeAvg(d.latencies),
			P50Latency:   Percentile(d.latencies, 50),
			P95Latency:   Percentile(d.latencies, 95),
			P99Latency:   Percentile(d.latencies, 99),
			RPM:          rpm,
			Availability: 100 - errRate,
			LastStatus:   lastStatus,
			LastChecked:  d.lastTS,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestCount > result[j].RequestCount
	})

	return result, nil
}

// --- Overview ---

// GetOverview computes the top-level dashboard snapshot.
func (s *MemoryStorage) GetOverview(timeRange TimeRange) (*Overview, error) {
	var totalReqs, totalErrs int64
	var latencies []time.Duration

	s.requests.ForEach(func(m RequestMetric) bool {
		if m.Timestamp.Before(timeRange.Start) || m.Timestamp.After(timeRange.End) {
			return true
		}
		totalReqs++
		latencies = append(latencies, m.Latency)
		if m.StatusCode >= 400 {
			totalErrs++
		}
		return true
	})

	var errRate float64
	if totalReqs > 0 {
		errRate = float64(totalErrs) / float64(totalReqs) * 100
	}

	duration := timeRange.End.Sub(timeRange.Start)
	rpm := float64(0)
	if duration.Minutes() > 0 {
		rpm = float64(totalReqs) / duration.Minutes()
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := Percentile(latencies, 95)

	// Latest runtime snapshot
	var goroutines int
	var heapMB float64
	runtimeHistory := s.runtimeStats.GetLast(1)
	if len(runtimeHistory) > 0 {
		goroutines = runtimeHistory[0].NumGoroutine
		heapMB = float64(runtimeHistory[0].HeapAlloc) / (1024 * 1024)
	}

	// Active alerts count
	activeAlerts := 0
	s.alertsMu.RLock()
	for _, a := range s.alerts {
		if a.State == AlertStateFiring {
			activeAlerts++
		}
	}
	s.alertsMu.RUnlock()

	// Top routes
	topRoutes, _ := s.GetRouteStats(timeRange)
	if len(topRoutes) > 10 {
		topRoutes = topRoutes[:10]
	}

	// Recent errors
	recentErrors, _ := s.GetErrors(ErrorFilter{
		TimeRange: timeRange,
		Limit:     5,
	})

	uptime := time.Since(s.startTime)

	return &Overview{
		AppName:          s.appName,
		Uptime:           formatDuration(uptime),
		TotalRequests:    totalReqs,
		TotalErrors:      totalErrs,
		ErrorRate:        errRate,
		AvgLatency:       ComputeAvg(latencies),
		P95Latency:       p95,
		RPM:              rpm,
		ActiveGoroutines: goroutines,
		HeapAllocMB:      heapMB,
		ActiveAlerts:     activeAlerts,
		TopRoutes:        topRoutes,
		RecentErrors:     recentErrors,
		Timestamp:        time.Now(),
	}, nil
}

// --- Maintenance ---

// Cleanup removes data older than the retention period.
func (s *MemoryStorage) Cleanup(retention time.Duration) error {
	cutoff := time.Now().Add(-retention)

	// Clean errors
	s.errorsMu.Lock()
	for fp, e := range s.errors {
		if e.LastSeen.Before(cutoff) {
			delete(s.errors, fp)
		}
	}
	s.errorsMu.Unlock()

	// Clean alerts
	s.alertsMu.Lock()
	filtered := s.alerts[:0]
	for _, a := range s.alerts {
		if !a.FiredAt.Before(cutoff) {
			filtered = append(filtered, a)
		}
	}
	s.alerts = filtered
	s.alertsMu.Unlock()

	// Clean N+1 detections
	s.n1Mu.Lock()
	filteredN1 := s.n1Detections[:0]
	for _, d := range s.n1Detections {
		if !d.DetectedAt.Before(cutoff) {
			filteredN1 = append(filteredN1, d)
		}
	}
	s.n1Detections = filteredN1
	s.n1Mu.Unlock()

	// Ring buffers handle their own capacity limits; they don't need explicit cleanup
	// since old entries are naturally overwritten.

	return nil
}

// Reset clears all stored data.
func (s *MemoryStorage) Reset() error {
	s.requests.Reset()
	s.queries.Reset()
	s.runtimeStats.Reset()
	s.dependencies.Reset()

	s.errorsMu.Lock()
	s.errors = make(map[string]*ErrorRecord)
	s.errorsMu.Unlock()

	s.healthMu.Lock()
	s.healthResults = make(map[string]*RingBuffer[HealthCheckResult])
	s.healthMu.Unlock()

	s.alertsMu.Lock()
	s.alerts = s.alerts[:0]
	s.alertsMu.Unlock()

	s.n1Mu.Lock()
	s.n1Detections = s.n1Detections[:0]
	s.n1Mu.Unlock()

	s.poolMu.Lock()
	s.poolStats = nil
	s.poolMu.Unlock()

	return nil
}

// Close is a no-op for in-memory storage.
func (s *MemoryStorage) Close() error {
	return nil
}

// --- Helpers ---

func computeRouteStats(method, path string, reqs []RequestMetric, duration time.Duration) RouteStats {
	var errCount int64
	latencies := make([]time.Duration, len(reqs))
	statusCodes := make(map[int]int64)

	for i, r := range reqs {
		latencies[i] = r.Latency
		statusCodes[r.StatusCode]++
		if r.StatusCode >= 400 {
			errCount++
		}
	}

	count := int64(len(reqs))
	errRate := float64(0)
	if count > 0 {
		errRate = float64(errCount) / float64(count) * 100
	}

	rpm := float64(0)
	if duration.Minutes() > 0 {
		rpm = float64(count) / duration.Minutes()
	}

	p50, p75, p90, p95, p99 := ComputePercentiles(latencies)

	return RouteStats{
		Method:       method,
		Path:         path,
		RequestCount: count,
		ErrorCount:   errCount,
		ErrorRate:    errRate,
		AvgLatency:   ComputeAvg(latencies),
		MinLatency:   ComputeMin(latencies),
		MaxLatency:   ComputeMax(latencies),
		P50Latency:   p50,
		P75Latency:   p75,
		P90Latency:   p90,
		P95Latency:   p95,
		P99Latency:   p99,
		RPM:          rpm,
		StatusCodes:  statusCodes,
		Trend:        "stable",
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours < 24 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd%dh%dm", days, hours, minutes)
}

// Ensure MemoryStorage satisfies the Storage interface at compile time.
var _ Storage = (*MemoryStorage)(nil)

// deleteError removes an error record by ID. Used internally.
func (s *MemoryStorage) deleteError(id string) error {
	s.errorsMu.Lock()
	defer s.errorsMu.Unlock()

	for fp, e := range s.errors {
		if e.ID == id {
			delete(s.errors, fp)
			return nil
		}
	}
	return fmt.Errorf("error record not found: %s", id)
}

// getErrorByID retrieves a single error record by ID.
func (s *MemoryStorage) getErrorByID(id string) (*ErrorRecord, error) {
	s.errorsMu.RLock()
	defer s.errorsMu.RUnlock()

	for _, e := range s.errors {
		if e.ID == id {
			cp := *e
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("error record not found: %s", id)
}

// getLatestHealthResults returns the latest health check result for each check.
func (s *MemoryStorage) getLatestHealthResults() map[string]HealthCheckResult {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()

	results := make(map[string]HealthCheckResult, len(s.healthResults))
	for name, buf := range s.healthResults {
		latest := buf.GetLast(1)
		if len(latest) > 0 {
			results[name] = latest[0]
		}
	}
	return results
}

