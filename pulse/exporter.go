package pulse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Exporter receives Pulse's telemetry as it is recorded, so it can be
// forwarded to another system: OpenTelemetry (see the
// github.com/MUKE-coder/pulse/otel module), a log pipeline, a data
// warehouse. Register one with [WithExporter].
//
// Every request is exported, whatever Tracing.SampleRate says; queries,
// outbound calls and errors are exported as they are recorded. Values have
// already been redacted. Records carry trace and span IDs (TraceID, SpanID,
// ParentSpanID), so an exporter can rebuild each request's span tree.
//
// Export is called from a single background goroutine with batches of
// events, and should return promptly: while it runs, new events queue up,
// and once the queue is full they are dropped and counted as the "export"
// internal error. A panic in Export is recovered and counted the same way.
type Exporter interface {
	Export(ctx context.Context, events []Event)
}

// EventKind identifies the record an [Event] carries. More kinds may be
// added in later versions; exporters should ignore kinds they don't know.
type EventKind string

const (
	EventRequest    EventKind = "request"    // Event.Request is set
	EventQuery      EventKind = "query"      // Event.Query is set
	EventDependency EventKind = "dependency" // Event.Dependency is set
	EventError      EventKind = "error"      // Event.Error is set
)

// Event is one piece of telemetry handed to an [Exporter]. Exactly one of
// the record fields is set, matching Kind. Exporters must not modify the
// records.
type Event struct {
	Kind       EventKind
	Request    *RequestMetric
	Query      *QueryMetric
	Dependency *DependencyMetric
	Error      *ErrorRecord
}

const (
	exportQueueSize     = 10000
	exportBatchSize     = 256
	exportFlushInterval = time.Second
	exportFinalTimeout  = 5 * time.Second
)

var errExportQueueFull = errors.New("export queue full; event dropped")

// exportPipeline fans recorded telemetry out to the configured exporters.
type exportPipeline struct {
	queue     chan Event
	exporters []Exporter
}

// newExportPipeline returns nil when no exporters are configured, which
// makes export a no-op.
func newExportPipeline(exporters []Exporter) *exportPipeline {
	var live []Exporter
	for _, e := range exporters {
		if e != nil {
			live = append(live, e)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return &exportPipeline{queue: make(chan Event, exportQueueSize), exporters: live}
}

// export queues e for the exporters without blocking; a no-op when none are
// configured.
func (p *Pulse) export(e Event) {
	if p.exporter == nil {
		return
	}
	select {
	case p.exporter.queue <- e:
	default:
		p.internalError("export", errExportQueueFull)
	}
}

// startExportPipeline delivers queued events in batches — when a batch
// fills, every exportFlushInterval, and once more at shutdown.
func startExportPipeline(p *Pulse) {
	ep := p.exporter
	if ep == nil {
		return
	}
	p.startBackground("exporter", func(ctx context.Context) {
		ticker := time.NewTicker(exportFlushInterval)
		defer ticker.Stop()

		batch := make([]Event, 0, exportBatchSize)
		flush := func(ctx context.Context) {
			if len(batch) == 0 {
				return
			}
			ep.deliver(ctx, p, append([]Event(nil), batch...))
			batch = batch[:0]
		}
		for {
			select {
			case e := <-ep.queue:
				batch = append(batch, e)
				if len(batch) >= exportBatchSize {
					flush(ctx)
				}
			case <-ticker.C:
				flush(ctx)
			case <-ctx.Done():
				// Shutting down: hand over whatever is queued, with a fresh
				// deadline since ctx is already canceled.
				for drained := false; !drained; {
					select {
					case e := <-ep.queue:
						batch = append(batch, e)
					default:
						drained = true
					}
				}
				final, cancel := context.WithTimeout(context.Background(), exportFinalTimeout)
				flush(final)
				cancel()
				return
			}
		}
	})
}

// deliver hands a batch to every exporter, containing panics.
func (ep *exportPipeline) deliver(ctx context.Context, p *Pulse, events []Event) {
	for _, ex := range ep.exporters {
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.internalError("export", fmt.Errorf("exporter %T panicked: %v", ex, r))
				}
			}()
			ex.Export(ctx, events)
		}()
	}
}
