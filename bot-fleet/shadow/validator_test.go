package shadow

import (
	"testing"
)

func TestValidatorBasicMatching(t *testing.T) {
	v := NewValidator()

	// 1. Send an order (Buy 10 @ 100)
	v.ProcessOrder(1, "limit", "buy", 100, 10)

	// 2. Send a matching order (Sell 10 @ 100)
	v.ProcessOrder(2, "limit", "sell", 100, 10)

	// 3. Simulate contestant's actual fills arriving
	v.ProcessFill(1, 10, 100) // Order 1 filled 10 @ 100
	v.ProcessFill(2, 10, 100) // Order 2 filled 10 @ 100

	score := v.GetCorrectnessScore()
	if score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorPartialMatching(t *testing.T) {
	v := NewValidator()

	// 1. Resting Buy (Buy 100 @ 50)
	v.ProcessOrder(1, "limit", "buy", 50, 100)

	// 2. Incoming smaller Sell (Sell 20 @ 50)
	v.ProcessOrder(2, "limit", "sell", 50, 20)

	// 3. Simulate fills
	v.ProcessFill(1, 20, 50) // Order 1 partially filled
	v.ProcessFill(2, 20, 50) // Order 2 fully filled

	score := v.GetCorrectnessScore()
	if score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorPricePriority(t *testing.T) {
	v := NewValidator()

	// 1. Resting Buy at 50
	v.ProcessOrder(1, "limit", "buy", 50, 10)

	// 2. Resting Buy at 55 (Better price)
	v.ProcessOrder(2, "limit", "buy", 55, 10)

	// 3. Market sell (or limit sell crossing spread)
	v.ProcessOrder(3, "limit", "sell", 45, 10)

	// We expect Order 2 to be filled first at 55
	v.ProcessFill(2, 10, 55)
	v.ProcessFill(3, 10, 55) // The aggressive order fills at the resting price

	score := v.GetCorrectnessScore()
	if score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorIncorrectFills(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "limit", "buy", 100, 10)
	v.ProcessOrder(2, "limit", "sell", 100, 10)

	// Contestant engine messed up and reported wrong price
	v.ProcessFill(1, 10, 99)
	v.ProcessFill(2, 10, 99)

	score := v.GetCorrectnessScore()
	if score == 100.0 {
		t.Errorf("Expected score < 100.0 for incorrect fills, got %f", score)
	}
}
