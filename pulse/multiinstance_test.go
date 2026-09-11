package pulse

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

// Replicas starting at the same moment against one shared SecretKeyFile must
// all end up with the same key; otherwise a token issued by one is rejected
// by the others.
func TestSecretKeyFile_ConcurrentStartsAgree(t *testing.T) {
	t.Parallel()
	for round := 0; round < 10; round++ {
		path := filepath.Join(t.TempDir(), "keys", "secret.key")
		const replicas = 16
		keys := make([]string, replicas)
		errs := make([]error, replicas)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range keys {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				keys[i], _, errs[i] = resolveSecretKey(DashboardConfig{SecretKeyFile: path})
			}(i)
		}
		close(start)
		wg.Wait()

		for i := range keys {
			if errs[i] != nil {
				t.Fatalf("round %d replica %d: %v", round, i, errs[i])
			}
			if keys[i] != keys[0] {
				t.Fatalf("round %d: replicas disagree on the key (%q vs %q)", round, keys[i][:8], keys[0][:8])
			}
		}
		if onDisk, _ := os.ReadFile(path); string(onDisk) != keys[0] {
			t.Fatalf("round %d: file holds a different key than the replicas use", round)
		}
	}
}

func login(t *testing.T, router *gin.Engine) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/pulse/api/auth/login", strings.NewReader(`{"username":"admin","password":"pulse"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	return resp.Token
}

func verifyStatus(router *gin.Engine, token string) int {
	req := httptest.NewRequest("GET", "/pulse/api/auth/verify", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

// The review's multi-replica test: a dashboard token issued by one replica
// works on another when they share a signing key — and, as documented, not
// when each has its own ephemeral key.
func TestReplicas_TokensAcrossInstances(t *testing.T) {
	mount := func(opts ...Option) *gin.Engine {
		router := gin.New()
		p := Mount(context.Background(), router, nil, append([]Option{WithDevMode(), WithUSEDisabled()}, opts...)...)
		t.Cleanup(func() { _ = p.Shutdown() })
		return router
	}

	const shared = "replica-test-shared-signing-key-0123456789"
	a, b := mount(WithSecretKey(shared), WithInstanceID("a")), mount(WithSecretKey(shared), WithInstanceID("b"))
	if code := verifyStatus(b, login(t, a)); code != 200 {
		t.Fatalf("with a shared key, replica B rejected replica A's token (status %d)", code)
	}

	c, d := mount(), mount() // no key configured: each generates its own
	if code := verifyStatus(d, login(t, c)); code != 401 {
		t.Fatalf("with ephemeral keys, replica D accepted replica C's token (status %d)", code)
	}
}
