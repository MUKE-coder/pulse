package pulse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Test model
type TestUser struct {
	ID   uint   `gorm:"primarykey"`
	Name string `gorm:"size:100"`
	Age  int
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(&TestUser{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func setupTestPulseWithDB(t *testing.T) (*gorm.DB, *Pulse) {
	t.Helper()
	db := setupTestDB(t)

	cfg := applyDefaults(Config{DevMode: true})
	p := newPulse(context.Background(), cfg)
	p.storage = NewMemoryStorage("test")

	plugin := &PulsePlugin{
		pulse:     p,
		n1Tracker: make(map[string]*n1Trace),
	}
	p.gormPlugin = plugin

	if err := db.Use(plugin); err != nil {
		t.Fatalf("failed to register plugin: %v", err)
	}

	t.Cleanup(func() { p.Shutdown() })

	return db, p
}

func TestGormPlugin_Name(t *testing.T) {
	plugin := &PulsePlugin{}
	if plugin.Name() != "pulse" {
		t.Fatalf("expected name 'pulse', got %q", plugin.Name())
	}
}

func TestGormPlugin_ImplementsInterface(t *testing.T) {
	var _ gorm.Plugin = (*PulsePlugin)(nil)
}

func TestGormPlugin_TracksQueries(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	// Insert
	db.Create(&TestUser{Name: "Alice", Age: 30})
	time.Sleep(50 * time.Millisecond) // async storage

	queries, _ := p.storage.GetSlowQueries(0, 100)
	if len(queries) == 0 {
		t.Fatal("expected at least 1 query tracked")
	}

	found := false
	for _, q := range queries {
		if q.Operation == "INSERT" {
			found = true
			if q.Table != "test_users" {
				t.Errorf("expected table 'test_users', got %q", q.Table)
			}
			if q.Duration < 0 {
				t.Errorf("expected non-negative duration, got %v", q.Duration)
			}
		}
	}
	if !found {
		t.Error("expected to find an INSERT query")
	}
}

func TestGormPlugin_TracksSelectQueries(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	db.Create(&TestUser{Name: "Bob", Age: 25})
	time.Sleep(50 * time.Millisecond)

	// Query
	var users []TestUser
	db.Where("age > ?", 20).Find(&users)
	time.Sleep(50 * time.Millisecond)

	patterns, _ := p.storage.GetQueryPatterns(TimeRange{
		Start: time.Now().Add(-time.Minute),
		End:   time.Now().Add(time.Minute),
	})

	foundSelect := false
	for _, pat := range patterns {
		if pat.Operation == "SELECT" {
			foundSelect = true
		}
	}
	if !foundSelect {
		t.Error("expected to find a SELECT query pattern")
	}
}

func TestGormPlugin_TracksUpdateQueries(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	db.Create(&TestUser{Name: "Charlie", Age: 35})
	time.Sleep(50 * time.Millisecond)

	db.Model(&TestUser{}).Where("name = ?", "Charlie").Update("age", 36)
	time.Sleep(50 * time.Millisecond)

	queries, _ := p.storage.GetSlowQueries(0, 100)
	foundUpdate := false
	for _, q := range queries {
		if q.Operation == "UPDATE" {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Error("expected to find an UPDATE query")
	}
}

func TestGormPlugin_TracksDeleteQueries(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	user := TestUser{Name: "Dave", Age: 40}
	db.Create(&user)
	time.Sleep(50 * time.Millisecond)

	db.Delete(&user)
	time.Sleep(50 * time.Millisecond)

	queries, _ := p.storage.GetSlowQueries(0, 100)
	foundDelete := false
	for _, q := range queries {
		if q.Operation == "DELETE" {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Error("expected to find a DELETE query")
	}
}

func TestGormPlugin_TracksErrorQueries(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	// Query a non-existent table via raw SQL
	var result []map[string]interface{}
	db.Raw("SELECT * FROM nonexistent_table").Scan(&result)
	time.Sleep(50 * time.Millisecond)

	queries, _ := p.storage.GetSlowQueries(0, 100)
	foundError := false
	for _, q := range queries {
		if q.Error != "" {
			foundError = true
		}
	}
	if !foundError {
		t.Error("expected to find a query with error")
	}
}

func TestGormPlugin_N1Detection(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	// Create test data
	for i := 0; i < 10; i++ {
		db.Create(&TestUser{Name: "User", Age: 20 + i})
	}
	time.Sleep(50 * time.Millisecond)

	// Simulate N+1: query each user individually within the same trace
	ctx := ContextWithTraceID(context.Background(), "trace-n1-test")
	ctx = ContextWithPulse(ctx, p)

	var users []TestUser
	db.WithContext(ctx).Find(&users)

	// N+1 pattern: individual queries for each user
	for _, u := range users {
		var user TestUser
		db.WithContext(ctx).First(&user, u.ID)
	}
	time.Sleep(100 * time.Millisecond)

	// No tracing middleware here, so finalize the hand-built trace ourselves.
	p.gormPlugin.CleanupTraceN1("trace-n1-test")

	detections, _ := p.storage.GetN1Detections(TimeRange{
		Start: time.Now().Add(-time.Minute),
		End:   time.Now().Add(time.Minute),
	})

	if len(detections) == 0 {
		t.Error("expected N+1 detection to be triggered")
	} else {
		d := detections[0]
		if d.RequestTraceID != "trace-n1-test" {
			t.Errorf("expected trace ID 'trace-n1-test', got %q", d.RequestTraceID)
		}
		// The real repeat count (one lookup per user), not the threshold.
		if d.Count != len(users) {
			t.Errorf("expected count %d, got %d", len(users), d.Count)
		}
	}
}

func TestGormPlugin_TraceIDLinksToRequest(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	traceID := "test-trace-123"
	ctx := ContextWithTraceID(context.Background(), traceID)

	db.WithContext(ctx).Create(&TestUser{Name: "Eve", Age: 28})
	time.Sleep(50 * time.Millisecond)

	queries, _ := p.storage.GetSlowQueries(0, 100)
	found := false
	for _, q := range queries {
		if q.RequestTraceID == traceID {
			found = true
		}
	}
	if !found {
		t.Error("expected query to be linked to trace ID")
	}
}

func TestGormPlugin_NormalizesSQL(t *testing.T) {
	db, p := setupTestPulseWithDB(t)

	db.Create(&TestUser{Name: "Frank", Age: 45})
	time.Sleep(50 * time.Millisecond)

	queries, _ := p.storage.GetSlowQueries(0, 100)
	for _, q := range queries {
		if q.NormalizedSQL == "" {
			t.Error("expected normalized SQL to be populated")
		}
		if q.Operation == "" {
			t.Error("expected operation to be populated")
		}
	}
}

func TestGormPlugin_CleanupTraceN1(t *testing.T) {
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = NewMemoryStorage("test")
	defer p.Shutdown()

	plugin := &PulsePlugin{
		pulse:     p,
		n1Tracker: make(map[string]*n1Trace),
	}

	// trace-1 repeats one pattern 6 times (over the default threshold of 5);
	// trace-2 runs a single query.
	for i := 0; i < 6; i++ {
		plugin.trackN1("trace-1", "GET /users", "select * from users where id = ?", 2*time.Millisecond)
	}
	plugin.trackN1("trace-2", "GET /posts", "select * from posts where id = ?", time.Millisecond)

	plugin.CleanupTraceN1("trace-1")

	if _, exists := plugin.n1Tracker["trace-1"]; exists {
		t.Error("expected trace-1 to be cleaned up")
	}
	if _, exists := plugin.n1Tracker["trace-2"]; !exists {
		t.Error("expected trace-2 to still exist")
	}

	detections, _ := p.storage.GetN1Detections(TimeRange{
		Start: time.Now().Add(-time.Minute),
		End:   time.Now().Add(time.Minute),
	})
	if len(detections) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(detections))
	}
	d := detections[0]
	if d.Count != 6 || d.TotalDuration != 12*time.Millisecond || d.AvgDuration != 2*time.Millisecond || d.Route != "GET /users" {
		t.Errorf("detection = %+v, want 6 queries totalling 12ms on GET /users", d)
	}
}

// TestGormPlugin_N1FinalizedPerRequest drives real requests through the
// tracing middleware. Every request's tally must be finalized — v1.0.0 never
// removed them, leaking one map per request — and each detection must carry
// that request's real query count rather than the threshold.
func TestGormPlugin_N1FinalizedPerRequest(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&TestUser{Name: "N1", Age: 1})

	router := gin.New()
	p := Mount(context.Background(), router, db, WithDevMode())
	t.Cleanup(func() { p.Shutdown() })

	const perRequest, requests = 12, 20
	router.GET("/loop", func(c *gin.Context) {
		for i := 0; i < perRequest; i++ {
			var u TestUser
			db.WithContext(c.Request.Context()).First(&u)
		}
		c.Status(http.StatusOK)
	})
	for i := 0; i < requests; i++ {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/loop", nil))
	}

	p.gormPlugin.n1TrackerMu.Lock()
	remaining := len(p.gormPlugin.n1Tracker)
	p.gormPlugin.n1TrackerMu.Unlock()
	if remaining != 0 {
		t.Fatalf("n1Tracker holds %d traces after every request completed, want 0", remaining)
	}

	detections, _ := p.storage.GetN1Detections(TimeRange{
		Start: time.Now().Add(-time.Minute),
		End:   time.Now().Add(time.Minute),
	})
	if len(detections) != requests {
		t.Fatalf("expected %d detections (one per request), got %d", requests, len(detections))
	}
	for _, d := range detections {
		if d.Count != perRequest || d.Route != "GET /loop" {
			t.Fatalf("detection = %d× on %q, want %d× on GET /loop", d.Count, d.Route, perRequest)
		}
	}
}

// TestGormPlugin_SweepFinalizesIdleTraces covers traces nothing finalizes
// (hand-built trace contexts, goroutines outliving their request).
func TestGormPlugin_SweepFinalizesIdleTraces(t *testing.T) {
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = NewMemoryStorage("test")
	defer p.Shutdown()

	plugin := &PulsePlugin{pulse: p, n1Tracker: make(map[string]*n1Trace)}
	for i := 0; i < 5; i++ {
		plugin.trackN1("orphan", "", "select * from posts where author_id = ?", time.Millisecond)
	}
	plugin.trackN1("active", "", "select 1", time.Millisecond)

	plugin.sweepIdleN1Traces(time.Now())
	if len(plugin.n1Tracker) != 2 {
		t.Fatalf("sweep finalized fresh traces: %d left, want 2", len(plugin.n1Tracker))
	}

	plugin.n1Tracker["orphan"].lastSeen = time.Now().Add(-2 * n1TraceIdleTTL)
	plugin.sweepIdleN1Traces(time.Now())

	if _, ok := plugin.n1Tracker["orphan"]; ok {
		t.Error("expected idle trace to be swept")
	}
	if _, ok := plugin.n1Tracker["active"]; !ok {
		t.Error("expected active trace to remain")
	}
	detections, _ := p.storage.GetN1Detections(TimeRange{
		Start: time.Now().Add(-time.Minute),
		End:   time.Now().Add(time.Minute),
	})
	if len(detections) != 1 || detections[0].Count != 5 {
		t.Fatalf("expected one 5× detection from the swept trace, got %+v", detections)
	}
}

func BenchmarkGormPlugin_Callback(b *testing.B) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatal(err)
	}
	db.AutoMigrate(&TestUser{})

	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = NewMemoryStorage("bench")
	defer p.Shutdown()

	plugin := &PulsePlugin{
		pulse:     p,
		n1Tracker: make(map[string]*n1Trace),
	}
	db.Use(plugin)

	// Pre-create a user for queries
	db.Create(&TestUser{Name: "Bench", Age: 1})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var user TestUser
		db.First(&user, 1)
	}
}
