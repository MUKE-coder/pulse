package pulse

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Per-route baselines. Pulse can say a route is slow; this is what lets it
// say a route got slower. Every hour, each route's latency is summarised to
// a handful of numbers — count, errors, p50, p95, p99 — and those summaries
// are kept far longer than the request records they came from, because a
// regression is something you notice over weeks.
//
// A route's baseline is the median of its hourly p95s over the window: a
// median rather than a mean so one bad hour doesn't become the yardstick,
// and hours too quiet to be representative are left out entirely.

// routeHourStat is one route's latency over one hour.
type routeHourStat struct {
	Hour   int64         `json:"hour"` // Unix hour: the start of the hour
	Method string        `json:"method"`
	Route  string        `json:"route"`
	Count  int64         `json:"count"`
	Errors int64         `json:"errors"`
	P50    time.Duration `json:"p50"`
	P95    time.Duration `json:"p95"`
	P99    time.Duration `json:"p99"`
}

// baselineStore is implemented by storage backends that keep hourly route
// summaries. Unexported for the same reason as rollupStore.
type baselineStore interface {
	saveRouteHours(instance string, stats []routeHourStat) error
	// routeHoursSince returns every summary at or after the given Unix
	// hour, from every instance.
	routeHoursSince(sinceHour int64) ([]routeHourStat, error)
	pruneRouteHours(beforeHour int64) error
}

func hourOf(t time.Time) int64 { return t.Unix() / 3600 }

// routeHourAccum tallies one route's current hour.
type routeHourAccum struct {
	count  int64
	errors int64
	hist   latencyHistogram
}

// baselines accumulates the hour in progress; completed hours go to storage.
type baselines struct {
	mu      sync.Mutex
	hour    int64
	routes  map[rollupRouteKey]*routeHourAccum
	tracked int // routes seen this hour, to bound the map
}

// maxBaselineRoutes bounds how many routes one hour can tally, so a scanner
// hitting random paths can't grow it without end. Unmatched requests already
// collapse to one route (see unmatchedRoute).
const maxBaselineRoutes = 500

func newBaselines(now time.Time) *baselines {
	return &baselines{hour: hourOf(now), routes: make(map[rollupRouteKey]*routeHourAccum)}
}

// observe records one completed request against the hour in progress.
func (b *baselines) observe(method, route string, status int, latency time.Duration, at time.Time) {
	if route == "" {
		route = unmatchedRoute
	}
	key := rollupRouteKey{method: method, route: route}

	b.mu.Lock()
	defer b.mu.Unlock()
	if hour := hourOf(at); hour != b.hour {
		return // an hour that has already been summarised, or isn't open yet
	}
	accum := b.routes[key]
	if accum == nil {
		if len(b.routes) >= maxBaselineRoutes {
			return
		}
		accum = &routeHourAccum{}
		b.routes[key] = accum
	}
	accum.count++
	if status >= 500 {
		accum.errors++
	}
	accum.hist.add(latency)
}

// takeCompleted returns the summaries of every hour before now's, and starts
// tallying the current one. It returns nothing while the hour is still open.
func (b *baselines) takeCompleted(now time.Time) []routeHourStat {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := hourOf(now)
	if current == b.hour || len(b.routes) == 0 {
		if current != b.hour {
			b.hour = current
		}
		return nil
	}
	out := make([]routeHourStat, 0, len(b.routes))
	for key, accum := range b.routes {
		out = append(out, routeHourStat{
			Hour: b.hour, Method: key.method, Route: key.route,
			Count: accum.count, Errors: accum.errors,
			P50: accum.hist.quantile(0.50), P95: accum.hist.quantile(0.95), P99: accum.hist.quantile(0.99),
		})
	}
	b.hour = current
	b.routes = make(map[rollupRouteKey]*routeHourAccum)
	return out
}

// current returns the hour in progress, for comparing against the baseline
// before it has been written out.
func (b *baselines) current() []routeHourStat {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]routeHourStat, 0, len(b.routes))
	for key, accum := range b.routes {
		out = append(out, routeHourStat{
			Hour: b.hour, Method: key.method, Route: key.route,
			Count: accum.count, Errors: accum.errors,
			P50: accum.hist.quantile(0.50), P95: accum.hist.quantile(0.95), P99: accum.hist.quantile(0.99),
		})
	}
	return out
}

// routeBaseline is what a route usually does, and what it is doing now.
type routeBaseline struct {
	Method    string        `json:"method"`
	Route     string        `json:"route"`
	Baseline  time.Duration `json:"baseline_p95"`
	Current   time.Duration `json:"current_p95"`
	Ratio     float64       `json:"ratio"` // current ÷ baseline; 0 when there is no baseline yet
	Hours     int           `json:"hours"` // hours the baseline is drawn from
	Requests  int64         `json:"requests"`
	Regressed bool          `json:"regressed"`
}

