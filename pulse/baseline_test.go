package pulse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBaselines_SummariseCompletedHoursOnly(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	b := newBaselines(start)

	for i := 0; i < 10; i++ {
		b.observe("GET", "/users/:id", 200, 100*time.Millisecond, start)
	}
	b.observe("GET", "/users/:id", 500, 100*time.Millisecond, start)

	if got := b.takeCompleted(start.Add(5 * time.Minute)); got != nil {
		t.Fatalf("the hour is still open, so nothing should flush: %+v", got)
	}
	if live := b.current(); len(live) != 1 || live[0].Count != 11 || live[0].Errors != 1 {
		t.Fatalf("open hour reads back wrong: %+v", live)
	}

	stats := b.takeCompleted(start.Add(time.Hour))
	if len(stats) != 1 {
		t.Fatalf("got %d summaries, want 1", len(stats))
	}
	if stats[0].Hour != hourOf(start) || stats[0].Count != 11 || stats[0].Errors != 1 {
		t.Errorf("summary wrong: %+v", stats[0])
	}
	if stats[0].P95 <= 0 {
		t.Errorf("want a latency quantile, got %v", stats[0].P95)
	}
	if live := b.current(); len(live) != 0 {
		t.Errorf("the new hour should start empty, got %+v", live)
	}

	// Requests timestamped outside the open hour are dropped rather than
	// counted against the wrong hour.
	b.observe("GET", "/users/:id", 200, time.Millisecond, start)
	if live := b.current(); len(live) != 0 {
		t.Errorf("a request from a summarised hour should be dropped, got %+v", live)
	}
}

func TestBaselines_BoundRoutesPerHour(t *testing.T) {
	now := time.Now()
	b := newBaselines(now)
	for i := 0; i < maxBaselineRoutes+50; i++ {
		b.observe("GET", "/scan/"+string(rune('a'+i%26))+time.Duration(i).String(), 404, time.Millisecond, now)
	}
	if got := len(b.current()); got != maxBaselineRoutes {
		t.Errorf("tracked %d routes, want it capped at %d", got, maxBaselineRoutes)
	}
}

// A route whose p95 doubles against a week of hours is a regression; one
// that is merely busy is not.
func TestCompareToBaselines_FlagsASlowdownAgainstHistory(t *testing.T) {
	cfg := applyDefaults(Config{}).Baselines
	now := time.Date(2026, 3, 8, 12, 15, 0, 0, time.UTC)

	var past []routeHourStat
	for i := 1; i <= 24; i++ {
		hour := hourOf(now.Add(-time.Duration(i) * time.Hour))
		past = append(past,
			routeHourStat{Hour: hour, Method: "GET", Route: "/checkout", Count: 500, P95: 100 * time.Millisecond},
			routeHourStat{Hour: hour, Method: "GET", Route: "/health", Count: 500, P95: 5 * time.Millisecond},
		)
	}
	// One pathological hour must not become the yardstick: the baseline is a
	// median, so this 10s hour is outvoted by the 100ms ones.
	past[0].P95 = 10 * time.Second

	current := []routeHourStat{
		{Hour: hourOf(now), Method: "GET", Route: "/checkout", Count: 120, P95: 300 * time.Millisecond},
		{Hour: hourOf(now), Method: "GET", Route: "/health", Count: 900, P95: 6 * time.Millisecond},
	}

	byRoute := map[string]routeBaseline{}
	for _, b := range compareToBaselines(past, current, cfg, now) {
		byRoute[b.Route] = b
	}
	if len(byRoute) != 2 {
		t.Fatalf("got %d routes, want 2", len(byRoute))
	}

	checkout := byRoute["/checkout"]
	if checkout.Baseline != 100*time.Millisecond {
		t.Errorf("baseline %v, want the median 100ms", checkout.Baseline)
	}
	if !checkout.Regressed || checkout.Ratio < 2.9 || checkout.Ratio > 3.1 {
		t.Errorf("/checkout should read as 3× slower: %+v", checkout)
	}
	if checkout.Hours != 24 {
		t.Errorf("baseline drawn from %d hours, want 24", checkout.Hours)
	}
	if byRoute["/health"].Regressed {
		t.Errorf("/health is 1ms slower, not regressed: %+v", byRoute["/health"])
	}
}

