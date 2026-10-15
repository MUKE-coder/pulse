package pulse

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// callProfile runs a profile handler directly, past the auth middleware.
func callProfile(h gin.HandlerFunc, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h(c)
	return w
}

// profilablePulse mounts with both profiling gates open.
func profilablePulse(t *testing.T, opts ...Option) (*Pulse, *gin.Engine) {
	t.Helper()
	t.Setenv(ProfileEnabledEnv, "true")
	router := gin.New()
	p := Mount(context.Background(), router, nil,
		append([]Option{WithDevMode(), WithProfiling()}, opts...)...)
	t.Cleanup(func() { _ = p.Shutdown() })
	return p, router
}

// isPprof reports whether the body is a pprof profile, which is gzipped
// protobuf — what go tool pprof expects to read.
func isPprof(body []byte) bool {
	return len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b
}

// A heap profile is served as the protobuf the standard tools read.
func TestProfile_HeapServesPprofProtobuf(t *testing.T) {
	p, _ := profilablePulse(t)
	w := callProfile(runtimeProfileHandler(p, "heap"), "/profile/heap")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if !isPprof(w.Body.Bytes()) {
		t.Errorf("body is not a pprof profile (%d bytes)", w.Body.Len())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// Without both gates there is no profile, and the answer says how to open it.
func TestProfile_RefusedUntilBothGatesAreOpen(t *testing.T) {
	t.Setenv(ProfileEnabledEnv, "")
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithProfiling())
	t.Cleanup(func() { _ = p.Shutdown() })

	w := callProfile(runtimeProfileHandler(p, "heap"), "/profile/heap")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while the env gate is shut", w.Code)
	}
	if !strings.Contains(w.Body.String(), ProfileEnabledEnv) {
		t.Errorf("answer does not say what to set: %s", w.Body)
	}
}

// An empty mutex profile would read as "no contention", so Pulse says the
// runtime isn't sampling it instead of serving one.
func TestProfile_MutexOffIsExplainedNotEmpty(t *testing.T) {
	p, _ := profilablePulse(t)
	w := callProfile(runtimeProfileHandler(p, "mutex"), "/profile/mutex")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while mutex profiling is off", w.Code)
	}
	if !strings.Contains(w.Body.String(), "WithMutexProfiling") {
		t.Errorf("answer does not say how to turn it on: %s", w.Body)
	}
}

// Asked for, it is turned on at Mount and served.
func TestProfile_MutexOnIsServed(t *testing.T) {
	t.Cleanup(func() { runtime.SetMutexProfileFraction(0) })
	p, _ := profilablePulse(t, WithMutexProfiling(5))

	if got := runtime.SetMutexProfileFraction(-1); got != 5 {
		t.Fatalf("mutex profile fraction = %d, want the configured 5", got)
	}
	w := callProfile(runtimeProfileHandler(p, "mutex"), "/profile/mutex")
	if w.Code != http.StatusOK || !isPprof(w.Body.Bytes()) {
		t.Errorf("status = %d, pprof = %v", w.Code, isPprof(w.Body.Bytes()))
	}
}

func TestProfile_CPUSamplesAndRefusesASecondAtOnce(t *testing.T) {
	p, _ := profilablePulse(t)

	cpuProfileMu.Lock() // stand in for a profile already running
	w := callProfile(cpuProfileHandler(p), "/profile/cpu?seconds=1")
	cpuProfileMu.Unlock()
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 while another CPU profile runs", w.Code)
	}

	w = callProfile(cpuProfileHandler(p), "/profile/cpu?seconds=1")
	if w.Code != http.StatusOK || !isPprof(w.Body.Bytes()) {
		t.Fatalf("status = %d, pprof = %v: %s", w.Code, isPprof(w.Body.Bytes()), w.Body.String()[:min(200, w.Body.Len())])
	}
}

// The listing says what can be profiled and what the runtime isn't keeping.
func TestProfile_ListNamesWhatIsAvailable(t *testing.T) {
	p, _ := profilablePulse(t)
	w := callProfile(profileListHandler(p), "/profile/list")

	var resp struct {
		Profiles []struct {
			Name string `json:"name"`
			Off  bool   `json:"off"`
			Hint string `json:"hint"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byName := map[string]bool{}
	for _, e := range resp.Profiles {
		byName[e.Name] = e.Off
	}
	for _, want := range []string{"cpu", "heap", "allocs", "goroutine", "mutex", "block"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("%s is missing from the listing", want)
		}
	}
	if !byName["mutex"] || !byName["block"] {
		t.Error("mutex and block should be marked off until they are turned on")
	}
}

// go tool pprof cannot set headers, so the token may travel in the URL —
// and the endpoints stay closed without one.
func TestProfile_TokenInQueryAuthenticates(t *testing.T) {
	p, router := profilablePulse(t, WithPprofHandlers())

	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodPost, p.config.Prefix+"/api/auth/login",
		bytes.NewBufferString(`{"username":"admin","password":"pulse"}`)))
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("login failed: %s", login.Body)
	}

	for _, path := range []string{"/api/profile/heap", "/debug/pprof/heap"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p.config.Prefix+path+"?token="+out.Token, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s with a token: status %d", path, w.Code)
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p.config.Prefix+path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: status %d, want 401", path, w.Code)
		}
	}
}
