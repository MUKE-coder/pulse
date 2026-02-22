package pulse

import (
	"time"
)

// RequestMetric captures data about a single HTTP request.
type RequestMetric struct {
	Method       string        `json:"method"`
	Path         string        `json:"path"`
	StatusCode   int           `json:"status_code"`
	Latency      time.Duration `json:"latency"`
	RequestSize  int64         `json:"request_size"`
	ResponseSize int64         `json:"response_size"`
	ClientIP     string        `json:"client_ip"`
	UserAgent    string        `json:"user_agent"`
	Error        string        `json:"error,omitempty"`
	TraceID      string        `json:"trace_id"`
	Timestamp    time.Time     `json:"timestamp"`
}

// QueryMetric captures data about a single database query.
type QueryMetric struct {
	SQL            string        `json:"sql"`
	NormalizedSQL  string        `json:"normalized_sql"`
	Duration       time.Duration `json:"duration"`
	RowsAffected   int64         `json:"rows_affected"`
	Error          string        `json:"error,omitempty"`
	Operation      string        `json:"operation"`
	Table          string        `json:"table"`
	CallerFile     string        `json:"caller_file,omitempty"`
	CallerLine     int           `json:"caller_line,omitempty"`
	RequestTraceID string        `json:"request_trace_id,omitempty"`
	Timestamp      time.Time     `json:"timestamp"`
}

// RuntimeMetric captures a snapshot of Go runtime statistics.
type RuntimeMetric struct {
	HeapAlloc     uint64    `json:"heap_alloc"`
	HeapInUse     uint64    `json:"heap_in_use"`
	HeapObjects   uint64    `json:"heap_objects"`
	StackInUse    uint64    `json:"stack_in_use"`
	TotalAlloc    uint64    `json:"total_alloc"`
	Sys           uint64    `json:"sys"`
	NumGoroutine  int       `json:"num_goroutine"`
	GCPauseNs     uint64    `json:"gc_pause_ns"`
	NumGC         uint32    `json:"num_gc"`
	GCCPUFraction float64   `json:"gc_cpu_fraction"`
	Timestamp     time.Time `json:"timestamp"`
}

// RequestContext captures relevant context from an HTTP request for error records.
type RequestContext struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Query       string            `json:"query,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	ClientIP    string            `json:"client_ip"`
	UserAgent   string            `json:"user_agent"`
	ContentType string            `json:"content_type,omitempty"`
}

// ErrorRecord represents an aggregated error occurrence.
type ErrorRecord struct {
	ID             string          `json:"id"`
	Fingerprint    string          `json:"fingerprint"`
	Method         string          `json:"method"`
	Route          string          `json:"route"`
	ErrorMessage   string          `json:"error_message"`
	ErrorType      string          `json:"error_type"`
	StackTrace     string          `json:"stack_trace,omitempty"`
	RequestContext *RequestContext `json:"request_context,omitempty"`
	Count          int64           `json:"count"`
	FirstSeen      time.Time       `json:"first_seen"`
	LastSeen       time.Time       `json:"last_seen"`
	Muted          bool            `json:"muted"`
	Resolved       bool            `json:"resolved"`
}

// HealthCheckResult records the outcome of a single health check execution.
type HealthCheckResult struct {
	Name      string                 `json:"name"`
	Type      string                 `json:"type"`
	Status    string                 `json:"status"`
	Latency   time.Duration          `json:"latency"`
	Error     string                 `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// AlertState represents the current lifecycle state of an alert.
type AlertState string

const (
	AlertStateOK       AlertState = "ok"
	AlertStatePending  AlertState = "pending"
	AlertStateFiring   AlertState = "firing"
	AlertStateResolved AlertState = "resolved"
)

