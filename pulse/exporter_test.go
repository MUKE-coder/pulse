package pulse

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type recordingExporter struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingExporter) Export(_ context.Context, events []Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, events...)
}

// waitFor polls until the recorded events satisfy done, or fails.
func (r *recordingExporter) waitFor(t *testing.T, done func([]Event) bool) []Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		got := append([]Event(nil), r.events...)
		r.mu.Unlock()
		if done(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("exporter received %d events, not the expected ones", len(got))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func eventsOf(events []Event, kind EventKind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// A request continuing an inbound trace, with a query and an outbound call
// inside it, must export records that form one span tree — and the span
// Pulse propagates to the dependency must be the one it exports. This works
// at 0% sampling: exporters see every request.
func TestExporter_SpanTreeAcrossRequestQueryAndDependency(t *testing.T) {
	var downstreamTraceparent string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamTraceparent = r.Header.Get(TraceparentHeader)
	}))
	defer downstream.Close()

	db := setupTestDB(t)
	rec := &recordingExporter{}
	router := gin.New()
	p := Mount(context.Background(), router, db, WithDevMode(), WithSampleRate(0), WithExporter(rec))
	t.Cleanup(func() { _ = p.Shutdown() })
	client := WrapHTTPClient(p, nil, "downstream")

	router.GET("/orders/:id", func(c *gin.Context) {
		db.WithContext(c.Request.Context()).Create(&TestUser{Name: "export"})
		req, _ := http.NewRequestWithContext(c.Request.Context(), "GET", downstream.URL, nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
		c.Status(http.StatusOK)
	})

	const inboundTrace, inboundParent = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	req := httptest.NewRequest("GET", "/orders/7", nil)
	req.Header.Set(TraceparentHeader, "00-"+inboundTrace+"-"+inboundParent+"-01")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	_, responseSpan, ok := ParseTraceparent(w.Header().Get(TraceparentHeader))
	if !ok {
		t.Fatalf("response traceparent %q is not valid", w.Header().Get(TraceparentHeader))
	}

	events := rec.waitFor(t, func(ev []Event) bool {
		return len(eventsOf(ev, EventRequest)) == 1 && len(eventsOf(ev, EventQuery)) >= 1 && len(eventsOf(ev, EventDependency)) == 1
	})

	request := eventsOf(events, EventRequest)[0].Request
	if request.TraceID != inboundTrace || request.ParentSpanID != inboundParent || request.SpanID != responseSpan {
		t.Errorf("request span = trace %s parent %s span %s; want trace %s parent %s span %s (from the response traceparent)",
			request.TraceID, request.ParentSpanID, request.SpanID, inboundTrace, inboundParent, responseSpan)
	}
	if request.Path != "/orders/:id" || request.StatusCode != 200 {
		t.Errorf("request = %s %d", request.Path, request.StatusCode)
	}

	for _, e := range eventsOf(events, EventQuery) {
		q := e.Query
		if q.RequestTraceID != inboundTrace || q.ParentSpanID != request.SpanID || q.SpanID == "" || q.SpanID == request.SpanID {
			t.Errorf("query span = trace %s parent %s span %s; want a child of request span %s",
				q.RequestTraceID, q.ParentSpanID, q.SpanID, request.SpanID)
		}
	}

	dep := eventsOf(events, EventDependency)[0].Dependency
	_, downstreamParent, _ := ParseTraceparent(downstreamTraceparent)
	if dep.TraceID != inboundTrace || dep.ParentSpanID != request.SpanID || dep.SpanID == "" || dep.SpanID != downstreamParent {
		t.Errorf("dependency span = trace %s parent %s span %s; want a child of %s whose span %s the downstream saw as its parent",
			dep.TraceID, dep.ParentSpanID, dep.SpanID, request.SpanID, downstreamParent)
	}

	if stored, _ := p.storage.GetRequests(RequestFilter{TimeRange: Last1h()}); len(stored) != 0 {
		t.Errorf("stored %d requests at 0%% sampling; exporting must not change what is stored", len(stored))
	}
}

