package pulseotel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MUKE-coder/pulse/pulse"
)

// NewOTLP sends spans and metrics to the collector the standard environment
// variables name, with no pipeline for the application to assemble.
func TestNewOTLP_SendsSpansAndMetricsToTheCollector(t *testing.T) {
	var mu sync.Mutex
	received := map[string]int{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_SERVICE_NAME", "orders")

	ctx := context.Background()
	exp, err := NewOTLP(ctx, WithMetricInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Shutdown(ctx)

	now := time.Now()
	exp.Export(ctx, []pulse.Event{
		{Kind: pulse.EventRequest, Request: &pulse.RequestMetric{
			Method: "GET", Path: "/orders/:id", StatusCode: 200,
			Latency: 30 * time.Millisecond, Timestamp: now}},
		{Kind: pulse.EventRuntime, Runtime: &pulse.RuntimeMetric{
			HeapAlloc: 4 << 20, NumGoroutine: 12, Timestamp: now}},
	})
	if err := exp.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if received["/v1/traces"] == 0 {
		t.Error("no spans reached the collector")
	}
	if received["/v1/metrics"] == 0 {
		t.Error("no metrics reached the collector")
	}
}

// WithoutMetrics exports spans alone.
func TestNewOTLP_WithoutMetrics(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	exp, err := NewOTLP(context.Background(), WithoutMetrics())
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Shutdown(context.Background())
	if exp.MeterProvider() != nil {
		t.Error("WithoutMetrics still built a meter provider")
	}
}
