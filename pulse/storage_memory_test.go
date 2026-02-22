package pulse

import (
	"fmt"
	"testing"
	"time"
)

func newTestStorage() *MemoryStorage {
	return NewMemoryStorage("test-app")
}

func TestMemoryStorage_StoreAndGetRequests(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	for i := 0; i < 10; i++ {
		s.StoreRequest(RequestMetric{
			Method:    "GET",
			Path:      "/users",
			StatusCode: 200,
			Latency:   time.Duration(i+1) * 10 * time.Millisecond,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		})
	}

	reqs, err := s.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 10 {
		t.Fatalf("expected 10 requests, got %d", len(reqs))
	}
}

func TestMemoryStorage_GetRequests_WithFilter(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreRequest(RequestMetric{Method: "GET", Path: "/users", StatusCode: 200, Timestamp: now})
	s.StoreRequest(RequestMetric{Method: "POST", Path: "/users", StatusCode: 201, Timestamp: now})
	s.StoreRequest(RequestMetric{Method: "GET", Path: "/posts", StatusCode: 200, Timestamp: now})

	reqs, _ := s.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
		Method:    "GET",
	})
	if len(reqs) != 2 {
		t.Fatalf("expected 2 GET requests, got %d", len(reqs))
	}

	reqs, _ = s.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
		Path:      "/users",
	})
	if len(reqs) != 2 {
		t.Fatalf("expected 2 /users requests, got %d", len(reqs))
	}
}

func TestMemoryStorage_GetRequests_Pagination(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	for i := 0; i < 20; i++ {
		s.StoreRequest(RequestMetric{Method: "GET", Path: "/test", StatusCode: 200, Timestamp: now})
	}

	reqs, _ := s.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
		Limit:     5,
	})
	if len(reqs) != 5 {
		t.Fatalf("expected 5 with limit, got %d", len(reqs))
	}

	reqs, _ = s.GetRequests(RequestFilter{
		TimeRange: TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)},
		Offset:    15,
		Limit:     10,
	})
	if len(reqs) != 5 {
		t.Fatalf("expected 5 with offset 15, got %d", len(reqs))
	}
}

func TestMemoryStorage_RouteStats(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	for i := 0; i < 100; i++ {
		status := 200
		if i%10 == 0 {
			status = 500
		}
		s.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       "/users",
			StatusCode: status,
			Latency:    time.Duration(i+1) * time.Millisecond,
			Timestamp:  now,
		})
	}

	stats, err := s.GetRouteStats(TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 route, got %d", len(stats))
	}

	rs := stats[0]
	if rs.Method != "GET" || rs.Path != "/users" {
		t.Fatalf("unexpected route: %s %s", rs.Method, rs.Path)
	}
	if rs.RequestCount != 100 {
		t.Fatalf("expected 100 requests, got %d", rs.RequestCount)
	}
	if rs.ErrorCount != 10 {
		t.Fatalf("expected 10 errors, got %d", rs.ErrorCount)
	}
	if rs.P50Latency == 0 || rs.P95Latency == 0 || rs.P99Latency == 0 {
		t.Fatal("expected non-zero percentiles")
	}
}

func TestMemoryStorage_Queries(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreQuery(QueryMetric{
		SQL:           "SELECT * FROM users WHERE id = 1",
		NormalizedSQL: "select * from users where id = ?",
		Duration:      500 * time.Millisecond,
		Operation:     "SELECT",
		Table:         "users",
		Timestamp:     now,
	})
	s.StoreQuery(QueryMetric{
		SQL:           "SELECT * FROM users WHERE id = 2",
		NormalizedSQL: "select * from users where id = ?",
		Duration:      300 * time.Millisecond,
		Operation:     "SELECT",
		Table:         "users",
		Timestamp:     now,
	})

	slow, _ := s.GetSlowQueries(200*time.Millisecond, 10)
	if len(slow) != 2 {
		t.Fatalf("expected 2 slow queries, got %d", len(slow))
	}
	// Should be sorted by duration desc
	if slow[0].Duration < slow[1].Duration {
		t.Fatal("expected descending duration order")
	}

	patterns, _ := s.GetQueryPatterns(TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)})
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].Count != 2 {
		t.Fatalf("expected count 2, got %d", patterns[0].Count)
	}
}

