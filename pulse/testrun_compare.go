package pulse

import (
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// Before / during / after comparison for a load-test run, computed from the
// per-minute request rollups so it counts every request regardless of
// sampling. Resolution is one minute.

// testRunWindow summarizes request traffic over a span of minutes.
type testRunWindow struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Requests  int64     `json:"requests"`
	RPS       float64   `json:"rps"`
	ErrorRate float64   `json:"error_rate"` // % of responses that were 5xx
	P50Ms     float64   `json:"p50_ms"`
	P95Ms     float64   `json:"p95_ms"`
	P99Ms     float64   `json:"p99_ms"`
}

// testRunComparison is the response of GET /pulse/api/test-runs/:id/compare.
type testRunComparison struct {
	Run    TestRun       `json:"run"`
	Before testRunWindow `json:"before"` // the same length of time before the run
	During testRunWindow `json:"during"`
	After  testRunWindow `json:"after"` // the same length after, up to now

	// Degraded is set when p95 during the run exceeded the before-run p95 by
	// more than 10%.
	Degraded bool `json:"degraded"`
	// RecoverySeconds is how long after the run ended per-minute p95 took to
	// return within 10% of the before-run p95 and stay there for three
	// minutes. Nil until that happens, or when there is no before-run
	// traffic to compare with.
	RecoverySeconds *float64 `json:"recovery_seconds"`
	Resolution      string   `json:"resolution"`

	// Routes breaks the same windows down per route. A load test usually
	// hammers two or three endpoints, and a global average hides the one
	// that fell over (see issue #11).
	Routes []testRunRoute `json:"routes,omitempty"`
	// Database is what the run did to the database: queries per request
	// and N+1 findings, before against during. An N+1 that is invisible at
	// one request a second is exactly what falls over at 500 VUs.
	Database *testRunDatabase `json:"database,omitempty"`
	// Profile describes the CPU profile captured over the run, if any.
	Profile *testRunProfileInfo `json:"profile,omitempty"`
}

// testRunRouteWindow is one route's traffic over one window. Latency is an
// average rather than a percentile: per-route percentiles are not kept per
// minute, and a mislabelled number is worse than an honest one.
type testRunRouteWindow struct {
	Requests  int64   `json:"requests"`
	ErrorRate float64 `json:"error_rate"`
	AvgMs     float64 `json:"avg_ms"`
}

type testRunRoute struct {
	Method string             `json:"method"`
	Route  string             `json:"route"`
	Before testRunRouteWindow `json:"before"`
	During testRunRouteWindow `json:"during"`
	After  testRunRouteWindow `json:"after"`
	// SlowdownRatio is during ÷ before average latency; 0 when the route
	// saw no traffic before the run.
	SlowdownRatio float64 `json:"slowdown_ratio"`
}

type testRunDatabase struct {
	QueriesBefore         int64   `json:"queries_before"`
	QueriesDuring         int64   `json:"queries_during"`
	QueriesPerReqBefore   float64 `json:"queries_per_request_before"`
	QueriesPerReqDuring   float64 `json:"queries_per_request_during"`
	N1DetectionsBefore    int     `json:"n1_detections_before"`
	N1DetectionsDuring    int     `json:"n1_detections_during"`
	SlowQueriesDuring     int     `json:"slow_queries_during"`
	QueriesPerReqIncrease bool    `json:"queries_per_request_increased"`
}

type testRunProfileInfo struct {
	Available  bool      `json:"available"`
	InFlight   bool      `json:"in_flight"`
	CapturedAt time.Time `json:"captured_at,omitempty"`
	Bytes      int       `json:"bytes,omitempty"`
}

// recoveryTolerance and recoveryStreak define "recovered": p95 within 10% of
// the baseline for three consecutive minutes with traffic.
const (
	recoveryTolerance = 0.10
	recoveryStreak    = 3
)

