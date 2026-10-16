// Profile sampling + flame graph rendering.
//
// Two modes, because they answer different questions:
//
//   - CPU: a runtime/pprof CPU profile, folded with each sample weighted by
//     the time it stands for. This is what a flame graph is usually read
//     for — which code is burning the machine.
//   - Goroutines: stacks sampled from runtime.GoroutineProfile at a chosen
//     rate. Good for finding leaks and stuck workers, but a goroutine
//     parked in a network read counts on every tick while using no CPU, so
//     parked stacks are folded away unless asked for.
//
// Either way the samples are folded into the standard
// "stack;trace;path count" format that Brendan Gregg's flamegraph.pl
// understands, then rendered to SVG directly so callers don't need Perl or
// any external tooling installed.
package pulse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/pprof/profile"
	"time"
)

// ProfileEnabledEnv is the environment variable that gates the profile
// endpoint at runtime. Pulse refuses to start sampling unless this is set
// to a truthy value AND Config.Profiling.Enabled is true. Two locks make
// it harder to accidentally enable profiling in production via a config
// change alone.
const ProfileEnabledEnv = "PULSE_PROFILE_ENABLED"

// errProfilingDisabled is returned by [profileSampler.Sample] when the
// caller asks for a profile but the engine is not in a state to produce one
// (either the config field is false or the env var isn't set).
var errProfilingDisabled = errors.New("profiling is disabled — set Config.Profiling.Enabled and PULSE_PROFILE_ENABLED=true")

// errProfileInFlight is returned by [profileSampler.Sample] when another
// sample window is already running. The API maps it to 409 Conflict.
var errProfileInFlight = errors.New("a profile window is already in flight; try again in a moment")

// FlameNode is one node in the folded stack tree.
type FlameNode struct {
	Name     string                `json:"name"`
	Value    int                   `json:"value"`
	Children map[string]*FlameNode `json:"-"`
}

// profileSampler implements on-demand CPU profile sampling. It is created
// at Mount time but does not start any background goroutines — sampling
// only runs when the API endpoint is hit.
type profileSampler struct {
	pulse *Pulse

	// running ensures only one sample window is active at a time. Concurrent
	// requests get 409 Conflict — running a second profile while one is in flight
	// can produce confusing overlapping data and roughly doubles CPU cost.
	running atomic.Bool

	// last is the most recent folded sample tree, cached for the duration of
	// LastWindow. Subsequent renders reuse it instead of re-sampling.
	mu         sync.RWMutex
	last       *FlameNode
	lastTaken  time.Time
	lastDur    time.Duration
	lastHz     int
	lastFrames int
}

func newProfileSampler(p *Pulse) *profileSampler {
	return &profileSampler{pulse: p}
}

// Sample runs a sampling window of the requested duration at the given
// rate, returning the folded tree. Returns errProfilingDisabled when not
// permitted, and errProfileInFlight when a window is already in flight.
//
// Sampling is best-effort: GoroutineProfile gives us the call stack of
// every live goroutine, not just the currently-on-CPU one. We treat each
// recorded frame as one sample. The resulting flame graph emphasises hot
// code paths rather than literal CPU time — which, per Brendan Gregg, is
// usually what operators want when chasing a regression.
func (s *profileSampler) Sample(ctx context.Context, duration time.Duration, hz int) (*FlameNode, error) {
	return s.SampleGoroutines(ctx, duration, hz, false)
}

// SampleGoroutines samples goroutine stacks. With includeParked false —
// the default the dashboard uses — goroutines sitting in a network read, a
// channel or a lock are left out, because they contribute a sample on
// every tick while consuming nothing, and they are what buries the working
// code in a goroutine flame graph.
func (s *profileSampler) SampleGoroutines(ctx context.Context, duration time.Duration, hz int, includeParked bool) (*FlameNode, error) {
	if s == nil || !profilingPermitted(s.pulse.config) {
		return nil, errProfilingDisabled
	}
	if !s.running.CompareAndSwap(false, true) {
		return nil, errProfileInFlight
	}
	defer s.running.Store(false)

	if duration <= 0 {
		duration = s.pulse.config.Profiling.DefaultDuration
	}
	if hz <= 0 {
		hz = s.pulse.config.Profiling.SampleHz
	}
	if hz <= 0 {
		hz = 100
	}
	if duration > s.pulse.config.Profiling.MaxDuration && s.pulse.config.Profiling.MaxDuration > 0 {
		duration = s.pulse.config.Profiling.MaxDuration
	}

	interval := time.Second / time.Duration(hz)
	deadline := time.Now().Add(duration)

	root := &FlameNode{Name: "all", Children: map[string]*FlameNode{}}
	frames := 0

	// Pre-size the goroutine record buffer once. NumGoroutine + slack.
	buf := make([]runtime.StackRecord, runtime.NumGoroutine()+64)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Grow the buffer if needed (handles bursts of new goroutines).
		n, ok := runtime.GoroutineProfile(buf)
		for !ok {
			buf = make([]runtime.StackRecord, n*2+64)
			n, ok = runtime.GoroutineProfile(buf)
		}

		for i := 0; i < n; i++ {
			stack := decodeStack(buf[i].Stack())
			if len(stack) == 0 {
				continue
			}
			if !includeParked && isParkedStack(stack) {
				continue
			}
			// The goroutine taking the profile is this one. Counting it
			// makes Pulse's own sampling loop the widest tower in its own
			// flame graph.
			if isSamplingStack(stack) {
				continue
			}
			addStackToTree(root, stack)
			frames += len(stack)
		}

		time.Sleep(interval)
	}

	s.mu.Lock()
	s.last = root
	s.lastTaken = time.Now()
	s.lastDur = duration
	s.lastHz = hz
	s.lastFrames = frames
	s.mu.Unlock()

	return root, nil
}

