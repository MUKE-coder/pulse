package pulse

import (
	"testing"
	"time"
)

// Behaviour every Storage backend must share. Each test runs against both
// MemoryStorage and SQLiteStorage so the backends can't drift apart again —
// v1.0.0 shipped with test-run and alert-resolution semantics that differed
// between them.

func forEachBackend(t *testing.T, fn func(t *testing.T, s Storage)) {
	backends := []struct {
		name string
		open func(t *testing.T) Storage
	}{
		{"memory", func(t *testing.T) Storage { return NewMemoryStorage("conformance") }},
		{"sqlite", func(t *testing.T) Storage { return newSQLiteForTest(t) }},
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) { fn(t, b.open(t)) })
	}
}

func wideRange() TimeRange {
	return TimeRange{Start: time.Now().Add(-24 * time.Hour), End: time.Now().Add(time.Hour)}
}

// A load-test harness posts a run at start and again at end with the same ID;
// the result must be one completed run.
func TestConformance_TestRunUpsertsByID(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		start := time.Now().Add(-10 * time.Minute)
		end := time.Now().Add(-time.Minute)

		if err := s.StoreTestRun(TestRun{ID: "run-1", Name: "spike", StartedAt: start,
			Metadata: map[string]interface{}{"vus_peak": 50.0}}); err != nil {
			t.Fatalf("store start: %v", err)
		}
		if err := s.StoreTestRun(TestRun{ID: "run-1", Name: "spike", StartedAt: start, EndedAt: end,
			Metadata: map[string]interface{}{"vus_peak": 50.0, "thresholds_passed": true}}); err != nil {
			t.Fatalf("store end: %v", err)
		}

		runs, _ := s.GetTestRuns(wideRange())
		if len(runs) != 1 {
			t.Fatalf("got %d runs, want 1", len(runs))
		}
		if !runs[0].EndedAt.Equal(end) || runs[0].Metadata["thresholds_passed"] != true {
			t.Errorf("run not updated by the end record: %+v", runs[0])
		}
	})
}

// Resolving an alert re-stores its firing record with the same ID; the
// result must be one resolved record, and it must no longer count as active.
func TestConformance_AlertResolvesInPlace(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		firedAt := time.Now().Add(-5 * time.Minute)
		firing := AlertRecord{ID: "a-1", RuleName: "high_latency", Metric: "p95_latency",
			Value: 3000, Threshold: 2000, Operator: ">", Severity: "warning",
			State: AlertStateFiring, Message: "latency high", FiredAt: firedAt}
		if err := s.StoreAlert(firing); err != nil {
			t.Fatalf("store firing: %v", err)
		}

		resolvedAt := time.Now()
		resolved := firing
		resolved.State = AlertStateResolved
		resolved.ResolvedAt = &resolvedAt
		if err := s.StoreAlert(resolved); err != nil {
			t.Fatalf("store resolved: %v", err)
		}

		all, _ := s.GetAlerts(AlertFilter{TimeRange: wideRange()})
		if len(all) != 1 {
			t.Fatalf("got %d alert records, want 1", len(all))
		}
		a := all[0]
		if a.State != AlertStateResolved || a.ResolvedAt == nil || !a.FiredAt.Equal(firedAt) {
			t.Errorf("record not resolved in place: %+v", a)
		}

		stillFiring, _ := s.GetAlerts(AlertFilter{TimeRange: wideRange(), State: AlertStateFiring})
		if len(stillFiring) != 0 {
			t.Errorf("got %d firing alerts after resolution, want 0", len(stillFiring))
		}
		if ov, err := s.GetOverview(Last1h()); err != nil || ov.ActiveAlerts != 0 {
			t.Errorf("overview ActiveAlerts = %v (err %v), want 0", ov.ActiveAlerts, err)
		}
	})
}

// Errors dedupe by fingerprint: the count grows, the first ID stays valid,
// and the latest request context wins.
func TestConformance_ErrorsDedupeByFingerprint(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		first := buildErrorRecord("POST", "/orders", "boom", ErrorTypeInternal, "",
			&RequestContext{Method: "POST", Path: "/orders/1"}, "trace-1")
		second := buildErrorRecord("POST", "/orders", "boom", ErrorTypeInternal, "",
			&RequestContext{Method: "POST", Path: "/orders/2"}, "trace-2")
		_ = s.StoreError(first)
		_ = s.StoreError(second)

		errs, _ := s.GetErrors(ErrorFilter{TimeRange: wideRange()})
		if len(errs) != 1 || errs[0].Count != 2 {
			t.Fatalf("got %d records (count %v), want 1 record with count 2", len(errs), errs)
		}
		got, err := s.GetErrorByID(first.ID)
		if err != nil || got == nil {
			t.Fatalf("GetErrorByID(first ID): %v", err)
		}
		if got.RequestContext == nil || got.RequestContext.Path != "/orders/2" {
			t.Errorf("latest request context not kept: %+v", got.RequestContext)
		}
	})
}

func TestConformance_CleanupDropsExpired(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		old := time.Now().Add(-3 * time.Hour)
		fresh := time.Now().Add(-time.Minute)

		oldErr := buildErrorRecord("GET", "/old", "old", ErrorTypeInternal, "", nil, "")
		oldErr.FirstSeen, oldErr.LastSeen = old, old
		newErr := buildErrorRecord("GET", "/new", "new", ErrorTypeInternal, "", nil, "")
		_ = s.StoreError(oldErr)
		_ = s.StoreError(newErr)

		_ = s.StoreAlert(AlertRecord{ID: "old", RuleName: "r", State: AlertStateFiring, FiredAt: old})
		_ = s.StoreAlert(AlertRecord{ID: "new", RuleName: "r", State: AlertStateFiring, FiredAt: fresh})

		_ = s.StoreTestRun(TestRun{ID: "old", Name: "old", StartedAt: old, EndedAt: old.Add(time.Minute)})
		_ = s.StoreTestRun(TestRun{ID: "new", Name: "new", StartedAt: fresh})

		if err := s.Cleanup(time.Hour); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}

		if errs, _ := s.GetErrors(ErrorFilter{}); len(errs) != 1 || errs[0].Route != "/new" {
			t.Errorf("errors after cleanup = %v, want only /new", errs)
		}
		if alerts, _ := s.GetAlerts(AlertFilter{TimeRange: wideRange()}); len(alerts) != 1 || alerts[0].ID != "new" {
			t.Errorf("alerts after cleanup = %v, want only new", alerts)
		}
		if runs, _ := s.GetTestRuns(wideRange()); len(runs) != 1 || runs[0].ID != "new" {
			t.Errorf("test runs after cleanup = %v, want only new", runs)
		}
	})
}