// compareTestRun builds the comparison for run as of now. An in-flight run
// is treated as ending now.
func compareTestRun(r *rollups, run TestRun, now time.Time) testRunComparison {
	end := run.EndedAt
	if end.IsZero() || end.After(now) {
		end = now
	}
	first := minuteOf(run.StartedAt)
	last := first
	if end.After(run.StartedAt) {
		last = minuteOf(end.Add(-time.Nanosecond)) // an end on a minute boundary excludes that minute
	}
	span := last - first + 1
	afterLast := last + span
	if nowMinute := minuteOf(now); afterLast > nowMinute {
		afterLast = nowMinute
	}

	cmp := testRunComparison{
		Run:        run,
		Before:     r.window(first-span, first-1),
		During:     r.window(first, last),
		After:      r.window(last+1, afterLast),
		Resolution: "1m",
	}

	// Compare histogram bucket ranges rather than midpoints, so bucket width
	// can't make a minute read as 10% off the baseline when it isn't: the
	// limit is the top of the baseline p95's bucket plus the tolerance.
	beforeHist := r.summaryMinutes(first-span, first-1).hist
	if _, baseHi, ok := beforeHist.quantileBounds(0.95); ok {
		limit := time.Duration(float64(baseHi) * (1 + recoveryTolerance))
		duringHist := r.summaryMinutes(first, last).hist
		if lo, _, ok := duringHist.quantileBounds(0.95); ok {
			cmp.Degraded = lo > limit
		}
		if !run.EndedAt.IsZero() {
			cmp.RecoverySeconds = r.recoveryAfter(limit, last+1, minuteOf(now), end)
		}
	}

	cmp.Routes = r.routeWindows(first, last, span, afterLast)
	return cmp
}

// maxComparedRoutes keeps the table to the routes a load test actually
// touched, busiest first.
const maxComparedRoutes = 20

// routeWindows breaks the three windows down per route, ordered by the
// traffic each took during the run.
func (r *rollups) routeWindows(first, last, span, afterLast int64) []testRunRoute {
	at := func(from, to int64) map[rollupRouteKey]routeCounts {
		if to < from {
			return map[rollupRouteKey]routeCounts{}
		}
		return r.routeTotals(time.Unix(from*60, 0), time.Unix((to+1)*60, 0).Add(-time.Nanosecond))
	}
	before, during, after := at(first-span, first-1), at(first, last), at(last+1, afterLast)

	out := make([]testRunRoute, 0, len(during))
	for key, counts := range during {
		row := testRunRoute{
			Method: key.method, Route: key.route,
			Before: routeWindowOf(before[key]),
			During: routeWindowOf(counts),
			After:  routeWindowOf(after[key]),
		}
		if row.Before.AvgMs > 0 && row.During.AvgMs > 0 {
			row.SlowdownRatio = row.During.AvgMs / row.Before.AvgMs
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].During.Requests > out[j].During.Requests })
	if len(out) > maxComparedRoutes {
		out = out[:maxComparedRoutes]
	}
	return out
}

func routeWindowOf(c routeCounts) testRunRouteWindow {
	w := testRunRouteWindow{Requests: c.total}
	if c.total > 0 {
		w.ErrorRate = float64(c.status5xx) / float64(c.total) * 100
		w.AvgMs = durationMs(c.latency / time.Duration(c.total))
	}
	return w
}

// window summarizes the minutes [first, last]; an empty span (last < first)
// yields a zero window.
func (r *rollups) window(first, last int64) testRunWindow {
	w := testRunWindow{From: time.Unix(first*60, 0), To: time.Unix((last+1)*60, 0)}
	if last < first {
		w.To = w.From
		return w
	}
	s := r.summaryMinutes(first, last)
	w.Requests = s.counts.total
	w.RPS = float64(s.counts.total) / float64((last-first+1)*60)
	if s.counts.total > 0 {
		w.ErrorRate = float64(s.counts.status5xx) / float64(s.counts.total) * 100
	}
	w.P50Ms = durationMs(s.hist.quantile(0.50))
	w.P95Ms = durationMs(s.hist.quantile(0.95))
	w.P99Ms = durationMs(s.hist.quantile(0.99))
	return w
}

