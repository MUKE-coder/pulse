package pulse

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// One request, end to end. Pulse records a span ID on the request, on every
// query it ran and on every call it made (see trace.go), so the pieces of a
// trace can be put back together and shown as a waterfall: what the request
// spent its time on, and which step failed.

// traceSpan is one recorded step of a trace.
type traceSpan struct {
	Kind         string        `json:"kind"` // "request", "query" or "dependency"
	Name         string        `json:"name"`
	SpanID       string        `json:"span_id,omitempty"`
	ParentSpanID string        `json:"parent_span_id,omitempty"`
	Start        time.Time     `json:"start"`
	Duration     time.Duration `json:"duration"`
	StatusCode   int           `json:"status_code,omitempty"`
	Error        string        `json:"error,omitempty"`
	Detail       string        `json:"detail,omitempty"` // the SQL, or the URL called
	InstanceID   string        `json:"instance_id,omitempty"`
}

// traceStore is implemented by storage backends that can find the records of
// one trace. Unexported for the same reason as rollupStore.
type traceStore interface {
	traceSpans(traceID string) ([]traceSpan, error)
}

// maxTraceLogs bounds the lines shown beside a trace.
const maxTraceLogs = 200

// traceHandler serves GET /pulse/api/traces/:id: every span Pulse recorded
// for the trace, oldest first, with the lines logged inside it.
func traceHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ts, ok := p.storage.(traceStore)
		if !ok {
			c.JSON(http.StatusOK, gin.H{"trace_id": id, "spans": []traceSpan{}, "logs": []LogRecord{}, "supported": false})
			return
		}
		spans, err := ts.traceSpans(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(spans) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no spans recorded for this trace",
				"hint": "sampled-out requests keep their rollup counts but not their records"})
			return
		}
		sort.SliceStable(spans, func(i, j int) bool { return spans[i].Start.Before(spans[j].Start) })

		logs := []LogRecord{}
		if ls, ok := p.storage.(logStore); ok {
			if got, err := ls.queryLogs(logFilter{TraceID: id, MinLevel: lowestLogLevel, Limit: maxTraceLogs}); err == nil && got != nil {
				logs = got
			}
		}
		c.JSON(http.StatusOK, gin.H{"trace_id": id, "spans": spans, "logs": logs, "supported": true})
	}
}

// requestSpan describes the request itself.
func requestSpan(m RequestMetric) traceSpan {
	return traceSpan{
		Kind: "request", Name: m.Method + " " + m.Path, SpanID: m.SpanID, ParentSpanID: m.ParentSpanID,
		Start: m.Timestamp, Duration: m.Latency, StatusCode: m.StatusCode, Error: m.Error,
		InstanceID: m.InstanceID,
	}
}

func querySpan(m QueryMetric) traceSpan {
	name := "query"
	switch {
	case m.Operation != "" && m.Table != "":
		name = m.Operation + " " + m.Table
	case m.Operation != "":
		name = m.Operation
	}
	return traceSpan{
		Kind: "query", Name: name, SpanID: m.SpanID, ParentSpanID: m.ParentSpanID,
		Start: m.Timestamp, Duration: m.Duration, Error: m.Error, Detail: m.SQL,
	}
}

func dependencySpan(m DependencyMetric) traceSpan {
	name := m.Name
	if name == "" {
		name = m.Method
	}
	return traceSpan{
		Kind: "dependency", Name: name, SpanID: m.SpanID, ParentSpanID: m.ParentSpanID,
		Start: m.Timestamp, Duration: m.Latency, StatusCode: m.StatusCode, Error: m.Error,
		Detail: m.Method + " " + m.URL,
	}
}

// --- MemoryStorage ---

func (s *MemoryStorage) traceSpans(traceID string) ([]traceSpan, error) {
	var spans []traceSpan
	for _, m := range s.requests.Filter(func(m RequestMetric) bool { return m.TraceID == traceID }) {
		spans = append(spans, requestSpan(m))
	}
	for _, m := range s.queries.Filter(func(m QueryMetric) bool { return m.RequestTraceID == traceID }) {
		spans = append(spans, querySpan(m))
	}
	for _, m := range s.dependencies.Filter(func(m DependencyMetric) bool { return m.TraceID == traceID }) {
		spans = append(spans, dependencySpan(m))
	}
	return spans, nil
}

// --- sqlStore ---

func (s *sqlStore) traceSpans(traceID string) ([]traceSpan, error) {
	s.sync()
	var spans []traceSpan

	rows, err := s.query(`SELECT timestamp, method, path, status_code, latency_ns, error, span_id, parent_span_id, instance_id
		FROM requests WHERE trace_id = ?`, traceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m       RequestMetric
			ts, lat int64
		)
		if err := rows.Scan(&ts, &m.Method, &m.Path, &m.StatusCode, &lat, &m.Error,
			&m.SpanID, &m.ParentSpanID, &m.InstanceID); err != nil {
			rows.Close()
			return nil, err
		}
		m.Timestamp, m.Latency = time.Unix(0, ts), time.Duration(lat)
		spans = append(spans, requestSpan(m))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.query(`SELECT timestamp, sql_text, duration_ns, error, operation, table_name, span_id, parent_span_id
		FROM queries WHERE request_trace_id = ?`, traceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m       QueryMetric
			ts, dur int64
		)
		if err := rows.Scan(&ts, &m.SQL, &dur, &m.Error, &m.Operation, &m.Table,
			&m.SpanID, &m.ParentSpanID); err != nil {
			rows.Close()
			return nil, err
		}
		m.Timestamp, m.Duration = time.Unix(0, ts), time.Duration(dur)
		spans = append(spans, querySpan(m))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.query(`SELECT timestamp, name, method, url, status_code, latency_ns, error, span_id, parent_span_id
		FROM dependencies WHERE trace_id = ?`, traceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			m       DependencyMetric
			ts, lat int64
		)
		if err := rows.Scan(&ts, &m.Name, &m.Method, &m.URL, &m.StatusCode, &lat, &m.Error,
			&m.SpanID, &m.ParentSpanID); err != nil {
			rows.Close()
			return nil, err
		}
		m.Timestamp, m.Latency = time.Unix(0, ts), time.Duration(lat)
		spans = append(spans, dependencySpan(m))
	}
	rows.Close()
	return spans, rows.Err()
}

var (
	_ traceStore = (*MemoryStorage)(nil)
	_ traceStore = (*SQLiteStorage)(nil)
	_ traceStore = (*PostgresStorage)(nil)
)