// medianDuration is the middle value, which one bad hour cannot drag.
func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// compareToBaselines builds each route's baseline from its past hours and
// compares it with the hour in progress. Hours too quiet to be
// representative are left out, and a route without enough of them has no
// baseline rather than a bad one.
func compareToBaselines(past, current []routeHourStat, cfg BaselineConfig, now time.Time) []routeBaseline {
	window := baselineWindow(cfg)
	minRequests := baselineMinRequests(cfg)
	oldest := hourOf(now.Add(-window))

	type series struct {
		p95s  []time.Duration
		hours int
	}
	history := map[rollupRouteKey]*series{}
	for _, s := range past {
		if s.Hour < oldest || s.Count < minRequests {
			continue
		}
		key := rollupRouteKey{method: s.Method, route: s.Route}
		if history[key] == nil {
			history[key] = &series{}
		}
		history[key].p95s = append(history[key].p95s, s.P95)
		history[key].hours++
	}

	live := map[rollupRouteKey]routeHourStat{}
	for _, s := range current {
		live[rollupRouteKey{method: s.Method, route: s.Route}] = s
	}

	ratio := baselineRatio(cfg)
	minHours := baselineMinHours(cfg)
	out := make([]routeBaseline, 0, len(live))
	for key, now := range live {
		b := routeBaseline{Method: key.method, Route: key.route,
			Current: now.P95, Requests: now.Count}
		if h := history[key]; h != nil && h.hours >= minHours {
			b.Baseline = medianDuration(h.p95s)
			b.Hours = h.hours
			if b.Baseline > 0 && now.Count >= minRequests {
				b.Ratio = float64(now.P95) / float64(b.Baseline)
				b.Regressed = b.Ratio >= ratio
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ratio > out[j].Ratio })
	return out
}

// --- MemoryStorage ---

// maxStoredRouteHours bounds the summaries the Memory backend keeps: 500
// routes over a 30-day retention would be 360,000, which is more than an
// in-process buffer should hold.
const maxStoredRouteHours = 50000

// storedRouteHour is one summary as the Memory backend holds it: the
// instance is part of its identity, because every instance summarises the
// same hour for itself.
type storedRouteHour struct {
	instance string
	stat     routeHourStat
}

func (s *MemoryStorage) saveRouteHours(instance string, stats []routeHourStat) error {
	if len(stats) == 0 {
		return nil
	}
	s.routeHoursMu.Lock()
	defer s.routeHoursMu.Unlock()
	for _, stat := range stats {
		// Replace rather than append: a flush that is retried, or an hour
		// written twice, must not double-count. The SQL backends upsert on
		// the same key.
		replaced := false
		for i, held := range s.routeHours {
			if held.instance == instance && held.stat.Hour == stat.Hour &&
				held.stat.Method == stat.Method && held.stat.Route == stat.Route {
				s.routeHours[i].stat = stat
				replaced = true
				break
			}
		}
		if !replaced {
			s.routeHours = append(s.routeHours, storedRouteHour{instance: instance, stat: stat})
		}
	}
	if excess := len(s.routeHours) - maxStoredRouteHours; excess > 0 {
		s.routeHours = append([]storedRouteHour(nil), s.routeHours[excess:]...)
	}
	return nil
}

func (s *MemoryStorage) routeHoursSince(sinceHour int64) ([]routeHourStat, error) {
	s.routeHoursMu.Lock()
	defer s.routeHoursMu.Unlock()
	out := make([]routeHourStat, 0, len(s.routeHours))
	for _, held := range s.routeHours {
		if held.stat.Hour >= sinceHour {
			out = append(out, held.stat)
		}
	}
	return out, nil
}

func (s *MemoryStorage) pruneRouteHours(beforeHour int64) error {
	s.routeHoursMu.Lock()
	defer s.routeHoursMu.Unlock()
	kept := s.routeHours[:0]
	for _, held := range s.routeHours {
		if held.stat.Hour >= beforeHour {
			kept = append(kept, held)
		}
	}
	s.routeHours = kept
	return nil
}

var (
	_ baselineStore = (*MemoryStorage)(nil)
	_ baselineStore = (*SQLiteStorage)(nil)
	_ baselineStore = (*PostgresStorage)(nil)
)

// --- sqlStore ---

func (s *sqlStore) saveRouteHours(instance string, stats []routeHourStat) error {
	if len(stats) == 0 {
		return nil
	}
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed

	stmt, err := tx.Prepare(s.d.sql(`INSERT INTO route_hours
		(instance_id, hour, method, route, count, errors, p50_ns, p95_ns, p99_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, hour, method, route) DO UPDATE SET count = excluded.count,
		    errors = excluded.errors, p50_ns = excluded.p50_ns, p95_ns = excluded.p95_ns,
		    p99_ns = excluded.p99_ns`))
	if err != nil {
		return err
	}
	for _, stat := range stats {
		if _, err := stmt.Exec(s.d.args([]any{instance, stat.Hour, stat.Method, stat.Route,
			stat.Count, stat.Errors, int64(stat.P50), int64(stat.P95), int64(stat.P99)})...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlStore) routeHoursSince(sinceHour int64) ([]routeHourStat, error) {
	s.sync()
	rows, err := s.query(`SELECT hour, method, route, count, errors, p50_ns, p95_ns, p99_ns
		FROM route_hours WHERE hour >= ? ORDER BY hour ASC`, sinceHour)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []routeHourStat
	for rows.Next() {
		var (
			stat          routeHourStat
			p50, p95, p99 int64
		)
		if err := rows.Scan(&stat.Hour, &stat.Method, &stat.Route, &stat.Count, &stat.Errors,
			&p50, &p95, &p99); err != nil {
			return nil, err
		}
		stat.P50, stat.P95, stat.P99 = time.Duration(p50), time.Duration(p95), time.Duration(p99)
		out = append(out, stat)
	}
	return out, rows.Err()
}

func (s *sqlStore) pruneRouteHours(beforeHour int64) error {
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.exec(`DELETE FROM route_hours WHERE hour < ?`, beforeHour)
	return err
}

// flushBaselines writes out the hours that have completed and drops
// summaries past their own retention: they are meant to outlive the request
// records by a long way, so Storage.RetentionHours doesn't govern them.
func flushBaselines(p *Pulse) {
	if !boolValue(p.config.Baselines.Enabled) || p.baselines == nil {
		return
	}
	store, ok := p.storage.(baselineStore)
	if !ok {
		return
	}
	now := p.now()
	if stats := p.baselines.takeCompleted(now); len(stats) > 0 {
		p.internalError("storage: route hours", store.saveRouteHours(p.config.InstanceID, stats))
	}
	if p.isLeader() { // one instance prunes for all of them
		before := hourOf(now.Add(-baselineRetention(p.config.Baselines)))
		p.internalError("storage: route hours", store.pruneRouteHours(before))
	}
}

// routeBaselinesHandler serves GET /pulse/api/routes/baselines: what each
// route usually does, and how the hour in progress compares.
func routeBaselinesHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := p.config.Baselines
		resp := gin.H{
			"baselines": []routeBaseline{},
			"enabled":   boolValue(cfg.Enabled),
			"window":    baselineWindow(cfg).String(),
			"ratio":     baselineRatio(cfg),
		}
		store, ok := p.storage.(baselineStore)
		if !ok || !boolValue(cfg.Enabled) {
			resp["supported"] = ok
			c.JSON(http.StatusOK, resp)
			return
		}
		now := p.now()
		past, err := store.routeHoursSince(hourOf(now.Add(-baselineWindow(cfg))))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		resp["supported"] = true
		resp["baselines"] = compareToBaselines(past, p.baselines.current(), cfg, now)
		c.JSON(http.StatusOK, resp)
	}
}

// latencyText renders a duration the way the dashboard does.
func latencyText(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	if ms >= 1000 {
		return fmt.Sprintf("%.2fs", ms/1000)
	}
	if ms < 10 {
		return fmt.Sprintf("%.1fms", ms)
	}
	return fmt.Sprintf("%.0fms", ms)
}

// regressionWatcher alerts when a route's p95 stays above its baseline. It
// is the slow counterpart to the burn-rate alerts: one slow hour is noise,
// an hour at twice the usual latency is a release worth looking at.
type regressionWatcher struct {
	pulse *Pulse

	mu     sync.Mutex
	since  map[rollupRouteKey]time.Time // when each route's regression began
	firing map[rollupRouteKey]string    // open alert ID, per route
}

func newRegressionWatcher(p *Pulse) *regressionWatcher {
	return &regressionWatcher{
		pulse:  p,
		since:  make(map[rollupRouteKey]time.Time),
		firing: make(map[rollupRouteKey]string),
	}
}

func (w *regressionWatcher) start() {
	interval := 5 * time.Minute
	if w.pulse.config.DevMode {
		interval = 15 * time.Second
	}
	w.pulse.startBackground("regression-watcher", func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.evaluate()
			}
		}
	})
}

