package pulse

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// A profile captured over a load test.
//
// k6 can tell you p95 went from 40ms to 310ms at 500 VUs. It cannot tell
// you which function, and neither can an external APM at the resolution
// that matters. Pulse is inside the process, so when a harness registers a
// run as starting, Pulse starts a CPU profile; when the run ends, it stops
// and keeps it against the run. The Test Runs page can then say "p95 40ms
// → 310ms, recovered after 95s — and here is the flame graph of those
// 310ms" (see issue #11).
//
// Guardrails, all of them deliberate: it only runs when profiling is
// already enabled through both of its gates, only one profile exists at a
// time across the whole process, and it stops itself at
// Profiling.MaxTestRunProfile however long the run turns out to be.

// defaultMaxTestRunProfile caps a run profile when the config does not. A
// load test is minutes, not hours, and a CPU profile grows with the time
// it covers.
const defaultMaxTestRunProfile = 5 * time.Minute

// testRunProfileStore is implemented by storage backends that keep the
// profile captured over a run. Unexported for the same reason as
// rollupStore.
type testRunProfileStore interface {
	saveTestRunProfile(runID string, capturedAt time.Time, pprofData []byte) error
	testRunProfile(runID string) ([]byte, time.Time, error)
}

// runProfiler captures one CPU profile over one test run.
type runProfiler struct {
	pulse *Pulse

	mu      sync.Mutex
	runID   string
	buf     *bytes.Buffer
	started time.Time
	timer   *time.Timer
}

func newRunProfiler(p *Pulse) *runProfiler {
	return &runProfiler{pulse: p}
}

// maxDuration is how long a run profile may run for.
func (rp *runProfiler) maxDuration() time.Duration {
	if d := rp.pulse.config.Profiling.MaxTestRunProfile; d > 0 {
		return d
	}
	return defaultMaxTestRunProfile
}

// start begins a profile for runID. It is a no-op — not an error — when
// profiling is off, because registering a run should work either way; the
// response says whether a profile is being taken.
func (rp *runProfiler) start(runID string) bool {
	if rp == nil || runID == "" || !profilingPermitted(rp.pulse.config) {
		return false
	}

	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.runID != "" {
		return false // one at a time, so two overlapping runs can't both profile
	}
	// Share the sampler's lock, so a run profile and an on-demand flame
	// graph can't fight over the runtime's single CPU profiler.
	if rp.pulse.profileSampler == nil || !rp.pulse.profileSampler.running.CompareAndSwap(false, true) {
		return false
	}

	buf := &bytes.Buffer{}
	if err := pprof.StartCPUProfile(buf); err != nil {
		rp.pulse.profileSampler.running.Store(false)
		rp.pulse.internalError("profile: test run", err)
		return false
	}

	rp.runID, rp.buf, rp.started = runID, buf, time.Now()
	// However the run ends — or doesn't — the profiler stops itself.
	rp.timer = time.AfterFunc(rp.maxDuration(), func() { rp.stopAndStore(runID) })
	if rp.pulse.config.DevMode {
		rp.pulse.logger.Printf("[pulse] profiling test run %s", runID)
	}
	return true
}

// stopAndStore ends the profile for runID and keeps it against that run.
// Calling it for a different run, or when nothing is running, does nothing.
func (rp *runProfiler) stopAndStore(runID string) {
	rp.mu.Lock()
	if rp.runID == "" || rp.runID != runID {
		rp.mu.Unlock()
		return
	}
	buf, started := rp.buf, rp.started
	if rp.timer != nil {
		rp.timer.Stop()
	}
	rp.runID, rp.buf, rp.timer = "", nil, nil
	pprof.StopCPUProfile()
	if rp.pulse.profileSampler != nil {
		rp.pulse.profileSampler.running.Store(false)
	}
	rp.mu.Unlock()

	store, ok := rp.pulse.storage.(testRunProfileStore)
	if !ok || buf == nil || buf.Len() == 0 {
		return
	}
	rp.pulse.internalError("storage: test run profile",
		store.saveTestRunProfile(runID, started, buf.Bytes()))
	if rp.pulse.config.DevMode {
		rp.pulse.logger.Printf("[pulse] test run %s profiled over %s (%d bytes)",
			runID, time.Since(started).Round(time.Second), buf.Len())
	}
}

