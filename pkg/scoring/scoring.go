package scoring

import "math"

// LatencyScore returns 0–100.
// 100 if p99 <= 500µs, linear decay to 0 at p99 = 5000µs.
func LatencyScore(p99Us float64) float64 {
	const target = 500.0
	const ceiling = 5000.0
	if p99Us <= target {
		return 100.0
	}
	if p99Us >= ceiling {
		return 0.0
	}
	return 100.0 * (1.0 - (p99Us-target)/(ceiling-target))
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
//   composite = (throughput * 0.3) + (latency * 0.3) + (correctness * 0.4)
func CompositeScore(correctness, latencyScore, throughputScore float64) float64 {
	score := (throughputScore * 0.3) + (latencyScore * 0.3) + (correctness * 0.4)
	return math.Round(score*100) / 100
}
