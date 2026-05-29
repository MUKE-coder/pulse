package pulse

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SLO is a service-level objective: a Target ratio of "good" events to total
// events, evaluated by an [SLI] over a sliding Window.
//
// Example — "99.9% of /api/* requests succeed over a 30-day window":
//
//	pulse.SLO{
//	    Name:   "API availability",
//	    Target: 0.999,
//	    Window: 30 * 24 * time.Hour,
//	    Indicator: pulse.SLIErrorRate{Routes: []string{"/api/*"}},
//	}
//
// If [SLO.BurnRateAlerts] is nil, [DefaultBurnRateAlerts] is used: a fast-burn
// page (1h window, 14.4× burn rate, critical) and a slow-burn ticket (6h
// window, 6× burn rate, warning) as recommended by the Google SRE workbook.
type SLO struct {
	Name           string
	Target         float64       // 0 < Target < 1, e.g. 0.999
	Window         time.Duration // compliance window
	Indicator      SLI
	BurnRateAlerts []BurnRateAlert // nil → DefaultBurnRateAlerts()
}

// SLI is the per-event "good?" verdict an SLO uses to compute compliance.
// Implementations are sealed (private isSLI marker) so the evaluator can
// switch over the concrete types safely.
type SLI interface {
	// isSLI prevents external implementations.
	isSLI()
	// describes returns a short string for diagnostics + the dashboard.
	describe() string
	// classify returns (good, total) increments for a single request.
	// A request that doesn't belong to this SLI (e.g. wrong route) returns
	// (0, 0).
	classify(m RequestMetric) (good, total int64)
}

// SLIErrorRate counts non-5xx responses on matched routes as "good". 4xx
// responses count as good too: client errors don't burn server SLOs.
//
// Routes are matched with [filepath.Match]-style globs (the same rules
// Tracing.ExcludePaths uses). An empty Routes list matches every request.
type SLIErrorRate struct {
	Routes []string
}

func (SLIErrorRate) isSLI() {}
func (s SLIErrorRate) describe() string {
	if len(s.Routes) == 0 {
		return "error_rate(*)"
	}
	return "error_rate(" + strings.Join(s.Routes, ",") + ")"
}
func (s SLIErrorRate) classify(m RequestMetric) (int64, int64) {
	if !routeMatches(m.Path, s.Routes) {
		return 0, 0
	}
	if m.StatusCode >= 500 {
		return 0, 1
	}
	return 1, 1
}

// SLILatency counts responses below Threshold on matched routes as "good".
// Errors (5xx) count as bad regardless of latency: a fast crash is still a
// crash.
type SLILatency struct {
	Routes    []string
	Threshold time.Duration
}

func (SLILatency) isSLI() {}
func (s SLILatency) describe() string {
	if len(s.Routes) == 0 {
		return fmt.Sprintf("latency(*<%s)", s.Threshold)
	}
	return fmt.Sprintf("latency(%s<%s)", strings.Join(s.Routes, ","), s.Threshold)
}
func (s SLILatency) classify(m RequestMetric) (int64, int64) {
	if !routeMatches(m.Path, s.Routes) {
		return 0, 0
	}
	if m.StatusCode >= 500 {
		return 0, 1
	}
	if m.Latency <= s.Threshold {
		return 1, 1
	}
	return 0, 1
}

// BurnRateAlert is a single burn-rate alert rule attached to an SLO. It
// fires when the observed error rate over Window divided by the SLO's
// target error rate (= 1 - SLO.Target) exceeds BurnRateMultiple.
//
// Picking multiples: Google's SRE workbook expresses these as percent-of-
// budget burned in a window. 2% of monthly budget burned in 1h ≈ 14.4× burn
// rate; 5% in 6h ≈ 6×. See https://sre.google/workbook/alerting-on-slos/.
type BurnRateAlert struct {
	Name             string        // e.g. "fast-burn"
	Window           time.Duration // e.g. 1 * time.Hour
	BurnRateMultiple float64       // e.g. 14.4
	Severity         string        // "critical" | "warning" | "info"
}

