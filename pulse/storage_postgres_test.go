package pulse

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// postgresTestEnv names the database the PostgreSQL tests use, for example
// postgres://postgres:pulse@localhost:55432/pulse?sslmode=disable. The tests
// are skipped without it.
const postgresTestEnv = "PULSE_TEST_POSTGRES_DSN"

func openPostgresTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(postgresTestEnv)
	if dsn == "" {
		t.Skipf("%s is not set", postgresTestEnv)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testSchema returns a fresh schema name, dropped when the test ends.
func testSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	schema := fmt.Sprintf("pulse_test_%d", rand.Uint64()>>1)
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	return schema
}

// newPostgresForTest returns a PostgresStorage in a schema of its own.
func newPostgresForTest(t *testing.T) *PostgresStorage {
	t.Helper()
	db := openPostgresTestDB(t)
	s, err := NewPostgresStorage(db, "test", testSchema(t, db))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Instances starting at the same moment create the schema once, and then
// share it: what one stores, another reads.
func TestPostgres_ConcurrentStartSharesOneSchema(t *testing.T) {
	db := openPostgresTestDB(t)
	schema := testSchema(t, db)

	stores := make([]*PostgresStorage, 4)
	errs := make([]error, len(stores))
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stores[i], errs[i] = NewPostgresStorage(db, "test", schema)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("instance %d: %v", i, err)
		}
		t.Cleanup(func() { _ = stores[i].Close() })
	}

	_ = stores[0].StoreRequest(RequestMetric{Method: "GET", Path: "/a", StatusCode: 200, Timestamp: time.Now()})
	_ = stores[1].StoreRequest(RequestMetric{Method: "GET", Path: "/b", StatusCode: 200, Timestamp: time.Now()})
	stores[0].sync()
	stores[1].sync()
	got, err := stores[3].GetRequests(RequestFilter{TimeRange: wideRange()})
	if err != nil || len(got) != 2 {
		t.Fatalf("a third instance sees %d requests (err %v), want both", len(got), err)
	}
}

// PostgreSQL rejects NUL bytes and invalid UTF-8 in text; such values arrive
// in paths and headers, and must be stored cleaned rather than dropped.
func TestPostgres_StoresUnsafeText(t *testing.T) {
	s := newPostgresForTest(t)
	_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/a\x00b", UserAgent: "bot\xff\xfe",
		StatusCode: 404, Timestamp: time.Now()})
	got, err := s.GetRequests(RequestFilter{TimeRange: wideRange()})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d requests (err %v); the write was lost", len(got), err)
	}
	if got[0].Path != "/ab" || got[0].UserAgent != "bot�" {
		t.Errorf("stored path %q, user agent %q", got[0].Path, got[0].UserAgent)
	}
	if _, _, failed := s.writeQueueStats(); failed != 0 {
		t.Errorf("%d writes failed", failed)
	}
}

// Rollups persist on both SQL backends: saving a minute again replaces it.
func TestSQLBackends_RollupsUpsertAndLoad(t *testing.T) {
	backends := map[string]func(t *testing.T) rollupStore{
		"sqlite":   func(t *testing.T) rollupStore { return newSQLiteForTest(t) },
		"postgres": func(t *testing.T) rollupStore { return newPostgresForTest(t) },
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			minute := minuteOf(time.Now())
			var h latencyHistogram
			h.add(40 * time.Millisecond)
			snap := func(total int64) minuteSnapshot {
				return minuteSnapshot{minute: minute,
					routes: map[rollupRouteKey]routeCounts{{"GET", "/x"}: {total: total, status5xx: 1, latency: time.Second}},
					hist:   &h, slos: map[string]sloCount{"avail": {good: total - 1, total: total}}}
			}
			if err := s.saveRollups([]minuteSnapshot{snap(5)}); err != nil {
				t.Fatal(err)
			}
			if err := s.saveRollups([]minuteSnapshot{snap(9)}); err != nil {
				t.Fatal(err)
			}
			got, err := s.loadRollups(minute - 1)
			if err != nil || len(got) != 1 {
				t.Fatalf("loaded %d minutes (err %v)", len(got), err)
			}
			if c := got[0].routes[rollupRouteKey{"GET", "/x"}]; c.total != 9 || got[0].slos["avail"].good != 8 || got[0].hist.count() != 1 {
				t.Errorf("minute after re-save = %+v", got[0])
			}
		})
	}
}

// Two instances mounted on one PostgreSQL schema record into it together.
func TestPostgres_MountSharesStorage(t *testing.T) {
	if os.Getenv(postgresTestEnv) == "" {
		t.Skipf("%s is not set", postgresTestEnv)
	}
	db := openPostgresTestDB(t)
	schema := testSchema(t, db)

	var instances []*Pulse
	var routers []*gin.Engine
	for _, id := range []string{"a", "b"} {
		router := gin.New()
		p := Mount(context.Background(), router, nil, WithDevMode(), WithInstanceID(id),
			WithPostgres(os.Getenv(postgresTestEnv)), WithPostgresSchema(schema))
		t.Cleanup(func() { _ = p.Shutdown() })
		router.GET("/hello", func(c *gin.Context) { c.Status(http.StatusOK) })
		instances, routers = append(instances, p), append(routers, router)
	}
	for _, r := range routers {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello", nil))
	}
	// Each instance commits its writes in batches, so another instance sees
	// them within a flush interval rather than at once.
	for i, p := range instances {
		eventually(t, fmt.Sprintf("instance %d sees both instances' requests", i), func() bool {
			reqs, err := p.storage.GetRequests(RequestFilter{TimeRange: wideRange(), Path: "/hello"})
			return err == nil && len(reqs) == 2
		})
	}
}

// eventually polls cond until it holds, failing the test after 5s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
	}
}