// Without enough history, or enough traffic, a route has no baseline rather
// than a bad one — and nothing is ever flagged off a handful of requests.
func TestCompareToBaselines_WaitsForEnoughEvidence(t *testing.T) {
	cfg := applyDefaults(Config{}).Baselines
	now := time.Date(2026, 3, 8, 12, 15, 0, 0, time.UTC)

	young := []routeHourStat{
		{Hour: hourOf(now.Add(-time.Hour)), Method: "GET", Route: "/new", Count: 500, P95: 50 * time.Millisecond},
		{Hour: hourOf(now.Add(-2 * time.Hour)), Method: "GET", Route: "/new", Count: 500, P95: 50 * time.Millisecond},
	}
	current := []routeHourStat{{Hour: hourOf(now), Method: "GET", Route: "/new", Count: 500, P95: time.Second}}

	got := compareToBaselines(young, current, cfg, now)
	if len(got) != 1 {
		t.Fatalf("got %d routes, want 1", len(got))
	}
	if got[0].Baseline != 0 || got[0].Regressed || got[0].Ratio != 0 {
		t.Errorf("two hours is not a baseline: %+v", got[0])
	}

	// Enough hours, but every one of them too quiet to mean anything.
	var quiet []routeHourStat
	for i := 1; i <= 24; i++ {
		quiet = append(quiet, routeHourStat{Hour: hourOf(now.Add(-time.Duration(i) * time.Hour)),
			Method: "GET", Route: "/new", Count: 2, P95: 50 * time.Millisecond})
	}
	if got = compareToBaselines(quiet, current, cfg, now); got[0].Regressed {
		t.Errorf("hours of two requests are not evidence: %+v", got[0])
	}

	// Enough history, but the hour in progress has barely started.
	var history []routeHourStat
	for i := 1; i <= 24; i++ {
		history = append(history, routeHourStat{Hour: hourOf(now.Add(-time.Duration(i) * time.Hour)),
			Method: "GET", Route: "/new", Count: 500, P95: 50 * time.Millisecond})
	}
	thin := []routeHourStat{{Hour: hourOf(now), Method: "GET", Route: "/new", Count: 3, P95: time.Second}}
	if got = compareToBaselines(history, thin, cfg, now); got[0].Regressed {
		t.Errorf("three slow requests are not a regression: %+v", got[0])
	}
	// Hours older than the window don't count towards it either.
	stale := []routeHourStat{}
	for i := 1; i <= 24; i++ {
		stale = append(stale, routeHourStat{Hour: hourOf(now.Add(-baselineWindow(cfg) - time.Duration(i)*time.Hour)),
			Method: "GET", Route: "/new", Count: 500, P95: 50 * time.Millisecond})
	}
	if got = compareToBaselines(stale, current, cfg, now); got[0].Baseline != 0 {
		t.Errorf("hours outside the window should be ignored: %+v", got[0])
	}
}

// routeHourHistory is a day of steady hours for one route, ending before now.
func routeHourHistory(now time.Time, method, route string, p95 time.Duration) []routeHourStat {
	stats := make([]routeHourStat, 0, 24)
	for i := 1; i <= 24; i++ {
		stats = append(stats, routeHourStat{
			Hour: hourOf(now.Add(-time.Duration(i) * time.Hour)), Method: method, Route: route,
			Count: 500, Errors: 1, P50: p95 / 2, P95: p95, P99: p95 * 2,
		})
	}
	return stats
}

// Summaries must survive a round trip, come back from every instance, and
// prune by hour — they outlive the request records they came from.
func TestConformance_RouteHoursRoundTripAndPrune(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		store, ok := s.(baselineStore)
		if !ok {
			t.Fatalf("%T keeps no route hours", s)
		}
		now := time.Now()
		hour := hourOf(now)

		if err := store.saveRouteHours("web-1", []routeHourStat{
			{Hour: hour - 1, Method: "GET", Route: "/checkout", Count: 400, Errors: 2,
				P50: 20 * time.Millisecond, P95: 90 * time.Millisecond, P99: 300 * time.Millisecond},
			{Hour: hour - 100, Method: "GET", Route: "/checkout", Count: 10, P95: time.Second},
		}); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := store.saveRouteHours("web-2", []routeHourStat{
			{Hour: hour - 1, Method: "GET", Route: "/checkout", Count: 100, P95: 50 * time.Millisecond},
		}); err != nil {
			t.Fatalf("save from a second instance: %v", err)
		}

		got, err := store.routeHoursSince(hour - 24)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d summaries in the window, want both instances' and not the old one: %+v", len(got), got)
		}
		var total int64
		for _, s := range got {
			if s.Hour != hour-1 || s.Method != "GET" || s.Route != "/checkout" {
				t.Errorf("summary came back wrong: %+v", s)
			}
			total += s.Count
		}
		if total != 500 {
			t.Errorf("counts total %d, want 500 across the two instances", total)
		}

		// Saving the same hour again replaces that instance's summary rather
		// than adding to it, so a retried flush can't double-count.
		if err := store.saveRouteHours("web-2", []routeHourStat{
			{Hour: hour - 1, Method: "GET", Route: "/checkout", Count: 150, P95: 60 * time.Millisecond},
		}); err != nil {
			t.Fatalf("re-save: %v", err)
		}
		if got, _ = store.routeHoursSince(hour - 24); len(got) != 2 {
			t.Errorf("a re-flushed hour should replace, not accumulate: %+v", got)
		}

		if err := store.pruneRouteHours(hour - 24); err != nil {
			t.Fatalf("prune: %v", err)
		}
		if got, _ = store.routeHoursSince(0); len(got) != 2 {
			t.Errorf("prune should drop only the hours before the cutoff: %+v", got)
		}
	})
}

