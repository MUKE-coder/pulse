package pulse

import (
	"context"
	"testing"
	"time"

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
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")

	plugin := &PulsePlugin{
		pulse:     p,
		n1Tracker: make(map[string]map[string]int),
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
		if d.Count < 5 {
			t.Errorf("expected count >= 5, got %d", d.Count)
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
	p := newPulse(applyDefaults(Config{}))
	p.storage = NewMemoryStorage("test")
	defer p.Shutdown()

	plugin := &PulsePlugin{
		pulse:     p,
		n1Tracker: make(map[string]map[string]int),
	}

	// Simulate tracking
	plugin.n1Tracker["trace-1"] = map[string]int{"select * from users where id = ?": 3}
	plugin.n1Tracker["trace-2"] = map[string]int{"select * from posts where id = ?": 1}

	plugin.CleanupTraceN1("trace-1")

	if _, exists := plugin.n1Tracker["trace-1"]; exists {
		t.Error("expected trace-1 to be cleaned up")
	}
	if _, exists := plugin.n1Tracker["trace-2"]; !exists {
		t.Error("expected trace-2 to still exist")
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

	p := newPulse(applyDefaults(Config{}))
	p.storage = NewMemoryStorage("bench")
	defer p.Shutdown()

	plugin := &PulsePlugin{
		pulse:     p,
		n1Tracker: make(map[string]map[string]int),
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