// SampleCPU runs a CPU profile for the requested duration and folds it into
// the same tree the goroutine sampler produces, weighted by CPU time rather
// than by how many goroutines happened to be sitting in a frame.
//
// This is what a flame graph is usually read for: a goroutine parked in a
// network read for the whole window shows up as the widest tower in a
// goroutine profile while using no CPU at all, which buries the code that
// is actually burning the machine (see issue #10).
func (s *profileSampler) SampleCPU(ctx context.Context, duration time.Duration) (*FlameNode, error) {
	if s == nil || !profilingPermitted(s.pulse.config) {
		return nil, errProfilingDisabled
	}
	if !s.running.CompareAndSwap(false, true) {
		return nil, errProfileInFlight
	}
	defer s.running.Store(false)

	duration = s.clampDuration(duration)

	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		// Something else in the process holds the CPU profiler — the
		// runtime allows only one at a time.
		return nil, fmt.Errorf("%w: %v", errProfileInFlight, err)
	}

	select {
	case <-ctx.Done():
		pprof.StopCPUProfile()
		return nil, ctx.Err()
	case <-time.After(duration):
		pprof.StopCPUProfile()
	}

	root, frames, err := foldCPUProfile(buf.Bytes())
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.last = root
	s.lastTaken = time.Now()
	s.lastDur = duration
	s.lastHz = 0 // the runtime picks the CPU sampling rate, nominally 100Hz
	s.lastFrames = frames
	s.mu.Unlock()

	return root, nil
}

// clampDuration applies the configured default and maximum.
func (s *profileSampler) clampDuration(duration time.Duration) time.Duration {
	if duration <= 0 {
		duration = s.pulse.config.Profiling.DefaultDuration
	}
	if duration <= 0 {
		duration = 5 * time.Second
	}
	if max := s.pulse.config.Profiling.MaxDuration; max > 0 && duration > max {
		duration = max
	}
	return duration
}

// foldCPUProfile turns a pprof protobuf into the flame tree. Each sample
// carries the nanoseconds it accounts for, so the widths are CPU time.
func foldCPUProfile(raw []byte) (*FlameNode, int, error) {
	prof, err := profile.ParseData(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("parse cpu profile: %w", err)
	}

	// A CPU profile has two values per sample: count, then nanoseconds.
	valueIndex := 0
	for i, st := range prof.SampleType {
		if st.Type == "cpu" && st.Unit == "nanoseconds" {
			valueIndex = i
		}
	}

	root := &FlameNode{Name: "all", Children: map[string]*FlameNode{}}
	frames := 0
	for _, sample := range prof.Sample {
		if valueIndex >= len(sample.Value) {
			continue
		}
		weight := int(sample.Value[valueIndex] / int64(time.Microsecond)) // µs of CPU
		if weight <= 0 {
			continue
		}
		// pprof lists locations leaf-first; the tree wants root-first.
		stack := make([]string, 0, len(sample.Location))
		for i := len(sample.Location) - 1; i >= 0; i-- {
			for j := len(sample.Location[i].Line) - 1; j >= 0; j-- {
				fn := sample.Location[i].Line[j].Function
				if fn == nil || skipFlameFrame(fn.Name) {
					continue
				}
				stack = append(stack, shortenFunctionName(fn.Name))
			}
		}
		if len(stack) == 0 {
			continue
		}
		addStackToTreeWeighted(root, stack, weight)
		frames += len(stack)
	}
	return root, frames, nil
}

// Cached returns the most recent sample tree if it was taken within
// maxAge, otherwise nil.
func (s *profileSampler) Cached(maxAge time.Duration) *FlameNode {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.last == nil || time.Since(s.lastTaken) > maxAge {
		return nil
	}
	return s.last
}

// --- stack decoding ---

