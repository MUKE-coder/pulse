// USE method dashboard — Utilization / Saturation / Errors per resource.
// Implements GitHub issue #3.
//
// The layout follows Brendan Gregg's USE methodology
// (https://brendangregg.com/usemethod.html): for every system resource,
// walk the same three questions — how busy is it, is anything queued behind
// it, and is it returning errors. Pulse surfaces that triple as a colour-
// coded grid so the eye lands on the bottleneck.
package pulse

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
)

// Level represents the health band of a single USE metric value.
type Level string

const (
	LevelGreen   Level = "green"
	LevelAmber   Level = "amber"
	LevelRed     Level = "red"
	LevelUnknown Level = "unknown"
)

// USESnapshot is the top-level payload returned by GET /pulse/api/use.
type USESnapshot struct {
	Timestamp time.Time     `json:"timestamp"`
	Host      string        `json:"host,omitempty"`
	OS        string        `json:"os,omitempty"`
	Resources []ResourceUSE `json:"resources"`
	// Sources lists which resources we could collect data for. Useful when
	// running in a container where /proc/diskstats etc. may be sparse.
	Sources map[string]string `json:"sources,omitempty"`
}

// ResourceUSE is one row of the U/S/E grid.
type ResourceUSE struct {
	Name        string    `json:"name"`        // CPU, Memory, Disk, ...
	Utilization USEMetric `json:"utilization"`
	Saturation  USEMetric `json:"saturation"`
	Errors      USEMetric `json:"errors"`
}

// USEMetric is a single cell in the U/S/E grid. Display is a pre-formatted
// human string so the dashboard does not need to know about units.
type USEMetric struct {
	Value       float64 `json:"value"`
	Display     string  `json:"display"`
	Level       Level   `json:"level"`
	Description string  `json:"description"`
}

// useSampler is the background goroutine that periodically polls all
// resources and updates the cached snapshot served by /pulse/api/use.
type useSampler struct {
	pulse *Pulse

	mu       sync.RWMutex
	snapshot USESnapshot

	// Cached host info — calling cpu.Info or host.Info on every tick is
	// pointless and on some platforms expensive.
	cpuCount int
	hostname string
	osLabel  string

	// Network throughput is a delta calculation. Keep the previous sample.
	prevNet     []psnet.IOCountersStat
	prevNetTime time.Time
}

func newUSESampler(p *Pulse) *useSampler {
	s := &useSampler{pulse: p}
	s.cacheHostInfo()

	interval := 5 * time.Second
	if p.config.DevMode {
		interval = 2 * time.Second
	}

	p.startBackground("use-sampler", func(ctx context.Context) {
		// One immediate tick so /pulse/api/use isn't empty on first request.
		s.sample()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sample()
			}
		}
	})

	return s
}

func (s *useSampler) cacheHostInfo() {
	s.cpuCount = runtime.NumCPU()
	s.osLabel = runtime.GOOS + "/" + runtime.GOARCH
	if name, err := os.Hostname(); err == nil {
		s.hostname = name
	}
}

// Snapshot returns the most recent USE grid. Safe for concurrent use.
func (s *useSampler) Snapshot() USESnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *useSampler) sample() {
	now := time.Now()
	sources := map[string]string{}

	resources := []ResourceUSE{
		s.sampleCPU(sources),
		s.sampleMemory(sources),
		s.sampleDisk(sources),
		s.sampleNetwork(now, sources),
		s.sampleDBPool(sources),
		s.sampleGoroutines(sources),
	}

	s.mu.Lock()
	s.snapshot = USESnapshot{
		Timestamp: now,
		Host:      s.hostname,
		OS:        s.osLabel,
		Resources: resources,
		Sources:   sources,
	}
	s.mu.Unlock()
}

// --- per-resource samplers ---