// profiling reports the run being profiled right now, if any.
func (rp *runProfiler) profiling() string {
	if rp == nil {
		return ""
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.runID
}

// --- storage ---

// maxStoredRunProfiles bounds what the Memory backend keeps: profiles are
// hundreds of kilobytes each, and the interesting ones are the recent ones.
const maxStoredRunProfiles = 10

type storedRunProfile struct {
	runID      string
	capturedAt time.Time
	data       []byte
}

func (s *MemoryStorage) saveTestRunProfile(runID string, capturedAt time.Time, data []byte) error {
	s.runProfilesMu.Lock()
	defer s.runProfilesMu.Unlock()
	for i, held := range s.runProfiles {
		if held.runID == runID {
			s.runProfiles[i] = storedRunProfile{runID, capturedAt, data}
			return nil
		}
	}
	s.runProfiles = append(s.runProfiles, storedRunProfile{runID, capturedAt, data})
	if excess := len(s.runProfiles) - maxStoredRunProfiles; excess > 0 {
		s.runProfiles = append([]storedRunProfile(nil), s.runProfiles[excess:]...)
	}
	return nil
}

func (s *MemoryStorage) testRunProfile(runID string) ([]byte, time.Time, error) {
	s.runProfilesMu.Lock()
	defer s.runProfilesMu.Unlock()
	for _, held := range s.runProfiles {
		if held.runID == runID {
			return held.data, held.capturedAt, nil
		}
	}
	return nil, time.Time{}, nil
}

func (s *sqlStore) saveTestRunProfile(runID string, capturedAt time.Time, data []byte) error {
	s.sync()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.exec(`INSERT INTO test_run_profiles (run_id, captured_at, profile)
		VALUES (?, ?, ?)
		ON CONFLICT (run_id) DO UPDATE SET captured_at = excluded.captured_at, profile = excluded.profile`,
		runID, capturedAt.UnixNano(), data)
	return err
}

func (s *sqlStore) testRunProfile(runID string) ([]byte, time.Time, error) {
	s.sync()
	var (
		data     []byte
		captured int64
	)
	err := s.queryRow(`SELECT profile, captured_at FROM test_run_profiles WHERE run_id = ?`,
		runID).Scan(&data, &captured)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	return data, time.Unix(0, captured), nil
}

var (
	_ testRunProfileStore = (*MemoryStorage)(nil)
	_ testRunProfileStore = (*SQLiteStorage)(nil)
	_ testRunProfileStore = (*PostgresStorage)(nil)
)

// --- API ---

// testRunProfileHandler serves GET /pulse/api/test-runs/:id/profile: the CPU
// profile captured while the run was in flight. Default format is the pprof
// protobuf, so `go tool pprof` can open it directly; ?format=svg renders the
// flame graph the dashboard shows, and ?format=json returns the folded tree.
func testRunProfileHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		store, ok := p.storage.(testRunProfileStore)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "this storage backend keeps no run profiles"})
			return
		}
		runID := c.Param("id")
		data, capturedAt, err := store.testRunProfile(runID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(data) == 0 {
			hint := "register the run's start with POST /test-runs before it begins, with profiling enabled"
			if p.runProfiler.profiling() == runID {
				hint = "the run is still in flight; the profile is kept when its end is registered"
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "no profile for this run", "hint": hint})
			return
		}

		switch c.DefaultQuery("format", "pprof") {
		case "svg", "json":
			root, _, err := foldCPUProfile(data)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if c.Query("format") == "json" {
				c.JSON(http.StatusOK, root)
				return
			}
			c.Data(http.StatusOK, "image/svg+xml", []byte(FlameGraphSVG(root, queryInt(c, "width", 1400), 16)))
		default:
			c.Header("Content-Disposition",
				fmt.Sprintf("attachment; filename=\"pulse-run-%s.pprof\"", runID))
			c.Header("X-Pulse-Captured-At", capturedAt.UTC().Format(time.RFC3339))
			c.Data(http.StatusOK, "application/octet-stream", data)
		}
	}
}
