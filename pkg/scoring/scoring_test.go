package scoring

import (
	"math"
	"testing"
)

func TestLatencyScore(t *testing.T) {
	tests := []struct {
		p99  float64
		want float64
	}{
		{0, 100},
		{500, 100},
		{2750, 50},
		{5000, 0},
		{10000, 0},
	}
	for _, tt := range tests {
		got := LatencyScore(tt.p99)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("LatencyScore(%v) = %v, want %v", tt.p99, got, tt.want)
		}
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
	want := (90.0 * 0.3) + (80.0 * 0.3) + (95.0 * 0.4) // 27 + 24 + 38 = 89.00
	if got != want {
		t.Errorf("CompositeScore(95, 80, 90) = %v, want %v", got, want)
	}
}
