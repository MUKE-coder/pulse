package pulse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// clusterMember is one of several instances sharing a PostgreSQL schema.
type clusterMember struct {
	p      *Pulse
	router *gin.Engine
}

// mountCluster mounts one instance per ID on a fresh shared schema, or
// skips when PULSE_TEST_POSTGRES_DSN isn't set.
func mountCluster(t *testing.T, ids []string, opts ...Option) []clusterMember {
	t.Helper()
	dsn := os.Getenv(postgresTestEnv)
	if dsn == "" {
		t.Skipf("%s is not set", postgresTestEnv)
	}
	schema := testSchema(t, openPostgresTestDB(t))
	var members []clusterMember
	for _, id := range ids {
		router := gin.New()
		all := append([]Option{WithDevMode(), WithInstanceID(id), WithPostgres(dsn), WithPostgresSchema(schema)}, opts...)
		p := Mount(context.Background(), router, nil, all...)
		t.Cleanup(func() { _ = p.Shutdown() })
		members = append(members, clusterMember{p, router})
	}
	return members
}

func (m clusterMember) serve(path string, n int) {
	for i := 0; i < n; i++ {
		m.router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
}

// syncCluster does what each instance's rollup flusher does on its tick:
// every instance saves its rollups, then every instance loads the others'.
func syncCluster(members []clusterMember) {
	for _, m := range members {
		flushRollups(m.p)
	}
	for _, m := range members {
		flushRollups(m.p)
	}
}

// Every instance's figures cover the requests every instance served.
func TestCluster_FiguresCoverEveryInstance(t *testing.T) {
	members := mountCluster(t, []string{"api-1", "api-2"})
	for _, m := range members {
		m.router.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
		m.router.GET("/fail", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	}
	members[0].serve("/ok", 3)
	members[1].serve("/fail", 2)
	syncCluster(members)

	now := time.Now()
	for i, m := range members {
		c := m.p.rollups.summary(now.Add(-time.Hour), now.Add(time.Minute)).counts
		if c.total != 5 || c.status5xx != 2 {
			t.Errorf("instance %d counts %d requests (%d 5xx), want 5 (2)", i, c.total, c.status5xx)
		}
	}

	var resp struct {
		Instances []instanceInfo `json:"instances"`
		Shared    bool           `json:"shared"`
	}
	eventually(t, "api-2 lists both instances, one leading", func() bool {
		callLogsAPI(t, instancesHandler(members[1].p), "/instances", nil, &resp)
		byID, leaders := map[string]instanceInfo{}, 0
		for _, in := range resp.Instances {
			byID[in.ID] = in
			if in.Leader {
				leaders++
			}
		}
		return resp.Shared && len(byID) == 2 && leaders == 1 && byID["api-2"].Self &&
			byID["api-1"].Requests == 3 && byID["api-2"].Requests == 2 && byID["api-1"].Status == "running"
	})
}

// An application-wide alert is evaluated by the leader alone, so it is
// announced once, however many instances run.
func TestCluster_OneNotificationPerAlert(t *testing.T) {
	var mu sync.Mutex
	var fired int
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n struct{ Alert, State string }
		_ = json.NewDecoder(r.Body).Decode(&n)
		mu.Lock()
		if n.Alert == "cluster_errors" && n.State == string(AlertStateFiring) {
			fired++
		}
		mu.Unlock()
	}))
	defer hook.Close()

	members := mountCluster(t, []string{"api-1", "api-2"},
		WithWebhookAlerts(WebhookConfig{URL: hook.URL}),
		WithAlertRule(AlertRule{Name: "cluster_errors", Metric: "error_rate", Operator: ">", Threshold: 10, Severity: "critical"}))
	if members[0].p.isLeader() == members[1].p.isLeader() {
		t.Fatalf("leaders: %v, %v; want exactly one", members[0].p.isLeader(), members[1].p.isLeader())
	}
	for _, m := range members {
		m.router.GET("/fail", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
		m.serve("/fail", 3)
	}
	syncCluster(members)
	for round := 0; round < 2; round++ { // ok → pending → firing
		for _, m := range members {
			m.p.aggregator.run()
			m.p.alertEngine.evaluate()
		}
	}

	eventually(t, "the alert is announced", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fired > 0
	})
	time.Sleep(300 * time.Millisecond) // room for a second announcement, were one sent
	mu.Lock()
	defer mu.Unlock()
	if fired != 1 {
		t.Errorf("the webhook got %d firing notifications, want exactly 1", fired)
	}
}

