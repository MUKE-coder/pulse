// Package pulseotel sends Pulse's telemetry to OpenTelemetry as traces and
// metrics, so an application already running Datadog, Honeycomb or Grafana
// sees Pulse's data in the tools it has.
//
//	exp, err := pulseotel.NewOTLP(ctx, pulseotel.WithService("orders", "1.4.0"))
//	if err != nil { ... }
//	p := pulse.Mount(ctx, router, db, pulse.WithExporter(exp))
//	...
//	_ = p.Shutdown()                       // hands the last events to exp
//	_ = exp.Shutdown(context.Background()) // flushes them to the collector
//
// [NewOTLP] reads the standard OTEL_EXPORTER_OTLP_* environment variables.
// [New] and [NewWithMetrics] take providers you build yourself.
//
// Every request becomes a server span. Every GORM query and every call made
// through [pulse.WrapHTTPClient] becomes a client span beneath it. Spans keep
// Pulse's own trace and span IDs, the ones Pulse reads from and writes to
// traceparent headers, so they join traces started by callers and continued
// by downstream services.
//
// Metrics mirror the spans under their semantic-convention names —
// http.server.request.duration, http.client.request.duration,
// db.client.operation.duration — alongside go.memory.used and
// go.goroutine.count from Pulse's runtime sampler.
//
// Pulse exports every request, whatever its sample rate for storage;
// sampling for OpenTelemetry is up to the sampler passed to [New]. Requests
// that end in a 5xx carry an error status; the error message is recorded as
// an exception event. Query text is exported in Pulse's normalized form,
// with literal values replaced, so it cannot leak data from the statement.
package pulseotel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"time"

	"github.com/MUKE-coder/pulse/pulse"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/MUKE-coder/pulse/otel"

// Exporter is a [pulse.Exporter] that turns Pulse's records into
// OpenTelemetry spans. Create it with [New].
type Exporter struct {
	tp     *sdktrace.TracerProvider
	tracer trace.Tracer

	mp *sdkmetric.MeterProvider // nil when only spans are exported
	m  *metrics
}

// New returns an Exporter backed by a TracerProvider built from opts —
// typically [sdktrace.WithBatcher] with an OTLP exporter, and
// [sdktrace.WithResource] naming the service. The provider's ID generator
// is always Pulse's, so spans keep the IDs Pulse assigned them.
func New(opts ...sdktrace.TracerProviderOption) *Exporter {
	e, _ := newExporter(nil, opts...) // no meter provider, so no error
	return e
}

// NewWithMetrics is [New] with metrics as well as spans, recorded with mp —
// request, outbound-call and query durations, and the Go runtime samples.
// For the usual setup, OTLP configured by the standard environment
// variables, use [NewOTLP].
func NewWithMetrics(mp *sdkmetric.MeterProvider, opts ...sdktrace.TracerProviderOption) (*Exporter, error) {
	return newExporter(mp, opts...)
}

func newExporter(mp *sdkmetric.MeterProvider, opts ...sdktrace.TracerProviderOption) (*Exporter, error) {
	opts = append(opts, sdktrace.WithIDGenerator(pulseIDs{}))
	tp := sdktrace.NewTracerProvider(opts...)
	e := &Exporter{
		tp:     tp,
		tracer: tp.Tracer(instrumentationName, trace.WithInstrumentationVersion(pulse.Version)),
		mp:     mp,
	}
	if mp != nil {
		m, err := newMetrics(mp)
		if err != nil {
			return nil, fmt.Errorf("pulseotel: metrics: %w", err)
		}
		e.m = m
	}
	return e, nil
}

// TracerProvider returns the provider the spans are recorded with.
func (e *Exporter) TracerProvider() *sdktrace.TracerProvider { return e.tp }

// MeterProvider returns the provider the metrics are recorded with, or nil
// when the Exporter only records spans.
func (e *Exporter) MeterProvider() *sdkmetric.MeterProvider { return e.mp }

// ForceFlush exports anything still buffered: spans, and metrics when they
// are recorded.
func (e *Exporter) ForceFlush(ctx context.Context) error {
	err := e.tp.ForceFlush(ctx)
	if e.mp != nil {
		err = errors.Join(err, e.mp.ForceFlush(ctx))
	}
	return err
}

