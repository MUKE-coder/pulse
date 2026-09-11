package pulse

import (
	"context"
	"encoding/binary"
	"math"
	"math/bits"
	"sort"
	"sync"
	"time"
)

// Per-minute request rollups.
//
// The tracing middleware records every request here before its sampling
// decision, so request counts, error rates and SLO compliance stay exact at
// any Tracing.SampleRate. Stored requests are a biased sample: errors and
// slow requests are always kept, successes only at SampleRate. Rollups are
// also far cheaper than raw rows over long windows — a 30-day SLO is 43,200
// small counters rather than every request.
//
// On a backend that implements rollupStore (SQLite), rollups are saved every
// rollupFlushInterval and restored at Mount, so SLO windows survive restarts.

// unmatchedRoute groups requests that matched no route (404s), so scanners
// probing random paths can't grow the per-route maps without bound.
const unmatchedRoute = "(unmatched)"

const rollupFlushInterval = 30 * time.Second

type rollupRouteKey struct{ method, route string }

// routeCounts are one route's tallies for one minute (or summed over many).
type routeCounts struct {
	total     int64
	status4xx int64
	status5xx int64
	latency   time.Duration // sum, for averages
}

func (c *routeCounts) add(o routeCounts) {
	c.total += o.total
	c.status4xx += o.status4xx
	c.status5xx += o.status5xx
	c.latency += o.latency
}

// sloCount is one SLO's good and total events for one minute.
type sloCount struct{ good, total int64 }

type minuteRollup struct {
	routes map[rollupRouteKey]*routeCounts
	hist   latencyHistogram
}

type sloRollup struct {
	slo    SLO
	window time.Duration // longest window the SLO reads (Window or a burn-rate window)
	counts map[int64]*sloCount
}

// rollups holds per-minute tallies keyed by Unix minute: this instance's
// own, and — when instances share the storage — each other instance's,
// loaded as they save them. Reads cover them all.
type rollups struct {
	mu      sync.Mutex
	detail  time.Duration // how long per-route counts and latency histograms are kept
	minutes map[int64]*minuteRollup
	slos    map[string]*sloRollup
	since   time.Time      // earliest moment the rollups cover
	persist bool           // a rollupStore is attached: track changed minutes
	dirty   map[int64]bool // minutes changed since the last takeDirty

	peersMu       sync.Mutex
	peers         map[string]*rollups // other instances' rollups, by instance ID
	peerWatermark int64               // latest save time of peer rows loaded (Unix ms)
}

func newRollups(detail time.Duration, slos []SLO, now time.Time) *rollups {
	if detail <= 0 {
		detail = 24 * time.Hour
	}
	r := &rollups{
		detail:  detail,
		minutes: make(map[int64]*minuteRollup),
		slos:    make(map[string]*sloRollup),
		since:   now,
		dirty:   make(map[int64]bool),
	}
	r.trackSLOs(slos)
	return r
}

func minuteOf(t time.Time) int64 { return t.Unix() / 60 }

// trackSLOs starts counting events for every SLO not already tracked.
func (r *rollups) trackSLOs(slos []SLO) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range slos {
		if s.Indicator == nil {
			continue
		}
		if _, ok := r.slos[s.Name]; ok {
			continue
		}
		window := s.Window
		for _, ba := range s.burnRateAlerts() {
			if ba.Window > window {
				window = ba.Window
			}
		}
		r.slos[s.Name] = &sloRollup{slo: s, window: window, counts: make(map[int64]*sloCount)}
	}
}

// observe records one completed request. fullPath is Gin's matched route
// pattern ("" when nothing matched); path is what SLO route globs are
// matched against (the pattern, or the raw path for unmatched requests).
func (r *rollups) observe(method, fullPath, path string, status int, latency time.Duration, at time.Time) {
	minute := minuteOf(at)
	key := rollupRouteKey{method: method, route: fullPath}
	if fullPath == "" {
		key.route = unmatchedRoute
	}
	req := RequestMetric{Method: method, Path: path, StatusCode: status, Latency: latency}

	r.mu.Lock()
	defer r.mu.Unlock()

	b := r.minutes[minute]
	if b == nil {
		b = &minuteRollup{routes: make(map[rollupRouteKey]*routeCounts)}
		r.minutes[minute] = b
	}
	c := b.routes[key]
	if c == nil {
		c = &routeCounts{}
		b.routes[key] = c
	}
	c.total++
	switch {
	case status >= 500:
		c.status5xx++
	case status >= 400:
		c.status4xx++
	}
	c.latency += latency
	b.hist.add(latency)

	for _, s := range r.slos {
		good, total := s.slo.Indicator.classify(req)
		if total == 0 {
			continue
		}
		sc := s.counts[minute]
		if sc == nil {
			sc = &sloCount{}
			s.counts[minute] = sc
		}
		sc.good += good
		sc.total += total
	}

	if r.persist {
		r.dirty[minute] = true
	}
}

