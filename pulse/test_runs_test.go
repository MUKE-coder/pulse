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

func TestStorageTestRuns_StoreAndWindow(t *testing.T) {
	t.Parallel()
	s := NewMemoryStorage("test")
	now := time.Now()

	if err := s.StoreTestRun(TestRun{
		ID: "r1", Name: "spike",
		StartedAt: now.Add(-10 * time.Minute),
		EndedAt:   now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("StoreTestRun: %v", err)
	}
	if err := s.StoreTestRun(TestRun{
		ID: "r2", Name: "soak",
		StartedAt: now.Add(-2 * time.Hour),
		EndedAt:   now.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("StoreTestRun: %v", err)
	}

	// Window includes r1, excludes r2.
	got, _ := s.GetTestRuns(TimeRange{Start: now.Add(-30 * time.Minute), End: now})
	if len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("window filter: got %d runs (%v), want [r1]", len(got), got)
	}

	// Cleanup at a 30m cutoff drops r2 (ended 60m ago), keeps r1 (ended 5m ago).
	if err := s.Cleanup(30 * time.Minute); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	all, _ := s.GetTestRuns(TimeRange{Start: now.Add(-24 * time.Hour), End: now})
	if len(all) != 1 || all[0].ID != "r1" {
		t.Fatalf("after cleanup: got %v, want [r1]", all)
	}
}

func TestTestRunsAPI_CreateAndList(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode())

	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	// POST a run.
	payload := `{"name":"average-load","type":"k6.average-load","started_at":"` +
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `",` +
		`"ended_at":"` + time.Now().UTC().Format(time.RFC3339) + `",` +
		`"metadata":{"vus_peak":50}}`
	req := httptest.NewRequest("POST", "/pulse/api/test-runs", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("POST status=%d body=%s", w.Code, w.Body.String())
	}

	var created TestRun
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("server should assign an ID when none is provided")
	}

	// GET the list.
	req2 := httptest.NewRequest("GET", "/pulse/api/test-runs?range=1h", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("GET status=%d body=%s", w2.Code, w2.Body.String())
	}
	var runs []TestRun
	if err := json.Unmarshal(w2.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(runs) != 1 || runs[0].Name != "average-load" {
		t.Fatalf("got %d runs (%v), want one named average-load", len(runs), runs)
	}
}

func TestTestRunsAPI_RejectsEmptyName(t *testing.T) {
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode())
	token := signJWT(jwtClaims{
		Username: "test", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix(),
	}, p.config.Dashboard.SecretKey)

	req := httptest.NewRequest("POST", "/pulse/api/test-runs",
		strings.NewReader(`{"type":"k6.spike"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 for empty name, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestTestRunsAPI_RequiresAuth(t *testing.T) {
	router := gin.New()
	_ = Mount(context.Background(), router, nil, WithDevMode())

	req := httptest.NewRequest("POST", "/pulse/api/test-runs",
		strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauth POST should be 401, got %d", w.Code)
	}
}