// DefaultBurnRateAlerts returns the multi-window burn-rate alert set
// recommended by the Google SRE workbook: a fast-burn page (1h, 14.4×) and
// a slow-burn ticket (6h, 6×).
func DefaultBurnRateAlerts() []BurnRateAlert {
	return []BurnRateAlert{
		{Name: "fast-burn", Window: 1 * time.Hour, BurnRateMultiple: 14.4, Severity: "critical"},
		{Name: "slow-burn", Window: 6 * time.Hour, BurnRateMultiple: 6.0, Severity: "warning"},
	}
}

// SLOStatus is the public snapshot of an SLO's current state, returned by
// /pulse/api/slos and used by the dashboard.
type SLOStatus struct {
	Name               string             `json:"name"`
	Target             float64            `json:"target"`
	Window             string             `json:"window"`
	Indicator          string             `json:"indicator"`
	TotalEvents        int64              `json:"total_events"`
	GoodEvents         int64              `json:"good_events"`
	Compliance         float64            `json:"compliance"`
	BudgetConsumedPct  float64            `json:"budget_consumed_pct"`
	BudgetRemainingPct float64            `json:"budget_remaining_pct"`
	BurnWindows        []BurnWindowStatus `json:"burn_windows"`
	Status             string             `json:"status"` // ok | fast-burn | slow-burn | exhausted
	Timestamp          time.Time          `json:"timestamp"`
}

// BurnWindowStatus is one row in [SLOStatus.BurnWindows] — the live state of
// a single burn-rate rule.
type BurnWindowStatus struct {
	Name       string  `json:"name"`
	Window     string  `json:"window"`
	Compliance float64 `json:"compliance"`
	BurnRate   float64 `json:"burn_rate"`
	Threshold  float64 `json:"threshold"`
	Severity   string  `json:"severity"`
	Firing     bool    `json:"firing"`
}

// sloEvaluator periodically evaluates configured SLOs against stored requests
// and synthesizes burn-rate alerts through the existing alert pipeline.
type sloEvaluator struct {
	pulse *Pulse
	slos  []SLO

	mu     sync.RWMutex
	status map[string]SLOStatus           // snapshot for /pulse/api/slos
	firing map[string]map[string]string   // sloName → burnAlertName → alertID
}

func newSLOEvaluator(p *Pulse, slos []SLO) *sloEvaluator {
	// Backfill defaults so every SLO has at least one burn-rate alert.
	prepared := make([]SLO, 0, len(slos))
	for _, s := range slos {
		if s.BurnRateAlerts == nil {
			s.BurnRateAlerts = DefaultBurnRateAlerts()
		}
		prepared = append(prepared, s)
	}

	ev := &sloEvaluator{
		pulse:  p,
		slos:   prepared,
		status: make(map[string]SLOStatus, len(prepared)),
		firing: make(map[string]map[string]string, len(prepared)),
	}

	interval := 30 * time.Second
	if p.config.DevMode {
		interval = 10 * time.Second
	}

	p.startBackground("slo-evaluator", func(ctx context.Context) {
		// One immediate tick after a short warm-up — gives the request ring
		// buffer something to chew on.
		warmup := 5 * time.Second
		if p.config.DevMode {
			warmup = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(warmup):
		}
		ev.evaluate()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ev.evaluate()
			}
		}
	})

	return ev
}

// evaluate scans the request store for each SLO, computes compliance over
// the SLO window and each burn-rate sub-window, and fires/resolves burn-rate
// alerts as needed.
func (ev *sloEvaluator) evaluate() {
	now := time.Now()

	// Determine the widest window we need to scan once, then re-filter
	// per-SLO in memory. Avoids hitting storage N times per tick.
	var widest time.Duration
	for _, s := range ev.slos {
		if s.Window > widest {
			widest = s.Window
		}
		for _, ba := range s.BurnRateAlerts {
			if ba.Window > widest {
				widest = ba.Window
			}
		}
	}
	if widest == 0 {
		return
	}

	tr := TimeRange{Start: now.Add(-widest), End: now.Add(time.Second)}
	requests, err := ev.pulse.storage.GetRequests(RequestFilter{TimeRange: tr})
	if err != nil {
		if ev.pulse.config.DevMode {
			ev.pulse.logger.Printf("[pulse] slo: failed to load requests: %v", err)
		}
		return
	}

	for _, slo := range ev.slos {
		ev.evaluateSLO(slo, requests, now)
	}
}

