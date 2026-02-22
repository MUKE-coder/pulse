package pulse

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ExportRequest defines the request body for data export.
type ExportRequest struct {
	Format string `json:"format"` // "json" or "csv"
	Type   string `json:"type"`   // "requests", "queries", "errors", "runtime", "health", "alerts"
	Range  string `json:"range"`  // "5m", "15m", "1h", "6h", "24h", "7d"
}

const maxExportRecords = 100000

// registerExportRoute registers the data export endpoint.
func registerExportRoute(group *gin.RouterGroup, p *Pulse) {
	group.POST("/data/export", func(c *gin.Context) {
		var req ExportRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		if req.Format == "" {
			req.Format = "json"
		}
		if req.Range == "" {
			req.Range = "1h"
		}

		tr := ParseTimeRange(req.Range)

		switch req.Format {
		case "json":
			exportJSON(c, p, req.Type, tr)
		case "csv":
			exportCSV(c, p, req.Type, tr)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported format, use 'json' or 'csv'"})
		}
	})
}

func exportJSON(c *gin.Context, p *Pulse, dataType string, tr TimeRange) {
	data, filename, err := getExportData(p, dataType, tr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal data"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.json", filename))
	c.Data(http.StatusOK, "application/json", jsonData)
}

func exportCSV(c *gin.Context, p *Pulse, dataType string, tr TimeRange) {
	data, filename, err := getExportData(p, dataType, tr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.csv", filename))
	c.Header("Content-Type", "text/csv")
	c.Status(http.StatusOK)

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	switch records := data.(type) {
	case []RequestMetric:
		writer.Write([]string{"method", "path", "status_code", "latency_ms", "request_size", "response_size", "client_ip", "trace_id", "error", "timestamp"})
		for _, r := range records {
			writer.Write([]string{
				r.Method, r.Path, strconv.Itoa(r.StatusCode),
				fmt.Sprintf("%.2f", float64(r.Latency)/float64(time.Millisecond)),
				strconv.FormatInt(r.RequestSize, 10),
				strconv.FormatInt(r.ResponseSize, 10),
				r.ClientIP, r.TraceID, r.Error,
				r.Timestamp.Format(time.RFC3339),
			})
		}

	case []QueryMetric:
		writer.Write([]string{"sql", "operation", "table", "duration_ms", "rows_affected", "error", "caller", "trace_id", "timestamp"})
		for _, q := range records {
			caller := q.CallerFile
			if q.CallerLine > 0 {
				caller = fmt.Sprintf("%s:%d", q.CallerFile, q.CallerLine)
			}
			writer.Write([]string{
				q.SQL, q.Operation, q.Table,
				fmt.Sprintf("%.2f", float64(q.Duration)/float64(time.Millisecond)),
				strconv.FormatInt(q.RowsAffected, 10),
				q.Error, caller, q.RequestTraceID,
				q.Timestamp.Format(time.RFC3339),
			})
		}

	case []ErrorRecord:
		writer.Write([]string{"id", "method", "route", "error_message", "error_type", "count", "muted", "resolved", "first_seen", "last_seen"})
		for _, e := range records {
			writer.Write([]string{
				e.ID, e.Method, e.Route, e.ErrorMessage, e.ErrorType,
				strconv.FormatInt(e.Count, 10),
				strconv.FormatBool(e.Muted),
				strconv.FormatBool(e.Resolved),
				e.FirstSeen.Format(time.RFC3339),
				e.LastSeen.Format(time.RFC3339),
			})
		}

	case []RuntimeMetric:
		writer.Write([]string{"heap_alloc", "heap_in_use", "heap_objects", "stack_in_use", "sys", "goroutines", "gc_pause_ns", "num_gc", "gc_cpu_fraction", "timestamp"})
		for _, r := range records {
			writer.Write([]string{
				strconv.FormatUint(r.HeapAlloc, 10),
				strconv.FormatUint(r.HeapInUse, 10),
				strconv.FormatUint(r.HeapObjects, 10),
				strconv.FormatUint(r.StackInUse, 10),
				strconv.FormatUint(r.Sys, 10),
				strconv.Itoa(r.NumGoroutine),
				strconv.FormatUint(r.GCPauseNs, 10),
				strconv.FormatUint(uint64(r.NumGC), 10),
				fmt.Sprintf("%.6f", r.GCCPUFraction),
				r.Timestamp.Format(time.RFC3339),
			})
		}

	case []AlertRecord:
		writer.Write([]string{"id", "rule_name", "metric", "value", "threshold", "operator", "severity", "state", "route", "message", "fired_at", "resolved_at"})
		for _, a := range records {
			resolvedAt := ""
			if a.ResolvedAt != nil {
				resolvedAt = a.ResolvedAt.Format(time.RFC3339)
			}
			writer.Write([]string{
				a.ID, a.RuleName, a.Metric,
				fmt.Sprintf("%.2f", a.Value),
				fmt.Sprintf("%.2f", a.Threshold),
				a.Operator, a.Severity, string(a.State),
				a.Route, a.Message,
				a.FiredAt.Format(time.RFC3339),
				resolvedAt,
			})
		}
	}
}

func getExportData(p *Pulse, dataType string, tr TimeRange) (interface{}, string, error) {
	timestamp := time.Now().Format("20060102_150405")

	switch dataType {
	case "requests":
		data, err := p.storage.GetRequests(RequestFilter{TimeRange: tr, Limit: maxExportRecords})
		return data, fmt.Sprintf("pulse_requests_%s", timestamp), err

	case "queries":
		data, err := p.storage.GetSlowQueries(0, maxExportRecords) // 0 threshold = all queries
		return data, fmt.Sprintf("pulse_queries_%s", timestamp), err

	case "errors":
		data, err := p.storage.GetErrors(ErrorFilter{TimeRange: tr, Limit: maxExportRecords})
		return data, fmt.Sprintf("pulse_errors_%s", timestamp), err

	case "runtime":
		data, err := p.storage.GetRuntimeHistory(tr)
		return data, fmt.Sprintf("pulse_runtime_%s", timestamp), err

	case "alerts":
		data, err := p.storage.GetAlerts(AlertFilter{TimeRange: tr, Limit: maxExportRecords})
		return data, fmt.Sprintf("pulse_alerts_%s", timestamp), err

	default:
		return nil, "", fmt.Errorf("unsupported export type %q, use: requests, queries, errors, runtime, alerts", dataType)
	}
}
