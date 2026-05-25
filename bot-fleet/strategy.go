package main

import (
	"context"
	"math/rand"
	"time"

	"golang.org/x/time/rate"
)

// Strategy interface — controls WHEN a bot sends, never WHAT it sends
type Strategy interface {
	Wait(ctx context.Context) error
	Name() string
}

// ─────────────────────────────────────────────────────────────────────────────
// MarketMakerStrategy
// Burst = 2: allows one paired BUY+SELL quote without artificial throttle
// ─────────────────────────────────────────────────────────────────────────────
type MarketMakerStrategy struct {
	limiter *rate.Limiter
}

func NewMarketMakerStrategy(ratePerSec float64) *MarketMakerStrategy {
	return &MarketMakerStrategy{
		limiter: rate.NewLimiter(rate.Limit(ratePerSec), 2),
	}
}

func (s *MarketMakerStrategy) Wait(ctx context.Context) error {
	return s.limiter.Wait(ctx)
}

func (s *MarketMakerStrategy) Name() string { return "MARKET_MAKER" }

// ─────────────────────────────────────────────────────────────────────────────
// MomentumStrategy
// ─────────────────────────────────────────────────────────────────────────────
type MomentumStrategy struct {
	limiter     *rate.Limiter
	rng         *rand.Rand
	burstSize   int
	burstFired  int
	inBurst     bool
	baseSleepMs int64
}

func NewMomentumStrategy(ratePerSec float64, botNumericID int64) *MomentumStrategy {
	burst := 16

	// Fix 1: Calculate the exact time required to refill 'burst' amount of tokens
	// If rate = 100/sec, 16 tokens takes exactly 160ms to regenerate.
	requiredRefillMs := int64((float64(burst) / ratePerSec) * 1000.0)

	return &MomentumStrategy{
		limiter: rate.NewLimiter(rate.Limit(ratePerSec), burst),
		// Fix 3: Multiply ID by a prime number to guarantee massive PRNG separation
		rng:         rand.New(rand.NewSource(time.Now().UnixNano() + (botNumericID * 7919))),
		burstSize:   burst,
		burstFired:  0,
		inBurst:     false,
		baseSleepMs: requiredRefillMs,
	}
}

func (s *MomentumStrategy) Wait(ctx context.Context) error {
	if s.inBurst {
		err := s.limiter.Wait(ctx)
		s.burstFired++
		if s.burstFired >= s.burstSize {
			s.inBurst = false
			s.burstFired = 0
		}
		return err
	}

	// Jitter is applied to the base refill time.
	// Using up to 20% extra sleep ensures the bucket is always 100% full when waking.
	jitter := s.rng.Int63n((s.baseSleepMs / 5) + 1)
	sleepDuration := time.Duration(s.baseSleepMs+jitter) * time.Millisecond

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(sleepDuration):
		s.inBurst = true
		s.burstFired = 1
		return s.limiter.Wait(ctx)
	}
}

func (s *MomentumStrategy) Name() string { return "MOMENTUM_TRADER" }

// ─────────────────────────────────────────────────────────────────────────────
// NoiseStrategy
// ─────────────────────────────────────────────────────────────────────────────
type NoiseStrategy struct {
	limiter    *rate.Limiter
	rng        *rand.Rand
	minSleepMs int64
	maxSleepMs int64
}

func NewNoiseStrategy(ratePerSec float64, botNumericID int64) *NoiseStrategy {
	// Fix 2: Calculate dynamic sleep bounds based on the requested rate
	avgSleepMs := int64(1000.0 / ratePerSec)
	if avgSleepMs < 2 {
		avgSleepMs = 2 // Prevent modulo panics at extreme high speeds
	}

	return &NoiseStrategy{
		// A higher burst (10) allows the bot to fire rapidly after a long sleep,
		// maintaining the overall average TPS configuration requested by the orchestrator.
		limiter:    rate.NewLimiter(rate.Limit(ratePerSec), 10),
		rng:        rand.New(rand.NewSource(time.Now().UnixNano() + (botNumericID * 104729))),
		minSleepMs: avgSleepMs / 4,
		maxSleepMs: avgSleepMs + (avgSleepMs / 2),
	}
}

func (s *NoiseStrategy) Wait(ctx context.Context) error {
	sleepRange := s.maxSleepMs - s.minSleepMs
	if sleepRange <= 0 {
		sleepRange = 1
	}
	sleepMs := s.minSleepMs + s.rng.Int63n(sleepRange)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(sleepMs) * time.Millisecond):
		return s.limiter.Wait(ctx)
	}
}

func (s *NoiseStrategy) Name() string { return "NOISE_TRADER" }

// ─────────────────────────────────────────────────────────────────────────────
// DefaultStrategy
// ─────────────────────────────────────────────────────────────────────────────
type DefaultStrategy struct {
	limiter *rate.Limiter
	name    string
}

func NewDefaultStrategy(ratePerSec float64, strategyType StrategyType) *DefaultStrategy {
	return &DefaultStrategy{
		limiter: rate.NewLimiter(rate.Limit(ratePerSec), 1),
		name:    string(strategyType),
	}
}

func (s *DefaultStrategy) Wait(ctx context.Context) error {
	return s.limiter.Wait(ctx)
}

func (s *DefaultStrategy) Name() string { return s.name }

// ─────────────────────────────────────────────────────────────────────────────
// Factory
// ─────────────────────────────────────────────────────────────────────────────
func newStrategy(b *Bot) Strategy {
	switch b.config.Strategy {
	case MarketMaker:
		return NewMarketMakerStrategy(b.config.RatePerSec)
	case MomentumTrader:
		return NewMomentumStrategy(b.config.RatePerSec, b.config.NumericID)
	case NoiseTrader:
		return NewNoiseStrategy(b.config.RatePerSec, b.config.NumericID)
	default:
		return NewDefaultStrategy(b.config.RatePerSec, b.config.Strategy)
	}
}