func (s *useSampler) sampleCPU(src map[string]string) ResourceUSE {
	r := ResourceUSE{Name: "CPU"}

	// Utilization: % busy averaged over a 1s window.
	if pct, err := cpu.Percent(time.Second, false); err == nil && len(pct) > 0 {
		v := pct[0]
		r.Utilization = USEMetric{
			Value: v, Display: fmt.Sprintf("%.1f%%", v),
			Description: "Aggregate CPU utilisation across all cores (1s window).",
			Level:       band(v, 60, 85),
		}
		src["cpu"] = "gopsutil/v4/cpu"
	} else {
		r.Utilization = unknownMetric("CPU utilisation unavailable on this platform.")
	}

	// Saturation: 1-minute load average normalised by CPU count.
	if avg, err := load.Avg(); err == nil && s.cpuCount > 0 {
		norm := avg.Load1 / float64(s.cpuCount)
		r.Saturation = USEMetric{
			Value: norm, Display: fmt.Sprintf("load1=%.2f (norm %.2fx)", avg.Load1, norm),
			Description: "Run-queue length per CPU. >1 means tasks are queueing.",
			Level:       band(norm, 1, 2),
		}
	} else {
		r.Saturation = unknownMetric("Load average not exposed on this platform (e.g. Windows).")
	}

	// Errors: surfacing CPU stalls would need perf counters; mark unknown.
	r.Errors = unknownMetric("CPU stall counters require perf-counter access.")
	return r
}

func (s *useSampler) sampleMemory(src map[string]string) ResourceUSE {
	r := ResourceUSE{Name: "Memory"}

	if v, err := mem.VirtualMemory(); err == nil {
		used := v.UsedPercent
		r.Utilization = USEMetric{
			Value: used, Display: fmt.Sprintf("%.1f%%", used),
			Description: "Host memory used as a percentage of total.",
			Level:       band(used, 70, 90),
		}
		src["memory"] = "gopsutil/v4/mem"
	} else {
		r.Utilization = unknownMetric("Memory utilisation unavailable.")
	}

	// Saturation: most recent GC pause (in ms) from the runtime sampler.
	if hist, _ := s.pulse.storage.GetRuntimeHistory(Last5m()); len(hist) > 0 {
		latest := hist[len(hist)-1]
		pauseMs := float64(latest.GCPauseNs) / float64(time.Millisecond)
		r.Saturation = USEMetric{
			Value: pauseMs, Display: fmt.Sprintf("GC %s", time.Duration(latest.GCPauseNs)),
			Description: "Most recent GC pause. Long pauses == allocation/GC pressure.",
			Level:       band(pauseMs, 1, 10),
		}
	} else {
		r.Saturation = unknownMetric("No runtime samples yet.")
	}

	// Errors: gopsutil doesn't expose OOM counters portably. Container
	// orchestrators surface them out-of-band; mark unknown for now.
	r.Errors = unknownMetric("OOM events tracked by the container runtime, not by Pulse.")
	return r
}