// decodeStack converts a slice of PCs into a leaf-to-root list of function
// names. Pulse's own frames and runtime scheduler frames are dropped so the
// flame graph foregrounds user code.
func decodeStack(pcs []uintptr) []string {
	if len(pcs) == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs)
	out := make([]string, 0, len(pcs))
	for {
		f, more := frames.Next()
		if !skipFlameFrame(f.Function) {
			out = append(out, shortenFunctionName(f.Function))
		}
		if !more {
			break
		}
	}
	// Reverse so root-to-leaf order matches flame-graph convention.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// skipFlameFrame drops noisy frames that obscure the interesting code paths.
// Different from shouldSkipFrame in errors.go because that one drops less
// (we keep stdlib frames there for stack-trace debugging).
func skipFlameFrame(fn string) bool {
	if fn == "" {
		return true
	}
	switch fn {
	case "runtime.goexit", "runtime.main", "runtime.gopark", "runtime.systemstack_switch":
		return true
	}
	// Pulse's own sampling loop — uninteresting.
	if strings.HasPrefix(fn, "github.com/MUKE-coder/pulse/pulse.(*profileSampler).") {
		return true
	}
	return false
}

func shortenFunctionName(fn string) string {
	// Trim repo path so labels read like "handler.GetUsers" rather than
	// "github.com/acme/api/internal/handler.GetUsers". Keeps SVG readable.
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		fn = fn[i+1:]
	}
	return fn
}

func addStackToTree(root *FlameNode, stack []string) {
	addStackToTreeWeighted(root, stack, 1)
}

// addStackToTreeWeighted adds a stack worth more than one sample, which is
// what a CPU profile gives: each sample carries the nanoseconds it stands
// for, so a frame's width is time rather than a count of observations.
func addStackToTreeWeighted(root *FlameNode, stack []string, weight int) {
	if weight <= 0 {
		return
	}
	cur := root
	cur.Value += weight
	for _, name := range stack {
		child, ok := cur.Children[name]
		if !ok {
			child = &FlameNode{Name: name, Children: map[string]*FlameNode{}}
			cur.Children[name] = child
		}
		child.Value += weight
		cur = child
	}
}

// parkedLeaves are the runtime frames a goroutine sits in while it is
// waiting for something: a network read, a channel, a lock, a timer. A
// goroutine parked in one of these for the whole window contributes a
// sample on every tick while using no CPU at all, which is why they
// dominate a goroutine flame graph and why folding them is the default.
var parkedLeaves = []string{
	"netpollblock", "runtime_pollWait", "gopark", "selectgo", "chanrecv",
	"chansend", "notesleep", "semacquire", "Semacquire", "futexsleep",
	"runtime_Semacquire", "runtime_SemacquireMutex", "stopTheWorld",
	"timerproc", "sigNoteSleep", "epollwait", "kevent", "usleep",
}

// isSamplingStack reports whether a stack belongs to the request that is
// taking this profile.
func isSamplingStack(stack []string) bool {
	for i := len(stack) - 1; i >= 0 && i >= len(stack)-4; i-- {
		if strings.Contains(stack[i], "GoroutineProfile") || strings.Contains(stack[i], "goroutineProfile") {
			return true
		}
	}
	return false
}

// isParkedStack reports whether a stack's deepest frames are a goroutine
// waiting rather than working.
func isParkedStack(stack []string) bool {
	if len(stack) == 0 {
		return false
	}
	// Look at the last few frames: the leaf is the park itself, and the
	// one above it is usually the runtime helper that parked it.
	for i := len(stack) - 1; i >= 0 && i >= len(stack)-3; i-- {
		for _, parked := range parkedLeaves {
			if strings.Contains(stack[i], parked) {
				return true
			}
		}
	}
	return false
}

// --- folded format ---

// Folded returns the standard "frame;frame;frame count" line-per-sample
// format consumed by Brendan Gregg's flamegraph.pl. Provided so callers can
// pipe Pulse profiles into other tooling.
func Folded(root *FlameNode) string {
	if root == nil {
		return ""
	}
	var b strings.Builder
	var walk func(n *FlameNode, prefix string)
	walk = func(n *FlameNode, prefix string) {
		// Sum of children — anything left over is "self time" recorded at
		// this frame and shows up as a leaf in the folded output.
		var childSum int
		names := make([]string, 0, len(n.Children))
		for k := range n.Children {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			c := n.Children[k]
			path := prefix
			if c.Name != "" {
				if path != "" {
					path += ";"
				}
				path += c.Name
			}
			walk(c, path)
			childSum += c.Value
		}
		self := n.Value - childSum
		if self > 0 && prefix != "" {
			b.WriteString(prefix)
			b.WriteByte(' ')
			b.WriteString(strconv.Itoa(self))
			b.WriteByte('\n')
		}
	}
	walk(root, "")
	return b.String()
}

