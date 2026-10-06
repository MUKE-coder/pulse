package pulseotel

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// defaultMetricInterval is how often metrics are pushed to the collector.
const defaultMetricInterval = 30 * time.Second

// OTLPOption configures [NewOTLP].
type OTLPOption func(*otlpConfig)

type otlpConfig struct {
	res      *resource.Resource
	attrs    []attribute.KeyValue
	metrics  bool
	interval time.Duration
}

// WithService names the service the telemetry describes, overriding
// OTEL_SERVICE_NAME. An empty version is left out.
func WithService(name, version string) OTLPOption {
	return func(c *otlpConfig) {
		c.attrs = append(c.attrs, semconv.ServiceName(name))
		if version != "" {
			c.attrs = append(c.attrs, semconv.ServiceVersion(version))
		}
	}
}

// WithResource replaces the resource describing this process, instead of the
// one built from OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
func WithResource(r *resource.Resource) OTLPOption {
	return func(c *otlpConfig) { c.res = r }
}

// WithoutMetrics exports spans only.
func WithoutMetrics() OTLPOption {
	return func(c *otlpConfig) { c.metrics = false }
}

// WithMetricInterval sets how often metrics are pushed (default 30s).
func WithMetricInterval(d time.Duration) OTLPOption {
	return func(c *otlpConfig) { c.interval = d }
}

// NewOTLP returns an [Exporter] that sends Pulse's spans and metrics to an
// OpenTelemetry collector over OTLP/HTTP, configured by the standard
// environment variables:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT    collector base URL (default http://localhost:4318)
//	OTEL_EXPORTER_OTLP_HEADERS     headers to send, such as "api-key=secret"
//	OTEL_SERVICE_NAME              service name reported in the resource
//	OTEL_RESOURCE_ATTRIBUTES       further resource attributes
//
// Usage:
//
//	exp, err := pulseotel.NewOTLP(ctx, pulseotel.WithService("orders", "1.4.0"))
//	if err != nil {
//		log.Fatal(err)
//	}
//	p := pulse.Mount(ctx, router, db, pulse.WithExporter(exp))
//	...
//	_ = p.Shutdown()                       // hands the exporter its last events
//	_ = exp.Shutdown(context.Background()) // flushes them to the collector
func NewOTLP(ctx context.Context, opts ...OTLPOption) (*Exporter, error) {
	cfg := otlpConfig{metrics: true, interval: defaultMetricInterval}
	for _, opt := range opts {
		opt(&cfg)
	}

	res := cfg.res
	if res == nil {
		var err error
		// resource.Default reads OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
		if res, err = resource.Merge(resource.Default(), resource.NewSchemaless(cfg.attrs...)); err != nil {
			return nil, fmt.Errorf("pulseotel: resource: %w", err)
		}
	}

	traces, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("pulseotel: otlp traces: %w", err)
	}
	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithBatcher(traces), sdktrace.WithResource(res)}
	if !cfg.metrics {
		return New(traceOpts...), nil
	}

	values, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("pulseotel: otlp metrics: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(values, sdkmetric.WithInterval(cfg.interval))),
	)
	return NewWithMetrics(mp, traceOpts...)
}