func (s *useSampler) sampleDisk(src map[string]string) ResourceUSE {
	r := ResourceUSE{Name: "Disk"}

	// Utilization: percent used on the busiest filesystem.
	parts, err := disk.Partitions(false)
	if err != nil || len(parts) == 0 {
		r.Utilization = unknownMetric("No partitions reported.")
		r.Saturation = unknownMetric("Disk I/O queue depth unavailable.")
		r.Errors = unknownMetric("Disk I/O error counters unavailable.")
		return r
	}
	var worstPct float64
	var worstMount string
	for _, p := range parts {
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u == nil {
			continue
		}
		if u.UsedPercent > worstPct {
			worstPct = u.UsedPercent
			worstMount = p.Mountpoint
		}
	}
	if worstMount != "" {
		r.Utilization = USEMetric{
			Value: worstPct, Display: fmt.Sprintf("%.1f%% (%s)", worstPct, worstMount),
			Description: "Used space on the fullest mounted filesystem.",
			Level:       band(worstPct, 80, 95),
		}
		src["disk"] = "gopsutil/v4/disk"
	} else {
		r.Utilization = unknownMetric("No filesystem usage data.")
	}

	// Saturation: I/O queue depth from io counters. Not all platforms
	// expose this (notably Windows in some configurations).
	if iostat, err := disk.IOCounters(); err == nil && len(iostat) > 0 {
		var maxQ uint64
		for _, c := range iostat {
			if c.IopsInProgress > maxQ {
				maxQ = c.IopsInProgress
			}
		}
		r.Saturation = USEMetric{
			Value: float64(maxQ), Display: fmt.Sprintf("%d in-flight", maxQ),
			Description: "Max in-flight I/O ops across mounted disks.",
			Level:       band(float64(maxQ), 4, 16),
		}
	} else {
		r.Saturation = unknownMetric("Disk queue depth unavailable on this platform.")
	}

	// Errors: I/O errors per disk if exposed.
	r.Errors = unknownMetric("Per-disk I/O errors not exposed by gopsutil cross-platform.")
	return r
}

func (s *useSampler) sampleNetwork(now time.Time, src map[string]string) ResourceUSE {
	r := ResourceUSE{Name: "Network"}

	curr, err := psnet.IOCounters(false)
	if err != nil || len(curr) == 0 {
		r.Utilization = unknownMetric("Network counters unavailable.")
		r.Saturation = unknownMetric("Network counters unavailable.")
		r.Errors = unknownMetric("Network counters unavailable.")
		return r
	}
	src["network"] = "gopsutil/v4/net"

	// Utilization: total throughput (bytes/sec) since last sample.
	// First call has no baseline — surface "warming up".
	if s.prevNet == nil || s.prevNetTime.IsZero() {
		r.Utilization = warmingUpMetric("Need two samples to compute throughput.")
		r.Saturation = USEMetric{Value: 0, Display: "0 retransmits/sec", Description: "Per-interval TCP retransmissions.", Level: LevelGreen}
		r.Errors = USEMetric{Value: 0, Display: "0 errors", Description: "Cumulative receive/transmit errors.", Level: LevelGreen}
	} else {
		dt := now.Sub(s.prevNetTime).Seconds()
		if dt <= 0 {
			dt = 1
		}
		bytesDelta := float64(curr[0].BytesSent+curr[0].BytesRecv) -
			float64(s.prevNet[0].BytesSent+s.prevNet[0].BytesRecv)
		mbps := (bytesDelta / dt) * 8 / 1e6
		if mbps < 0 {
			mbps = 0
		}
		r.Utilization = USEMetric{
			Value: mbps, Display: fmt.Sprintf("%.2f Mbps", mbps),
			Description: "Aggregate bandwidth across all interfaces.",
			// No principled threshold without knowing the link speed; treat
			// >800 Mbps as amber and >1200 as red as a heuristic that errs on
			// the safe side for typical cloud workloads.
			Level: band(mbps, 800, 1200),
		}

		// Saturation: change in network errors since last sample. Errors
		// here include receive errors and drops, but not TCP retransmits
		// (gopsutil doesn't expose those cross-platform).
		errDelta := float64(curr[0].Errin+curr[0].Errout+curr[0].Dropin+curr[0].Dropout) -
			float64(s.prevNet[0].Errin+s.prevNet[0].Errout+s.prevNet[0].Dropin+s.prevNet[0].Dropout)
		if errDelta < 0 {
			errDelta = 0
		}
		r.Saturation = USEMetric{
			Value: errDelta / dt, Display: fmt.Sprintf("%.0f drops/sec", errDelta/dt),
			Description: "Receive errors + drops/sec.",
			Level:       band(errDelta/dt, 1, 10),
		}
		r.Errors = USEMetric{
			Value: float64(curr[0].Errin + curr[0].Errout),
			Display: fmt.Sprintf("%d total errors", curr[0].Errin+curr[0].Errout),
			Description: "Cumulative interface errors since boot.",
			Level: levelFromError(curr[0].Errin + curr[0].Errout),
		}
	}

	s.prevNet = curr
	s.prevNetTime = now
	return r
}

