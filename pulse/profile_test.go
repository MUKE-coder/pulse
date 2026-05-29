package pulse

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- Folded format ---

func TestFlameTree_AddAndFold(t *testing.T) {
	t.Parallel()
	root := &FlameNode{Name: "all", Children: map[string]*FlameNode{}}
	addStackToTree(root, []string{"main", "handler", "db.Query"})
	addStackToTree(root, []string{"main", "handler", "db.Query"})
	addStackToTree(root, []string{"main", "handler", "encode.JSON"})

	if root.Value != 3 {
		t.Fatalf("root value = %d, want 3", root.Value)
	}
	handler := root.Children["main"].Children["handler"]
	if handler.Value != 3 {
		t.Fatalf("handler value = %d, want 3", handler.Value)
	}

	folded := Folded(root)
	// Two leaves expected, both rooted at "main;handler".
	if !strings.Contains(folded, "main;handler;db.Query 2\n") {
		t.Errorf("folded missing db.Query leaf: %q", folded)
	}
	if !strings.Contains(folded, "main;handler;encode.JSON 1\n") {
		t.Errorf("folded missing encode.JSON leaf: %q", folded)
	}
}

// --- SVG renderer ---

func TestFlameGraphSVG_Shape(t *testing.T) {
	t.Parallel()
	root := &FlameNode{Name: "all", Children: map[string]*FlameNode{}}
	addStackToTree(root, []string{"main", "loop", "work"})
	addStackToTree(root, []string{"main", "loop", "work"})
	addStackToTree(root, []string{"main", "loop", "io"})

	svg := FlameGraphSVG(root, 800, 16)
	if !strings.HasPrefix(svg, `<svg`) {
		t.Fatal("expected SVG output to begin with <svg")
	}
	if !strings.HasSuffix(svg, `</svg>`) {
		t.Fatal("expected SVG output to end with </svg>")
	}
	// At least one frame label survived rendering.
	if !strings.Contains(svg, "main") {
		t.Errorf("SVG missing main frame label")
	}
}

func TestFlameGraphSVG_EmptyTreeStillRenders(t *testing.T) {
	t.Parallel()
	svg := FlameGraphSVG(nil, 400, 12)
	if !strings.Contains(svg, "No profile data yet") {
		t.Errorf("empty-tree fallback missing explanatory text: %s", svg)
	}
}

// --- Gating ---

func TestProfilingPermitted_RequiresBothGates(t *testing.T) {
	cfg := applyDefaults(Config{})
	t.Setenv(ProfileEnabledEnv, "")

	// Neither — refused.
	if profilingPermitted(cfg) {
		t.Fatal("default config should not permit profiling")
	}

	// Only config — still refused.
	cfg.Profiling.Enabled = boolPtr(true)
	if profilingPermitted(cfg) {
		t.Fatal("config flag alone should not permit profiling without env var")
	}

	// Only env — still refused.
	cfg.Profiling.Enabled = boolPtr(false)
	t.Setenv(ProfileEnabledEnv, "true")
	if profilingPermitted(cfg) {
		t.Fatal("env var alone should not permit profiling without config flag")
	}

	// Both — finally permitted.
	cfg.Profiling.Enabled = boolPtr(true)
	if !profilingPermitted(cfg) {
		t.Fatal("both gates set should permit profiling")
	}
}

// --- API endpoint ---

func TestProfileFlamegraphAPI_DisabledReturns503(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode())
	// Profiling left disabled by default.

	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	req := httptest.NewRequest("GET", "/pulse/api/profile/flamegraph", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 503 {
		t.Fatalf("expected 503 when profiling disabled, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), ProfileEnabledEnv) {
		t.Errorf("503 body should mention the env var so operators know how to enable: %s", w.Body.String())
	}
}

func TestProfileFlamegraphAPI_EnabledReturnsSVG(t *testing.T) {
	t.Setenv(ProfileEnabledEnv, "true")
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithProfiling())
	if p.profileSampler == nil {
		t.Fatal("WithProfiling() should construct the sampler")
	}

	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	// Use the smallest possible window so the test is fast.
	req := httptest.NewRequest("GET",
		"/pulse/api/profile/flamegraph?duration=100ms&hz=50&width=600", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String()[:min(200, w.Body.Len())])
	}
	if got := w.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("Content-Type = %q, want image/svg+xml", got)
	}
	if !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Fatalf("body does not look like SVG: %q", w.Body.String()[:80])
	}
}

func TestProfileFlamegraphAPI_JSONFormat(t *testing.T) {
	t.Setenv(ProfileEnabledEnv, "1")
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithProfiling())

	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	req := httptest.NewRequest("GET",
		"/pulse/api/profile/flamegraph?duration=100ms&hz=50&format=json", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		t.Fatalf("JSON output should start with '{', got %q", w.Body.String()[:40])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
