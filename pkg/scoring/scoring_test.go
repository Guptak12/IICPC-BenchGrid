package scoring

import (
	"math"
	"testing"
)

func TestLatencyBucket(t *testing.T) {
	tests := []struct {
		us   float64
		want float64
	}{
		{0, 100},
		{500, 100},
		{2750, 50},
		{5000, 0},
		{10000, 0},
	}
	for _, tt := range tests {
		got := latencyBucket(tt.us)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("latencyBucket(%v) = %v, want %v", tt.us, got, tt.want)
		}
	}
}

func TestLatencyScoreWeighted(t *testing.T) {
	// All perfect: 100 across all buckets → 100
	got := LatencyScore(100, 100, 100)
	if math.Abs(got-100.0) > 0.01 {
		t.Errorf("LatencyScore(100,100,100) = %v, want 100", got)
	}

	// All terrible: 0 across all buckets → 0
	got = LatencyScore(10000, 10000, 10000)
	if math.Abs(got-0.0) > 0.01 {
		t.Errorf("LatencyScore(10000,10000,10000) = %v, want 0", got)
	}

	// p50=perfect(100), p90=mid(50), p99=terrible(0) → 0.20*100 + 0.30*50 + 0.50*0 = 35
	got = LatencyScore(100, 2750, 10000)
	if math.Abs(got-35.0) > 0.01 {
		t.Errorf("LatencyScore(100,2750,10000) = %v, want 35", got)
	}

	// p50=perfect(100), p90=perfect(100), p99=mid(50) → 0.20*100 + 0.30*100 + 0.50*50 = 75
	got = LatencyScore(100, 100, 2750)
	if math.Abs(got-75.0) > 0.01 {
		t.Errorf("LatencyScore(100,100,2750) = %v, want 75", got)
	}

	// All at midpoint (2750µs → each bucket scores 50) → 50
	got = LatencyScore(2750, 2750, 2750)
	if math.Abs(got-50.0) > 0.01 {
		t.Errorf("LatencyScore(2750,2750,2750) = %v, want 50", got)
	}
}

func TestCompositeScore(t *testing.T) {
	// Perfect engine: 100 correctness, 100 latency, 100 throughput = 100
	got := CompositeScore(100, 100, 100)
	if got != 100.0 {
		t.Errorf("CompositeScore(100,100,100) = %v, want 100", got)
	}

	// Dynamic calculation check
	got = CompositeScore(95.0, 80.0, 90.0)
	want := math.Round(((90.0 * 0.3) + (80.0 * 0.3) + (95.0 * 0.4)) * 100) / 100
	if got != want {
		t.Errorf("CompositeScore(95, 80, 90) = %v, want %v", got, want)
	}
}

func TestDynamicLatencyScore(t *testing.T) {
	// target=1000, ceiling=10000
	// p50=500 (<=1000 -> 100), p90=5500 (midway -> 50), p99=10000 (>=10000 -> 0)
	// Weighted: 0.2*100 + 0.3*50 + 0.5*0 = 35.0
	got := DynamicLatencyScore(500, 5500, 10000, 1000, 10000)
	if math.Abs(got-35.0) > 0.01 {
		t.Errorf("DynamicLatencyScore(500,5500,10000,1000,10000) = %v, want 35", got)
	}
}