// AlertRecord captures an alert event.
type AlertRecord struct {
	ID         string     `json:"id"`
	RuleName   string     `json:"rule_name"`
	Metric     string     `json:"metric"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	Operator   string     `json:"operator"`
	Severity   string     `json:"severity"`
	State      AlertState `json:"state"`
	Route      string     `json:"route,omitempty"`
	Message    string     `json:"message"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// DependencyMetric captures data about an outbound HTTP request to a dependency.
type DependencyMetric struct {
	Name         string        `json:"name"`
	Method       string        `json:"method"`
	URL          string        `json:"url"`
	StatusCode   int           `json:"status_code"`
	Latency      time.Duration `json:"latency"`
	RequestSize  int64         `json:"request_size"`
	ResponseSize int64         `json:"response_size"`
	Error        string        `json:"error,omitempty"`
	Timestamp    time.Time     `json:"timestamp"`
}

// TimeRange represents a time window for querying metrics.
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Last5m returns a TimeRange spanning the last 5 minutes.
func Last5m() TimeRange {
	now := time.Now()
	return TimeRange{Start: now.Add(-5 * time.Minute), End: now}
}

// Last15m returns a TimeRange spanning the last 15 minutes.
func Last15m() TimeRange {
	now := time.Now()
	return TimeRange{Start: now.Add(-15 * time.Minute), End: now}
}

// Last1h returns a TimeRange spanning the last hour.
func Last1h() TimeRange {
	now := time.Now()
	return TimeRange{Start: now.Add(-1 * time.Hour), End: now}
}

// Last6h returns a TimeRange spanning the last 6 hours.
func Last6h() TimeRange {
	now := time.Now()
	return TimeRange{Start: now.Add(-6 * time.Hour), End: now}
}

// Last24h returns a TimeRange spanning the last 24 hours.
func Last24h() TimeRange {
	now := time.Now()
	return TimeRange{Start: now.Add(-24 * time.Hour), End: now}
}

// Last7d returns a TimeRange spanning the last 7 days.
func Last7d() TimeRange {
	now := time.Now()
	return TimeRange{Start: now.Add(-7 * 24 * time.Hour), End: now}
}

// ParseTimeRange converts a string range identifier to a TimeRange.
func ParseTimeRange(r string) TimeRange {
	switch r {
	case "5m":
		return Last5m()
	case "15m":
		return Last15m()
	case "1h":
		return Last1h()
	case "6h":
		return Last6h()
	case "24h":
		return Last24h()
	case "7d":
		return Last7d()
	default:
		return Last1h()
	}
}

// RouteStats holds aggregated statistics for a single route.
type RouteStats struct {
	Method       string        `json:"method"`
	Path         string        `json:"path"`
	RequestCount int64         `json:"request_count"`
	ErrorCount   int64         `json:"error_count"`
	ErrorRate    float64       `json:"error_rate"`
	AvgLatency   time.Duration `json:"avg_latency"`
	MinLatency   time.Duration `json:"min_latency"`
	MaxLatency   time.Duration `json:"max_latency"`
	P50Latency   time.Duration `json:"p50_latency"`
	P75Latency   time.Duration `json:"p75_latency"`
	P90Latency   time.Duration `json:"p90_latency"`
	P95Latency   time.Duration `json:"p95_latency"`
	P99Latency   time.Duration `json:"p99_latency"`
	RPM          float64       `json:"rpm"`
	StatusCodes  map[int]int64 `json:"status_codes"`
	Trend        string        `json:"trend"` // "improving", "stable", "degrading"
}

// RouteDetail holds detailed information for a specific route.
type RouteDetail struct {
	RouteStats
	LatencyTimeSeries []TimeSeriesPoint `json:"latency_time_series"`
	RecentRequests    []RequestMetric   `json:"recent_requests"`
	RecentErrors      []ErrorRecord     `json:"recent_errors"`
	TopQueries        []QueryPattern    `json:"top_queries"`
}

// TimeSeriesPoint is a single data point in a time series.
type TimeSeriesPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// QueryPattern represents an aggregated SQL query pattern.
type QueryPattern struct {
	NormalizedSQL string        `json:"normalized_sql"`
	Operation     string        `json:"operation"`
	Table         string        `json:"table"`
	Count         int64         `json:"count"`
	AvgDuration   time.Duration `json:"avg_duration"`
	MaxDuration   time.Duration `json:"max_duration"`
	TotalDuration time.Duration `json:"total_duration"`
	ErrorCount    int64         `json:"error_count"`
}

// N1Detection represents a detected N+1 query issue.
type N1Detection struct {
	Pattern        string        `json:"pattern"`
	Count          int           `json:"count"`
	TotalDuration  time.Duration `json:"total_duration"`
	RequestTraceID string        `json:"request_trace_id"`
	Route          string        `json:"route"`
	DetectedAt     time.Time     `json:"detected_at"`
}

// PoolStats holds database connection pool statistics.
type PoolStats struct {
	MaxOpenConnections int   `json:"max_open_connections"`
	OpenConnections    int   `json:"open_connections"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"wait_count"`
	WaitDuration       int64 `json:"wait_duration_ms"`
	MaxIdleClosed      int64 `json:"max_idle_closed"`
	MaxIdleTimeClosed  int64 `json:"max_idle_time_closed"`
	MaxLifetimeClosed  int64 `json:"max_lifetime_closed"`
}

// ErrorGroup groups errors by fingerprint with aggregate counts.
type ErrorGroup struct {
	Fingerprint  string    `json:"fingerprint"`
	ErrorMessage string    `json:"error_message"`
	ErrorType    string    `json:"error_type"`
	Route        string    `json:"route"`
	Method       string    `json:"method"`
	Count        int64     `json:"count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	Muted        bool      `json:"muted"`
	Resolved     bool      `json:"resolved"`
}

// DependencyStats holds aggregated statistics for an external dependency.
type DependencyStats struct {
	Name         string        `json:"name"`
	RequestCount int64         `json:"request_count"`
	ErrorCount   int64         `json:"error_count"`
	ErrorRate    float64       `json:"error_rate"`
	AvgLatency   time.Duration `json:"avg_latency"`
	P50Latency   time.Duration `json:"p50_latency"`
	P95Latency   time.Duration `json:"p95_latency"`
	P99Latency   time.Duration `json:"p99_latency"`
	RPM          float64       `json:"rpm"`
	Availability float64       `json:"availability"`
	LastStatus   string        `json:"last_status"`
	LastChecked  time.Time     `json:"last_checked"`
}

// Overview is the top-level dashboard snapshot.
type Overview struct {
	AppName          string            `json:"app_name"`
	Uptime           string            `json:"uptime"`
	TotalRequests    int64             `json:"total_requests"`
	TotalErrors      int64             `json:"total_errors"`
	ErrorRate        float64           `json:"error_rate"`
	AvgLatency       time.Duration     `json:"avg_latency"`
	P95Latency       time.Duration     `json:"p95_latency"`
	RPM              float64           `json:"rpm"`
	ActiveGoroutines int               `json:"active_goroutines"`
	HeapAllocMB      float64           `json:"heap_alloc_mb"`
	ActiveAlerts     int               `json:"active_alerts"`
	HealthStatus     string            `json:"health_status"`
	TopRoutes        []RouteStats      `json:"top_routes"`
	RecentErrors     []ErrorRecord     `json:"recent_errors"`
	ThroughputSeries []TimeSeriesPoint `json:"throughput_series"`
	ErrorSeries      []TimeSeriesPoint `json:"error_series"`
	Timestamp        time.Time         `json:"timestamp"`
}

// RequestFilter for querying stored requests.
type RequestFilter struct {
	TimeRange  TimeRange
	Method     string
	Path       string
	StatusCode int
	MinLatency time.Duration
	Limit      int
	Offset     int
}

// ErrorFilter for querying stored errors.
type ErrorFilter struct {
	TimeRange TimeRange
	ErrorType string
	Route     string
	Muted     *bool
	Resolved  *bool
	Limit     int
	Offset    int
}

// AlertFilter for querying stored alerts.
type AlertFilter struct {
	TimeRange TimeRange
	State     AlertState
	Severity  string
	Limit     int
	Offset    int
}
