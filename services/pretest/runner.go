package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/coder/websocket"
	gojson "github.com/goccy/go-json"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/guptak12/bot-fleet/shadow"
)

// Strategy Types matching bot-fleet
type StrategyType string

const (
	MarketMaker    StrategyType = "MARKET_MAKER"
	MomentumTrader StrategyType = "MOMENTUM_TRADER"
	NoiseTrader    StrategyType = "NOISE_TRADER"
)

// Order Types matching bot-fleet
type OrderType string

const (
	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
	Cancel OrderType = "CANCEL"
)

type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

// OrderMessage sent to contestant C++ engine
type OrderMessage struct {
	BotID    string    `json:"bot_id"`
	OrderID  int64     `json:"order_id"`
	Type     OrderType `json:"type"`
	Side     Side      `json:"side"`
	Price    int64     `json:"price"`
	Quantity int64     `json:"quantity"`
}

// OrderAck received from contestant C++ engine
type OrderAck struct {
	OrderID      int64  `json:"order_id"`
	Status       string `json:"status"`
	FilledQty    int64  `json:"filled_qty,omitempty"`
	FilledPrice  int64  `json:"filled_price,omitempty"`
	MatchedWith  int64  `json:"matched_with,omitempty"`
	EngineSeqID  int64  `json:"engine_seq_id,omitempty"`
	ProcessingNs int64  `json:"processing_ns"`
}

// PretestBot represents a single bot running in the pretest fleet
type PretestBot struct {
	NumericID    int64
	StringID     string
	Strategy     StrategyType
	MidPrice     int64
	Spread       int64
	OrdersToSend int
	RatePerSec   float64
	Seed         int64

	rng          *rand.Rand
	seqNum       atomic.Int64
	ordersSent   int
	activeOrders []int64
	SendTimes    map[int64]int64
	sendTimesMu  sync.RWMutex
}

func NewPretestBot(numericID int64, stringID string, strategy StrategyType, midPrice, spread float64, ordersToSend int, ratePerSec float64, seed int64) *PretestBot {
	return &PretestBot{
		NumericID:    numericID,
		StringID:     stringID,
		Strategy:     strategy,
		MidPrice:     int64(midPrice * 100),
		Spread:       int64(spread * 100),
		OrdersToSend: ordersToSend,
		RatePerSec:   ratePerSec,
		Seed:         seed,
		rng:          rand.New(rand.NewSource(seed)),
		SendTimes:    make(map[int64]int64),
	}
}

// GenerateNextOrder implements deterministic strategy generation for pretests
func (b *PretestBot) GenerateNextOrder() OrderMessage {
	b.ordersSent++
	seq := b.seqNum.Add(1)

	var side Side = Buy
	var orderType OrderType = Limit
	var price int64 = b.MidPrice
	var qty int64 = 100
	var isCancel bool
	var cancelID int64

	switch b.Strategy {
	case MarketMaker:
		orderType = Limit
		if seq%2 == 0 {
			side = Buy
			variation := b.rng.Int63n(b.Spread/10 + 1)
			price = b.MidPrice - b.Spread/2 - variation
		} else {
			side = Sell
			variation := b.rng.Int63n(b.Spread/10 + 1)
			price = b.MidPrice + b.Spread/2 + variation
		}
		qty = 500 + b.rng.Int63n(500)

	case MomentumTrader:
		if seq%5 == 0 && len(b.activeOrders) > 0 {
			cancelID = b.activeOrders[0]
			b.activeOrders = b.activeOrders[1:]
			isCancel = true
		} else {
			orderType = Market
			price = 0
			if b.rng.Float64() < 0.2 {
				orderType = Limit
				variation := b.rng.Int63n(b.Spread*2 + 1)
				price = b.MidPrice - b.Spread + variation
			}
			qty = 2000 + b.rng.Int63n(3000)
			side = Buy
			if b.rng.Intn(2) == 0 {
				side = Sell
			}
		}

	case NoiseTrader:
		roll := b.rng.Float64()
		switch {
		case roll < 0.60:
			orderType = Limit
			variation := b.rng.Int63n(b.MidPrice/10 + 1)
			if b.rng.Intn(2) == 0 {
				price = b.MidPrice + variation
			} else {
				price = b.MidPrice - variation
			}
		case roll < 0.85:
			orderType = Market
		default:
			if len(b.activeOrders) > 0 {
				cancelID = b.activeOrders[0]
				b.activeOrders = b.activeOrders[1:]
				isCancel = true
			} else {
				orderType = Limit
				price = b.MidPrice
			}
		}
		if !isCancel {
			side = Buy
			if b.rng.Intn(2) == 0 {
				side = Sell
			}
			qty = b.zipfQuantity()
		}
	}

	if isCancel {
		return OrderMessage{BotID: b.StringID, OrderID: cancelID, Type: Cancel}
	}

	// Embed the side in the bot ID part of the order ID:
	// even bot ID for BUY, odd bot ID for SELL.
	botPart := b.NumericID << 1
	if side == Sell {
		botPart |= 1
	}
	orderID := (botPart << 32) | (seq & 0xFFFFFFFF)
	if orderType == Limit {
		b.activeOrders = append(b.activeOrders, orderID)
	}

	return OrderMessage{BotID: b.StringID, OrderID: orderID, Type: orderType, Side: side, Price: price, Quantity: qty}
}