func activeAlerts(t *testing.T, p *Pulse, rule string) []AlertRecord {
	t.Helper()
	all, err := p.storage.GetAlerts(AlertFilter{})
	if err != nil {
		t.Fatalf("alerts: %v", err)
	}
	var out []AlertRecord
	for _, a := range all {
		if a.RuleName == rule && a.State == AlertStateFiring {
			out = append(out, a)
		}
	}
	return out
}

// One slow sample is noise. A route that stays slow for the configured
// window pages once, and closes itself when it recovers.
func TestRegressionWatcher_PagesOnlyOnASustainedSlowdown(t *testing.T) {
	p := newTestPulse(t)
	now := time.Date(2026, 3, 8, 12, 15, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	store := p.storage.(baselineStore)
	if err := store.saveRouteHours("test", routeHourHistory(now, "GET", "/checkout", 100*time.Millisecond)); err != nil {
		t.Fatalf("save history: %v", err)
	}
	slowHour := func(at time.Time, latency time.Duration) {
		p.baselines = newBaselines(at)
		for i := 0; i < 200; i++ {
			p.baselines.observe("GET", "/checkout", 200, latency, at)
		}
	}
	slowHour(now, 400*time.Millisecond)

	w := newRegressionWatcher(p)
	w.evaluate()
	if got := activeAlerts(t, p, "route_regression"); len(got) != 0 {
		t.Fatalf("the first slow sample should not page: %+v", got)
	}

	now = now.Add(baselineFor(p.config.Baselines) + time.Minute)
	w.evaluate()
	firing := activeAlerts(t, p, "route_regression")
	if len(firing) != 1 {
		t.Fatalf("got %d alerts after a sustained slowdown, want 1: %+v", len(firing), firing)
	}
	if firing[0].Route != "GET /checkout" || firing[0].Value < 3.9 || firing[0].Value > 4.1 {
		t.Errorf("alert should name the route and how much slower: %+v", firing[0])
	}
	for _, want := range []string{"4.0× slower", "100ms baseline", "24 hours"} {
		if !strings.Contains(firing[0].Message, want) {
			t.Errorf("message %q should carry %q", firing[0].Message, want)
		}
	}

	now = now.Add(10 * time.Minute)
	w.evaluate()
	if got := activeAlerts(t, p, "route_regression"); len(got) != 1 || got[0].ID != firing[0].ID {
		t.Errorf("a continuing regression should not open a second alert: %+v", got)
	}

	// Recovered: the open alert closes in place, with its original ID.
	now = now.Add(time.Hour)
	slowHour(now, 100*time.Millisecond)
	w.evaluate()
	if got := activeAlerts(t, p, "route_regression"); len(got) != 0 {
		t.Fatalf("the alert should have resolved: %+v", got)
	}
	all, _ := p.storage.GetAlerts(AlertFilter{})
	var resolved int
	for _, a := range all {
		if a.RuleName == "route_regression" && a.State == AlertStateResolved {
			resolved++
			if a.ID != firing[0].ID || a.ResolvedAt == nil {
				t.Errorf("resolution should close the same record: %+v", a)
			}
		}
	}
	if resolved != 1 {
		t.Errorf("got %d resolved records, want 1", resolved)
	}
}

func TestAPI_RouteBaselines(t *testing.T) {
	p, router := setupAPIPulse(t)
	now := time.Date(2026, 3, 8, 12, 15, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	if err := p.storage.(baselineStore).saveRouteHours("test",
		routeHourHistory(now, "GET", "/checkout", 100*time.Millisecond)); err != nil {
		t.Fatalf("save history: %v", err)
	}
	p.baselines = newBaselines(now)
	for i := 0; i < 200; i++ {
		p.baselines.observe("GET", "/checkout", 200, 250*time.Millisecond, now)
	}

	token := loginAndGetToken(t, router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, authedRequest("GET", "/pulse/api/routes/baselines", token, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Baselines []routeBaseline `json:"baselines"`
		Enabled   bool            `json:"enabled"`
		Supported bool            `json:"supported"`
		Window    string          `json:"window"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled || !resp.Supported || resp.Window != baselineWindow(p.config.Baselines).String() {
		t.Errorf("unexpected envelope: %+v", resp)
	}
	if len(resp.Baselines) != 1 {
		t.Fatalf("got %d baselines, want 1: %+v", len(resp.Baselines), resp.Baselines)
	}
	got := resp.Baselines[0]
	if got.Route != "/checkout" || got.Baseline != 100*time.Millisecond || !got.Regressed {
		t.Errorf("baseline comparison wrong: %+v", got)
	}
}
