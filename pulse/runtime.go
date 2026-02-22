package pulse

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// SystemInfo holds static build and system information collected at startup.
type SystemInfo struct {
	GoVersion    string `json:"go_version"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	NumCPU       int    `json:"num_cpu"`
	Compiler     string `json:"compiler"`
	PID          int    `json:"pid"`
	Hostname     string `json:"hostname"`
	BuildVersion string `json:"build_version,omitempty"`
	BuildTime    string `json:"build_time,omitempty"`
	VCSRevision  string `json:"vcs_revision,omitempty"`
	VCSTime      string `json:"vcs_time,omitempty"`
	VCSModified  bool   `json:"vcs_modified,omitempty"`
}

// LeakDetector tracks goroutine counts over time to detect leaks.
type LeakDetector struct {
	mu        sync.RWMutex
	samples   []goroutineSample
	threshold int // goroutines per hour to flag
	leaking   bool
}

type goroutineSample struct {
	count     int
	timestamp time.Time
}

// RuntimeSampler collects Go runtime metrics on a background goroutine.
type RuntimeSampler struct {
	pulse        *Pulse
	systemInfo   SystemInfo
	leakDetector *LeakDetector
}

// newRuntimeSampler creates and starts the runtime metrics sampler.
func newRuntimeSampler(p *Pulse) *RuntimeSampler {
	rs := &RuntimeSampler{
		pulse:      p,
		systemInfo: collectSystemInfo(),
		leakDetector: &LeakDetector{
			threshold: p.config.Runtime.LeakThreshold,
			samples:   make([]goroutineSample, 0, 720), // 1 hour at 5s intervals
		},
	}

	interval := p.config.Runtime.SampleInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}

	p.startBackground("runtime-sampler", func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Collect an initial sample immediately
		rs.sample()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rs.sample()
			}
		}
	})

	return rs
}

// sample collects a single runtime metric snapshot.
func (rs *RuntimeSampler) sample() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	numGoroutines := runtime.NumGoroutine()

	// Compute last GC pause
	var lastGCPauseNs uint64
	if memStats.NumGC > 0 {
		// PauseNs is a circular buffer of recent GC pause durations
		idx := (memStats.NumGC + 255) % 256
		lastGCPauseNs = memStats.PauseNs[idx]
	}

	metric := RuntimeMetric{
		HeapAlloc:     memStats.HeapAlloc,
		HeapInUse:     memStats.HeapInuse,
		HeapObjects:   memStats.HeapObjects,
		StackInUse:    memStats.StackInuse,
		TotalAlloc:    memStats.TotalAlloc,
		Sys:           memStats.Sys,
		NumGoroutine:  numGoroutines,
		GCPauseNs:     lastGCPauseNs,
		NumGC:         memStats.NumGC,
		GCCPUFraction: memStats.GCCPUFraction,
		Timestamp:     time.Now(),
	}

	// Store (fire-and-forget, don't block sampler)
	if err := rs.pulse.storage.StoreRuntime(metric); err != nil && rs.pulse.config.DevMode {
		rs.pulse.logger.Printf("[pulse] failed to store runtime metric: %v", err)
	}

	// Broadcast runtime metrics to WebSocket clients
	rs.pulse.BroadcastRuntime(metric)

	// Feed leak detector
	rs.leakDetector.addSample(numGoroutines)
}

// GetSystemInfo returns static system information.
func (rs *RuntimeSampler) GetSystemInfo() SystemInfo {
	return rs.systemInfo
}

// IsLeaking returns whether a goroutine leak has been detected.
func (rs *RuntimeSampler) IsLeaking() bool {
	return rs.leakDetector.isLeaking()
}

// GoroutineGrowthRate returns the estimated goroutine growth per hour.
func (rs *RuntimeSampler) GoroutineGrowthRate() float64 {
	return rs.leakDetector.growthRate()
}

// --- Leak Detector ---

func (ld *LeakDetector) addSample(count int) {
	ld.mu.Lock()
	defer ld.mu.Unlock()

	now := time.Now()
	ld.samples = append(ld.samples, goroutineSample{count: count, timestamp: now})

	// Keep only last hour of samples
	cutoff := now.Add(-1 * time.Hour)
	start := 0
	for start < len(ld.samples) && ld.samples[start].timestamp.Before(cutoff) {
		start++
	}
	if start > 0 {
		ld.samples = ld.samples[start:]
	}

	// Need at least 2 samples spanning 10+ minutes to detect a leak
	if len(ld.samples) < 2 {
		return
	}

	first := ld.samples[0]
	last := ld.samples[len(ld.samples)-1]
	elapsed := last.timestamp.Sub(first.timestamp)

	if elapsed < 10*time.Minute {
		return
	}

	// Calculate growth rate per hour
	growth := last.count - first.count
	hoursElapsed := elapsed.Hours()
	ratePerHour := float64(growth) / hoursElapsed

	ld.leaking = ratePerHour >= float64(ld.threshold)
}

func (ld *LeakDetector) isLeaking() bool {
	ld.mu.RLock()
	defer ld.mu.RUnlock()
	return ld.leaking
}

func (ld *LeakDetector) growthRate() float64 {
	ld.mu.RLock()
	defer ld.mu.RUnlock()

	if len(ld.samples) < 2 {
		return 0
	}

	first := ld.samples[0]
	last := ld.samples[len(ld.samples)-1]
	elapsed := last.timestamp.Sub(first.timestamp)

	if elapsed < time.Minute {
		return 0
	}

	growth := last.count - first.count
	return float64(growth) / elapsed.Hours()
}

// --- System Info ---

func collectSystemInfo() SystemInfo {
	info := SystemInfo{
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		Compiler:  runtime.Compiler,
		PID:       os.Getpid(),
	}

	if hostname, err := os.Hostname(); err == nil {
		info.Hostname = hostname
	}

	// Extract build info from debug.ReadBuildInfo
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.BuildVersion = bi.Main.Version
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.VCSRevision = s.Value
			case "vcs.time":
				info.VCSTime = s.Value
			case "vcs.modified":
				info.VCSModified = s.Value == "true"
			}
		}
	}

	return info
}