// evaluate compares every route with its baseline, and alerts on the ones
// that have stayed slower for long enough.
func (w *regressionWatcher) evaluate() {
	p := w.pulse
	if !p.isLeader() { // the cluster pages once
		return
	}
	store, ok := p.storage.(baselineStore)
	if !ok {
		return
	}
	cfg := p.config.Baselines
	now := p.now()
	past, err := store.routeHoursSince(hourOf(now.Add(-baselineWindow(cfg))))
	if err != nil {
		p.internalError("storage: route hours", err)
		return
	}

	for _, b := range compareToBaselines(past, p.baselines.current(), cfg, now) {
		key := rollupRouteKey{method: b.Method, route: b.Route}
		if !b.Regressed {
			w.resolve(key, b, now)
			continue
		}
		w.mu.Lock()
		started, seen := w.since[key]
		if !seen {
			started, w.since[key] = now, now
		}
		_, firing := w.firing[key]
		w.mu.Unlock()
		if !firing && now.Sub(started) >= baselineFor(cfg) {
			w.fire(key, b, started, now)
		}
	}
}

// deployWindow bounds how long after a deploy a regression can still be
// blamed on it. Beyond a day the two are just both true.
const deployWindow = 24 * time.Hour

// deployAttribution names the build a regression began under, when it began
// soon enough after that build was deployed to be its doing. A step change
// right after a deploy is a release that got slower, which is a different
// investigation from one that crept up over a month.
func deployAttribution(p *Pulse, started time.Time, version string) string {
	ls, ok := p.storage.(lifecycleStore)
	if !ok || version == "" {
		return ""
	}
	// When this build first started is when it was deployed; starts of the
	// same build after that are restarts.
	events, err := ls.lifecycleEvents(TimeRange{Start: started.Add(-30 * 24 * time.Hour), End: p.now()})
	if err != nil {
		return ""
	}
	for _, e := range events { // oldest first
		if e.Type != lifecycleStart || e.Version != version {
			continue
		}
		since := started.Sub(e.At)
		if since < 0 || since > deployWindow {
			return "" // the regression predates this build, or long outlives its deploy
		}
		return fmt.Sprintf(", which began %s after %s was deployed",
			since.Round(time.Minute), version)
	}
	return ""
}