// minuteRange returns the Unix minutes overlapping [from, to], clipped to
// the data the rollups actually hold.
func (r *rollups) minuteRange(from, to time.Time) (first, last int64) {
	if since := r.dataSince(); from.Before(since) {
		from = since
	}
	return minuteOf(from), minuteOf(to)
}

// members returns this instance's rollups followed by its peers'.
func (r *rollups) members() []*rollups {
	r.peersMu.Lock()
	defer r.peersMu.Unlock()
	out := make([]*rollups, 0, 1+len(r.peers))
	out = append(out, r)
	for _, p := range r.peers {
		out = append(out, p)
	}
	return out
}

// peer returns instance id's rollups, creating them — tracking the same
// SLOs as this instance's — on first use.
func (r *rollups) peer(id string) *rollups {
	r.peersMu.Lock()
	defer r.peersMu.Unlock()
	if p := r.peers[id]; p != nil {
		return p
	}
	r.mu.Lock()
	slos := make([]SLO, 0, len(r.slos))
	for _, s := range r.slos {
		slos = append(slos, s.slo)
	}
	detail := r.detail
	r.mu.Unlock()
	if r.peers == nil {
		r.peers = make(map[string]*rollups)
	}
	p := newRollups(detail, slos, time.Now()) // restore moves since back to its data
	r.peers[id] = p
	return p
}

// rollupSummary is the global view of a span of minutes.
type rollupSummary struct {
	counts routeCounts
	hist   latencyHistogram
}

// summary returns global totals and the merged latency histogram for the
// minutes overlapping [from, to].
func (r *rollups) summary(from, to time.Time) rollupSummary {
	first, last := r.minuteRange(from, to)
	return r.summaryMinutes(first, last)
}

// summaryMinutes is summary over the Unix minutes [first, last], across
// this instance and its peers.
func (r *rollups) summaryMinutes(first, last int64) rollupSummary {
	var s rollupSummary
	for _, m := range r.members() {
		m.addOwnSummary(&s, first, last)
	}
	return s
}

// addOwnSummary adds this instance's own minutes [first, last] to s.
func (r *rollups) addOwnSummary(s *rollupSummary, first, last int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for m := first; m <= last; m++ {
		b := r.minutes[m]
		if b == nil {
			continue
		}
		for _, c := range b.routes {
			s.counts.add(*c)
		}
		s.hist.merge(&b.hist)
	}
}

// instanceSummaries returns each instance's own totals for the minutes
// overlapping [from, to]: this instance's under self, and each peer's.
func (r *rollups) instanceSummaries(self string, from, to time.Time) map[string]routeCounts {
	first, last := r.minuteRange(from, to)
	var own rollupSummary
	r.addOwnSummary(&own, first, last)
	out := map[string]routeCounts{self: own.counts}

	r.peersMu.Lock()
	peers := make(map[string]*rollups, len(r.peers))
	for id, p := range r.peers {
		peers[id] = p
	}
	r.peersMu.Unlock()
	for id, p := range peers {
		var s rollupSummary
		p.addOwnSummary(&s, first, last)
		out[id] = s.counts
	}
	return out
}

// routeTotals returns per-route totals for the minutes overlapping [from, to].
func (r *rollups) routeTotals(from, to time.Time) map[rollupRouteKey]routeCounts {
	first, last := r.minuteRange(from, to)
	out := make(map[rollupRouteKey]routeCounts)
	for _, m := range r.members() {
		m.addOwnRouteTotals(out, first, last)
	}
	return out
}