// evaluateSLO computes the long-window compliance + all burn-rate sub-windows
// for a single SLO, updates the dashboard snapshot, and fires/resolves
// burn-rate alerts.
func (ev *sloEvaluator) evaluateSLO(slo SLO, requests []RequestMetric, now time.Time) {
	if slo.Target <= 0 || slo.Target >= 1 || slo.Window <= 0 {
		return
	}
	budgetErrorRate := 1 - slo.Target

	// Long window compliance.
	good, total := countWindow(slo.Indicator, requests, now, slo.Window)
	compliance := 1.0
	if total > 0 {
		compliance = float64(good) / float64(total)
	}
	consumed := budgetConsumed(compliance, slo.Target)

	// Burn-rate sub-windows.
	worstStatus := "ok"
	worstSeverity := 0
	windows := make([]BurnWindowStatus, 0, len(slo.BurnRateAlerts))
	for _, ba := range slo.BurnRateAlerts {
		gw, tw := countWindow(slo.Indicator, requests, now, ba.Window)
		var burnRate float64
		var wCompliance float64 = 1.0
		if tw > 0 {
			wCompliance = float64(gw) / float64(tw)
			observedErrRate := 1 - wCompliance
			if budgetErrorRate > 0 {
				burnRate = observedErrRate / budgetErrorRate
			}
		}
		firing := burnRate > ba.BurnRateMultiple && tw > 0
		windows = append(windows, BurnWindowStatus{
			Name:       ba.Name,
			Window:     ba.Window.String(),
			Compliance: wCompliance,
			BurnRate:   burnRate,
			Threshold:  ba.BurnRateMultiple,
			Severity:   ba.Severity,
			Firing:     firing,
		})
		if firing {
			rank := severityRank(ba.Severity)
			if rank > worstSeverity {
				worstSeverity = rank
				worstStatus = ba.Name
			}
		}
		ev.reconcileAlert(slo, ba, burnRate, wCompliance, firing, now)
	}

	if consumed >= 1 {
		worstStatus = "exhausted"
	}

	snap := SLOStatus{
		Name:               slo.Name,
		Target:             slo.Target,
		Window:             slo.Window.String(),
		Indicator:          slo.Indicator.describe(),
		TotalEvents:        total,
		GoodEvents:         good,
		Compliance:         compliance,
		BudgetConsumedPct:  consumed * 100,
		BudgetRemainingPct: (1 - consumed) * 100,
		BurnWindows:        windows,
		Status:             worstStatus,
		Timestamp:          now,
	}

	ev.mu.Lock()
	ev.status[slo.Name] = snap
	ev.mu.Unlock()
}