// fire announces a sustained regression. The message carries the numbers
// someone needs to decide whether to roll back: how much slower, measured
// against what, for how long.
func (w *regressionWatcher) fire(key rollupRouteKey, b routeBaseline, started, now time.Time) {
	route := strings.TrimSpace(b.Method + " " + b.Route)
	alert := AlertRecord{
		ID:        GenerateTraceID(),
		RuleName:  "route_regression",
		Metric:    "p95_vs_baseline",
		Value:     b.Ratio,
		Threshold: baselineRatio(w.pulse.config.Baselines),
		Operator:  ">=",
		Severity:  "warning",
		State:     AlertStateFiring,
		Route:     route,
		Message: fmt.Sprintf("%s is %.1f× slower than usual: p95 %s against a %s baseline over %d hours, for the last %s%s",
			route, b.Ratio, latencyText(b.Current), latencyText(b.Baseline), b.Hours,
			now.Sub(started).Round(time.Minute),
			deployAttribution(w.pulse, started, buildVersion(collectSystemInfo()))),
		FiredAt: now,
	}

	w.mu.Lock()
	w.firing[key] = alert.ID
	w.mu.Unlock()
	w.announce(alert, "regression")
}

// resolve closes an open regression once the route comes back to its usual
// latency, in place: same ID, original firing time, state resolved.
func (w *regressionWatcher) resolve(key rollupRouteKey, b routeBaseline, now time.Time) {
	w.mu.Lock()
	id, firing := w.firing[key]
	delete(w.firing, key)
	delete(w.since, key)
	w.mu.Unlock()
	if !firing {
		return
	}

	route := strings.TrimSpace(b.Method + " " + b.Route)
	alert := AlertRecord{
		ID:         id,
		RuleName:   "route_regression",
		Metric:     "p95_vs_baseline",
		Value:      b.Ratio,
		Threshold:  baselineRatio(w.pulse.config.Baselines),
		Operator:   ">=",
		Severity:   "warning",
		State:      AlertStateResolved,
		Route:      route,
		Message:    fmt.Sprintf("[Resolved] %s is back to its usual latency: p95 %s", route, latencyText(b.Current)),
		FiredAt:    now,
		ResolvedAt: &now,
	}
	w.announce(alert, "regression resolved")
}

func (w *regressionWatcher) announce(alert AlertRecord, what string) {
	p := w.pulse
	p.internalError("storage: alerts", p.storage.StoreAlert(alert))
	p.BroadcastAlert(alert)
	if p.alertEngine != nil {
		p.alertEngine.notify(alert)
	}
	if p.config.DevMode {
		p.logger.Printf("[pulse] %s: %s", what, alert.Message)
	}
}