func TestMemoryStorage_ErrorDeduplication(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	for i := 0; i < 5; i++ {
		s.StoreError(ErrorRecord{
			ID:           fmt.Sprintf("err-%d", i),
			Fingerprint:  "fp-1",
			Method:       "POST",
			Route:        "POST /users",
			ErrorMessage: "validation failed",
			ErrorType:    "validation",
			Count:        1,
			FirstSeen:    now,
			LastSeen:     now.Add(time.Duration(i) * time.Minute),
		})
	}

	errors, _ := s.GetErrors(ErrorFilter{})
	if len(errors) != 1 {
		t.Fatalf("expected 1 deduplicated error, got %d", len(errors))
	}
	if errors[0].Count != 5 {
		t.Fatalf("expected count 5, got %d", errors[0].Count)
	}
}

func TestMemoryStorage_ErrorMuteResolve(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreError(ErrorRecord{
		ID:           "err-1",
		Fingerprint:  "fp-1",
		ErrorMessage: "test error",
		Count:        1,
		FirstSeen:    now,
		LastSeen:     now,
	})

	err := s.UpdateError("err-1", map[string]interface{}{"muted": true})
	if err != nil {
		t.Fatal(err)
	}

	rec, err := s.getErrorByID("err-1")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Muted {
		t.Fatal("expected muted to be true")
	}

	err = s.UpdateError("err-1", map[string]interface{}{"resolved": true})
	if err != nil {
		t.Fatal(err)
	}

	rec, _ = s.getErrorByID("err-1")
	if !rec.Resolved {
		t.Fatal("expected resolved to be true")
	}
}

func TestMemoryStorage_ErrorUpdateNotFound(t *testing.T) {
	s := newTestStorage()
	err := s.UpdateError("nonexistent", map[string]interface{}{"muted": true})
	if err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
}

func TestMemoryStorage_HealthResults(t *testing.T) {
	s := newTestStorage()

	for i := 0; i < 5; i++ {
		s.StoreHealthResult(HealthCheckResult{
			Name:      "postgres",
			Type:      "database",
			Status:    "healthy",
			Latency:   time.Duration(i+1) * time.Millisecond,
			Timestamp: time.Now(),
		})
	}

	history, _ := s.GetHealthHistory("postgres", 3)
	if len(history) != 3 {
		t.Fatalf("expected 3 results, got %d", len(history))
	}

	// Non-existent check
	history, _ = s.GetHealthHistory("redis", 3)
	if history != nil {
		t.Fatalf("expected nil for non-existent check, got %v", history)
	}
}

func TestMemoryStorage_Alerts(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreAlert(AlertRecord{ID: "a1", RuleName: "high_latency", State: AlertStateFiring, Severity: "critical", FiredAt: now})
	s.StoreAlert(AlertRecord{ID: "a2", RuleName: "high_errors", State: AlertStateResolved, Severity: "warning", FiredAt: now})

	alerts, _ := s.GetAlerts(AlertFilter{})
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(alerts))
	}

	alerts, _ = s.GetAlerts(AlertFilter{State: AlertStateFiring})
	if len(alerts) != 1 {
		t.Fatalf("expected 1 firing alert, got %d", len(alerts))
	}
}

func TestMemoryStorage_Dependencies(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	for i := 0; i < 10; i++ {
		s.StoreDependencyMetric(DependencyMetric{
			Name:       "stripe",
			Method:     "POST",
			StatusCode: 200,
			Latency:    time.Duration(i+1) * 10 * time.Millisecond,
			Timestamp:  now,
		})
	}

	stats, _ := s.GetDependencyStats(TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)})
	if len(stats) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(stats))
	}
	if stats[0].Name != "stripe" {
		t.Fatalf("expected stripe, got %s", stats[0].Name)
	}
	if stats[0].RequestCount != 10 {
		t.Fatalf("expected 10 requests, got %d", stats[0].RequestCount)
	}
}

