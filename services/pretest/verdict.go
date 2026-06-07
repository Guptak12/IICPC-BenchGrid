package main

import (
	"math"

	"iicpc-sandbox/pkg/scoring"
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
	P50Us            int64   // p50 latency in microseconds (client RTT)
	P90Us            int64   // p90 latency in microseconds (client RTT)
	P99Us            int64   // p99 latency in microseconds (client RTT)
	EngineP99Us      int64   // p99 engine-reported latency in microseconds
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

	// --- Determine Verdict and Reason ---
	verdict := "Accepted"
	reason := "Optimal Execution (Passes all SLAs)"
	if res.Correctness < 100.0 {
		verdict = "Logic Violation (LV)"
		reason = "Correctness < 100% (Order Book Math Mismatch)"
	} else if res.P99Us > 5000 {
		verdict = "Tail Latency Exceeded (TLE)"
		reason = "P99 > 5000µs (Worst-case Tail Spikes)"
	} else if failRate > 0.10 || degradation > 0.30 {
		verdict = "Throughput Degradation"
		if failRate > 0.10 {
			reason = "Failure Rate > 10% (Dropped Orders)"
		} else {
			reason = "TPS Degradation > 30% (Severe Contention)"
		}
	}

	diagnostics := map[string]interface{}{
		"correctness":            res.Correctness,
		"p50_us":                res.P50Us,
		"p90_us":                res.P90Us,
		"p99_us":                res.P99Us,
		"engine_reported_p99_us": res.EngineP99Us,
		"orders_sent":           res.OrdersSent,
		"orders_failed":         res.OrdersFailed,
		"tps_start":             res.TpsStart,
		"tps_end":               res.TpsEnd,
		"failure_rate_pct":      failRate * 100.0,
		"tps_degradation_pct":   degradation * 100.0,
		"phantom_fills":         res.PhantomFills,
		"priority_violations":   res.PriorityViolations,
		"reason":                reason,
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
	throughputScore := scoring.ThroughputScore(failRate)
	latencyScore := scoring.LatencyScore(float64(res.P50Us), float64(res.P90Us), float64(res.P99Us))
	compositeScore := scoring.CompositeScore(res.Correctness, latencyScore, throughputScore)

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
