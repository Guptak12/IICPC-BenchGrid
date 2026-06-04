package main

import (
	"math"
)

// StrategyMetrics holds per-strategy performance tracking
type StrategyMetrics struct {
	OrdersSent   int     `json:"orders_sent"`
	OrdersFailed int     `json:"orders_failed"`
	AvgLatencyUs int64   `json:"avg_latency_us"`
	TotalLatency int64   `json:"-"` // accumulator, not exported
	AckCount     int     `json:"-"` // used to compute average
}

// PretestResults represents the raw measurements from a pretest execution
type PretestResults struct {
	Correctness      float64 // 0 to 100
	P99Us            int64   // p99 latency in microseconds
	OrdersSent       int64
	OrdersFailed     int64
	TpsStart         float64 // starting TPS
	TpsEnd             float64 // ending TPS
	PhantomFills       int64
	PriorityViolations int64
	StrategyBreakdown  map[string]*StrategyMetrics // per-strategy metrics
}

// EvaluateVerdict evaluates the pretest results and returns (verdict, compositeScore, diagnostics)
func EvaluateVerdict(res PretestResults) (string, float64, map[string]interface{}) {
	warnings := []string{}

	// Calculate Failure Rate
	failRate := 0.0
	if res.OrdersSent > 0 {
		failRate = float64(res.OrdersFailed) / float64(res.OrdersSent)
	}

	// Calculate TPS Degradation
	degradation := 0.0
	if res.TpsStart > 0 {
		degradation = (res.TpsStart - res.TpsEnd) / res.TpsStart
		if degradation < 0 {
			degradation = 0 // In case speed improved
		}
	}

	// --- Determine Verdict ---
	verdict := "Accepted"
	if res.Correctness < 50.0 {
		verdict = "Wrong Answer"
	} else if res.Correctness < 85.0 {
		verdict = "Partial — Correctness"
	} else if res.P99Us > 50000 {
		verdict = "Time Limit Exceeded"
	} else if res.P99Us > 10000 {
		verdict = "Partial — Latency"
	} else if failRate > 0.30 || degradation > 0.60 {
		verdict = "Throughput Exceeded"
	} else if failRate > 0.10 || degradation > 0.30 {
		verdict = "Partial — Throughput"
	}

	diagnostics := map[string]interface{}{
		"correctness":          res.Correctness,
		"p99_us":              res.P99Us,
		"orders_sent":         res.OrdersSent,
		"orders_failed":       res.OrdersFailed,
		"tps_start":           res.TpsStart,
		"tps_end":             res.TpsEnd,
		"failure_rate_pct":    failRate * 100.0,
		"tps_degradation_pct": degradation * 100.0,
		"phantom_fills":       res.PhantomFills,
		"priority_violations": res.PriorityViolations,
	}

	// --- Per-Strategy Breakdown (inspired by PolyBench strategy utilization) ---
	if res.StrategyBreakdown != nil {
		// Finalize average latencies before exporting
		for _, sm := range res.StrategyBreakdown {
			if sm.AckCount > 0 {
				sm.AvgLatencyUs = sm.TotalLatency / int64(sm.AckCount)
			}
		}
		diagnostics["strategy_breakdown"] = res.StrategyBreakdown
	}

	// --- Warnings and Diagnostics ---
	if res.Correctness < 50.0 {
		warnings = append(warnings, "Low Correctness: Score is below 50%. Matching engine has major logical/correctness issues.")
	} else if res.Correctness < 85.0 {
		warnings = append(warnings, "Partial Correctness: Fills mostly correct but contains price-time or attribution violations.")
	}

	if res.P99Us > 50000 {
		warnings = append(warnings, "Severe Latency: P99 latency exceeded 50ms.")
	} else if res.P99Us > 10000 {
		warnings = append(warnings, "Partial Latency: Engine responds but P99 is high. Consider optimizing lock contention and allocs.")
	}

	if failRate > 0.30 {
		warnings = append(warnings, "Severe Throughput: Order failure rate exceeded 30%. Too many dropped connections or unacknowledged messages.")
	} else if failRate > 0.10 {
		warnings = append(warnings, "Partial Throughput: Elevated order failure rate between 10% and 30%.")
	}

	if degradation > 0.60 {
		warnings = append(warnings, "Severe Performance Degradation: Engine slowed down by more than 60% during test.")
	} else if degradation > 0.30 {
		warnings = append(warnings, "Partial Throughput: Performance degraded by more than 30% during the test.")
	}

	// --- Calculate Scores ---
	// Throughput score: linear to successful processed orders percentage
	throughputScore := (1.0 - failRate) * 100.0

	// Latency score: 100 if P99 <= 500us, linear decay to 0 at P99 = 5ms (5000us)
	latencyScore := 0.0
	if res.P99Us <= 500 {
		latencyScore = 100.0
	} else if res.P99Us >= 5000 {
		latencyScore = 0.0
	} else {
		// Linear decay between 500us and 5000us
		latencyScore = 100.0 * (1.0 - float64(res.P99Us-500)/4500.0)
	}

	// Standard Composite Score Formula:
	// composite_score = (throughput_score * 0.3) + (latency_score * 0.3) + (correctness_score * 0.4)
	compositeScore := (throughputScore * 0.3) + (latencyScore * 0.3) + (res.Correctness * 0.4)
	compositeScore = math.Round(compositeScore*100) / 100 // round to 2 decimal places

	diagnostics["warnings"] = warnings
	diagnostics["throughput_score"] = math.Round(throughputScore*100) / 100
	diagnostics["latency_score"] = math.Round(latencyScore*100) / 100

	// --- Engine Archetype Classification (inspired by AlphaForgeBench behavioral profiles) ---
	// Classify engine based on multi-dimensional metric signature
	archetype := classifyArchetype(res.Correctness, latencyScore, throughputScore)
	diagnostics["engine_archetype"] = archetype

	return verdict, compositeScore, diagnostics
}

// classifyArchetype assigns a behavioral profile to the engine based on its
// multi-axis performance signature. Inspired by AlphaForgeBench's archetypal
// model profiles (aggressive-creative, balanced-stable, conservative-rigid).
func classifyArchetype(correctness, latencyScore, throughputScore float64) string {
	// "Latency-Optimized": great latency but sacrifices correctness
	if latencyScore >= 70 && correctness < 85 {
		return "Latency-Optimized"
	}
	// "Accuracy-Optimized": perfect matching but slow
	if correctness >= 95 && latencyScore < 30 {
		return "Accuracy-Optimized"
	}
	// "Low-Throughput": high failure rate under load
	if throughputScore < 70 {
		return "Low-Throughput"
	}
	// "Balanced": all axes within reasonable range
	if correctness >= 80 && latencyScore >= 30 && throughputScore >= 80 {
		return "Balanced"
	}
	// Default fallback
	return "Unclassified"
}
