package pulse

import (
	"testing"
	"time"
)

func TestPercentile_Empty(t *testing.T) {
	if got := Percentile(nil, 50); got != 0 {
		t.Fatalf("expected 0 for empty slice, got %v", got)
	}
}

func TestPercentile_SingleElement(t *testing.T) {
	sorted := []time.Duration{100 * time.Millisecond}
	if got := Percentile(sorted, 50); got != 100*time.Millisecond {
		t.Fatalf("expected 100ms, got %v", got)
	}
}

func TestPercentile_KnownValues(t *testing.T) {
	// 1ms to 100ms in 1ms increments (100 values)
	sorted := make([]time.Duration, 100)
	for i := 0; i < 100; i++ {
		sorted[i] = time.Duration(i+1) * time.Millisecond
	}

	tests := []struct {
		p    float64
		want time.Duration
	}{
		{0, 1 * time.Millisecond},
		{50, 50500 * time.Microsecond}, // interpolated between 50 and 51
		{100, 100 * time.Millisecond},
	}

	for _, tt := range tests {
		got := Percentile(sorted, tt.p)
		// Allow small tolerance for interpolation
		diff := got - tt.want
		if diff < 0 {
			diff = -diff
		}
		if diff > time.Millisecond {
			t.Errorf("P%.0f: expected ~%v, got %v", tt.p, tt.want, got)
		}
	}
}

func TestComputePercentiles(t *testing.T) {
	durations := make([]time.Duration, 1000)
	for i := 0; i < 1000; i++ {
		durations[i] = time.Duration(i+1) * time.Millisecond
	}

	p50, p75, p90, p95, p99 := ComputePercentiles(durations)

	// Check rough accuracy (within 2ms tolerance)
	checkApprox := func(name string, got, want time.Duration) {
		diff := got - want
		if diff < 0 {
			diff = -diff
		}
		if diff > 2*time.Millisecond {
			t.Errorf("%s: expected ~%v, got %v", name, want, got)
		}
	}

	checkApprox("P50", p50, 500*time.Millisecond)
	checkApprox("P75", p75, 750*time.Millisecond)
	checkApprox("P90", p90, 900*time.Millisecond)
	checkApprox("P95", p95, 950*time.Millisecond)
	checkApprox("P99", p99, 990*time.Millisecond)
}

func TestComputePercentiles_Empty(t *testing.T) {
	p50, p75, p90, p95, p99 := ComputePercentiles(nil)
	if p50 != 0 || p75 != 0 || p90 != 0 || p95 != 0 || p99 != 0 {
		t.Fatal("expected all zeros for empty input")
	}
}

func TestComputeAvg(t *testing.T) {
	durations := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}
	avg := ComputeAvg(durations)
	if avg != 200*time.Millisecond {
		t.Fatalf("expected 200ms, got %v", avg)
	}
}

func TestComputeAvg_Empty(t *testing.T) {
	if got := ComputeAvg(nil); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
}

func TestComputeMinMax(t *testing.T) {
	durations := []time.Duration{300 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	if got := ComputeMin(durations); got != 100*time.Millisecond {
		t.Fatalf("min: expected 100ms, got %v", got)
	}
	if got := ComputeMax(durations); got != 300*time.Millisecond {
		t.Fatalf("max: expected 300ms, got %v", got)
	}
}