func (b *PretestBot) zipfQuantity() int64 {
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

// RunPretestFleet executes a small deterministic pretest suite (5 bots, 100 orders each)
func RunPretestFleet(ctx context.Context, endpoint string, baseSeed int64) (PretestResults, error) {
	numBots := 5
	ordersPerBot := 100

	// Create deterministic bots
	bots := make([]*PretestBot, numBots)
	strategies := []StrategyType{MarketMaker, MarketMaker, MomentumTrader, MomentumTrader, NoiseTrader}

	for i := 0; i < numBots; i++ {
		bots[i] = NewPretestBot(
			int64(i+1),
			fmt.Sprintf("pretest-bot-%d", i+1),
			strategies[i],
			100.0, // Mid Price
			0.10,  // Spread
			ordersPerBot,
			50.0, // 50 orders/sec
			baseSeed+int64(i),
		)
	}

	validator := shadow.NewValidator()
	hist := hdrhistogram.New(1, 10000000000, 3) // up to 10 seconds in nanoseconds

	strategyBreakdown := map[string]*StrategyMetrics{
		string(MarketMaker):    {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
		string(MomentumTrader): {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
		string(NoiseTrader):    {OrdersSent: 0, OrdersFailed: 0, AvgLatencyUs: 0},
	}
	var breakdownMu sync.Mutex

	var totalSent, totalFailed atomic.Int64
	var tpsSamples []float64
	var tpsSamplesMu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)

	// Throughput sample tracking
	doneChan := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		var lastSent int64
		for {
			select {
			case <-doneChan:
				return
			case <-ticker.C:
				curr := totalSent.Load()
				tps := float64(curr-lastSent) / 0.2
				lastSent = curr
				tpsSamplesMu.Lock()
				tpsSamples = append(tpsSamples, tps)
				tpsSamplesMu.Unlock()
			}
		}
	}()

	eventChan := make(chan PretestEvent, 10000)
	var botRunnersDone sync.WaitGroup
	botRunnersDone.Add(numBots)

	var sendLoopsDone sync.WaitGroup
	sendLoopsDone.Add(numBots)

	// 1. Start the single consumer goroutine on g
	acceptedOrders := make(map[int64]bool)
	g.Go(func() error {
		nextSeqID := int64(0)
		jitterBuffer := make(map[int64]PretestEvent)
		jitterGapThreshold := 50

		for {
			select {
			case <-gctx.Done():
				return nil
			case ev, ok := <-eventChan:
				if !ok {
					// Channel closed, process anything left
					for len(jitterBuffer) > 0 {
						if item, exists := jitterBuffer[nextSeqID]; exists {
							processEvent(item, validator, acceptedOrders)
							delete(jitterBuffer, nextSeqID)
							nextSeqID++
						} else {
							// Advance nextSeqID to the lowest key present in the buffer.
							minKey := int64(-1)
							for k := range jitterBuffer {
								if minKey == -1 || k < minKey {
									minKey = k
								}
							}
							if minKey != -1 {
								nextSeqID = minKey
							} else {
								break
							}
						}
					}
					return nil
				}

				jitterBuffer[ev.Ack.EngineSeqID] = ev
				for {
					item, exists := jitterBuffer[nextSeqID]
					if !exists {
						if len(jitterBuffer) >= jitterGapThreshold {
							// Skip the gap by advancing nextSeqID to the lowest key present in the buffer.
							minKey := int64(-1)
							for k := range jitterBuffer {
								if minKey == -1 || k < minKey {
									minKey = k
								}
							}
							if minKey != -1 {
								nextSeqID = minKey
								continue
							}
						}
						break
					}
					processEvent(item, validator, acceptedOrders)
					delete(jitterBuffer, nextSeqID)
					nextSeqID++
				}
			}
		}
	})

	// 2. Spawn bots concurrently
	for _, bot := range bots {
		b := bot
		g.Go(func() error {
			defer botRunnersDone.Done()
			// Connect to WebSocket C++ engine
			conn, _, err := websocket.Dial(gctx, endpoint, nil)
			if err != nil {
				return fmt.Errorf("websocket connection failed for %s: %v", b.StringID, err)
			}
			defer conn.Close(websocket.StatusGoingAway, "pretest completed")

			limiter := rate.NewLimiter(rate.Limit(b.RatePerSec), 5)
			
			// Setup receiver loop
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					_, msgBytes, err := conn.Read(gctx)
					receivedAt := time.Now().UnixNano()
					if err != nil {
						return
					}

					var ack OrderAck
					if err := gojson.Unmarshal(msgBytes, &ack); err != nil {
						continue
					}

					b.sendTimesMu.RLock()
					sendTime, ok := b.SendTimes[ack.OrderID]
					b.sendTimesMu.RUnlock()

					if ok {
						latency := ack.ProcessingNs
						if latency <= 0 {
							latency = receivedAt - sendTime
						}
						_ = hist.RecordValue(latency)

						// Track strategy-level metrics
						latencyUs := latency / 1000
						breakdownMu.Lock()
						sm := strategyBreakdown[string(b.Strategy)]
						sm.TotalLatency += latencyUs
						sm.AckCount++
						if strings.ToLower(ack.Status) == "rejected" {
							sm.OrdersFailed++
						}
						breakdownMu.Unlock()
					}

					select {
					case <-gctx.Done():
						return
					case eventChan <- PretestEvent{Ack: ack, IsOwn: ok}:
					}
				}
			}()

			// Send orders loop
			for i := 0; i < b.OrdersToSend; i++ {
				if err := limiter.Wait(gctx); err != nil {
					break
				}

				order := b.GenerateNextOrder()
				validator.ProcessOrder(order.OrderID, string(order.Type), string(order.Side), order.Price, order.Quantity)

				b.sendTimesMu.Lock()
				b.SendTimes[order.OrderID] = time.Now().UnixNano()
				b.sendTimesMu.Unlock()

				msgBytes, _ := gojson.Marshal(order)
				err = conn.Write(gctx, websocket.MessageText, msgBytes)
				if err != nil {
					totalFailed.Add(1)
					log.Printf("[%s] write error: %v\n", b.StringID, err)
					
					breakdownMu.Lock()
					strategyBreakdown[string(b.Strategy)].OrdersFailed++
					breakdownMu.Unlock()
				} else {
					totalSent.Add(1)

					breakdownMu.Lock()
					strategyBreakdown[string(b.Strategy)].OrdersSent++
					breakdownMu.Unlock()
				}
			}

			// Signal that this bot's send loop has finished
			sendLoopsDone.Done()

			// Wait for all bots to finish sending
			sendLoopsDone.Wait()

			// Wait a brief grace period to drain remaining responses
			time.Sleep(500 * time.Millisecond)
			conn.Close(websocket.StatusNormalClosure, "pretest completed")
			<-readerDone
			return nil
		})
	}

	// 3. Start a coordinator goroutine to close eventChan when all bot runners are done
	go func() {
		botRunnersDone.Wait()
		close(eventChan)
	}()

	err := g.Wait()
	close(doneChan)
	if err != nil {
		return PretestResults{}, err
	}

	// Calculate starting TPS vs ending TPS
	tpsSamplesMu.Lock()
	samples := tpsSamples
	tpsSamplesMu.Unlock()

	tpsStart := 0.0
	tpsEnd := 0.0
	if len(samples) >= 4 {
		// average of first 25% of samples
		quarter := len(samples) / 4
		var sumStart, sumEnd float64
		for i := 0; i < quarter; i++ {
			sumStart += samples[i]
			sumEnd += samples[len(samples)-1-i]
		}
		tpsStart = sumStart / float64(quarter)
		tpsEnd = sumEnd / float64(quarter)
	} else if len(samples) > 0 {
		// fallback if test is extremely fast
		tpsStart = samples[0]
		tpsEnd = samples[len(samples)-1]
	}

	// Default fallback values if no samples recorded
	if tpsStart == 0 {
		tpsStart = 100.0
		tpsEnd = 100.0
	}

	p99 := hist.ValueAtQuantile(99)
	p99Us := p99 / 1000 // convert to microseconds

	// Prevent overflow/out-of-bounds metrics
	if p99Us < 0 {
		p99Us = 0
	}

	correctnessScore := validator.GetCorrectnessScore()

	return PretestResults{
		Correctness:        correctnessScore,
		P99Us:              p99Us,
		OrdersSent:         totalSent.Load(),
		OrdersFailed:       totalFailed.Load(),
		TpsStart:           math.Round(tpsStart*100) / 100,
		TpsEnd:             math.Round(tpsEnd*100) / 100,
		PhantomFills:       validator.GetPhantomFills(),
		PriorityViolations: validator.GetPriorityViolations(),
		StrategyBreakdown:  strategyBreakdown,
	}, nil
}

type PretestEvent struct {
	Ack   OrderAck
	IsOwn bool
}

func processEvent(ev PretestEvent, validator *shadow.Validator, acceptedOrders map[int64]bool) {
	ack := ev.Ack
	status := strings.ToLower(ack.Status)
	if ev.IsOwn {
		if status == "accepted" {
			acceptedOrders[ack.OrderID] = true
			validator.ProcessAck(ack.OrderID, "accepted")
		} else if status == "filled" || status == "partial" {
			if !acceptedOrders[ack.OrderID] {
				acceptedOrders[ack.OrderID] = true
				validator.ProcessAck(ack.OrderID, "accepted")
			}
			// matchedWith can be omitted, we default to 0 if not present
			validator.ProcessFill(ack.OrderID, ack.FilledQty, ack.FilledPrice, ack.MatchedWith)
		} else {
			validator.ProcessAck(ack.OrderID, status)
		}
	} else {
		if status == "filled" || status == "partial" {
			validator.ProcessFill(ack.OrderID, ack.FilledQty, ack.FilledPrice, ack.MatchedWith)
		}
	}
}