func (s *useSampler) sampleDBPool(src map[string]string) ResourceUSE {
	r := ResourceUSE{Name: "DB pool"}
	pool, _ := s.pulse.storage.GetConnectionPoolStats()
	if pool == nil {
		r.Utilization = unknownMetric("No database configured.")
		r.Saturation = unknownMetric("No database configured.")
		r.Errors = unknownMetric("No database configured.")
		return r
	}
	src["db"] = "database/sql.Stats"

	var pct float64
	if pool.MaxOpenConnections > 0 {
		pct = float64(pool.InUse) / float64(pool.MaxOpenConnections) * 100
	}
	r.Utilization = USEMetric{
		Value: pct, Display: fmt.Sprintf("%d / %d (%.0f%%)", pool.InUse, pool.MaxOpenConnections, pct),
		Description: "Connections in use vs configured pool max.",
		Level:       band(pct, 70, 90),
	}

	r.Saturation = USEMetric{
		Value: float64(pool.WaitCount), Display: fmt.Sprintf("%d total waits", pool.WaitCount),
		Description: "Total connection-wait events since startup.",
		Level:       levelFromError(pool.WaitCount),
	}

	// pool stats don't expose error counts directly — surface a dash but
	// still distinguish "we have a pool" from "no pool".
	r.Errors = USEMetric{
		Value: 0, Display: "—",
		Description: "Pool-exhaustion errors are visible in the Errors page.",
		Level:       LevelGreen,
	}
	return r
}

func (s *useSampler) sampleGoroutines(src map[string]string) ResourceUSE {
	r := ResourceUSE{Name: "Goroutines"}
	n := runtime.NumGoroutine()
	r.Utilization = USEMetric{
		Value: float64(n), Display: fmt.Sprintf("%d", n),
		Description: "Live goroutine count.",
		Level:       band(float64(n), 500, 5000),
	}
	src["goroutines"] = "runtime.NumGoroutine"

	// Saturation: growth rate as measured by the runtime sampler (per hour).
	if s.pulse.runtimeSampler != nil {
		rate := s.pulse.runtimeSampler.GoroutineGrowthRate()
		r.Saturation = USEMetric{
			Value: rate, Display: fmt.Sprintf("%+0.0f/hr", rate),
			Description: "Goroutine count growth rate. Sustained growth suggests a leak.",
			Level:       band(rate, 50, 200),
		}
	} else {
		r.Saturation = unknownMetric("Runtime sampler disabled.")
	}

	// Errors: panics survive into the Errors page; surface count there.
	r.Errors = USEMetric{
		Value: 0, Display: "see Errors",
		Description: "Panics are tracked on the Errors page (error_type=panic).",
		Level:       LevelGreen,
	}
	return r
}

// --- helpers ---

// band returns a Level given a value and the green/amber thresholds. Values
// below amberFrom are green, [amberFrom, redFrom) are amber, >= redFrom are
// red. Use this for "lower is better" metrics (utilisation, error rates).
func band(value, amberFrom, redFrom float64) Level {
	switch {
	case value >= redFrom:
		return LevelRed
	case value >= amberFrom:
		return LevelAmber
	default:
		return LevelGreen
	}
}

func levelFromError[T uint64 | int64 | int](n T) Level {
	if n > 0 {
		return LevelAmber
	}
	return LevelGreen
}

func unknownMetric(desc string) USEMetric {
	return USEMetric{Display: "—", Level: LevelUnknown, Description: desc}
}

func warmingUpMetric(desc string) USEMetric {
	return USEMetric{Display: "warming up", Level: LevelUnknown, Description: desc}
}