// recoveryAfter returns the seconds from end to the start of the first run
// of recoveryStreak consecutive minutes in [first, last] whose p95 bucket
// starts at or below limit, or nil. A minute without traffic breaks the
// streak.
func (r *rollups) recoveryAfter(limit time.Duration, first, last int64, end time.Time) *float64 {
	streak := 0
	var streakStart int64
	for m := first; m <= last; m++ {
		s := r.summaryMinutes(m, m)
		if lo, _, ok := s.hist.quantileBounds(0.95); !ok || lo > limit {
			streak = 0
			continue
		}
		if streak == 0 {
			streakStart = m
		}
		streak++
		if streak == recoveryStreak {
			secs := math.Max(0, float64(streakStart*60-end.Unix()))
			return &secs
		}
	}
	return nil
}

func durationMs(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// testRunCompareHandler serves GET /pulse/api/test-runs/:id/compare.
func testRunCompareHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := p.now()
		retention := time.Duration(p.config.Storage.RetentionHours) * time.Hour
		runs, err := p.storage.GetTestRuns(TimeRange{Start: now.Add(-retention), End: now.Add(time.Hour)})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		for _, run := range runs {
			if run.ID == c.Param("id") {
				cmp := compareTestRun(p.rollups, run, now)
				addDatabaseDeltas(p, &cmp, run, now)
				addProfileInfo(p, &cmp, run)
				c.JSON(http.StatusOK, cmp)
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "test run not found"})
	}
}

// addDatabaseDeltas answers the question a load test raises about the
// database: did each request start doing more work? An N+1 that is
// invisible at one request a second is what falls over at 500 VUs.
func addDatabaseDeltas(p *Pulse, cmp *testRunComparison, run TestRun, now time.Time) {
	end := run.EndedAt
	if end.IsZero() || end.After(now) {
		end = now
	}
	span := end.Sub(run.StartedAt)
	if span <= 0 {
		return
	}
	beforeRange := TimeRange{Start: run.StartedAt.Add(-span), End: run.StartedAt}
	duringRange := TimeRange{Start: run.StartedAt, End: end}

	db := &testRunDatabase{}
	count := func(tr TimeRange) int64 {
		var total int64
		patterns, err := p.storage.GetQueryPatterns(tr)
		if err != nil {
			return 0
		}
		for _, pat := range patterns {
			total += pat.Count
		}
		return total
	}
	db.QueriesBefore, db.QueriesDuring = count(beforeRange), count(duringRange)
	if n1, err := p.storage.GetN1Detections(beforeRange); err == nil {
		db.N1DetectionsBefore = len(n1)
	}
	if n1, err := p.storage.GetN1Detections(duringRange); err == nil {
		db.N1DetectionsDuring = len(n1)
	}
	if slow, err := p.storage.GetSlowQueries(p.config.Database.SlowQueryThreshold, 0); err == nil {
		for _, q := range slow {
			if !q.Timestamp.Before(duringRange.Start) && !q.Timestamp.After(duringRange.End) {
				db.SlowQueriesDuring++
			}
		}
	}
	if cmp.Before.Requests > 0 {
		db.QueriesPerReqBefore = float64(db.QueriesBefore) / float64(cmp.Before.Requests)
	}
	if cmp.During.Requests > 0 {
		db.QueriesPerReqDuring = float64(db.QueriesDuring) / float64(cmp.During.Requests)
	}
	// A tenth of a query per request is noise; a real N+1 multiplies it.
	db.QueriesPerReqIncrease = db.QueriesPerReqBefore > 0 &&
		db.QueriesPerReqDuring > db.QueriesPerReqBefore*1.1

	if db.QueriesBefore+db.QueriesDuring > 0 || db.N1DetectionsDuring > 0 {
		cmp.Database = db
	}
}

// addProfileInfo says whether the run has a CPU profile to look at.
func addProfileInfo(p *Pulse, cmp *testRunComparison, run TestRun) {
	store, ok := p.storage.(testRunProfileStore)
	if !ok {
		return
	}
	info := &testRunProfileInfo{InFlight: p.runProfiler.profiling() == run.ID}
	if data, capturedAt, err := store.testRunProfile(run.ID); err == nil && len(data) > 0 {
		info.Available, info.CapturedAt, info.Bytes = true, capturedAt, len(data)
	}
	if info.Available || info.InFlight {
		cmp.Profile = info
	}
}