// Shutdown flushes what is buffered and shuts the providers down. Call it
// after [pulse.Pulse.Shutdown], which hands the exporter its last events.
func (e *Exporter) Shutdown(ctx context.Context) error {
	err := e.tp.Shutdown(ctx)
	if e.mp != nil {
		err = errors.Join(err, e.mp.Shutdown(ctx))
	}
	return err
}

// Export implements [pulse.Exporter].
func (e *Exporter) Export(ctx context.Context, events []pulse.Event) {
	for _, ev := range events {
		switch {
		case ev.Kind == pulse.EventRequest && ev.Request != nil:
			e.request(ctx, ev.Request)
		case ev.Kind == pulse.EventQuery && ev.Query != nil:
			e.query(ctx, ev.Query)
		case ev.Kind == pulse.EventDependency && ev.Dependency != nil:
			e.dependency(ctx, ev.Dependency)
		case ev.Kind == pulse.EventRuntime && ev.Runtime != nil && e.m != nil:
			e.m.runtime(ctx, ev.Runtime)
		}
		// Error records are covered by the request span's status and
		// exception event.
	}
}

func (e *Exporter) request(ctx context.Context, m *pulse.RequestMetric) {
	if e.m != nil {
		e.m.request(ctx, m)
	}
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(m.Method),
		semconv.HTTPRouteKey.String(m.Path),
		semconv.HTTPResponseStatusCodeKey.Int(m.StatusCode),
	}
	if m.ClientIP != "" {
		attrs = append(attrs, semconv.ClientAddressKey.String(m.ClientIP))
	}
	if m.UserAgent != "" {
		attrs = append(attrs, semconv.UserAgentOriginalKey.String(m.UserAgent))
	}
	if m.RequestSize > 0 {
		attrs = append(attrs, semconv.HTTPRequestBodySizeKey.Int64(m.RequestSize))
	}
	if m.ResponseSize >= 0 {
		attrs = append(attrs, semconv.HTTPResponseBodySizeKey.Int64(m.ResponseSize))
	}
	// A server span only fails on 5xx; a 4xx is the client's error.
	failed := m.StatusCode >= 500
	if failed {
		attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(m.StatusCode)))
	}

	// An inbound traceparent makes the caller's span a remote parent.
	span := e.start(ctx, m.TraceID, m.SpanID, m.ParentSpanID, true,
		m.Method+" "+m.Path, trace.SpanKindServer, m.Timestamp, attrs)
	end := m.Timestamp.Add(m.Latency)
	if m.Error != "" {
		span.AddEvent("exception", trace.WithTimestamp(end),
			trace.WithAttributes(attribute.String("exception.message", m.Error)))
	}
	if failed {
		span.SetStatus(codes.Error, statusMessage(m.Error, m.StatusCode))
	}
	span.End(trace.WithTimestamp(end))
}

func (e *Exporter) query(ctx context.Context, m *pulse.QueryMetric) {
	if e.m != nil {
		e.m.query(ctx, m)
	}
	attrs := []attribute.KeyValue{semconv.DBQueryTextKey.String(m.NormalizedSQL)}
	if m.Operation != "" {
		attrs = append(attrs, semconv.DBOperationNameKey.String(m.Operation))
	}
	if m.Table != "" {
		attrs = append(attrs, semconv.DBCollectionNameKey.String(m.Table))
	}
	if m.CallerFile != "" {
		attrs = append(attrs, semconv.CodeFilePathKey.String(m.CallerFile), semconv.CodeLineNumberKey.Int(m.CallerLine))
	}
	attrs = append(attrs, attribute.Int64("db.rows_affected", m.RowsAffected))
	if m.Error != "" {
		attrs = append(attrs, semconv.ErrorTypeKey.String("_OTHER"))
	}

	name := "db.query"
	switch {
	case m.Operation != "" && m.Table != "":
		name = m.Operation + " " + m.Table
	case m.Operation != "":
		name = m.Operation
	}
	span := e.start(ctx, m.RequestTraceID, m.SpanID, m.ParentSpanID, false,
		name, trace.SpanKindClient, m.Timestamp, attrs)
	if m.Error != "" {
		span.SetStatus(codes.Error, m.Error)
	}
	span.End(trace.WithTimestamp(m.Timestamp.Add(m.Duration)))
}