// Exported errors are redacted and carry the trace and span of the failing
// request.
func TestExporter_ErrorEventsAreRedactedWithTrace(t *testing.T) {
	rec := &recordingExporter{}
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithExporter(rec))
	t.Cleanup(func() { _ = p.Shutdown() })
	router.POST("/pay", func(c *gin.Context) {
		_ = c.Error(fmt.Errorf("charge failed for card 4111 1111 1111 1111"))
		c.Status(http.StatusBadGateway)
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/pay", nil))
	traceID, spanID, _ := ParseTraceparent(w.Header().Get(TraceparentHeader))

	events := rec.waitFor(t, func(ev []Event) bool { return len(eventsOf(ev, EventError)) == 1 })
	e := eventsOf(events, EventError)[0].Error
	if strings.Contains(e.ErrorMessage, "4111") {
		t.Errorf("exported error leaks the card number: %q", e.ErrorMessage)
	}
	if e.TraceID != traceID || e.SpanID != spanID {
		t.Errorf("error trace/span = %s/%s, want %s/%s", e.TraceID, e.SpanID, traceID, spanID)
	}
}

type panickingExporter struct{}

func (panickingExporter) Export(context.Context, []Event) { panic("exporter bug") }

// A panicking exporter is contained, and a full queue drops rather than
// blocking the request path.
func TestExporter_PanicsAndFullQueueAreContained(t *testing.T) {
	rec := &recordingExporter{}
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithExporter(panickingExporter{}), WithExporter(rec))
	router.GET("/x", func(c *gin.Context) { c.Status(200) })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	rec.waitFor(t, func(ev []Event) bool { return len(eventsOf(ev, EventRequest)) == 1 }) // later exporters still run
	_ = p.Shutdown()
	if n := p.internal.snapshot()["export"]; n == 0 {
		t.Error("the exporter panic was not counted as an internal error")
	}

	q := newPulse(context.Background(), applyDefaults(Config{Exporters: []Exporter{rec}}))
	q.exporter.queue = make(chan Event, 1) // no worker running
	q.export(Event{Kind: EventRequest, Request: &RequestMetric{}})
	q.export(Event{Kind: EventRequest, Request: &RequestMetric{}})
	if n := q.internal.snapshot()["export"]; n != 1 {
		t.Errorf("dropped events counted = %d, want 1", n)
	}
}

// Shutdown hands queued events to the exporters instead of dropping them.
func TestExporter_ShutdownFlushes(t *testing.T) {
	rec := &recordingExporter{}
	p := newPulse(context.Background(), applyDefaults(Config{Exporters: []Exporter{rec}}))
	p.storage = NewMemoryStorage("test")
	startExportPipeline(p)
	for i := 0; i < 10; i++ {
		p.export(Event{Kind: EventQuery, Query: &QueryMetric{SQL: fmt.Sprint(i)}})
	}
	_ = p.Shutdown()
	if n := len(rec.events); n != 10 {
		t.Fatalf("exporter received %d of 10 events queued before shutdown", n)
	}
}

func TestConformance_SpanIDsRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		now := time.Now()
		_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200, Timestamp: now,
			TraceID: "t1", SpanID: "s-req", ParentSpanID: "s-caller"})
		_ = s.StoreQuery(QueryMetric{SQL: "SELECT 1", Timestamp: now, RequestTraceID: "t1", SpanID: "s-q", ParentSpanID: "s-req"})
		rec := buildErrorRecord("GET", "/x", "boom", ErrorTypeInternal, "", nil, "t1")
		rec.SpanID = "s-req"
		_ = s.StoreError(rec)

		reqs, _ := s.GetRequests(RequestFilter{TimeRange: wideRange()})
		if len(reqs) != 1 || reqs[0].SpanID != "s-req" || reqs[0].ParentSpanID != "s-caller" {
			t.Errorf("request span IDs = %+v", reqs)
		}
		qs, _ := s.GetSlowQueries(0, 10)
		if len(qs) != 1 || qs[0].SpanID != "s-q" || qs[0].ParentSpanID != "s-req" {
			t.Errorf("query span IDs = %+v", qs)
		}
		if got, err := s.GetErrorByID(rec.ID); err != nil || got.TraceID != "t1" || got.SpanID != "s-req" {
			t.Errorf("error trace/span = %+v (err %v)", got, err)
		}
	})
}

// A database created before v1.2 gains the span columns when opened.
func TestSQLite_MigratesColumnsOfOlderDatabases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the v1.1 shape of a table that later gained columns.
	for _, stmt := range []string{
		`DROP TABLE requests`,
		`CREATE TABLE requests (timestamp INTEGER NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL,
			status_code INTEGER NOT NULL, latency_ns INTEGER NOT NULL, request_size INTEGER NOT NULL DEFAULT 0,
			response_size INTEGER NOT NULL DEFAULT 0, client_ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', trace_id TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO requests (timestamp, method, path, status_code, latency_ns) VALUES (1, 'GET', '/old', 200, 5)`,
	} {
		if _, err := old.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = old.Close()

	s, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/new", StatusCode: 200, Timestamp: time.Now(), SpanID: "abc"})
	reqs, err := s.GetRequests(RequestFilter{})
	if err != nil || len(reqs) != 2 {
		t.Fatalf("GetRequests after migration: %d rows, err %v", len(reqs), err)
	}
	if reqs[0].Path != "/old" || reqs[0].SpanID != "" || reqs[1].SpanID != "abc" {
		t.Errorf("rows after migration = %+v", reqs)
	}
	_ = s.Close()
	again, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatalf("migration must be idempotent: %v", err)
	}
	_ = again.Close()
}