// reconcileAlert is the firing/resolving state machine for a single burn-rate
// rule, using the existing AlertRecord pipeline + notification fan-out.
func (ev *sloEvaluator) reconcileAlert(
	slo SLO, ba BurnRateAlert, burnRate, compliance float64, firing bool, now time.Time,
) {
	ruleName := "slo:" + slo.Name + ":" + ba.Name

	ev.mu.Lock()
	if ev.firing[slo.Name] == nil {
		ev.firing[slo.Name] = make(map[string]string)
	}
	prevAlertID, wasFiring := ev.firing[slo.Name][ba.Name]
	ev.mu.Unlock()

	switch {
	case firing && !wasFiring:
		// Transition OK → firing.
		id := GenerateTraceID()
		alert := AlertRecord{
			ID:        id,
			RuleName:  ruleName,
			Metric:    "burn_rate",
			Value:     burnRate,
			Threshold: ba.BurnRateMultiple,
			Operator:  ">",
			Severity:  ba.Severity,
			State:     AlertStateFiring,
			Message: fmt.Sprintf(
				"SLO %q burn-rate %s firing: %.1f× target over %s (compliance %.2f%%; target %.2f%%)",
				slo.Name, ba.Name, burnRate, ba.Window, compliance*100, slo.Target*100,
			),
			FiredAt: now,
		}
		ev.storeAndNotify(alert)
		ev.mu.Lock()
		ev.firing[slo.Name][ba.Name] = id
		ev.mu.Unlock()

	case !firing && wasFiring:
		// Transition firing → resolved.
		resolved := now
		alert := AlertRecord{
			ID:        GenerateTraceID(),
			RuleName:  ruleName,
			Metric:    "burn_rate",
			Value:     burnRate,
			Threshold: ba.BurnRateMultiple,
			Operator:  ">",
			Severity:  ba.Severity,
			State:     AlertStateResolved,
			Message: fmt.Sprintf(
				"SLO %q burn-rate %s resolved: %.1f× target over %s",
				slo.Name, ba.Name, burnRate, ba.Window,
			),
			FiredAt:    now,
			ResolvedAt: &resolved,
		}
		// We preserve the original firing time so the resolved record links
		// to the same incident.
		alert.ID = prevAlertID
		ev.storeAndNotify(alert)
		ev.mu.Lock()
		delete(ev.firing[slo.Name], ba.Name)
		ev.mu.Unlock()
	}
}

// storeAndNotify persists an alert, broadcasts it over the WebSocket, and
// dispatches notifications through the existing AlertEngine channels.
func (ev *sloEvaluator) storeAndNotify(alert AlertRecord) {
	if err := ev.pulse.storage.StoreAlert(alert); err != nil && ev.pulse.config.DevMode {
		ev.pulse.logger.Printf("[pulse] slo: failed to store alert: %v", err)
	}
	ev.pulse.BroadcastAlert(alert)
	if ev.pulse.alertEngine != nil {
		go ev.pulse.alertEngine.sendNotifications(alert)
	}
	if ev.pulse.config.DevMode {
		ev.pulse.logger.Printf("[pulse] slo: %s — %s", alert.State, alert.Message)
	}
}

// Snapshot returns the current SLO status, in the same order configuration
// listed them.
func (ev *sloEvaluator) Snapshot() []SLOStatus {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	out := make([]SLOStatus, 0, len(ev.slos))
	for _, s := range ev.slos {
		if v, ok := ev.status[s.Name]; ok {
			out = append(out, v)
		}
	}
	return out
}

// --- helpers ---

// countWindow walks the request slice and accumulates good/total per the SLI
// for events within the last window from now.
func countWindow(sli SLI, requests []RequestMetric, now time.Time, window time.Duration) (good, total int64) {
	cutoff := now.Add(-window)
	for _, r := range requests {
		if r.Timestamp.Before(cutoff) {
			continue
		}
		g, t := sli.classify(r)
		good += g
		total += t
	}
	return good, total
}

// budgetConsumed returns the fraction of the SLO error budget that has
// been used. Clamped to [0, ∞) — values above 1 indicate the budget is
// already exhausted.
func budgetConsumed(compliance, target float64) float64 {
	budgetErr := 1 - target
	if budgetErr <= 0 {
		return 0
	}
	observed := 1 - compliance
	if observed < 0 {
		observed = 0
	}
	return observed / budgetErr
}

// routeMatches reports whether path matches any of the given globs. An empty
// glob list matches everything.
func routeMatches(path string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if g == path {
			return true
		}
		if matched, err := filepath.Match(g, path); err == nil && matched {
			return true
		}
		// Common shorthand: "/api/*" should also match "/api/foo/bar". The
		// stdlib Match treats "*" as path-segment-bounded, so we add a prefix
		// fallback.
		if strings.HasSuffix(g, "/*") {
			prefix := strings.TrimSuffix(g, "/*")
			if strings.HasPrefix(path, prefix+"/") || path == prefix {
				return true
			}
		}
	}
	return false
}

func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}
