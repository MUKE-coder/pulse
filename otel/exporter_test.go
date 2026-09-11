package pulseotel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	pulseotel "github.com/MUKE-coder/pulse/otel"
	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

type order struct {
	ID   uint
	Item string
}

type fixture struct {
	router *gin.Engine
	db     *gorm.DB
	pulse  *pulse.Pulse
	spans  *tracetest.InMemoryExporter
	client *http.Client
}

func setup(t *testing.T) *fixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "app.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&order{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	spans := tracetest.NewInMemoryExporter()
	exp := pulseotel.New(sdktrace.WithSyncer(spans))
	router := gin.New()
	// Sample rate 0: exporters must still see every request.
	p := pulse.Mount(context.Background(), router, db, pulse.WithDevMode(), pulse.WithSampleRate(0), pulse.WithExporter(exp))
	t.Cleanup(func() { _ = exp.Shutdown(context.Background()) })
	return &fixture{router: router, db: db, pulse: p, spans: spans, client: pulse.WrapHTTPClient(p, nil, "inventory")}
}

// flush stops Pulse, which hands every queued event to the exporter, and
// returns the spans recorded.
func (f *fixture) flush(t *testing.T) tracetest.SpanStubs {
	t.Helper()
	if err := f.pulse.Shutdown(); err != nil {
		t.Fatal(err)
	}
	return f.spans.GetSpans()
}

func find(spans tracetest.SpanStubs, match func(tracetest.SpanStub) bool) *tracetest.SpanStub {
	for i := range spans {
		if match(spans[i]) {
			return &spans[i]
		}
	}
	return nil
}

func attr(s *tracetest.SpanStub, key attribute.Key) attribute.Value {
	for _, kv := range s.Attributes {
		if kv.Key == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

// A request continuing a caller's trace, with a query and an outbound call
// inside it, becomes one span tree under the caller's span — with the span
// IDs Pulse put on the wire.
func TestExport_SpanTreeKeepsPulseIDs(t *testing.T) {
	var downstreamTraceparent string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamTraceparent = r.Header.Get(pulse.TraceparentHeader)
	}))
	defer downstream.Close()

	f := setup(t)
	f.router.POST("/orders/:id", func(c *gin.Context) {
		f.db.WithContext(c.Request.Context()).Create(&order{Item: "book"})
		req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, downstream.URL+"/stock?sku=1", nil)
		if resp, err := f.client.Do(req); err == nil {
			resp.Body.Close()
		}
		c.Status(http.StatusCreated)
	})

	const callerTrace, callerSpan = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	req := httptest.NewRequest(http.MethodPost, "/orders/7", nil)
	req.Header.Set(pulse.TraceparentHeader, "00-"+callerTrace+"-"+callerSpan+"-01")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	_, responseSpan, ok := pulse.ParseTraceparent(w.Header().Get(pulse.TraceparentHeader))
	if !ok {
		t.Fatalf("bad response traceparent %q", w.Header().Get(pulse.TraceparentHeader))
	}
	spans := f.flush(t)

	server := find(spans, func(s tracetest.SpanStub) bool { return s.SpanKind == trace.SpanKindServer })
	if server == nil {
		t.Fatalf("no server span among %d spans", len(spans))
	}
	if server.Name != "POST /orders/:id" {
		t.Errorf("server span name = %q", server.Name)
	}
	if got := server.SpanContext.TraceID().String(); got != callerTrace {
		t.Errorf("server trace = %s, want the caller's %s", got, callerTrace)
	}
	if got := server.SpanContext.SpanID().String(); got != responseSpan {
		t.Errorf("server span = %s, want %s from the response traceparent", got, responseSpan)
	}
	if got := server.Parent.SpanID().String(); got != callerSpan || !server.Parent.IsRemote() {
		t.Errorf("server parent = %s (remote %v), want the caller's span %s, remote", got, server.Parent.IsRemote(), callerSpan)
	}
	if got := attr(server, "http.response.status_code").AsInt64(); got != http.StatusCreated {
		t.Errorf("http.response.status_code = %d", got)
	}
	if !server.EndTime.After(server.StartTime) {
		t.Errorf("server span has no duration: %v → %v", server.StartTime, server.EndTime)
	}

	query := find(spans, func(s tracetest.SpanStub) bool { return attr(&s, "db.operation.name").AsString() != "" })
	if query == nil {
		t.Fatal("no query span")
	}
	if query.SpanContext.TraceID() != server.SpanContext.TraceID() || query.Parent.SpanID() != server.SpanContext.SpanID() {
		t.Errorf("query span is not a child of the server span: parent %s", query.Parent.SpanID())
	}
	if text := attr(query, "db.query.text").AsString(); text == "" || containsLiteral(text, "book") {
		t.Errorf("db.query.text = %q; want the normalized statement without literal values", text)
	}

	dep := find(spans, func(s tracetest.SpanStub) bool { return attr(&s, "service.peer.name").AsString() == "inventory" })
	if dep == nil {
		t.Fatal("no dependency span")
	}
	_, downstreamParent, _ := pulse.ParseTraceparent(downstreamTraceparent)
	if dep.SpanContext.SpanID().String() != downstreamParent {
		t.Errorf("dependency span = %s, but the downstream service was told its parent is %s", dep.SpanContext.SpanID(), downstreamParent)
	}
	if dep.Parent.SpanID() != server.SpanContext.SpanID() || dep.Parent.IsRemote() {
		t.Errorf("dependency span parent = %s (remote %v), want the local server span", dep.Parent.SpanID(), dep.Parent.IsRemote())
	}
}

func containsLiteral(s, lit string) bool {
	for i := 0; i+len(lit) <= len(s); i++ {
		if s[i:i+len(lit)] == lit {
			return true
		}
	}
	return false
}

// 5xx requests fail their span; a query outside any request starts its own
// trace.
func TestExport_FailuresAndUntracedQueries(t *testing.T) {
	f := setup(t)
	f.router.GET("/boom", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })
	f.router.GET("/missing", func(c *gin.Context) { c.Status(http.StatusNotFound) })
	f.router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	f.router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
	f.db.Create(&order{Item: "background job"})
	spans := f.flush(t)

	boom := find(spans, func(s tracetest.SpanStub) bool { return s.Name == "GET /boom" })
	if boom == nil || boom.Status.Code != codes.Error || attr(boom, "error.type").AsString() != "503" {
		t.Errorf("5xx span = %+v, want error status with error.type 503", boom)
	}
	if boom != nil && boom.Parent.IsValid() {
		t.Errorf("a request without traceparent should be a root span, got parent %s", boom.Parent.SpanID())
	}
	missing := find(spans, func(s tracetest.SpanStub) bool { return s.Name == "GET /missing" })
	if missing == nil || missing.Status.Code == codes.Error {
		t.Errorf("4xx server span = %+v, want no error status", missing)
	}

	bg := find(spans, func(s tracetest.SpanStub) bool {
		return s.SpanKind == trace.SpanKindClient && attr(&s, "db.operation.name").AsString() != ""
	})
	if bg == nil || !bg.SpanContext.IsValid() || bg.Parent.IsValid() {
		t.Errorf("untraced query span = %+v, want a valid root span", bg)
	}
}
