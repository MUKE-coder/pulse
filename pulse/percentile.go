package pulse

import (
	"math"
	"sort"
	"time"
)

// Percentile computes the p-th percentile of a sorted slice of durations.
// p should be between 0 and 100. The input slice must be sorted.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}

	// Use the "nearest rank" method
	rank := (p / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper || upper >= len(sorted) {
		return sorted[lower]
	}

	// Interpolate
	frac := rank - float64(lower)
	return time.Duration(float64(sorted[lower])*(1-frac) + float64(sorted[upper])*frac)
}

// ComputePercentiles computes p50, p75, p90, p95, p99 from an unsorted slice of durations.
func ComputePercentiles(durations []time.Duration) (p50, p75, p90, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return
	}

	// Make a sorted copy
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p50 = Percentile(sorted, 50)
	p75 = Percentile(sorted, 75)
	p90 = Percentile(sorted, 90)
	p95 = Percentile(sorted, 95)
	p99 = Percentile(sorted, 99)
	return
}

// ComputeAvg computes the average of a slice of durations.
func ComputeAvg(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

// ComputeMin returns the minimum duration.
func ComputeMin(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	min := durations[0]
	for _, d := range durations[1:] {
		if d < min {
			min = d
		}
	}
	return min
}

// ComputeMax returns the maximum duration.
func ComputeMax(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	max := durations[0]
	for _, d := range durations[1:] {
		if d > max {
			max = d
		}
	}
	return max
}

// PercentileFloat computes the p-th percentile of a sorted float64 slice.
func PercentileFloat(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}

	rank := (p / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper || upper >= len(sorted) {
		return sorted[lower]
	}

	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
