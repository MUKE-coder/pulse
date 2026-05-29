package pulse

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newSQLiteForTest(t *testing.T) *SQLiteStorage {
	t.Helper()
	s, err := NewSQLiteStorage(":memory:", "test")
	if err != nil {
		t.Fatalf("open SQLiteStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLite_RequestsAndRouteStats(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)
	now := time.Now()

	for _, code := range []int{200, 200, 200, 500, 500} {
		if err := s.StoreRequest(RequestMetric{
			Method: "GET", Path: "/users", StatusCode: code,
			Latency: 10 * time.Millisecond, Timestamp: now,
		}); err != nil {
			t.Fatalf("StoreRequest: %v", err)
		}
	}
	if err := s.StoreRequest(RequestMetric{
		Method: "POST", Path: "/users", StatusCode: 201,
		Latency: 5 * time.Millisecond, Timestamp: now,
	}); err != nil {
		t.Fatalf("StoreRequest POST: %v", err)
	}

	tr := TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)}
	reqs, err := s.GetRequests(RequestFilter{TimeRange: tr})
	if err != nil {
		t.Fatalf("GetRequests: %v", err)
	}
	if len(reqs) != 6 {
		t.Fatalf("got %d requests, want 6", len(reqs))
	}

	stats, err := s.GetRouteStats(tr)
	if err != nil {
		t.Fatalf("GetRouteStats: %v", err)
	}
	// /users is hit 5x, /users via POST hit once → 2 distinct route entries.
	if len(stats) != 2 {
		t.Fatalf("got %d route entries, want 2 (%v)", len(stats), stats)
	}
	for _, st := range stats {
		if st.Method == "GET" && st.Path == "/users" {
			if st.RequestCount != 5 || st.ErrorCount != 2 {
				t.Errorf("GET /users counts (req=%d err=%d), want (5, 2)",
					st.RequestCount, st.ErrorCount)
			}
			if st.ErrorRate < 39 || st.ErrorRate > 41 {
				t.Errorf("GET /users error_rate = %.2f%%, want ~40%%", st.ErrorRate)
			}
		}
	}
}

func TestSQLite_ErrorsDeduplicate(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)
	now := time.Now()
	rec := ErrorRecord{
		Fingerprint:  "abc123",
		ID:           "id-1",
		Method:       "GET",
		Route:        "/x",
		ErrorMessage: "boom",
		ErrorType:    "internal",
		Count:        1,
		FirstSeen:    now,
		LastSeen:     now,
	}
	for i := 0; i < 5; i++ {
		rec.LastSeen = now.Add(time.Duration(i) * time.Second)
		if err := s.StoreError(rec); err != nil {
			t.Fatalf("StoreError #%d: %v", i, err)
		}
	}

	all, err := s.GetErrors(ErrorFilter{TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)}})
	if err != nil {
		t.Fatalf("GetErrors: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 error row after dedup, got %d", len(all))
	}
	// First store inserts with count=1; four subsequent stores each +1 → 5.
	if all[0].Count != 5 {
		t.Errorf("Count = %d, want 5", all[0].Count)
	}

	one, err := s.GetErrorByID("id-1")
	if err != nil {
		t.Fatalf("GetErrorByID: %v", err)
	}
	if one == nil || one.Fingerprint != "abc123" {
		t.Fatalf("GetErrorByID returned %+v", one)
	}

	if err := s.UpdateError("id-1", map[string]interface{}{"muted": true, "resolved": true}); err != nil {
		t.Fatalf("UpdateError: %v", err)
	}
	one, _ = s.GetErrorByID("id-1")
	if one == nil || !one.Muted || !one.Resolved {
		t.Fatalf("after UpdateError: %+v", one)
	}

	if err := s.DeleteError("id-1"); err != nil {
		t.Fatalf("DeleteError: %v", err)
	}
	if _, err := s.GetErrorByID("id-1"); err == nil {
		t.Fatal("expected GetErrorByID to fail after DeleteError")
	}
}

