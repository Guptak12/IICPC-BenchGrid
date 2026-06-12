package scoring

import "math"

// latencyBucket returns 0–100 for a single percentile bucket.
// 100 if latency <= 500µs, linear decay to 0 at 5000µs.
func latencyBucket(latencyUs float64) float64 {
	const target = 500.0
	const ceiling = 5000.0
	if latencyUs <= target {
		return 100.0
	}
	if latencyUs >= ceiling {
		return 0.0
	}
	return 100.0 * (1.0 - (latencyUs-target)/(ceiling-target))
}

// LatencyScore computes a weighted composite latency score from p50, p90, p99.
//
// Weighting (designed to heavily penalize tail latency):
//
//	p50:  20%  — rewards blazing-fast baseline algorithms
//	p90:  30%  — penalizes inconsistent mid-range performance
//	p99:  50%  — heavily punishes worst-case tail spikes
//
// Each bucket is independently scored 0–100 using the same linear decay
// (100 at ≤500µs, 0 at ≥5000µs), then combined with the weights above.
func LatencyScore(p50Us, p90Us, p99Us float64) float64 {
	s50 := latencyBucket(p50Us)
	s90 := latencyBucket(p90Us)
	s99 := latencyBucket(p99Us)
	return math.Round((s50*0.20+s90*0.30+s99*0.50)*100) / 100
}

// DynamicLatencyScore computes a weighted composite latency score using dynamic targets.
func DynamicLatencyScore(p50Us, p90Us, p99Us, target, ceiling float64) float64 {
	s50 := dynamicLatencyBucket(p50Us, target, ceiling)
	s90 := dynamicLatencyBucket(p90Us, target, ceiling)
	s99 := dynamicLatencyBucket(p99Us, target, ceiling)
	return math.Round((s50*0.20+s90*0.30+s99*0.50)*100) / 100
}

func dynamicLatencyBucket(latencyUs, target, ceiling float64) float64 {
	if latencyUs <= target {
		return 100.0
	}
	if latencyUs >= ceiling {
		return 0.0
	}
	if target >= ceiling {
		return 0.0
	}
	return 100.0 * (1.0 - (latencyUs-target)/(ceiling-target))
}

// ThroughputScore returns 0–100 based on order failure rate.
func ThroughputScore(failRate float64) float64 {
	if failRate < 0 {
		failRate = 0
	}
	if failRate > 1 {
		failRate = 1
	}
	return (1.0 - failRate) * 100.0
}

// CompositeScore applies the canonical weighted formula.
//
//	composite = (throughput * 0.3) + (latency * 0.3) + (correctness * 0.4)
func CompositeScore(correctness, latencyScore, throughputScore float64) float64 {
	score := (throughputScore * 0.3) + (latencyScore * 0.3) + (correctness * 0.4)
	return math.Round(score*100) / 100
}
