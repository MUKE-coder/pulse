package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fillRequests pushes n requests spaced one minute apart, newest now.
func fillRequests(s Storage, n int, now time.Time) {
	for i := n - 1; i >= 0; i-- {
		_ = s.StoreRequest(RequestMetric{Method: "GET", Path: "/x", StatusCode: 200,
			Timestamp: now.Add(-time.Duration(i) * time.Minute)})
	}
}

// A full ring buffer reports how far back it actually reaches, and that
// drives retention coverage.
func TestCapacity_MemoryReportsEffectiveRetention(t *testing.T) {
	now := time.Now()
	p := newPulse(context.Background(), applyDefaults(Config{})) // 24h retention
	p.storage = newMemoryStorage("test", MemoryCapacity{Requests: 100})
	t.Cleanup(func() { _ = p.Shutdown() })

	fillRequests(p.storage, 150, now) // the buffer keeps the newest 100: 99 minutes

	var req capacityStat
	for _, st := range p.storage.(capacityReporter).capacityStats(now) {
		if st.Kind == "requests" {
			req = st
		}
	}
	if !req.Full || req.Stored != 100 || req.Capacity != 100 {
		t.Fatalf("requests = %+v, want full at 100", req)
	}
	if got := time.Duration(req.EffectiveRetentionSeconds * float64(time.Second)); got != 99*time.Minute {
		t.Errorf("effective retention = %s, want 99m", got)
	}

	coverage, limiting, ok := retentionCoverage(p, now)
	if !ok || limiting == nil || limiting.Kind != "requests" {
		t.Fatalf("limiting = %+v, want requests", limiting)
	}
	if want := 99.0 / (24 * 60); coverage < want-0.001 || coverage > want+0.001 {
		t.Errorf("coverage = %.4f, want %.4f", coverage, want)
	}
}

// A buffer that isn't full doesn't limit retention.
func TestCapacity_BufferNotFullIsNotLimiting(t *testing.T) {
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = newMemoryStorage("test", MemoryCapacity{Requests: 100})
	t.Cleanup(func() { _ = p.Shutdown() })
	fillRequests(p.storage, 50, time.Now())

	if coverage, limiting, _ := retentionCoverage(p, time.Now()); coverage != 1 || limiting != nil {
		t.Fatalf("coverage = %.2f limited by %+v, want 1 and nothing", coverage, limiting)
	}
}

func TestCapacity_RuntimeBufferCoversRetention(t *testing.T) {
	c := memoryCapacityFor(applyDefaults(Config{})) // 24h at 5s
	if c.Runtime != 24*60*12+1 {
		t.Errorf("runtime capacity = %d, want %d (24h of 5s samples)", c.Runtime, 24*60*12+1)
	}
	c = memoryCapacityFor(applyDefaults(Config{Storage: StorageConfig{RetentionHours: 1}}))
	if c.Runtime != defaultRuntimeCapacity {
		t.Errorf("runtime capacity = %d, want the %d floor", c.Runtime, defaultRuntimeCapacity)
	}
}

// The built-in rule fires with a message naming the limiting kind.
func TestCapacity_RetentionAlertNamesTheLimitingKind(t *testing.T) {
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = newMemoryStorage("test", MemoryCapacity{Requests: 100})
	t.Cleanup(func() { _ = p.Shutdown() })
	fillRequests(p.storage, 150, time.Now())

	ae := &AlertEngine{pulse: p, ruleStates: map[string]*ruleState{}}
	rule := AlertRule{Name: "storage_retention_limited", Metric: "retention_coverage",
		Operator: "<", Threshold: 0.5, Severity: "warning"} // no duration, so it fires at once
	ae.ruleStates[rule.Name] = &ruleState{rule: rule, state: AlertStateOK}
	ae.evaluate() // OK → pending
	ae.evaluate() // pending → firing

	alerts, _ := p.storage.GetAlerts(AlertFilter{State: AlertStateFiring})
	if len(alerts) != 1 {
		t.Fatalf("got %d firing alerts, want 1", len(alerts))
	}
	msg := alerts[0].Message
	if !strings.Contains(msg, "requests: holding 1h39m0s of 24h retention") || !strings.Contains(msg, "MemoryCapacity") {
		t.Errorf("message = %q, want the limiting kind, what it holds, and the remedy", msg)
	}
}

// Dropped SQLite writes surface as new drops per evaluation, not a
// forever-growing total.
func TestCapacity_DroppedWritesMetricReportsNewDrops(t *testing.T) {
	s, err := openSQLiteStorage(":memory:", "test", 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p := newPulse(context.Background(), applyDefaults(Config{}))
	p.storage = s
	t.Cleanup(func() { _ = p.Shutdown() })
	ae := &AlertEngine{pulse: p, ruleStates: map[string]*ruleState{}}
	rule := AlertRule{Metric: "storage_dropped_writes"}

	s.writeMu.Lock() // stall the writer so the 8-slot queue overflows
	dropped := 0
	for i := 0; i < 1000; i++ {
		if errors.Is(s.StoreRequest(RequestMetric{Timestamp: time.Now()}), errSQLiteQueueFull) {
			dropped++
		}
	}
	s.writeMu.Unlock()

	if v, ok := ae.getMetricValue(rule); !ok || int(v) != dropped || dropped == 0 {
		t.Fatalf("first evaluation = %.0f (ok=%v), want %d new drops", v, ok, dropped)
	}
	if v, _ := ae.getMetricValue(rule); v != 0 {
		t.Fatalf("second evaluation = %.0f, want 0 (no new drops)", v)
	}
}

func TestCapacity_StorageEndpointAndPrometheus(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithPrometheus(),
		WithMemoryCapacity(MemoryCapacity{Requests: 10}))
	t.Cleanup(func() { _ = p.Shutdown() })
	fillRequests(p.storage, 20, time.Now())

	token := signJWT(jwtClaims{Username: "t", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix()},
		p.config.Dashboard.SecretKey)
	req := httptest.NewRequest("GET", "/pulse/api/storage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp struct {
		Driver            string         `json:"driver"`
		RetentionCoverage float64        `json:"retention_coverage"`
		LimitedBy         string         `json:"limited_by"`
		Kinds             []capacityStat `json:"kinds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if resp.Driver != "Memory" || resp.LimitedBy != "requests" || resp.RetentionCoverage >= 0.5 || len(resp.Kinds) == 0 {
		t.Fatalf("storage response = %+v", resp)
	}

	metrics := buildPrometheusMetrics(p)
	for _, want := range []string{
		`pulse_storage_records{kind="requests"} 10`,
		`pulse_storage_effective_retention_seconds{kind="requests"}`,
		"pulse_storage_retention_coverage_ratio",
		"pulse_storage_dropped_writes_total 0",
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("Prometheus output missing %q", want)
		}
	}
}

func TestSQLiteFilePath(t *testing.T) {
	t.Parallel()
	for dsn, want := range map[string]string{
		"pulse.db":                   "pulse.db",
		"file:data/pulse.db?cache=x": "data/pulse.db",
		":memory:":                   "",
		"file::memory:?cache=shared": "",
		"/var/lib/pulse/pulse.db":    "/var/lib/pulse/pulse.db",
	} {
		if got := sqliteFilePath(dsn); got != want {
			t.Errorf("sqliteFilePath(%q) = %q, want %q", dsn, got, want)
		}
	}
}