func (r *rollups) addOwnRouteTotals(out map[rollupRouteKey]routeCounts, first, last int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for m := first; m <= last; m++ {
		b := r.minutes[m]
		if b == nil {
			continue
		}
		for k, c := range b.routes {
			sum := out[k]
			sum.add(*c)
			out[k] = sum
		}
	}
}

// sloCounts returns the named SLO's good and total events for the minutes
// overlapping [from, to].
func (r *rollups) sloCounts(name string, from, to time.Time) (good, total int64) {
	first, last := r.minuteRange(from, to)
	for _, m := range r.members() {
		g, t := m.ownSLOCounts(name, first, last)
		good += g
		total += t
	}
	return good, total
}

func (r *rollups) ownSLOCounts(name string, first, last int64) (good, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.slos[name]
	if s == nil {
		return 0, 0
	}
	for m := first; m <= last; m++ {
		if c := s.counts[m]; c != nil {
			good += c.good
			total += c.total
		}
	}
	return good, total
}

// coverage is the fraction of the window ending at now that falls after the
// rollups began collecting — in this process, in restored history, or on
// another instance.
func (r *rollups) coverage(now time.Time, window time.Duration) float64 {
	if window <= 0 {
		return 1
	}
	covered := now.Sub(r.dataSince())
	return math.Max(0, math.Min(1, float64(covered)/float64(window)))
}

// dataSince is the earliest moment the rollups — this instance's or a
// peer's — cover.
func (r *rollups) dataSince() time.Time {
	r.mu.Lock()
	since := r.since
	r.mu.Unlock()
	r.peersMu.Lock()
	peers := make([]*rollups, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.peersMu.Unlock()
	for _, p := range peers {
		p.mu.Lock()
		if p.since.Before(since) {
			since = p.since
		}
		p.mu.Unlock()
	}
	return since
}

// horizon is how far back any rollup is kept.
func (r *rollups) horizon() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.detail
	for _, s := range r.slos {
		if s.window > h {
			h = s.window
		}
	}
	return h
}

// cutoffs returns the first minute still kept for per-route detail and for
// SLO counts, as of now.
func (r *rollups) cutoffs(now time.Time) (detail, slo int64) {
	detail = minuteOf(now.Add(-r.detail))
	slo = detail
	for _, s := range r.slos {
		if m := minuteOf(now.Add(-s.window)); m < slo {
			slo = m
		}
	}
	return detail, slo
}

// prune drops minutes that have aged out of every window that reads them,
// and peers left with nothing.
func (r *rollups) prune(now time.Time) {
	r.pruneOwn(now)
	r.peersMu.Lock()
	defer r.peersMu.Unlock()
	for id, p := range r.peers {
		if p.pruneOwn(now) {
			delete(r.peers, id)
		}
	}
}

// pruneOwn prunes this instance's own minutes, and reports whether none are
// left.
func (r *rollups) pruneOwn(now time.Time) (empty bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		empty = len(r.minutes) == 0
		for _, s := range r.slos {
			empty = empty && len(s.counts) == 0
		}
	}()
	detailCutoff := minuteOf(now.Add(-r.detail))
	for m := range r.minutes {
		if m < detailCutoff {
			delete(r.minutes, m)
		}
	}
	for _, s := range r.slos {
		cutoff := minuteOf(now.Add(-s.window))
		for m := range s.counts {
			if m < cutoff {
				delete(s.counts, m)
			}
		}
	}
	return
}

// reset discards every rollup, as part of a dashboard data reset.
func (r *rollups) reset(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.minutes = make(map[int64]*minuteRollup)
	for _, s := range r.slos {
		s.counts = make(map[int64]*sloCount)
	}
	r.dirty = make(map[int64]bool)
	r.since = now

	r.peersMu.Lock()
	r.peers = nil
	r.peerWatermark = time.Now().UnixMilli() // don't reload what was just discarded
	r.peersMu.Unlock()
}

// --- Persistence ---

// rollupStore is implemented by storage backends that persist rollups across
// restarts. It is unexported on purpose: the public Storage interface is
// frozen (see STABILITY.md), so optional capabilities are discovered by type
// assertion.
type rollupStore interface {
	saveRollups(instance string, minutes []minuteSnapshot) error
	loadRollups(instance string, sinceMinute int64) ([]minuteSnapshot, error)
	loadPeerRollups(instance string, sinceMinute, updatedAfter int64) (map[string][]minuteSnapshot, int64, error)
	pruneRollups(detailBefore, sloBefore int64) error
}

