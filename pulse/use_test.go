package pulse

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestBand covers the colour-banding helper used by every USE cell.
func TestBand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value, amber, red float64
		want              Level
	}{
		{10, 60, 85, LevelGreen},
		{60, 60, 85, LevelAmber},
		{84.9, 60, 85, LevelAmber},
		{85, 60, 85, LevelRed},
		{100, 60, 85, LevelRed},
	}
	for _, c := range cases {
		if got := band(c.value, c.amber, c.red); got != c.want {
			t.Errorf("band(%v, %v, %v) = %s, want %s", c.value, c.amber, c.red, got, c.want)
		}
	}
}

func TestLevelFromError(t *testing.T) {
	t.Parallel()
	if got := levelFromError(int64(0)); got != LevelGreen {
		t.Errorf("zero errors should be green, got %s", got)
	}
	if got := levelFromError(int64(1)); got != LevelAmber {
		t.Errorf("non-zero errors should at least be amber, got %s", got)
	}
}

// TestUSESampler_SnapshotShape doesn't assert on the live values (they're
// host-dependent) — it just confirms the snapshot includes every resource
// row we promise, with each cell populated.
func TestUSESampler_SnapshotShape(t *testing.T) {
	p := newTestPulse(t)
	sampler := newUSESampler(p)

	// Wait for the initial sample to land. The sampler runs an immediate
	// tick on start, so a small wait is sufficient.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sampler.Snapshot().Resources) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	snap := sampler.Snapshot()
	if len(snap.Resources) == 0 {
		t.Fatal("expected at least one resource in the snapshot")
	}

	wantResources := []string{"CPU", "Memory", "Disk", "Network", "DB pool", "Goroutines"}
	got := map[string]ResourceUSE{}
	for _, r := range snap.Resources {
		got[r.Name] = r
	}
	for _, name := range wantResources {
		r, ok := got[name]
		if !ok {
			t.Errorf("missing resource %q in snapshot", name)
			continue
		}
		// Every cell must at least have a display string and a level
		// (even if "unknown" / "—") so the dashboard can render it.
		if r.Utilization.Display == "" {
			t.Errorf("%s: Utilization.Display is empty", name)
		}
		if r.Saturation.Display == "" {
			t.Errorf("%s: Saturation.Display is empty", name)
		}
		if r.Errors.Display == "" {
			t.Errorf("%s: Errors.Display is empty", name)
		}
		if r.Utilization.Level == "" || r.Saturation.Level == "" || r.Errors.Level == "" {
			t.Errorf("%s: empty Level on a cell (%+v)", name, r)
		}
	}
}

// TestUSEAPIEndpoint exercises the auth + serialization path of /pulse/api/use.
func TestUSEAPIEndpoint(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil,
		WithDevMode(),
		WithAppName("use-api-test"),
	)
	if p.useSampler == nil {
		t.Fatal("USE sampler should be enabled by default")
	}

	// The first sample takes ~1s because cpu.Percent blocks for its window.
	// Wait until the snapshot is populated before hitting the endpoint.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.useSampler.Snapshot().Resources) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	req := httptest.NewRequest("GET", "/pulse/api/use", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var snap USESnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("response body is not a USESnapshot: %v\n%s", err, w.Body.String())
	}
	if len(snap.Resources) < 4 {
		t.Fatalf("expected at least 4 resources, got %d (body: %s)", len(snap.Resources), w.Body.String())
	}
}

// TestWithUSEDisabled confirms the opt-out works.
func TestWithUSEDisabled(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil,
		WithDevMode(),
		WithUSEDisabled(),
	)
	if p.useSampler != nil {
		t.Fatal("WithUSEDisabled() should suppress the sampler")
	}

	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)
	req := httptest.NewRequest("GET", "/pulse/api/use", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	// Body should still parse as a USESnapshot — just with an empty Resources slice.
	if strings.Contains(w.Body.String(), `"resources":null`) ||
		strings.Contains(w.Body.String(), `"resources":[`) {
		// Both representations are acceptable.
		return
	}
	t.Fatalf("unexpected disabled-mode body: %s", w.Body.String())
}
