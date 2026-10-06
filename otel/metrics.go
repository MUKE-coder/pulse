package pulseotel

import (
	"context"
	"net/url"
	"strconv"

	"github.com/MUKE-coder/pulse/pulse"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Metrics recorded alongside the spans, named after the OpenTelemetry
// semantic conventions, so collectors, dashboards and alerts that already
// know those names pick them up without configuration.
type metrics struct {
	server     metric.Float64Histogram // http.server.request.duration
	client     metric.Float64Histogram // http.client.request.duration
	db         metric.Float64Histogram // db.client.operation.duration
	memory     metric.Int64Gauge       // go.memory.used
	goroutines metric.Int64Gauge       // go.goroutine.count
}

func newMetrics(mp metric.MeterProvider) (*metrics, error) {
	meter := mp.Meter(instrumentationName, metric.WithInstrumentationVersion(pulse.Version))
	var (
		m   metrics
		err error
	)
	if m.server, err = meter.Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of HTTP server requests.")); err != nil {
		return nil, err
	}
	if m.client, err = meter.Float64Histogram("http.client.request.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of outbound HTTP requests.")); err != nil {
		return nil, err
	}
	if m.db, err = meter.Float64Histogram("db.client.operation.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of database queries.")); err != nil {
		return nil, err
	}
	if m.memory, err = meter.Int64Gauge("go.memory.used",
		metric.WithUnit("By"), metric.WithDescription("Memory used by the Go runtime.")); err != nil {
		return nil, err
	}
	if m.goroutines, err = meter.Int64Gauge("go.goroutine.count",
		metric.WithUnit("{goroutine}"), metric.WithDescription("Goroutines currently running.")); err != nil {
		return nil, err
	}
	return &m, nil
}

// instanceAttr identifies the Pulse instance a record came from, so metrics
// from replicas sharing a collector stay apart.
func instanceAttr(instance string) []attribute.KeyValue {
	if instance == "" {
		return nil
	}
	return []attribute.KeyValue{attribute.String("service.instance.id", instance)}
}

func (m *metrics) request(ctx context.Context, r *pulse.RequestMetric) {
	attrs := append(instanceAttr(r.InstanceID),
		semconv.HTTPRequestMethodKey.String(r.Method),
		semconv.HTTPRouteKey.String(r.Path),
		semconv.HTTPResponseStatusCodeKey.Int(r.StatusCode),
	)
	if r.StatusCode >= 500 { // a 4xx is the client's error, not the server's
		attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(r.StatusCode)))
	}
	m.server.Record(ctx, r.Latency.Seconds(), metric.WithAttributes(attrs...))
}

func (m *metrics) query(ctx context.Context, q *pulse.QueryMetric) {
	var attrs []attribute.KeyValue
	if q.Operation != "" {
		attrs = append(attrs, semconv.DBOperationNameKey.String(q.Operation))
	}
	if q.Table != "" {
		attrs = append(attrs, semconv.DBCollectionNameKey.String(q.Table))
	}
	if q.Error != "" {
		attrs = append(attrs, semconv.ErrorTypeKey.String("_OTHER"))
	}
	m.db.Record(ctx, q.Duration.Seconds(), metric.WithAttributes(attrs...))
}

func (m *metrics) dependency(ctx context.Context, d *pulse.DependencyMetric) {
	attrs := []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(d.Method)}
	if u, err := url.Parse(d.URL); err == nil && u.Hostname() != "" {
		attrs = append(attrs, semconv.ServerAddressKey.String(u.Hostname()))
	}
	if d.StatusCode > 0 {
		attrs = append(attrs, semconv.HTTPResponseStatusCodeKey.Int(d.StatusCode))
	}
	if d.Error != "" || d.StatusCode >= 400 {
		errType := "_OTHER"
		if d.StatusCode >= 400 {
			errType = strconv.Itoa(d.StatusCode)
		}
		attrs = append(attrs, semconv.ErrorTypeKey.String(errType))
	}
	m.client.Record(ctx, d.Latency.Seconds(), metric.WithAttributes(attrs...))
}

func (m *metrics) runtime(ctx context.Context, s *pulse.RuntimeMetric) {
	instance := instanceAttr(s.InstanceID)
	heap := append(instanceAttr(s.InstanceID), attribute.String("go.memory.type", "heap"))
	stack := append(instanceAttr(s.InstanceID), attribute.String("go.memory.type", "stack"))
	m.memory.Record(ctx, int64(s.HeapAlloc), metric.WithAttributes(heap...))
	m.memory.Record(ctx, int64(s.StackInUse), metric.WithAttributes(stack...))
	m.goroutines.Record(ctx, int64(s.NumGoroutine), metric.WithAttributes(instance...))
}
