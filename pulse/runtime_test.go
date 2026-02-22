package pulse

import (
	"runtime"
	"testing"
	"time"
)

func TestRuntimeSampler_CollectsMetrics(t *testing.T) {
	cfg := applyDefaults(Config{
		Runtime: RuntimeConfig{
			Enabled:        boolPtr(true),
			SampleInterval: 100 * time.Millisecond,
		},
	})
	p := newPulse(cfg)
	p.storage = NewMemoryStorage("test")
	defer p.Shutdown()

	rs := newRuntimeSampler(p)

	// Wait for a few samples
	time.Sleep(350 * time.Millisecond)

	history, err := p.storage.GetRuntimeHistory(TimeRange{
		Start: time.Now().Add(-time.Minute),
		End:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should have at least 2 samples (initial + ticker)
	if len(history) < 2 {
		t.Fatalf("expected at least 2 runtime samples, got %d", len(history))
	}

	// Validate the most recent sample
	latest := history[len(history)-1]
	if latest.HeapAlloc == 0 {
		t.Error("expected non-zero HeapAlloc")
	}
	if latest.Sys == 0 {
		t.Error("expected non-zero Sys")
	}
	if latest.NumGoroutine == 0 {
		t.Error("expected non-zero NumGoroutine")
	}
	if latest.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}

	// Verify system info
	info := rs.GetSystemInfo()
	if info.GoVersion == "" {
		t.Error("expected non-empty GoVersion")
	}
	if info.GOOS != runtime.GOOS {
		t.Errorf("expected GOOS %q, got %q", runtime.GOOS, info.GOOS)
	}
	if info.GOARCH != runtime.GOARCH {
		t.Errorf("expected GOARCH %q, got %q", runtime.GOARCH, info.GOARCH)
	}
	if info.NumCPU == 0 {
		t.Error("expected non-zero NumCPU")
	}
	if info.PID == 0 {
		t.Error("expected non-zero PID")
	}
}

func TestLeakDetector_NoLeakWithStableCounts(t *testing.T) {
	ld := &LeakDetector{
		threshold: 100,
		samples:   make([]goroutineSample, 0),
	}

	now := time.Now()
	// Simulate stable goroutine count over 30 minutes
	for i := 0; i < 360; i++ {
		ld.mu.Lock()
		ld.samples = append(ld.samples, goroutineSample{
			count:     50 + (i % 3), // slight jitter
			timestamp: now.Add(time.Duration(i) * 5 * time.Second),
		})
		ld.mu.Unlock()
	}

	// Manually trigger evaluation by adding a final sample
	ld.addSample(52)

	if ld.isLeaking() {
		t.Error("expected no leak with stable goroutine count")
	}

	rate := ld.growthRate()
	if rate > 10 {
		t.Errorf("expected low growth rate, got %.1f/hr", rate)
	}
}

func TestLeakDetector_DetectsLeak(t *testing.T) {
	ld := &LeakDetector{
		threshold: 50,
		samples:   make([]goroutineSample, 0),
	}

	now := time.Now()
	// Simulate growing goroutine count: 100 → 300 over 50 minutes (within 1hr window)
	for i := 0; i < 600; i++ {
		count := 100 + (i * 200 / 600)
		ld.mu.Lock()
		ld.samples = append(ld.samples, goroutineSample{
			count:     count,
			timestamp: now.Add(-50*time.Minute + time.Duration(i)*5*time.Second),
		})
		ld.mu.Unlock()
	}

	ld.addSample(300)

	if !ld.isLeaking() {
		t.Error("expected leak detection with ~240/hr growth rate")
	}

	rate := ld.growthRate()
	if rate < 100 {
		t.Errorf("expected high growth rate, got %.1f/hr", rate)
	}
}

func TestLeakDetector_InsufficientData(t *testing.T) {
	ld := &LeakDetector{
		threshold: 100,
		samples:   make([]goroutineSample, 0),
	}

	// Only one sample — not enough to detect
	ld.addSample(100)

	if ld.isLeaking() {
		t.Error("expected no leak with only 1 sample")
	}
	if ld.growthRate() != 0 {
		t.Error("expected 0 growth rate with insufficient data")
	}
}

func TestLeakDetector_TrimsSamplesToOneHour(t *testing.T) {
	ld := &LeakDetector{
		threshold: 100,
		samples:   make([]goroutineSample, 0),
	}

	now := time.Now()
	// Add samples spanning 2 hours
	for i := 0; i < 1440; i++ {
		ts := now.Add(-2*time.Hour + time.Duration(i)*5*time.Second)
		ld.mu.Lock()
		ld.samples = append(ld.samples, goroutineSample{count: 50, timestamp: ts})
		ld.mu.Unlock()
	}

	// Adding a new sample should trim old ones
	ld.addSample(50)

	ld.mu.RLock()
	defer ld.mu.RUnlock()

	if len(ld.samples) > 730 { // ~1 hour at 5s intervals + some tolerance
		t.Errorf("expected samples trimmed to ~1hr, got %d samples", len(ld.samples))
	}
}

func TestCollectSystemInfo(t *testing.T) {
	info := collectSystemInfo()

	if info.GoVersion == "" {
		t.Error("expected non-empty GoVersion")
	}
	if info.GOOS == "" {
		t.Error("expected non-empty GOOS")
	}
	if info.GOARCH == "" {
		t.Error("expected non-empty GOARCH")
	}
	if info.Compiler == "" {
		t.Error("expected non-empty Compiler")
	}
	if info.NumCPU == 0 {
		t.Error("expected non-zero NumCPU")
	}
	if info.PID == 0 {
		t.Error("expected non-zero PID")
	}
}
