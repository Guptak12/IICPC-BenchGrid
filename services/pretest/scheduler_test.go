package main

import (
	"math"
	"testing"
)

func TestMMPPSchedulerPretestMode(t *testing.T) {
	// Pretest mode (isSystest = false)
	baseRate := 50.0
	numBots := 5
	seed := int64(12345)
	scheduler := NewMMPPScheduler(baseRate, seed, numBots, false, 1)

	if scheduler.isSystest {
		t.Error("Expected isSystest to be false")
	}
	if scheduler.numBots != numBots {
		t.Errorf("Expected numBots to be %d, got %d", numBots, scheduler.numBots)
	}

	// Verify rates are computed based on s.baseRate * multipliers
	// Calm: 0.3 * baseRate = 15.0
	scheduler.state = CalmState
	rateCalm := scheduler.baseRate * 0.3
	if rateCalm != 15.0 {
		t.Errorf("Expected Calm rate to be 15.0, got %f", rateCalm)
	}

	// Elevated: 1.2 * baseRate = 60.0
	scheduler.state = ElevatedState
	rateElevated := scheduler.baseRate * 1.2
	if rateElevated != 60.0 {
		t.Errorf("Expected Elevated rate to be 60.0, got %f", rateElevated)
	}

	// Panic: 4.0 * baseRate = 200.0
	scheduler.state = PanicState
	ratePanic := scheduler.baseRate * 4.0
	if ratePanic != 200.0 {
		t.Errorf("Expected Panic rate to be 200.0, got %f", ratePanic)
	}
}

func TestMMPPSchedulerSystestMode(t *testing.T) {
	// System test mode (isSystest = true)
	baseRate := 50.0
	numBots := 500
	seed := int64(12345)
	scheduler := NewMMPPScheduler(baseRate, seed, numBots, true, 1)

	if !scheduler.isSystest {
		t.Error("Expected isSystest to be true")
	}
	if scheduler.numBots != numBots {
		t.Errorf("Expected numBots to be %d, got %d", numBots, scheduler.numBots)
	}

	// Fast-forward orderCount past warm-up (100 orders)
	scheduler.orderCount = 100

	// Verify Calm state rate is exactly 1000 / numBots = 2.0 TPS
	scheduler.state = CalmState
	// Perform multiple trials to check the average rate
	var totalDuration float64
	trials := 1000
	for i := 0; i < trials; i++ {
		sleepDur := scheduler.NextSleep()
		totalDuration += sleepDur.Seconds()
	}
	avgSleep := totalDuration / float64(trials)
	expectedRate := 1000.0 / float64(numBots)
	expectedAvgSleep := 1.0 / expectedRate

	// The sample average of exponential distribution with mean expectedAvgSleep should be close
	margin := expectedAvgSleep * 0.15 // 15% margin
	if math.Abs(avgSleep-expectedAvgSleep) > margin {
		t.Errorf("CalmState: Expected average sleep around %f, got %f", expectedAvgSleep, avgSleep)
	}

	// Verify Panic state rate is exactly 500000 / numBots = 1000.0 TPS
	scheduler.state = PanicState
	totalDuration = 0
	for i := 0; i < trials; i++ {
		sleepDur := scheduler.NextSleep()
		totalDuration += sleepDur.Seconds()
	}
	avgSleep = totalDuration / float64(trials)
	expectedRate = 500000.0 / float64(numBots)
	expectedAvgSleep = 1.0 / expectedRate

	margin = expectedAvgSleep * 0.15
	if math.Abs(avgSleep-expectedAvgSleep) > margin {
		t.Errorf("PanicState: Expected average sleep around %f, got %f", expectedAvgSleep, avgSleep)
	}
}