// minuteSnapshot is one minute of rollups, detached from the live maps.
type minuteSnapshot struct {
	minute int64
	routes map[rollupRouteKey]routeCounts
	hist   *latencyHistogram // nil when the minute has no request detail
	slos   map[string]sloCount
}

// takeDirty returns every minute changed since the last call.
func (r *rollups) takeDirty() []minuteSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]minuteSnapshot, 0, len(r.dirty))
	for m := range r.dirty {
		snap := minuteSnapshot{minute: m, slos: make(map[string]sloCount)}
		if b := r.minutes[m]; b != nil {
			snap.routes = make(map[rollupRouteKey]routeCounts, len(b.routes))
			for k, c := range b.routes {
				snap.routes[k] = *c
			}
			h := b.hist
			snap.hist = &h
		}
		for name, s := range r.slos {
			if c := s.counts[m]; c != nil {
				snap.slos[name] = *c
			}
		}
		out = append(out, snap)
	}
	r.dirty = make(map[int64]bool)
	sort.Slice(out, func(i, j int) bool { return out[i].minute < out[j].minute })
	return out
}

// restore loads persisted minutes. Counts for SLOs no longer configured are
// ignored.
func (r *rollups) restore(snaps []minuteSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, snap := range snaps {
		if len(snap.routes) > 0 || snap.hist != nil {
			b := &minuteRollup{routes: make(map[rollupRouteKey]*routeCounts, len(snap.routes))}
			for k, c := range snap.routes {
				c := c
				b.routes[k] = &c
			}
			if snap.hist != nil {
				b.hist = *snap.hist
			}
			r.minutes[snap.minute] = b
		}
		for name, c := range snap.slos {
			if s := r.slos[name]; s != nil {
				c := c
				s.counts[snap.minute] = &c
			}
		}
		if t := time.Unix(snap.minute*60, 0); t.Before(r.since) {
			r.since = t
		}
	}
}

// restoreRollups loads persisted rollups, when the storage backend keeps
// them, and turns on change tracking so they keep being saved.
func restoreRollups(p *Pulse) {
	store, ok := p.storage.(rollupStore)
	if !ok {
		return
	}
	p.rollups.mu.Lock()
	p.rollups.persist = true
	p.rollups.mu.Unlock()

	snaps, err := store.loadRollups(p.config.InstanceID, minuteOf(p.now().Add(-p.rollups.horizon())))
	if err != nil {
		p.internalError("storage: rollups", err)
		return
	}
	p.rollups.restore(snaps)
	refreshPeerRollups(p, store)
}

// peerRollupOverlap is how far before the last load peer rows are read
// again, so a save that committed late isn't missed. Reading a minute again
// replaces it.
const peerRollupOverlap = time.Minute

// sharedRollupFlushInterval replaces rollupFlushInterval when instances
// share the storage, so each one's figures include the others' sooner.
const sharedRollupFlushInterval = 10 * time.Second

// refreshPeerRollups loads the rollups other instances saved since the last
// load.
func refreshPeerRollups(p *Pulse, store rollupStore) {
	r := p.rollups
	r.peersMu.Lock()
	after := r.peerWatermark - peerRollupOverlap.Milliseconds()
	r.peersMu.Unlock()

	byInstance, latest, err := store.loadPeerRollups(p.config.InstanceID, minuteOf(p.now().Add(-r.horizon())), after)
	if err != nil {
		p.internalError("storage: rollups", err)
		return
	}
	for id, snaps := range byInstance {
		r.peer(id).restore(snaps)
	}
	r.peersMu.Lock()
	if latest > r.peerWatermark {
		r.peerWatermark = latest
	}
	r.peersMu.Unlock()
}

