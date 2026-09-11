package pulse

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The SQLite backend queues metric writes and commits them in batches; these
// tests pin down the guarantees callers rely on.

func recentRange() TimeRange {
	return TimeRange{Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)}
}

// Reads flush the queue first, so concurrent writers' records are all
// visible to the next read.
func TestSQLite_ReadsSeeQueuedWrites(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)

	const writers, perWriter = 8, 250
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200, Timestamp: time.Now()}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	reqs, err := s.GetRequests(RequestFilter{TimeRange: recentRange()})
	if err != nil {
		t.Fatalf("GetRequests: %v", err)
	}
	if len(reqs) != writers*perWriter {
		t.Fatalf("got %d requests, want %d", len(reqs), writers*perWriter)
	}
}

// A stalled writer must never block the request path: stores keep returning
// immediately, and overflow is dropped and counted.
func TestSQLite_FullQueueDropsInsteadOfBlocking(t *testing.T) {
	t.Parallel()
	s, err := openSQLiteStorage(":memory:", "test", 64)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.writeMu.Lock() // stall the writer's next commit

	const attempts = 2000
	dropped := 0
	start := time.Now()
	for i := 0; i < attempts; i++ {
		err := s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200, Timestamp: time.Now()})
		switch {
		case errors.Is(err, errSQLiteQueueFull):
			dropped++
		case err != nil:
			t.Fatalf("StoreRequest: %v", err)
		}
	}
	elapsed := time.Since(start)
	s.writeMu.Unlock()

	if elapsed > 2*time.Second {
		t.Fatalf("%d stores took %s with the writer stalled; they must not block", attempts, elapsed)
	}
	if dropped == 0 {
		t.Fatal("expected drops with a stalled writer and a 64-slot queue")
	}
	reqs, _ := s.GetRequests(RequestFilter{TimeRange: recentRange()})
	if len(reqs)+dropped != attempts {
		t.Fatalf("stored %d + dropped %d = %d, want %d", len(reqs), dropped, len(reqs)+dropped, attempts)
	}
	if _, d, f := s.writeQueueStats(); d != int64(dropped) || f != 0 {
		t.Fatalf("writeQueueStats dropped=%d failed=%d, want dropped=%d failed=0", d, f, dropped)
	}
}

// Close commits everything still queued, and later stores fail cleanly.
func TestSQLite_CloseFlushesQueuedWrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "flush.db")

	s, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 300; i++ {
		_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200, Timestamp: time.Now()})
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.StoreRequest(RequestMetric{Timestamp: time.Now()}); !errors.Is(err, errSQLiteClosed) {
		t.Fatalf("StoreRequest after Close = %v, want errSQLiteClosed", err)
	}

	reopened, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	reqs, _ := reopened.GetRequests(RequestFilter{TimeRange: recentRange()})
	if len(reqs) != 300 {
		t.Fatalf("after reopen got %d requests, want 300", len(reqs))
	}
}

// Synchronous writes flush first, so a mute issued right after an error is
// stored finds the row.
func TestSQLite_SyncWritesSeeQueuedRows(t *testing.T) {
	t.Parallel()
	s := newSQLiteForTest(t)

	rec := buildErrorRecord("GET", "/x", "boom", ErrorTypeInternal, "", nil, "")
	if err := s.StoreError(rec); err != nil {
		t.Fatalf("StoreError: %v", err)
	}
	if err := s.UpdateError(rec.ID, map[string]interface{}{"muted": true}); err != nil {
		t.Fatalf("UpdateError right after StoreError: %v", err)
	}
}