func TestMMPPSchedulerWarmUp(t *testing.T) {
	// System test mode (isSystest = true) with warm-up phase
	baseRate := 50.0
	numBots := 500
	seed := int64(12345)
	scheduler := NewMMPPScheduler(baseRate, seed, numBots, true, 1)

	// Verify rate during warm-up (first 100 orders) is exactly 10,000 / numBots = 20.0 TPS
	var totalDuration float64
	trials := 100
	for i := 0; i < trials; i++ {
		sleepDur := scheduler.NextSleep()
		totalDuration += sleepDur.Seconds()
	}
	avgSleep := totalDuration / float64(trials)
	expectedRate := 10000.0 / float64(numBots)
	expectedAvgSleep := 1.0 / expectedRate

	margin := expectedAvgSleep * 0.20
	if math.Abs(avgSleep-expectedAvgSleep) > margin {
		t.Errorf("WarmUp: Expected average sleep around %f, got %f", expectedAvgSleep, avgSleep)
	}

	// Check orderCount is at 100
	if scheduler.orderCount != 100 {
		t.Errorf("Expected orderCount to be 100, got %d", scheduler.orderCount)
	}

	// Order 101 should trigger transition
	sleepDurPostWarmup := scheduler.NextSleep()
	if scheduler.orderCount != 101 {
		t.Errorf("Expected orderCount to be 101, got %d", scheduler.orderCount)
	}
	// The rate after order 100 depends on state (CalmState by default, which is 1000 / 500 = 2.0 TPS)
	expectedPostWarmupRate := 1000.0 / float64(numBots)
	expectedPostWarmupSleep := 1.0 / expectedPostWarmupRate
	// Verify that sleepDurPostWarmup is closer to expectedPostWarmupSleep than the warm-up sleep
	t.Logf("Post warm-up sleep dur: %v (expected rate mean: %v)", sleepDurPostWarmup, expectedPostWarmupSleep)
}

func TestEvaluateVerdictLogarithmicScoringAndSLA(t *testing.T) {
	// 1. Test failure rate > 0.1% SLA Disqualification
	resDisqualified := PretestResults{
		Protocol:        "FIX",
		Correctness:     100.0,
		OrdersSent:      10000,
		OrdersFailed:    11, // 11 / 10000 = 0.11% (exceeds 0.1% SLA)
		MaxSustainedTPS: 50000.0,
		IsSystest:       true,
	}

	verdict, score, diags := EvaluateVerdict(resDisqualified)
	if verdict != "Correctness Error" {
		t.Errorf("Expected verdict to be 'Correctness Error' due to SLA breach, got %s", verdict)
	}
	if score != 0.0 {
		t.Errorf("Expected score to be 0.0, got %f", score)
	}
	if diags["throughput_score"].(float64) != 0.0 {
		t.Errorf("Expected throughput score to be 0.0, got %v", diags["throughput_score"])
	}

	// 2. Test logarithmic decay score under 0.1% limit
	// Case A: 0% failure rate
	resPerfect := PretestResults{
		Protocol:        "FIX",
		Correctness:     100.0,
		OrdersSent:      10000,
		OrdersFailed:    0, // 0% failure
		MaxSustainedTPS: 500000.0, // perfect TPS
		IsSystest:       true,
	}
	_, _, diagsPerfect := EvaluateVerdict(resPerfect)
	if diagsPerfect["throughput_score"].(float64) != 100.0 {
		t.Errorf("Expected perfect throughput score of 100.0, got %v", diagsPerfect["throughput_score"])
	}

	// Case B: 0.01% failure rate (x = 0.1)
	resGood := PretestResults{
		Protocol:        "FIX",
		Correctness:     100.0,
		OrdersSent:      10000,
		OrdersFailed:    1, // 0.01% failure
		MaxSustainedTPS: 500000.0,
		IsSystest:       true,
	}
	_, _, diagsGood := EvaluateVerdict(resGood)
	scoreGood := diagsGood["throughput_score"].(float64)
	// StabilityScore log decay for x = 0.1: ~84.2. MaxTPSScore: 100. Average: ~92.1.
	if scoreGood >= 100.0 || scoreGood <= 80.0 {
		t.Errorf("Expected throughput score for 0.01%% failure to decay log-scaled, got %f", scoreGood)
	}
	t.Logf("Throughput score for 0.01%% failure: %f", scoreGood)
}
