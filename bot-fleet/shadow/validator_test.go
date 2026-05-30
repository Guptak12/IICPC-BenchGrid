package shadow

import (
	"testing"
)

// helper: encodes a real bot-style order ID: (numericID << 32) | seq
func makeOrderID(botID, seq int64) int64 {
	return (botID << 32) | seq
}

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

// ── Self-crossing tests (use real bot-encoded IDs) ──────────────────────────

func TestValidatorSelfCrossingPrevented(t *testing.T) {
	v := NewValidator()

	// Both orders from bot-1
	buy  := makeOrderID(1, 1)
	sell := makeOrderID(1, 2)

	v.ProcessOrder(buy, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(buy, "accepted")

	// Bot-1 sends a crossing SELL — should not produce any expected fills
	v.ProcessOrder(sell, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(sell, "accepted")

	// No fills reported by contestant — correct behaviour
	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected 100.0 when self-cross blocked correctly, got %f", score)
	}
}

func TestValidatorSelfCrossSkipsToNextBot(t *testing.T) {
	v := NewValidator()

	bot1buy  := makeOrderID(1, 1) // bot-1 resting BUY (arrives first → front of queue)
	bot2buy  := makeOrderID(2, 1) // bot-2 resting BUY (arrives second → back of queue)
	bot1sell := makeOrderID(1, 2) // bot-1 SELL — crosses both bids

	v.ProcessOrder(bot1buy, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(bot1buy, "accepted")

	v.ProcessOrder(bot2buy, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(bot2buy, "accepted")

	// Bot-1 sends a SELL at 100.
	// Price-time priority says bot1buy should match first,
	// but it is a self-cross — validator skips it.
	// Bot-2's resting buy is next and matches instead.
	v.ProcessOrder(bot1sell, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(bot1sell, "accepted")

	// Only the cross-bot pair fills
	v.ProcessFill(bot1sell, 10, 100, bot2buy)
	v.ProcessFill(bot2buy,  10, 100, bot1sell)

	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected 100.0 when self-cross skips to next valid order, got %f", score)
	}
}

func TestValidatorSelfCrossInfiniteLoopPrevented(t *testing.T) {
	v := NewValidator()

	// Bot-1 has the ONLY resting ask — incoming buy from same bot
	// Old code: infinite loop. New code: skips level, returns remainingQty unmatched.
	bot1sell := makeOrderID(1, 1)
	bot1buy  := makeOrderID(1, 2)

	v.ProcessOrder(bot1sell, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(bot1sell, "accepted")

	// Market buy from same bot — no eligible counterparty exists
	v.ProcessOrder(bot1buy, "MARKET", "BUY", 0, 10)
	v.ProcessAck(bot1buy, "accepted")

	// No fills expected — bot-1 cannot match itself
	// Test passes if it completes at all (no hang) and scores 100
	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected 100.0 when only counterparty is self, got %f", score)
	}
}

func TestValidatorSelfCrossMixedLevelMatchesOtherBot(t *testing.T) {
	v := NewValidator()

	// Price level 100 has BOTH a bot-1 order AND a bot-2 order
	bot1ask  := makeOrderID(1, 1) // bot-1 resting SELL at 100 (front of queue)
	bot2ask  := makeOrderID(2, 1) // bot-2 resting SELL at 100 (back of queue)
	bot1buy  := makeOrderID(1, 2) // bot-1 BUY — skips own ask, hits bot-2's ask

	v.ProcessOrder(bot1ask, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(bot1ask, "accepted")

	v.ProcessOrder(bot2ask, "LIMIT", "SELL", 100, 10)
	v.ProcessAck(bot2ask, "accepted")

	v.ProcessOrder(bot1buy, "LIMIT", "BUY", 100, 10)
	v.ProcessAck(bot1buy, "accepted")

	// Bot-1's own ask is skipped; bot-2's ask fills instead
	v.ProcessFill(bot1buy, 10, 100, bot2ask)
	v.ProcessFill(bot2ask, 10, 100, bot1buy)

	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Errorf("Expected 100.0 for mixed-level self-cross skip, got %f", score)
	}
}

// TestValidatorMatchedWithZeroBypass ensures engines cannot skip the
// counterparty check by reporting matched_with:0.
// Before the fix this scored 100.0 — it should score < 100.0.
func TestValidatorMatchedWithZeroBypass(t *testing.T) {
    v := NewValidator()

    v.ProcessOrder(1, "LIMIT", "BUY", 100, 10)
    v.ProcessAck(1, "accepted")
    v.ProcessOrder(2, "LIMIT", "SELL", 100, 10)
    v.ProcessAck(2, "accepted")

    // Correct fills would be: order 1 matched_with 2, order 2 matched_with 1.
    // Cheating engine sends matched_with:0 to skip the counterparty check.
    v.ProcessFill(1, 10, 100, 0) // ← zero bypass attempt
    v.ProcessFill(2, 10, 100, 0) // ← zero bypass attempt

    score := v.GetCorrectnessScore()
    if score >= 100.0 {
        t.Errorf("matched_with:0 bypass must not score 100.0, got %.2f", score)
    }
}

// TestValidatorMatchedWithWrongCounterparty ensures a fill with the right
// qty and price but wrong counterparty is penalised on priority score.
func TestValidatorMatchedWithWrongCounterparty(t *testing.T) {
    v := NewValidator()

    // Three orders: buy 1 rests, buy 2 rests, sell crosses buy 1 (best time priority)
    v.ProcessOrder(1, "LIMIT", "BUY", 100, 10)
    v.ProcessAck(1, "accepted")
    v.ProcessOrder(2, "LIMIT", "BUY", 100, 10)
    v.ProcessAck(2, "accepted")
    v.ProcessOrder(3, "LIMIT", "SELL", 100, 10)
    v.ProcessAck(3, "accepted")

    // Correct: sell fills against order 1 (arrived first = time priority).
    // Cheating engine fills against order 2 instead — same price, wrong counterparty.
    v.ProcessFill(3, 10, 100, 2)  // should be matched_with:1
    v.ProcessFill(2, 10, 100, 3)  // should be order 1 filled, not order 2

    score := v.GetCorrectnessScore()
    if score >= 100.0 {
        t.Errorf("wrong counterparty must not score 100.0, got %.2f", score)
    }
}

// TestValidatorFoldsConsecutivePartialFills ensures adjacent fills with the
// same price and counterparty are compared as one execution block.
func TestValidatorFoldsConsecutivePartialFills(t *testing.T) {
	v := NewValidator()

	v.ProcessOrder(1, "LIMIT", "BUY", 100, 60)
	v.ProcessAck(1, "accepted")
	v.ProcessOrder(2, "LIMIT", "SELL", 100, 60)
	v.ProcessAck(2, "accepted")

	v.ProcessFill(1, 10, 100, 2)
	v.ProcessFill(1, 20, 100, 2)
	v.ProcessFill(1, 30, 100, 2)
	v.ProcessFill(2, 10, 100, 1)
	v.ProcessFill(2, 20, 100, 1)
	v.ProcessFill(2, 30, 100, 1)

	if score := v.GetCorrectnessScore(); score != 100.0 {
		t.Fatalf("expected folded partial fills to score 100.0, got %.2f", score)
	}
}
