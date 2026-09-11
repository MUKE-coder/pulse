package pulse

import (
	"math"
	"net/http"
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
	return cmp
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
				c.JSON(http.StatusOK, compareTestRun(p.rollups, run, now))
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "test run not found"})
	}
}
