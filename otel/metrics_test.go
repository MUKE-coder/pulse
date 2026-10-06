package pulseotel

import (
	"context"
	"testing"
	"time"

	"github.com/MUKE-coder/pulse/pulse"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// collected returns the metrics recorded so far, by name.
func collected(t *testing.T, r *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func attrOf(set attribute.Set, key string) string {
	v, ok := set.Value(attribute.Key(key))
	if !ok {
		return ""
	}
	return v.Emit()
}

// Every kind of record Pulse exports becomes the metric its OpenTelemetry
// semantic convention names, so existing dashboards pick them up.
func TestMetrics_RecordEveryKindOfRecord(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	exp, err := NewWithMetrics(
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exp.Export(context.Background(), []pulse.Event{
		{Kind: pulse.EventRequest, Request: &pulse.RequestMetric{
			Method: "GET", Path: "/orders/:id", StatusCode: 500,
			Latency: 250 * time.Millisecond, Timestamp: now, InstanceID: "api-1"}},
		{Kind: pulse.EventQuery, Query: &pulse.QueryMetric{
			Operation: "SELECT", Table: "orders", Duration: 12 * time.Millisecond, Timestamp: now}},
		{Kind: pulse.EventDependency, Dependency: &pulse.DependencyMetric{
			Method: "POST", URL: "https://api.stripe.com/v1/charges", StatusCode: 200,
			Latency: 80 * time.Millisecond, Timestamp: now}},
		{Kind: pulse.EventRuntime, Runtime: &pulse.RuntimeMetric{
			HeapAlloc: 5 << 20, StackInUse: 1 << 20, NumGoroutine: 42, Timestamp: now, InstanceID: "api-1"}},
	})

	got := collected(t, reader)
	for _, name := range []string{
		"http.server.request.duration", "db.client.operation.duration",
		"http.client.request.duration", "go.memory.used", "go.goroutine.count",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("no %s recorded", name)
		}
	}

	server, ok := got["http.server.request.duration"].Data.(metricdata.Histogram[float64])
	if !ok || len(server.DataPoints) != 1 {
		t.Fatalf("http.server.request.duration = %#v", got["http.server.request.duration"].Data)
	}
	dp := server.DataPoints[0]
	if dp.Count != 1 || dp.Sum < 0.24 || dp.Sum > 0.26 {
		t.Errorf("request duration: count %d, sum %v s; want one point of ~0.25s", dp.Count, dp.Sum)
	}
	for key, want := range map[string]string{
		"http.route": "/orders/:id", "http.response.status_code": "500",
		"error.type": "500", "service.instance.id": "api-1",
	} {
		if got := attrOf(dp.Attributes, key); got != want {
			t.Errorf("request attr %s = %q, want %q", key, got, want)
		}
	}

	db, _ := got["db.client.operation.duration"].Data.(metricdata.Histogram[float64])
	if len(db.DataPoints) != 1 || attrOf(db.DataPoints[0].Attributes, "db.collection.name") != "orders" {
		t.Errorf("db metric = %#v", db.DataPoints)
	}
	client, _ := got["http.client.request.duration"].Data.(metricdata.Histogram[float64])
	if len(client.DataPoints) != 1 || attrOf(client.DataPoints[0].Attributes, "server.address") != "api.stripe.com" {
		t.Errorf("client metric = %#v", client.DataPoints)
	}

	goroutines, _ := got["go.goroutine.count"].Data.(metricdata.Gauge[int64])
	if len(goroutines.DataPoints) != 1 || goroutines.DataPoints[0].Value != 42 {
		t.Errorf("goroutine gauge = %#v", goroutines.DataPoints)
	}
	memory, _ := got["go.memory.used"].Data.(metricdata.Gauge[int64])
	if len(memory.DataPoints) != 2 {
		t.Errorf("go.memory.used has %d points, want heap and stack", len(memory.DataPoints))
	}
}

// Spans alone stay the default: no meter provider, no metrics.
func TestNew_RecordsSpansOnly(t *testing.T) {
	if exp := New(sdktrace.WithSyncer(tracetest.NewInMemoryExporter())); exp.MeterProvider() != nil {
		t.Error("New built a meter provider; metrics should be opt-in")
	}
}
