package main

import (
	"fmt"
	"math"
	"strings"

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
	Protocol           string  // detected protocol
	Correctness        float64 // 0 to 100
	P50Us              int64   // p50 latency in microseconds (client RTT)
	P90Us              int64   // p90 latency in microseconds (client RTT)
	P99Us              int64   // p99 latency in microseconds (client RTT)
	EngineP99Us        int64   // p99 engine-reported latency in microseconds
	OrdersSent         int64
	OrdersFailed       int64
	TpsStart           float64 // starting TPS
	TpsEnd             float64 // ending TPS
	MaxSustainedTPS    float64 // max sustained TPS for a 1s window before failure
	IsSystest          bool    // indicates system test stage vs pretest
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

	// Dynamic baseline targets & ceilings based on protocol (microseconds)
	target := 500.0
	ceiling := 5000.0
	switch strings.ToUpper(res.Protocol) {
	case "FIX":
		target = 500.0
		ceiling = 5000.0
	case "WS":
		target = 1500.0
		ceiling = 15000.0
	case "REST":
		target = 5000.0
		ceiling = 50000.0
	default:
		target = 500.0
		ceiling = 5000.0
	}

	if db != nil {
		var avgTop10 float64
		// Query average p99_us of the top 10% of accepted runs for the given protocol
		query := `
			WITH ordered_subs AS (
				SELECT p99_us, PERCENT_RANK() OVER (ORDER BY composite_score DESC) as rank
				FROM submissions
				WHERE status = 'completed' AND verdict = 'Accepted' AND diagnostics->>'protocol' = $1
			)
			SELECT COALESCE(AVG(p99_us), 0.0)
			FROM ordered_subs
			WHERE rank <= 0.10
		`
		err := db.QueryRow(query, res.Protocol).Scan(&avgTop10)
		if err == nil && avgTop10 > 0 {
			target = avgTop10
			ceiling = avgTop10 * 10.0
		}
	}

	// --- Determine Verdict and Reason ---
	verdict := "Accepted"
	reason := "Optimal Execution (Passes all SLAs)"
	if res.Correctness < 100.0 {
		verdict = "Logic Violation (LV)"
		reason = "Correctness < 100% (Order Book Math Mismatch)"
	} else if failRate > 0.001 {
		verdict = "Correctness Error"
		reason = fmt.Sprintf("Failure rate %.3f%% exceeded 0.1%% SLA threshold (Engine Dropped/Rejected Orders)", failRate*100.0)
	} else if float64(res.P99Us) > ceiling {
		verdict = "Tail Latency Exceeded (TLE)"
		reason = fmt.Sprintf("P99 > %.0fµs (Worst-case Tail Spikes)", ceiling)
	} else if degradation > 0.30 {
		verdict = "Throughput Degradation"
		reason = "TPS Degradation > 30% (Severe Contention)"
	}

	diagnostics := map[string]interface{}{
		"protocol":              res.Protocol,
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
		"max_sustained_tps":     res.MaxSustainedTPS,
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

	if float64(res.P99Us) > ceiling*10 {
		warnings = append(warnings, fmt.Sprintf("Severe Latency: P99 latency exceeded %.0fms.", ceiling*10/1000.0))
	} else if float64(res.P99Us) > ceiling {
		warnings = append(warnings, "Partial Latency: Engine responds but P99 is high. Consider optimizing lock contention and allocs.")
	}

	if failRate > 0.001 {
		warnings = append(warnings, "Severe Throughput: Order failure rate exceeded 0.1% SLA threshold. Too many dropped connections or unacknowledged messages.")
	}

	if degradation > 0.60 {
		warnings = append(warnings, "Severe Performance Degradation: Engine slowed down by more than 60% during test.")
	} else if degradation > 0.30 {
		warnings = append(warnings, "Partial Throughput: Performance degraded by more than 30% during the test.")
	}

	// --- Calculate Scores ---
	var throughputScore float64
	var compositeScore float64
	latencyScore := scoring.DynamicLatencyScore(float64(res.P50Us), float64(res.P90Us), float64(res.P99Us), target, ceiling)

	if failRate > 0.001 {
		throughputScore = 0.0
		compositeScore = 0.0
	} else {
		// Logarithmic decay stability score (100 -> 0 as failRate goes from 0 to 0.001)
		stabilityScore := 100.0 * (1.0 - math.Log(1.0+(math.E-1.0)*(failRate*1000.0)))
		if stabilityScore < 0 {
			stabilityScore = 0
		}

		maxTPSExpected := 100.0
		if res.IsSystest {
			maxTPSExpected = 500000.0
		}
		maxTPSScore := (res.MaxSustainedTPS / maxTPSExpected) * 100.0
		if maxTPSScore > 100.0 {
			maxTPSScore = 100.0
		}
		if maxTPSScore < 0 {
			maxTPSScore = 0
		}

		throughputScore = 0.5*stabilityScore + 0.5*maxTPSScore
		compositeScore = scoring.CompositeScore(res.Correctness, latencyScore, throughputScore)
	}

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