func TestMemoryStorage_Overview(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	for i := 0; i < 50; i++ {
		status := 200
		if i%5 == 0 {
			status = 500
		}
		s.StoreRequest(RequestMetric{
			Method:     "GET",
			Path:       "/users",
			StatusCode: status,
			Latency:    time.Duration(i+1) * time.Millisecond,
			Timestamp:  now,
		})
	}

	s.StoreRuntime(RuntimeMetric{
		HeapAlloc:    1024 * 1024 * 100,
		NumGoroutine: 42,
		Timestamp:    now,
	})

	overview, err := s.GetOverview(TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if overview.TotalRequests != 50 {
		t.Fatalf("expected 50 requests, got %d", overview.TotalRequests)
	}
	if overview.TotalErrors != 10 {
		t.Fatalf("expected 10 errors, got %d", overview.TotalErrors)
	}
	if overview.ActiveGoroutines != 42 {
		t.Fatalf("expected 42 goroutines, got %d", overview.ActiveGoroutines)
	}
}

func TestMemoryStorage_Reset(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreRequest(RequestMetric{Method: "GET", Path: "/test", Timestamp: now})
	s.StoreQuery(QueryMetric{SQL: "SELECT 1", Timestamp: now})
	s.StoreError(ErrorRecord{ID: "e1", Fingerprint: "fp", Count: 1, FirstSeen: now, LastSeen: now})
	s.StoreAlert(AlertRecord{ID: "a1", FiredAt: now})

	s.Reset()

	reqs, _ := s.GetRequests(RequestFilter{})
	if len(reqs) != 0 {
		t.Fatalf("expected 0 requests after reset, got %d", len(reqs))
	}
	errors, _ := s.GetErrors(ErrorFilter{})
	if len(errors) != 0 {
		t.Fatalf("expected 0 errors after reset, got %d", len(errors))
	}
	alerts, _ := s.GetAlerts(AlertFilter{})
	if len(alerts) != 0 {
		t.Fatalf("expected 0 alerts after reset, got %d", len(alerts))
	}
}

func TestMemoryStorage_Cleanup(t *testing.T) {
	s := newTestStorage()
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now()

	s.StoreError(ErrorRecord{ID: "old", Fingerprint: "fp-old", Count: 1, FirstSeen: old, LastSeen: old})
	s.StoreError(ErrorRecord{ID: "new", Fingerprint: "fp-new", Count: 1, FirstSeen: recent, LastSeen: recent})

	s.StoreAlert(AlertRecord{ID: "a-old", FiredAt: old})
	s.StoreAlert(AlertRecord{ID: "a-new", FiredAt: recent})

	s.Cleanup(24 * time.Hour)

	errors, _ := s.GetErrors(ErrorFilter{})
	if len(errors) != 1 {
		t.Fatalf("expected 1 error after cleanup, got %d", len(errors))
	}

	alerts, _ := s.GetAlerts(AlertFilter{})
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert after cleanup, got %d", len(alerts))
	}
}

func TestMemoryStorage_N1Detections(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreN1Detection(N1Detection{
		Pattern:        "select * from users where id = ?",
		Count:          10,
		TotalDuration:  500 * time.Millisecond,
		RequestTraceID: "trace-1",
		Route:          "GET /posts",
		DetectedAt:     now,
	})

	detections, _ := s.GetN1Detections(TimeRange{Start: now.Add(-time.Minute), End: now.Add(time.Minute)})
	if len(detections) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(detections))
	}
}

func TestMemoryStorage_PoolStats(t *testing.T) {
	s := newTestStorage()

	// No stats yet
	stats, _ := s.GetConnectionPoolStats()
	if stats != nil {
		t.Fatal("expected nil pool stats initially")
	}

	s.UpdatePoolStats(PoolStats{
		MaxOpenConnections: 25,
		OpenConnections:    10,
		InUse:              5,
		Idle:               5,
	})

	stats, _ = s.GetConnectionPoolStats()
	if stats == nil {
		t.Fatal("expected non-nil pool stats")
	}
	if stats.MaxOpenConnections != 25 {
		t.Fatalf("expected max 25, got %d", stats.MaxOpenConnections)
	}
}

func TestMemoryStorage_DeleteError(t *testing.T) {
	s := newTestStorage()
	now := time.Now()

	s.StoreError(ErrorRecord{ID: "e1", Fingerprint: "fp-1", Count: 1, FirstSeen: now, LastSeen: now})

	err := s.deleteError("e1")
	if err != nil {
		t.Fatal(err)
	}

	errors, _ := s.GetErrors(ErrorFilter{})
	if len(errors) != 0 {
		t.Fatalf("expected 0 errors after delete, got %d", len(errors))
	}

	err = s.deleteError("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent delete")
	}
}