func TestSQLite_TestRunsWindowAndUpsert(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)
	now := time.Now()

	r := TestRun{
		ID:        "run-1",
		Name:      "average-load",
		Type:      "k6.average-load",
		StartedAt: now.Add(-2 * time.Minute),
		Metadata:  map[string]interface{}{"vus_peak": 50.0}, // JSON numbers come back as float64
	}
	if err := s.StoreTestRun(r); err != nil {
		t.Fatalf("StoreTestRun (start): %v", err)
	}

	// Same ID with EndedAt — should upsert, not duplicate.
	r.EndedAt = now
	r.Metadata["thresholds_passed"] = true
	if err := s.StoreTestRun(r); err != nil {
		t.Fatalf("StoreTestRun (end): %v", err)
	}

	runs, err := s.GetTestRuns(TimeRange{Start: now.Add(-time.Hour), End: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("GetTestRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run after upsert, got %d", len(runs))
	}
	if runs[0].EndedAt.IsZero() {
		t.Fatal("EndedAt should round-trip non-zero after upsert")
	}
	if got, _ := runs[0].Metadata["thresholds_passed"].(bool); !got {
		t.Fatalf("metadata roundtrip lost thresholds_passed: %+v", runs[0].Metadata)
	}
}

func TestSQLite_Cleanup(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)
	now := time.Now()

	// Old request — should be dropped.
	if err := s.StoreRequest(RequestMetric{
		Method: "GET", Path: "/old", StatusCode: 200,
		Latency: 1 * time.Millisecond, Timestamp: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Recent request — should survive.
	if err := s.StoreRequest(RequestMetric{
		Method: "GET", Path: "/new", StatusCode: 200,
		Latency: 1 * time.Millisecond, Timestamp: now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Cleanup(time.Hour); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	left, _ := s.GetRequests(RequestFilter{TimeRange: TimeRange{
		Start: now.Add(-24 * time.Hour), End: now.Add(time.Hour),
	}})
	if len(left) != 1 {
		t.Fatalf("after cleanup got %d requests, want 1", len(left))
	}
	if left[0].Path != "/new" {
		t.Fatalf("kept the wrong row: %+v", left[0])
	}
}

func TestSQLite_MountWithSQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := dir + "/pulse-test.db"

	// First Mount writes one request and shuts down.
	{
		router := gin.New()
		p := Mount(
			t.Context(),
			router, nil,
			WithDevMode(),
			WithSQLite(dbPath),
		)
		if _, ok := p.storage.(*SQLiteStorage); !ok {
			t.Fatalf("storage = %T, want *SQLiteStorage", p.storage)
		}
		if err := p.storage.StoreRequest(RequestMetric{
			Method: "GET", Path: "/persisted", StatusCode: 200,
			Latency: 1 * time.Millisecond, Timestamp: time.Now(),
		}); err != nil {
			t.Fatalf("StoreRequest: %v", err)
		}
		if err := p.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}

	// Second Mount on the same file — data should survive.
	router := gin.New()
	p := Mount(
		t.Context(),
		router, nil,
		WithDevMode(),
		WithSQLite(dbPath),
	)
	t.Cleanup(func() { _ = p.Shutdown() })

	reqs, err := p.storage.GetRequests(RequestFilter{TimeRange: TimeRange{
		Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour),
	}})
	if err != nil {
		t.Fatalf("GetRequests after restart: %v", err)
	}
	found := false
	for _, r := range reqs {
		if r.Path == "/persisted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/persisted did not survive restart; got %d requests", len(reqs))
	}
}

// TestSQLite_EmptyDB exercises a representative subset of the interface on
// a brand-new database to confirm no method panics on zero rows.
func TestSQLite_EmptyDB(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)
	var _ Storage = s

	_, _ = s.GetSlowQueries(0, 10)
	_, _ = s.GetQueryPatterns(Last1h())
	_, _ = s.GetN1Detections(Last1h())
	_, _ = s.GetConnectionPoolStats()
	_, _ = s.GetRuntimeHistory(Last1h())
	_, _ = s.GetAlerts(AlertFilter{TimeRange: Last1h()})
	if r := s.GetLatestHealthResults(); r == nil {
		t.Fatal("GetLatestHealthResults must return a non-nil map even when empty")
	}
}