// When the leader's database session dies without it resigning — a crash,
// a partition — another instance takes over, and the old leader notices it
// no longer leads. When the new leader shuts down, leadership moves again.
func TestCluster_LeaderFailsOver(t *testing.T) {
	members := mountCluster(t, []string{"api-1", "api-2"})
	leader, follower := members[0].p, members[1].p
	if !leader.isLeader() {
		leader, follower = follower, leader
	}
	if !leader.isLeader() || follower.isLeader() {
		t.Fatalf("leaders: %v, %v; want exactly one", members[0].p.isLeader(), members[1].p.isLeader())
	}

	store := leader.storage.(*PostgresStorage)
	store.lead.mu.Lock()
	var pid int
	err := store.lead.conn.QueryRowContext(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid)
	store.lead.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openPostgresTestDB(t).Exec(`SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the follower takes over", func() bool {
		follower.campaign()
		return follower.isLeader()
	})
	leader.campaign()
	if leader.isLeader() {
		t.Error("the old leader still believes it leads after losing its session")
	}

	_ = follower.Shutdown() // resigns
	eventually(t, "the old leader takes over again", func() bool {
		leader.campaign()
		return leader.isLeader()
	})
}

// Rollups saved before v1.2, keyed without an instance, survive the move to
// instance-keyed tables and count as the local instance's.
func TestSQLite_RollupsFromBeforeInstancesMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	minute := minuteOf(time.Now())
	var h latencyHistogram
	h.add(3 * time.Millisecond)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`DROP TABLE request_rollups`, nil},
		{`DROP TABLE latency_rollups`, nil},
		{`DROP TABLE slo_rollups`, nil},
		{`CREATE TABLE request_rollups (minute INTEGER NOT NULL, method TEXT NOT NULL, route TEXT NOT NULL,
			total INTEGER NOT NULL, status_4xx INTEGER NOT NULL, status_5xx INTEGER NOT NULL,
			latency_ns INTEGER NOT NULL, PRIMARY KEY (minute, method, route))`, nil},
		{`CREATE TABLE latency_rollups (minute INTEGER PRIMARY KEY, hist BLOB NOT NULL)`, nil},
		{`CREATE TABLE slo_rollups (minute INTEGER NOT NULL, slo TEXT NOT NULL, good INTEGER NOT NULL,
			total INTEGER NOT NULL, PRIMARY KEY (minute, slo))`, nil},
		{`INSERT INTO request_rollups VALUES (?, 'GET', '/x', 7, 0, 1, 1000)`, []any{minute}},
		{`INSERT INTO latency_rollups VALUES (?, ?)`, []any{minute, h.encode()}},
		{`INSERT INTO slo_rollups VALUES (?, 'avail', 5, 6)`, []any{minute}},
	} {
		if _, err := s.db.Exec(stmt.q, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.q, err)
		}
	}
	_ = s.Close()

	s, err = NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	snaps, err := s.loadRollups("host-a", minute-1)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("loaded %d minutes (err %v), want the legacy one", len(snaps), err)
	}
	got := snaps[0]
	if got.routes[rollupRouteKey{"GET", "/x"}].total != 7 || got.hist == nil || got.hist.count() != 1 || got.slos["avail"].total != 6 {
		t.Errorf("legacy minute = %+v", got)
	}
	if leftover, _ := sqliteHasTable(s.db, "request_rollups_v11"); leftover {
		t.Error("the set-aside table was not dropped")
	}
}

func TestConformance_InstanceIDsRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		now := time.Now()
		_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200, Timestamp: now, InstanceID: "api-2"})
		_ = s.StoreRuntime(RuntimeMetric{NumGoroutine: 7, Timestamp: now, InstanceID: "api-2"})
		_ = s.StoreHealthResult(HealthCheckResult{Name: "db", Status: "healthy", Timestamp: now, InstanceID: "api-2"})
		_ = s.StoreAlert(AlertRecord{ID: "a1", RuleName: "r", State: AlertStateFiring, FiredAt: now, InstanceID: "api-2"})
		first := buildErrorRecord("GET", "/x", "boom", ErrorTypeInternal, "", nil, "")
		first.InstanceID = "api-1"
		latest := first
		latest.InstanceID = "api-2"
		_ = s.StoreError(first)
		_ = s.StoreError(latest)

		reqs, _ := s.GetRequests(RequestFilter{TimeRange: wideRange()})
		runtime, _ := s.GetRuntimeHistory(wideRange())
		health, _ := s.GetHealthHistory("db", 5)
		alerts, _ := s.GetAlerts(AlertFilter{TimeRange: wideRange()})
		rec, _ := s.GetErrorByID(first.ID)
		switch {
		case len(reqs) != 1 || reqs[0].InstanceID != "api-2":
			t.Errorf("requests = %+v", reqs)
		case len(runtime) != 1 || runtime[0].InstanceID != "api-2":
			t.Errorf("runtime = %+v", runtime)
		case len(health) != 1 || health[0].InstanceID != "api-2":
			t.Errorf("health = %+v", health)
		case len(alerts) != 1 || alerts[0].InstanceID != "api-2":
			t.Errorf("alerts = %+v", alerts)
		case rec == nil || rec.InstanceID != "api-2":
			t.Errorf("error = %+v, want the latest occurrence's instance", rec)
		}
	})
}
