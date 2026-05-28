package shadow

import (
	"testing"
)

func TestValidatorBasicMatching(t *testing.T) {
	v := NewValidator()

	// 1. Send an order (Buy 10 @ 100)
	v.ProcessOrder(1, "limit", "buy", 100, 10)
	v.ProcessAck(1, "accepted")

	// 2. Send a matching order (Sell 10 @ 100)
	v.ProcessOrder(2, "limit", "sell", 100, 10)
	v.ProcessAck(2, "accepted")

	// 3. Simulate contestant's actual fills arriving
	v.ProcessFill(1, 10, 100, 2) // Order 1 filled 10 @ 100
	v.ProcessFill(2, 10, 100, 1) // Order 2 filled 10 @ 100

	score := v.GetCorrectnessScore()
	if score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorPartialMatching(t *testing.T) {
	v := NewValidator()

	// 1. Resting Buy (Buy 100 @ 50)
	v.ProcessOrder(1, "limit", "buy", 50, 100)
	v.ProcessAck(1, "accepted")

	// 2. Incoming smaller Sell (Sell 20 @ 50)
	v.ProcessOrder(2, "limit", "sell", 50, 20)
	v.ProcessAck(2, "accepted")

	// 3. Simulate fills
	v.ProcessFill(1, 20, 50, 2) // Order 1 partially filled
	v.ProcessFill(2, 20, 50, 1) // Order 2 fully filled

	score := v.GetCorrectnessScore()
	if score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorPricePriority(t *testing.T) {
	v := NewValidator()

	// 1. Resting Buy at 50
	v.ProcessOrder(1, "limit", "buy", 50, 10)
	v.ProcessAck(1, "accepted")

	// 2. Resting Buy at 55 (Better price)
	v.ProcessOrder(2, "limit", "buy", 55, 10)
	v.ProcessAck(2, "accepted")

	// 3. Market sell (or limit sell crossing spread)
	v.ProcessOrder(3, "limit", "sell", 45, 10)
	v.ProcessAck(3, "accepted")

	// We expect Order 2 to be filled first at 55
	v.ProcessFill(2, 10, 55, 3)
	v.ProcessFill(3, 10, 55, 2) // The aggressive order fills at the resting price

	score := v.GetCorrectnessScore()
	if score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorIncorrectFills(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "limit", "buy", 100, 10)
	v.ProcessAck(1, "accepted")
	v.ProcessOrder(2, "limit", "sell", 100, 10)
	v.ProcessAck(2, "accepted")

	// Contestant engine messed up and reported wrong price
	v.ProcessFill(1, 10, 99, 2)
	v.ProcessFill(2, 10, 99, 1)

	score := v.GetCorrectnessScore()
	if score == 100.0 {
		t.Errorf("Expected score < 100.0 for incorrect fills, got %f", score)
	}
}

func TestValidatorUppercaseSellMatches(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(1, "accepted")
	v.ProcessOrder(2, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(2, "accepted")

	v.ProcessFill(1, 10, 100, 2)
	v.ProcessFill(2, 10, 100, 1)

	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorMarketOrderSweepsBook(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(1, "accepted")
	v.ProcessOrder(2, "LIMIT", "SELL", 101, 10)
	v.ProcessAck(2, "accepted")
	v.ProcessOrder(3, "MARKET", "BUY", 0, 15)
	v.ProcessAck(3, "accepted")

	v.ProcessFill(1, 10, 100, 3)
	v.ProcessFill(2, 5, 101, 3)
	v.ProcessFill(3, 10, 100, 1)
	v.ProcessFill(3, 5, 101, 2)

	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected score 100.0, got %f", score)
	}
}

func TestValidatorCancelRemovesRestingOrder(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(1, "accepted")
	v.ProcessOrder(1, "CANCEL", "BUY", 0, 0)
	v.ProcessAck(1, "cancelled")
	v.ProcessOrder(2, "LIMIT", "SELL", 90, 10)
	v.ProcessAck(2, "accepted")

	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected score 100.0 after valid cancel, got %f", score)
	}
}

func TestValidatorRejectingValidOrderIsIncorrect(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(1, "rejected")

	if score := v.GetCorrectnessScore(); score != 0.0 {
		t.Errorf("Expected score 0.0 for rejecting a valid order, got %f", score)
	}
}

func TestValidatorPriorityViolationPenalized(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(1, "accepted")
	v.ProcessOrder(2, "LIMIT", "SELL", 101, 10)
	v.ProcessAck(2, "accepted")
	v.ProcessOrder(3, "MARKET", "BUY", 0, 10)
	v.ProcessAck(3, "accepted")

	// Same quantity, but wrong price/counterparty: worse ask filled before best ask.
	v.ProcessFill(2, 10, 101, 3)
	v.ProcessFill(3, 10, 101, 2)

	if score := v.GetCorrectnessScore(); score >= 100.0 {
		t.Errorf("Expected score < 100.0 for priority violation, got %f", score)
	}
}