func (e *Exporter) dependency(ctx context.Context, m *pulse.DependencyMetric) {
	if e.m != nil {
		e.m.dependency(ctx, m)
	}
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(m.Method),
		semconv.URLFullKey.String(m.URL),
	}
	if u, err := url.Parse(m.URL); err == nil && u.Hostname() != "" {
		attrs = append(attrs, semconv.ServerAddressKey.String(u.Hostname()))
	}
	if m.Name != "" {
		attrs = append(attrs, semconv.ServicePeerNameKey.String(m.Name))
	}
	if m.StatusCode > 0 {
		attrs = append(attrs, semconv.HTTPResponseStatusCodeKey.Int(m.StatusCode))
	}
	if m.RequestSize > 0 {
		attrs = append(attrs, semconv.HTTPRequestBodySizeKey.Int64(m.RequestSize))
	}
	if m.ResponseSize > 0 {
		attrs = append(attrs, semconv.HTTPResponseBodySizeKey.Int64(m.ResponseSize))
	}
	// A client span fails on any 4xx or 5xx, or when no response came back.
	failed := m.Error != "" || m.StatusCode >= 400
	if failed {
		errType := "_OTHER"
		if m.StatusCode >= 400 {
			errType = strconv.Itoa(m.StatusCode)
		}
		attrs = append(attrs, semconv.ErrorTypeKey.String(errType))
	}

	span := e.start(ctx, m.TraceID, m.SpanID, m.ParentSpanID, false,
		m.Method, trace.SpanKindClient, m.Timestamp, attrs)
	if failed {
		span.SetStatus(codes.Error, statusMessage(m.Error, m.StatusCode))
	}
	span.End(trace.WithTimestamp(m.Timestamp.Add(m.Latency)))
}

// start begins a span with the IDs Pulse assigned. Records without a valid
// trace (a query run outside any request, say) start a new trace with fresh
// IDs.
func (e *Exporter) start(ctx context.Context, traceHex, spanHex, parentHex string, remoteParent bool,
	name string, kind trace.SpanKind, at time.Time, attrs []attribute.KeyValue) trace.Span {
	opts := []trace.SpanStartOption{trace.WithSpanKind(kind), trace.WithTimestamp(at), trace.WithAttributes(attrs...)}

	tid, terr := trace.TraceIDFromHex(traceHex)
	sid, serr := trace.SpanIDFromHex(spanHex)
	if terr != nil || serr != nil {
		opts = append(opts, trace.WithNewRoot())
		_, span := e.tracer.Start(ctx, name, opts...)
		return span
	}

	ctx = context.WithValue(ctx, idsKey{}, ids{traceID: tid, spanID: sid})
	if pid, err := trace.SpanIDFromHex(parentHex); err == nil {
		ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    tid,
			SpanID:     pid,
			TraceFlags: trace.FlagsSampled,
			Remote:     remoteParent,
		}))
	} else {
		opts = append(opts, trace.WithNewRoot())
	}
	_, span := e.tracer.Start(ctx, name, opts...)
	return span
}

func statusMessage(msg string, status int) string {
	if msg != "" {
		return msg
	}
	return "HTTP " + strconv.Itoa(status)
}

// ids carries the IDs Pulse assigned a record from start to pulseIDs.
type ids struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

type idsKey struct{}

// pulseIDs is the provider's ID generator: it returns the IDs stashed by
// start, and random ones for spans started any other way.
type pulseIDs struct{}

func (pulseIDs) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	if id, ok := ctx.Value(idsKey{}).(ids); ok {
		return id.traceID, id.spanID
	}
	var tid trace.TraceID
	for !tid.IsValid() {
		binary.BigEndian.PutUint64(tid[:8], rand.Uint64())
		binary.BigEndian.PutUint64(tid[8:], rand.Uint64())
	}
	return tid, randomSpanID()
}

func (pulseIDs) NewSpanID(ctx context.Context, traceID trace.TraceID) trace.SpanID {
	if id, ok := ctx.Value(idsKey{}).(ids); ok && id.traceID == traceID {
		return id.spanID
	}
	return randomSpanID()
}

func randomSpanID() trace.SpanID {
	var sid trace.SpanID
	for !sid.IsValid() {
		binary.BigEndian.PutUint64(sid[:], rand.Uint64())
	}
	return sid
}
