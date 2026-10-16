package pulse

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// A flame graph is read to find what is burning the machine. The goroutine
// profile cannot answer that — a goroutine parked in a read samples on
// every tick while using no CPU — so CPU is its own mode (issue #10).
func TestProfileSampler_CPUModeWeighsByTime(t *testing.T) {
	p := newTestPulse(t)
	p.config.Profiling.Enabled = Bool(true)
	p.config.Profiling.DefaultDuration = 300 * time.Millisecond
	p.config.Profiling.MaxDuration = 2 * time.Second
	t.Setenv(ProfileEnabledEnv, "true")
	sampler := newProfileSampler(p)

	// Something CPU-hot to find, and something parked, so the two modes
	// have a reason to disagree.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		burnCPUForTest(stop)
	}()
	go func() {
		defer wg.Done()
		<-stop // parked in a channel receive for the whole window
	}()

	root, err := sampler.SampleCPU(context.Background(), 400*time.Millisecond)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("cpu sample: %v", err)
	}
	if root == nil || root.Value == 0 {
		t.Fatalf("cpu profile folded to nothing: %+v", root)
	}

	folded := Folded(root)
	if !strings.Contains(folded, "burnCPUForTest") {
		t.Errorf("the CPU-hot function is missing from the profile:\n%s", firstLines(folded, 10))
	}
	// Parked goroutines spend no CPU, so they should not dominate.
	if strings.Contains(folded, "chanrecv") {
		t.Errorf("a parked goroutine should not appear in a CPU profile:\n%s", firstLines(folded, 10))
	}
}

// Goroutine mode keeps its old meaning, minus the parked stacks that used
// to bury everything else.
func TestProfileSampler_GoroutineModeFoldsParkedStacks(t *testing.T) {
	p := newTestPulse(t)
	p.config.Profiling.Enabled = Bool(true)
	p.config.Profiling.DefaultDuration = 200 * time.Millisecond
	p.config.Profiling.MaxDuration = 2 * time.Second
	t.Setenv(ProfileEnabledEnv, "true")
	sampler := newProfileSampler(p)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-stop
	}()

	folded, err := sampler.SampleGoroutines(context.Background(), 200*time.Millisecond, 50, false)
	if err != nil {
		t.Fatalf("goroutine sample: %v", err)
	}
	withParked, err := sampler.SampleGoroutines(context.Background(), 200*time.Millisecond, 50, true)
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("goroutine sample with parked: %v", err)
	}

	if withParked.Value <= folded.Value {
		t.Errorf("including parked goroutines should add samples: %d with, %d without",
			withParked.Value, folded.Value)
	}
}

func TestIsParkedStack(t *testing.T) {
	parked := [][]string{
		{"main.worker", "runtime.chanrecv1", "runtime.chanrecv"},
		{"net.(*conn).Read", "internal/poll.runtime_pollWait"},
		{"main.serve", "runtime.selectgo"},
	}
	for _, stack := range parked {
		if !isParkedStack(stack) {
			t.Errorf("%v should read as parked", stack)
		}
	}
	working := [][]string{
		{"main.handler", "services.normalise", "runtime.concatstring2"},
		{"main.handler", "strings.Builder.WriteString"},
	}
	for _, stack := range working {
		if isParkedStack(stack) {
			t.Errorf("%v should not read as parked", stack)
		}
	}
}

// burnCPUForTest spins on real work until stopped. Named so it is easy to
// find in a profile.
func burnCPUForTest(stop <-chan struct{}) {
	acc := 0
	for {
		select {
		case <-stop:
			_ = acc
			return
		default:
		}
		for i := 0; i < 2_000_000; i++ {
			acc += i % 7
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
