package pulse

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Failures inside Pulse itself — storage writes, rollup persistence,
// lifecycle events, notification delivery. They used to be logged only in
// DevMode, so in production an observability tool that had stopped
// recording looked exactly like a quiet one. Now they are counted per
// component, logged at most once a minute per component, exported to
// Prometheus, and (for SQLite storage) surfaced by the pulse_storage health
// check.

const internalErrorLogInterval = time.Minute

type internalErrors struct {
	mu      sync.Mutex
	counts  map[string]int64
	last    map[string]string    // latest message per component
	logged  map[string]time.Time // when each component was last logged
	skipped map[string]int64     // errors not logged since then
}

// internalError records err (if non-nil) against component and logs it —
// every time in DevMode, otherwise at most once a minute per component.
func (p *Pulse) internalError(component string, err error) {
	if err == nil {
		return
	}
	log, skipped := p.internal.record(component, err, time.Now(), p.config.DevMode)
	if !log {
		return
	}
	if skipped > 0 {
		p.logger.Printf("[pulse] %s: %v (%d more since the last report)", component, err, skipped)
	} else {
		p.logger.Printf("[pulse] %s: %v", component, err)
	}
}

// record counts err and reports whether to log it now, and how many errors
// for the component went unlogged since the previous log line.
func (ie *internalErrors) record(component string, err error, now time.Time, always bool) (log bool, skipped int64) {
	ie.mu.Lock()
	defer ie.mu.Unlock()
	if ie.counts == nil {
		ie.counts = make(map[string]int64)
		ie.last = make(map[string]string)
		ie.logged = make(map[string]time.Time)
		ie.skipped = make(map[string]int64)
	}
	ie.counts[component]++
	ie.last[component] = err.Error()
	if always || now.Sub(ie.logged[component]) >= internalErrorLogInterval {
		skipped = ie.skipped[component]
		ie.skipped[component] = 0
		ie.logged[component] = now
		return true, skipped
	}
	ie.skipped[component]++
	return false, 0
}

// snapshot returns the error count per component.
func (ie *internalErrors) snapshot() map[string]int64 {
	ie.mu.Lock()
	defer ie.mu.Unlock()
	out := make(map[string]int64, len(ie.counts))
	for k, v := range ie.counts {
		out[k] = v
	}
	return out
}

// storageFailures returns the total storage-related errors so far and the
// most recent message among them.
func (ie *internalErrors) storageFailures() (total int64, latest string) {
	ie.mu.Lock()
	defer ie.mu.Unlock()
	names := make([]string, 0, len(ie.counts))
	for name, n := range ie.counts {
		if strings.HasPrefix(name, "storage") {
			total += n
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		latest = names[len(names)-1] + ": " + ie.last[names[len(names)-1]]
	}
	return total, latest
}

// storageHealthCheck reports trouble with Pulse's own storage: an
// unresponsive database, or writes that failed or were dropped since the
// previous check. It is not critical — Pulse's storage problems make the
// app "degraded", never "unhealthy".
func storageHealthCheck(p *Pulse) HealthCheck {
	var seen atomic.Int64
	return HealthCheck{
		Name:     "pulse_storage",
		Type:     "pulse",
		Critical: false,
		CheckFunc: func(ctx context.Context) error {
			if pinger, ok := p.storage.(interface{ ping(context.Context) error }); ok {
				if err := pinger.ping(ctx); err != nil {
					return fmt.Errorf("storage unresponsive: %w", err)
				}
			}
			total, latest := p.internal.storageFailures()
			if fresh := total - seen.Swap(total); fresh > 0 {
				return fmt.Errorf("%d storage operations failed since the last check (latest %s)", fresh, latest)
			}
			return nil
		},
	}
}

// writeInternalMetrics adds Pulse's own error counts to the Prometheus output.
func writeInternalMetrics(b *strings.Builder, p *Pulse) {
	counts := p.internal.snapshot()
	if len(counts) == 0 {
		return
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(b, "# HELP pulse_internal_errors_total Failures inside Pulse itself, by component\n")
	fmt.Fprintf(b, "# TYPE pulse_internal_errors_total counter\n")
	for _, name := range names {
		fmt.Fprintf(b, "pulse_internal_errors_total{component=%q} %d\n", name, counts[name])
	}
	b.WriteString("\n")
}
