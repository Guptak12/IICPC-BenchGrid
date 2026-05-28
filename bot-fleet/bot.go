package main

import (
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	gojson "github.com/goccy/go-json"
)

// StrategyType defines what kind of market participant this bot simulates
type StrategyType string

const (
	MarketMaker     StrategyType = "MARKET_MAKER"
	MomentumTrader  StrategyType = "MOMENTUM_TRADER"
	NoiseTrader     StrategyType = "NOISE_TRADER"
)


// OrderType mirrors real exchange order types
type OrderType string

const (
	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
	Cancel OrderType = "CANCEL"
)

// Side is buy or sell
type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

// OrderMessage -> what gets sent over WebSocket to the contestant's server
type OrderMessage struct {
	BotID      string    `json:"bot_id"`
	OrderID    int64    `json:"order_id"`
	Type       OrderType `json:"type"`
	Side       Side      `json:"side"`
	Price      int64   `json:"price"`      // 0 for MARKET orders
	Quantity   int64   `json:"quantity"`
}

// OrderAck -> what the contestant's server sends back
type OrderAck struct {
	OrderID int64 `json:"order_id"`
	Status  string `json:"status"` // "accepted", "rejected", "filled"
	FilledQty   int64  `json:"filled_qty,omitempty"`
	FilledPrice float64  `json:"filled_price,omitempty"`
	EngineSeqID int64  `json:"engine_seq_id,omitempty"`
}

// BotConfig -> holds everything a bot needs to know before it starts
type BotConfig struct {
	NumericID   int64
	StringID	string
	Strategy    StrategyType
	MidPrice     int64        // scaled: $100.50 = 10050
	Spread       int64        // scaled: $0.10 = 10
	OrdersToSend int          // how many orders this bot will send total
	RatePerSec  float64       // target orders per second
}

// BotResult is returned after a bot finishes its run
type BotResult struct {
	BotID        string
	Strategy     StrategyType
	Histogram    *hdrhistogram.Histogram
	OrdersSent   int
	OrdersFailed int
	Reconnects   int
}

// Bot is a single simulated market participant
type Bot struct {
	config  BotConfig
	rng     *rand.Rand   // per-bot RNG — not shared, so no mutex needed
	seqNum  atomic.Int64 // order sequence number, unique per bot
	SendTimes []int64 
}

func NewBot(cfg BotConfig) *Bot {
	return &Bot{
		config: cfg,
		rng: rand.New(rand.NewSource(time.Now().UnixNano() + cfg.NumericID)),
		// Pre-allocate SendTimes for all orders this bot will send
		// Index = seq & 0xFFFFFFFF — safe because OrdersToSend << 2^32
		SendTimes: make([]int64, cfg.OrdersToSend+1),
	}
}
	

func (b *Bot) NextOrder() OrderMessage {
	seq := b.seqNum.Add(1)
	orderID := (b.config.NumericID << 32) | (seq & 0xFFFFFFFF)

	switch b.config.Strategy {
	case MarketMaker:
		return b.marketMakerOrder(orderID, seq)
	case MomentumTrader:
		return b.momentumOrder(orderID, seq)
	default:
		return b.noiseOrder(orderID, seq)
	}
}

func (b *Bot) RecordSendTime(seq int64) {
	idx := seq & 0xFFFFFFFF
	b.SendTimes[idx] = time.Now().UnixNano()
}

func (b *Bot) CalculateLatency(orderID int64) int64 {
	seq := orderID & 0xFFFFFFFF // extract lower 32 bits = seq number
	sendTime := b.SendTimes[seq]
	if sendTime == 0 {
		return -1 // order was never sent — defensive check
	}
	return time.Now().UnixNano() - sendTime
}

func (b *Bot) MarshalOrder(msg OrderMessage) ([]byte, error) {
	return gojson.Marshal(msg)
}


func (b *Bot) marketMakerOrder(orderID, seq int64) OrderMessage {
	var side Side
	var price int64

	if seq%2 == 0 {
		side = Buy
		variation := b.rng.Int63n(b.config.Spread/10 + 1)
		price = b.config.MidPrice - b.config.Spread/2 - variation
	} else {
		side = Sell
		variation := b.rng.Int63n(b.config.Spread/10 + 1)
		price = b.config.MidPrice + b.config.Spread/2 + variation
	}

	quantity := 500 + b.rng.Int63n(500) // 5.00–10.00 units scaled

	return OrderMessage{
		BotID:    b.config.StringID,
		OrderID:  orderID,
		Type:     Limit,
		Side:     side,
		Price:    price,
		Quantity: quantity,
	}
}

func (b *Bot) momentumOrder(orderID, seq int64) OrderMessage {
	if seq%5 == 0 {
		return OrderMessage{
			BotID:   b.config.StringID,
			OrderID: orderID,
			Type:    Cancel,
			Side:    Buy,
		}
	}

	orderType := Market
	price := int64(0)

	if b.rng.Float64() < 0.2 {
		orderType = Limit
		variation := b.rng.Int63n(b.config.Spread*2 + 1)
		price = b.config.MidPrice - b.config.Spread + variation
	}

	quantity := 2000 + b.rng.Int63n(3000) // 20.00–50.00 units scaled

	side := Buy
	if b.rng.Int63n(2) == 0 {
		side = Sell
	}

	return OrderMessage{
		BotID:    b.config.StringID,
		OrderID:  orderID,
		Type:     orderType,
		Side:     side,
		Price:    price,
		Quantity: quantity,
	}
}

func (b *Bot) noiseOrder(orderID, _ int64) OrderMessage {
	roll := b.rng.Float64()
	var orderType OrderType
	var price int64

	switch {
	case roll < 0.60:
		orderType = Limit
		variation := b.rng.Int63n(b.config.MidPrice/10 + 1)
		if b.rng.Int63n(2) == 0 {
			price = b.config.MidPrice + variation
		} else {
			price = b.config.MidPrice - variation
		}
	case roll < 0.85:
		orderType = Market
	default:
		orderType = Cancel
	}

	side := Buy
	if b.rng.Int63n(2) == 0 {
		side = Sell
	}

	return OrderMessage{
		BotID:    b.config.StringID,
		OrderID:  orderID,
		Type:     orderType,
		Side:     side,
		Price:    price,
		Quantity: b.zipfQuantity(),
	}
}

func (b *Bot) zipfQuantity() int64 {
	u := b.rng.Float64()
	if u < 0.01 {
		u = 0.01
	}
	raw := int64((1.0 / u) * 200)
	if raw > 10000 {
		raw = 10000
	}
	return raw
}

// --- Startup helpers (called ONCE at config parse time, never in hot path) ---
// FloatToScaled converts human-readable float to scaled int64
// Call once when parsing JSON config, store result in BotConfig
func FloatToScaled(f float64) int64 {
	return int64(f * 100)
}

// ScaledToFloat converts scaled int64 back to float for display/logging only
func ScaledToFloat(scaled int64) float64 {
	return float64(scaled) / 100.0
}

// NewBotConfig converts float config values to scaled int64 at startup
// This is the ONLY place FloatToScaled is called
func NewBotConfig(numericID int64, stringID string, strategy StrategyType,
	midPrice, spread float64, ordersToSend int, ratePerSec float64) BotConfig {
	return BotConfig{
		NumericID:    numericID,
		StringID:     stringID,
		Strategy:     strategy,
		MidPrice:     FloatToScaled(midPrice), // float → scaled once, never again
		Spread:       FloatToScaled(spread),
		OrdersToSend: ordersToSend,
		RatePerSec:   ratePerSec,
	}
}