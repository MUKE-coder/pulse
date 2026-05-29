package pulse

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseTraceparent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		header    string
		wantOK    bool
		wantTrace string
		wantSpan  string
	}{
		{
			name:      "valid v00 sampled",
			header:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			wantOK:    true,
			wantTrace: "0af7651916cd43dd8448eb211c80319c",
			wantSpan:  "b7ad6b7169203331",
		},
		{name: "empty", header: "", wantOK: false},
		{name: "future version is rejected (Pulse only knows 00)",
			header: "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", wantOK: false},
		{name: "wrong number of fields",
			header: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331", wantOK: false},
		{name: "short trace-id",
			header: "00-deadbeef-b7ad6b7169203331-01", wantOK: false},
		{name: "non-hex trace-id",
			header: "00-zzz7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", wantOK: false},
		{name: "all-zero trace-id is forbidden by spec",
			header: "00-00000000000000000000000000000000-b7ad6b7169203331-01", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tid, pid, ok := ParseTraceparent(tt.header)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tt.wantOK)
			}
			if ok && (tid != tt.wantTrace || pid != tt.wantSpan) {
				t.Fatalf("got (%q, %q), want (%q, %q)", tid, pid, tt.wantTrace, tt.wantSpan)
			}
		})
	}
}

func TestBuildTraceparent_RoundTrip(t *testing.T) {
	t.Parallel()
	tid := GenerateTraceID()
	pid := GenerateSpanID()
	header := BuildTraceparent(tid, pid)

	gotTID, gotPID, ok := ParseTraceparent(header)
	if !ok {
		t.Fatalf("BuildTraceparent produced an unparseable value: %q", header)
	}
	if gotTID != tid || gotPID != pid {
		t.Fatalf("round-trip mismatch: got (%q, %q), want (%q, %q)", gotTID, gotPID, tid, pid)
	}
}

// TestMiddleware_HonorsIncomingTraceparent verifies that an incoming W3C
// traceparent header is adopted as the request's trace ID rather than a new
// one being generated. This is the OTel interop guarantee in #7.
func TestMiddleware_HonorsIncomingTraceparent(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil,
		WithDevMode(),
		WithAppName("traceparent-test"),
	)
	_ = p

	router.GET("/x", func(c *gin.Context) { c.Status(200) })

	const upstreamTrace = "0af7651916cd43dd8448eb211c80319c"
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(TraceparentHeader, "00-"+upstreamTrace+"-b7ad6b7169203331-01")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get(TraceIDHeader); got != upstreamTrace {
		t.Fatalf("X-Pulse-Trace-ID = %q, want upstream trace %q", got, upstreamTrace)
	}
	tp := w.Header().Get(TraceparentHeader)
	tid, _, ok := ParseTraceparent(tp)
	if !ok || tid != upstreamTrace {
		t.Fatalf("response traceparent = %q, want trace-id %q", tp, upstreamTrace)
	}
}

// TestMiddleware_GeneratesTraceparent verifies that requests without a
// traceparent get a freshly generated one in the response.
func TestMiddleware_GeneratesTraceparent(t *testing.T) {
	router := gin.New()
	Mount(context.Background(), router, nil, WithDevMode())
	router.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	tp := w.Header().Get(TraceparentHeader)
	if _, _, ok := ParseTraceparent(tp); !ok {
		t.Fatalf("response traceparent %q is not a valid W3C value", tp)
	}
}