// --- SVG renderer ---

// FlameGraphSVG renders the folded tree as an interactive SVG flame graph.
// Layout follows the standard convention: root at the bottom, leaves at the
// top, x-axis ordered alphabetically inside each parent's bucket, width
// proportional to sample count.
//
// width is the SVG canvas width in pixels; cellHeight is the height of one
// stack frame. Height grows with stack depth.
func FlameGraphSVG(root *FlameNode, width, cellHeight int) string {
	if root == nil || root.Value == 0 {
		return emptyFlameSVG(width)
	}
	if width <= 0 {
		width = 1200
	}
	if cellHeight <= 0 {
		cellHeight = 16
	}

	depth := flameDepth(root)
	if depth == 0 {
		depth = 1
	}
	totalHeight := (depth + 1) * cellHeight

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" `+
		`viewBox="0 0 %d %d" font-family="Verdana, monospace" font-size="11">`,
		width, totalHeight, width, totalHeight)
	b.WriteString(`<style>
		.f rect { stroke: rgba(0,0,0,0.15); }
		.f rect:hover { stroke: #000; stroke-width: 1; }
		.f text { pointer-events: none; fill: #111; }
		.bg { fill: #f7f7f0; }
	</style>`)
	fmt.Fprintf(&b, `<rect class="bg" x="0" y="0" width="%d" height="%d"/>`, width, totalHeight)
	b.WriteString(`<g class="f">`)

	scale := float64(width) / float64(root.Value)
	renderFlameNode(&b, root, 0, float64(totalHeight-cellHeight), scale, cellHeight, 0)

	b.WriteString(`</g></svg>`)
	return b.String()
}

func renderFlameNode(b *strings.Builder, n *FlameNode, x, y float64, scale float64, cellHeight, depth int) {
	width := float64(n.Value) * scale
	if width < 0.1 {
		return
	}
	color := flameColor(n.Name)
	label := n.Name
	if label == "" {
		label = "(root)"
	}
	// Self-cost is what's left after subtracting children. Helpful in
	// tooltips when comparing frames at similar visual width.
	var childSum int
	for _, c := range n.Children {
		childSum += c.Value
	}
	self := n.Value - childSum
	title := fmt.Sprintf("%s — %d samples (%d self)", label, n.Value, self)

	fmt.Fprintf(b,
		`<g><title>%s</title><rect x="%.2f" y="%.2f" width="%.2f" height="%d" fill="%s"/>`,
		html.EscapeString(title), x, y, width, cellHeight, color)
	if width > 50 {
		text := truncateLabel(label, int(width)/6)
		fmt.Fprintf(b,
			`<text x="%.2f" y="%.2f">%s</text>`,
			x+3, y+float64(cellHeight)-4, html.EscapeString(text))
	}
	b.WriteString(`</g>`)

	// Sort children for deterministic layout — same input always renders the
	// same SVG, important for caching and diff'ing flame graphs.
	names := make([]string, 0, len(n.Children))
	for k := range n.Children {
		names = append(names, k)
	}
	sort.Strings(names)

	cx := x
	for _, name := range names {
		c := n.Children[name]
		renderFlameNode(b, c, cx, y-float64(cellHeight), scale, cellHeight, depth+1)
		cx += float64(c.Value) * scale
	}
}

func emptyFlameSVG(width int) string {
	if width <= 0 {
		width = 1200
	}
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="60" font-family="Verdana, monospace" font-size="13">`+
			`<rect width="%d" height="60" fill="#f7f7f0"/>`+
			`<text x="20" y="35" fill="#555">No profile data yet — sample window returned 0 samples.</text>`+
			`</svg>`, width, width)
}

func flameDepth(n *FlameNode) int {
	if n == nil {
		return 0
	}
	max := 0
	for _, c := range n.Children {
		d := flameDepth(c)
		if d > max {
			max = d
		}
	}
	return max + 1
}

// flameColor produces Brendan Gregg's "hot" palette deterministically from
// the frame name, so the same function gets the same colour across reloads.
func flameColor(name string) string {
	// fnv-like hash; tiny and deterministic.
	h := uint32(2166136261)
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	// Reds-to-yellows band: hue 0..50, sat 65–80%, light 50–60%.
	hue := int(h % 50)
	sat := 65 + int((h>>8)%15)
	lig := 50 + int((h>>16)%10)
	return fmt.Sprintf("hsl(%d,%d%%,%d%%)", hue, sat, lig)
}

func truncateLabel(s string, max int) string {
	if max < 4 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// --- gating ---

// profilingPermitted reports whether the caller has opted in via BOTH the
// config flag and the env var. Either alone is insufficient.
func profilingPermitted(cfg Config) bool {
	if !boolValue(cfg.Profiling.Enabled) {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv(ProfileEnabledEnv)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
