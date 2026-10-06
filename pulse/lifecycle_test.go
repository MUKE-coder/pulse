package pulse

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type lifecycleResponse struct {
	Events     []lifecycleEvent `json:"events"`
	Storage    string           `json:"storage"`
	Persistent bool             `json:"persistent"`
	DataSince  time.Time        `json:"data_since"`
	InstanceID string           `json:"instance_id"`
}

func getLifecycle(t *testing.T, router *gin.Engine, p *Pulse) lifecycleResponse {
	t.Helper()
	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)
	req := httptest.NewRequest("GET", "/pulse/api/lifecycle?range=24h", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /lifecycle status=%d body=%s", w.Code, w.Body.String())
	}
	var resp lifecycleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestLifecycle_MemoryReportsWhereDataStarts(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithInstanceID("mem-1"))
	t.Cleanup(func() { _ = p.Shutdown() })

	resp := getLifecycle(t, router, p)
	if resp.Persistent || resp.Storage != "Memory" || resp.InstanceID != "mem-1" {
		t.Errorf("response = %+v, want non-persistent Memory storage for mem-1", resp)
	}
	if len(resp.Events) != 1 || resp.Events[0].Type != lifecycleStart || resp.Events[0].PreviousUnclean {
		t.Errorf("events = %+v, want one clean start", resp.Events)
	}
	if time.Since(resp.DataSince) > time.Minute {
		t.Errorf("data_since = %s, want about now", resp.DataSince)
	}
}

func TestLifecycle_CleanRestartIsNotFlagged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clean.db")

	first := Mount(context.Background(), gin.New(), nil, WithDevMode(), WithSQLite(path), WithInstanceID("api-1"))
	if err := first.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	router := gin.New()
	second := Mount(context.Background(), router, nil, WithDevMode(), WithSQLite(path), WithInstanceID("api-1"))
	t.Cleanup(func() { _ = second.Shutdown() })

	resp := getLifecycle(t, router, second)
	if !resp.Persistent || resp.Storage != "SQLite" {
		t.Errorf("response = %+v, want persistent SQLite storage", resp)
	}
	types := make([]string, len(resp.Events))
	for i, e := range resp.Events {
		types[i] = e.Type
	}
	if len(types) != 3 || types[0] != "start" || types[1] != "stop" || types[2] != "start" {
		t.Fatalf("event types = %v, want [start stop start]", types)
	}
	if resp.Events[2].PreviousUnclean {
		t.Error("a clean restart was flagged as following an unclean shutdown")
	}
}

// A run that dies without recording a stop (crash, kill -9) is flagged at
// the next start, with the time of the last request it recorded.
func TestLifecycle_UncleanRestartIsFlagged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")

	crashed := newPulse(context.Background(), applyDefaults(Config{DevMode: true, InstanceID: "api-1"}))
	s, err := NewSQLiteStorage(path, "test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	crashed.storage = s
	recordStart(crashed)
	lastRequest := time.Now().Add(-time.Second).Truncate(time.Millisecond)
	_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200, Timestamp: lastRequest})
	_ = s.Close() // the process "dies": no stop event is recorded

	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithSQLite(path), WithInstanceID("api-1"))
	t.Cleanup(func() { _ = p.Shutdown() })

	resp := getLifecycle(t, router, p)
	if len(resp.Events) != 2 {
		t.Fatalf("events = %+v, want the crashed start and the new start", resp.Events)
	}
	restart := resp.Events[1]
	if !restart.PreviousUnclean {
		t.Fatal("restart after a crash was not flagged")
	}
	if restart.GapFrom == nil || !restart.GapFrom.Equal(lastRequest) {
		t.Errorf("gap_from = %v, want the last recorded request at %s", restart.GapFrom, lastRequest)
	}

	// A different instance sharing the file has its own history.
	if prev, _ := p.storage.(lifecycleStore).lastLifecycleEvent("api-2"); prev != nil {
		t.Errorf("instance api-2 has no history, got %+v", prev)
	}
}

func TestBuildVersion(t *testing.T) {
	cases := []struct {
		name string
		info SystemInfo
		want string
	}{
		{"revision", SystemInfo{VCSRevision: "9f1c2b7e4a55d3"}, "9f1c2b7"},
		{"dirty tree", SystemInfo{VCSRevision: "9f1c2b7e4a55d3", VCSModified: true}, "9f1c2b7-dirty"},
		{"module version", SystemInfo{BuildVersion: "v1.2.0"}, "v1.2.0"},
		{"go run", SystemInfo{BuildVersion: "(devel)"}, ""},
		{"nothing stamped", SystemInfo{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildVersion(c.info); got != c.want {
				t.Errorf("buildVersion = %q, want %q", got, c.want)
			}
		})
	}
}

func TestConformance_LifecycleVersionRoundTrips(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s Storage) {
		ls := s.(lifecycleStore)
		now := time.Now()
		_ = ls.storeLifecycleEvent(lifecycleEvent{Type: lifecycleStart, InstanceID: "api-1",
			At: now.Add(-time.Hour), Version: "abc1234"})
		_ = ls.storeLifecycleEvent(lifecycleEvent{Type: lifecycleStart, InstanceID: "api-1",
			At: now, Version: "def5678"})

		events, err := ls.lifecycleEvents(wideRange())
		if err != nil || len(events) != 2 {
			t.Fatalf("got %d events (err %v), want 2", len(events), err)
		}
		if events[0].Version != "abc1234" || events[1].Version != "def5678" {
			t.Errorf("versions = %q, %q; want the builds each start ran",
				events[0].Version, events[1].Version)
		}
	})
}

// The lifecycle endpoint reports which build is running and when it first
// started — the deploy the dashboard marks on its charts.
func TestLifecycleHandler_ReportsTheRunningBuild(t *testing.T) {
	p, _ := mountForLogs(t)
	version := buildVersion(collectSystemInfo())
	deployed := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	_ = p.storage.(lifecycleStore).storeLifecycleEvent(lifecycleEvent{
		Type: lifecycleStart, InstanceID: "older", At: deployed, Version: version})

	var resp struct {
		Version    string    `json:"version"`
		DeployedAt time.Time `json:"deployed_at"`
	}
	callLogsAPI(t, lifecycleHandler(p), "/lifecycle?range=24h", nil, &resp)
	if resp.Version != version {
		t.Errorf("version = %q, want %q", resp.Version, version)
	}
	if !resp.DeployedAt.Equal(deployed) {
		t.Errorf("deployed_at = %s, want the first start of this build at %s", resp.DeployedAt, deployed)
	}
}
