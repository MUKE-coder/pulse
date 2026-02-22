package pulse

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// registerPrometheusRoute registers the Prometheus metrics endpoint.
func registerPrometheusRoute(router *gin.Engine, p *Pulse) {
	path := p.config.Prometheus.Path
	if path == "" {
		path = "/pulse/metrics"
	}

	router.GET(path, func(c *gin.Context) {
		metrics := buildPrometheusMetrics(p)
		c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(metrics))
	})
}

// buildPrometheusMetrics generates Prometheus exposition format metrics.
func buildPrometheusMetrics(p *Pulse) string {
	var b strings.Builder
	tr := Last1h()

	// --- HTTP Request Metrics ---
	writeRequestMetrics(&b, p, tr)

	// --- Runtime Metrics ---
	writeRuntimeMetrics(&b, p)

	// --- Health Check Metrics ---
	writeHealthMetrics(&b, p)

	// --- Error Metrics ---
	writeErrorMetrics(&b, p, tr)

	// --- Database Metrics ---
	writeDatabaseMetrics(&b, p, tr)

	// --- Uptime ---
	fmt.Fprintf(&b, "# HELP pulse_uptime_seconds Pulse uptime in seconds\n")
	fmt.Fprintf(&b, "# TYPE pulse_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "pulse_uptime_seconds %.0f\n\n", p.Uptime().Seconds())

	return b.String()
}

func writeRequestMetrics(b *strings.Builder, p *Pulse, tr TimeRange) {
	stats, _ := p.storage.GetRouteStats(tr)
	if len(stats) == 0 {
		return
	}

	// pulse_http_requests_total
	fmt.Fprintf(b, "# HELP pulse_http_requests_total Total HTTP requests\n")
	fmt.Fprintf(b, "# TYPE pulse_http_requests_total counter\n")
	for _, s := range stats {
		for code, count := range s.StatusCodes {
			fmt.Fprintf(b, "pulse_http_requests_total{method=%q,path=%q,status=%q} %d\n",
				s.Method, s.Path, fmt.Sprintf("%d", code), count)
		}
	}
	b.WriteString("\n")

	// pulse_http_request_duration_seconds
	fmt.Fprintf(b, "# HELP pulse_http_request_duration_seconds HTTP request latency\n")
	fmt.Fprintf(b, "# TYPE pulse_http_request_duration_seconds summary\n")
	for _, s := range stats {
		labels := fmt.Sprintf("method=%q,path=%q", s.Method, s.Path)
		fmt.Fprintf(b, "pulse_http_request_duration_seconds{%s,quantile=\"0.5\"} %f\n", labels, s.P50Latency.Seconds())
		fmt.Fprintf(b, "pulse_http_request_duration_seconds{%s,quantile=\"0.95\"} %f\n", labels, s.P95Latency.Seconds())
		fmt.Fprintf(b, "pulse_http_request_duration_seconds{%s,quantile=\"0.99\"} %f\n", labels, s.P99Latency.Seconds())
		fmt.Fprintf(b, "pulse_http_request_duration_seconds_sum{%s} %f\n", labels,
			s.AvgLatency.Seconds()*float64(s.RequestCount))
		fmt.Fprintf(b, "pulse_http_request_duration_seconds_count{%s} %d\n", labels, s.RequestCount)
	}
	b.WriteString("\n")

	// pulse_http_error_rate
	fmt.Fprintf(b, "# HELP pulse_http_error_rate HTTP error rate percentage\n")
	fmt.Fprintf(b, "# TYPE pulse_http_error_rate gauge\n")
	for _, s := range stats {
		fmt.Fprintf(b, "pulse_http_error_rate{method=%q,path=%q} %f\n", s.Method, s.Path, s.ErrorRate)
	}
	b.WriteString("\n")
}

func writeRuntimeMetrics(b *strings.Builder, p *Pulse) {
	history, _ := p.storage.GetRuntimeHistory(Last5m())
	if len(history) == 0 {
		return
	}
	latest := history[len(history)-1]

	fmt.Fprintf(b, "# HELP pulse_runtime_goroutines Number of goroutines\n")
	fmt.Fprintf(b, "# TYPE pulse_runtime_goroutines gauge\n")
	fmt.Fprintf(b, "pulse_runtime_goroutines %d\n\n", latest.NumGoroutine)

	fmt.Fprintf(b, "# HELP pulse_runtime_heap_bytes Heap memory allocated in bytes\n")
	fmt.Fprintf(b, "# TYPE pulse_runtime_heap_bytes gauge\n")
	fmt.Fprintf(b, "pulse_runtime_heap_bytes %d\n\n", latest.HeapAlloc)

	fmt.Fprintf(b, "# HELP pulse_runtime_heap_inuse_bytes Heap memory in use in bytes\n")
	fmt.Fprintf(b, "# TYPE pulse_runtime_heap_inuse_bytes gauge\n")
	fmt.Fprintf(b, "pulse_runtime_heap_inuse_bytes %d\n\n", latest.HeapInUse)

	fmt.Fprintf(b, "# HELP pulse_runtime_sys_bytes Total memory obtained from OS\n")
	fmt.Fprintf(b, "# TYPE pulse_runtime_sys_bytes gauge\n")
	fmt.Fprintf(b, "pulse_runtime_sys_bytes %d\n\n", latest.Sys)

	fmt.Fprintf(b, "# HELP pulse_runtime_gc_pause_ns Last GC pause duration in nanoseconds\n")
	fmt.Fprintf(b, "# TYPE pulse_runtime_gc_pause_ns gauge\n")
	fmt.Fprintf(b, "pulse_runtime_gc_pause_ns %d\n\n", latest.GCPauseNs)

	fmt.Fprintf(b, "# HELP pulse_runtime_gc_total Total number of GC cycles\n")
	fmt.Fprintf(b, "# TYPE pulse_runtime_gc_total counter\n")
	fmt.Fprintf(b, "pulse_runtime_gc_total %d\n\n", latest.NumGC)
}

func writeHealthMetrics(b *strings.Builder, p *Pulse) {
	p.healthMu.RLock()
	checks := make([]HealthCheck, len(p.healthChecks))
	copy(checks, p.healthChecks)
	p.healthMu.RUnlock()

	if len(checks) == 0 {
		return
	}

	fmt.Fprintf(b, "# HELP pulse_health_check_status Health check status (1=healthy, 0=unhealthy)\n")
	fmt.Fprintf(b, "# TYPE pulse_health_check_status gauge\n")

	fmt.Fprintf(b, "# HELP pulse_health_check_duration_seconds Health check latency\n")
	fmt.Fprintf(b, "# TYPE pulse_health_check_duration_seconds gauge\n")

	for _, check := range checks {
		history, _ := p.storage.GetHealthHistory(check.Name, 1)
		if len(history) == 0 {
			fmt.Fprintf(b, "pulse_health_check_status{name=%q} -1\n", check.Name)
			continue
		}
		latest := history[0]
		status := 0
		if latest.Status == "healthy" {
			status = 1
		}
		fmt.Fprintf(b, "pulse_health_check_status{name=%q} %d\n", check.Name, status)
		fmt.Fprintf(b, "pulse_health_check_duration_seconds{name=%q} %f\n", check.Name, latest.Latency.Seconds())
	}
	b.WriteString("\n")
}

func writeErrorMetrics(b *strings.Builder, p *Pulse, tr TimeRange) {
	groups, _ := p.storage.GetErrorGroups(tr)
	if len(groups) == 0 {
		return
	}

	fmt.Fprintf(b, "# HELP pulse_errors_total Total error count by type\n")
	fmt.Fprintf(b, "# TYPE pulse_errors_total counter\n")

	// Aggregate by error type
	byType := make(map[string]int64)
	for _, g := range groups {
		byType[g.ErrorType] += g.Count
	}
	for errType, count := range byType {
		fmt.Fprintf(b, "pulse_errors_total{type=%q} %d\n", errType, count)
	}
	b.WriteString("\n")
}

func writeDatabaseMetrics(b *strings.Builder, p *Pulse, tr TimeRange) {
	patterns, _ := p.storage.GetQueryPatterns(tr)
	if len(patterns) == 0 {
		return
	}

	fmt.Fprintf(b, "# HELP pulse_db_query_duration_seconds Database query latency\n")
	fmt.Fprintf(b, "# TYPE pulse_db_query_duration_seconds summary\n")
	for _, pat := range patterns {
		if len(pat.Operation) == 0 {
			continue
		}
		labels := fmt.Sprintf("operation=%q,table=%q", pat.Operation, pat.Table)
		fmt.Fprintf(b, "pulse_db_query_duration_seconds_sum{%s} %f\n", labels, pat.TotalDuration.Seconds())
		fmt.Fprintf(b, "pulse_db_query_duration_seconds_count{%s} %d\n", labels, pat.Count)
	}
	b.WriteString("\n")

	// Pool stats
	pool, _ := p.storage.GetConnectionPoolStats()
	if pool != nil {
		fmt.Fprintf(b, "# HELP pulse_db_pool_open_connections Open database connections\n")
		fmt.Fprintf(b, "# TYPE pulse_db_pool_open_connections gauge\n")
		fmt.Fprintf(b, "pulse_db_pool_open_connections %d\n\n", pool.OpenConnections)

		fmt.Fprintf(b, "# HELP pulse_db_pool_in_use In-use database connections\n")
		fmt.Fprintf(b, "# TYPE pulse_db_pool_in_use gauge\n")
		fmt.Fprintf(b, "pulse_db_pool_in_use %d\n\n", pool.InUse)

		fmt.Fprintf(b, "# HELP pulse_db_pool_idle Idle database connections\n")
		fmt.Fprintf(b, "# TYPE pulse_db_pool_idle gauge\n")
		fmt.Fprintf(b, "pulse_db_pool_idle %d\n\n", pool.Idle)
	}
}
