package pulse

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestConformance_TraceSpansAcrossRecords(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		now := time.Now()
		_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/orders/:id", StatusCode: 200,
			Latency: 120 * time.Millisecond, Timestamp: now, TraceID: "t1", SpanID: "s-req", InstanceID: "api-1"})
		_ = s.StoreQuery(QueryMetric{SQL: "SELECT * FROM orders WHERE id = 1", Operation: "SELECT",
			Table: "orders", Duration: 8 * time.Millisecond, Timestamp: now.Add(5 * time.Millisecond),
			RequestTraceID: "t1", SpanID: "s-query", ParentSpanID: "s-req"})
		_ = s.StoreDependencyMetric(DependencyMetric{Name: "payments", Method: "POST",
			URL: "https://pay.example/charge", StatusCode: 502, Latency: 60 * time.Millisecond,
			Timestamp: now.Add(20 * time.Millisecond), TraceID: "t1", SpanID: "s-dep",
			ParentSpanID: "s-req", Error: "bad gateway"})
		// A second trace must not leak into the first.
		_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/other", StatusCode: 200,
			Timestamp: now, TraceID: "t2", SpanID: "s-other"})

		spans, err := s.(traceStore).traceSpans("t1")
		if err != nil {
			t.Fatal(err)
		}
		if len(spans) != 3 {
			t.Fatalf("got %d spans, want the request, its query and its outbound call: %+v", len(spans), spans)
		}
		byKind := map[string]traceSpan{}
		for _, sp := range spans {
			byKind[sp.Kind] = sp
		}
		if r := byKind["request"]; r.Name != "GET /orders/:id" || r.SpanID != "s-req" ||
			r.StatusCode != 200 || r.Duration != 120*time.Millisecond {
			t.Errorf("request span = %+v", r)
		}
		if q := byKind["query"]; q.Name != "SELECT orders" || q.ParentSpanID != "s-req" ||
			q.Duration != 8*time.Millisecond {
			t.Errorf("query span = %+v", q)
		}
		if d := byKind["dependency"]; d.Name != "payments" || d.ParentSpanID != "s-req" ||
			d.StatusCode != 502 || d.Error == "" {
			t.Errorf("dependency span = %+v", d)
		}
	})
}

// The endpoint returns one request's spans together with the lines it logged.
func TestTraceHandler_ReturnsTheRequestAndItsLogs(t *testing.T) {
	p, router := mountForLogs(t)
	logger := slog.New(p.SlogHandler(nil))
	router.GET("/orders/:id", func(c *gin.Context) {
		logger.InfoContext(c, "loading order", "order_id", c.Param("id"))
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/orders/7", nil))
	traceID, spanID, _ := ParseTraceparent(w.Header().Get(TraceparentHeader))

	var resp struct {
		TraceID string      `json:"trace_id"`
		Spans   []traceSpan `json:"spans"`
		Logs    []LogRecord `json:"logs"`
	}
	callLogsAPI(t, traceHandler(p), "/traces/"+traceID, gin.Params{{Key: "id", Value: traceID}}, &resp)

	if resp.TraceID != traceID || len(resp.Spans) != 1 {
		t.Fatalf("trace %s returned %d spans", resp.TraceID, len(resp.Spans))
	}
	if s := resp.Spans[0]; s.Kind != "request" || s.Name != "GET /orders/:id" || s.SpanID != spanID {
		t.Errorf("span = %+v, want this request's own span", s)
	}
	if len(resp.Logs) != 1 || resp.Logs[0].Message != "loading order" {
		t.Errorf("logs = %+v, want the line the handler wrote", resp.Logs)
	}
}

// A trace nobody recorded is a 404 rather than an empty waterfall.
func TestTraceHandler_UnknownTrace(t *testing.T) {
	p, _ := mountForLogs(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/traces/missing", nil)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	traceHandler(p)(c)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