// flushRollups saves the minutes changed since the last flush and loads
// other instances' (when the backend persists rollups), then drops expired
// ones.
func flushRollups(p *Pulse) {
	now := p.now()
	if store, ok := p.storage.(rollupStore); ok {
		p.internalError("storage: rollups", store.saveRollups(p.config.InstanceID, p.rollups.takeDirty()))
		p.rollups.mu.Lock()
		detail, slo := p.rollups.cutoffs(now)
		p.rollups.mu.Unlock()
		p.internalError("storage: rollups", store.pruneRollups(detail, slo))
		refreshPeerRollups(p, store)
	}
	p.rollups.prune(now)
}

// startRollupFlusher flushes rollups on a tick, and once more on shutdown.
func startRollupFlusher(p *Pulse) {
	interval := rollupFlushInterval
	if p.sharedStorage() {
		interval = sharedRollupFlushInterval
	}
	p.startBackground("rollup-flusher", func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				flushRollups(p)
				return
			case <-ticker.C:
				flushRollups(p)
			}
		}
	})
}

// --- Latency histogram ---

// latencyHistogram counts latencies in log-linear buckets: sixteen
// equal-width buckets per doubling of microseconds, so each bucket spans at
// most 6.25% and a reported quantile is within that of the true value. 512
// buckets reach about 70 minutes.
type latencyHistogram [512]uint32

const histSubBuckets = 16 // buckets per doubling

// histIndex returns the bucket for latency d.
func histIndex(d time.Duration) int {
	var us uint64
	if d > 0 {
		us = uint64(d / time.Microsecond)
	}
	if us < histSubBuckets {
		return int(us)
	}
	e := bits.Len64(us) - 1 // floor(log2(us)), ≥ 4
	idx := (e-3)*histSubBuckets + int((us>>(e-4))&(histSubBuckets-1))
	if idx >= len(latencyHistogram{}) {
		idx = len(latencyHistogram{}) - 1
	}
	return idx
}

// histBounds returns the [lo, hi) microsecond range of bucket idx.
func histBounds(idx int) (lo, hi uint64) {
	if idx < histSubBuckets {
		return uint64(idx), uint64(idx) + 1
	}
	shift := uint(idx/histSubBuckets - 1) // = e - 4
	sub := uint64(idx % histSubBuckets)
	return (histSubBuckets + sub) << shift, (histSubBuckets + 1 + sub) << shift
}

func (h *latencyHistogram) add(d time.Duration) {
	h[histIndex(d)]++
}

func (h *latencyHistogram) merge(o *latencyHistogram) {
	for i, c := range o {
		h[i] += c
	}
}

func (h *latencyHistogram) count() uint64 {
	var n uint64
	for _, c := range h {
		n += uint64(c)
	}
	return n
}

// quantile returns the q-quantile (0 < q ≤ 1) as its bucket's midpoint, or 0
// for an empty histogram.
func (h *latencyHistogram) quantile(q float64) time.Duration {
	lo, hi, ok := h.quantileBounds(q)
	if !ok {
		return 0
	}
	return (lo + hi) / 2
}

// quantileBounds returns the [lo, hi) range of the bucket holding the
// q-quantile — the true value lies within it. ok is false for an empty
// histogram.
func (h *latencyHistogram) quantileBounds(q float64) (lo, hi time.Duration, ok bool) {
	total := h.count()
	if total == 0 {
		return 0, 0, false
	}
	rank := uint64(math.Ceil(q * float64(total)))
	if rank < 1 {
		rank = 1
	}
	var seen uint64
	for i, c := range h {
		seen += uint64(c)
		if seen >= rank {
			l, u := histBounds(i)
			return time.Duration(l) * time.Microsecond, time.Duration(u) * time.Microsecond, true
		}
	}
	return 0, 0, false
}

// encode serializes the non-empty buckets as (uint16 index, uint32 count)
// pairs, little-endian.
func (h *latencyHistogram) encode() []byte {
	out := make([]byte, 0, 64)
	for i, c := range h {
		if c == 0 {
			continue
		}
		out = binary.LittleEndian.AppendUint16(out, uint16(i))
		out = binary.LittleEndian.AppendUint32(out, c)
	}
	return out
}

func decodeLatencyHistogram(b []byte) latencyHistogram {
	var h latencyHistogram
	for len(b) >= 6 {
		idx := int(binary.LittleEndian.Uint16(b))
		if idx < len(h) {
			h[idx] += binary.LittleEndian.Uint32(b[2:])
		}
		b = b[6:]
	}
	return h
}
