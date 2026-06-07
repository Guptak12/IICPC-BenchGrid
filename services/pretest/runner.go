package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/guptak12/bot-fleet/shadow"
	"google.golang.org/protobuf/proto"
	"iicpc-sandbox/pkg/protocol"
)

// Strategy Types matching bot-fleet
type StrategyType string

const (
	MarketMaker    StrategyType = "MARKET_MAKER"
	MomentumTrader StrategyType = "MOMENTUM_TRADER"
	NoiseTrader    StrategyType = "NOISE_TRADER"
)

type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

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

// GenerateNextOrder implements deterministic strategy generation for pretests returning Protobuf Orders
func (b *PretestBot) GenerateNextOrder() *protocol.Order {
	b.ordersSent++
	seq := b.seqNum.Add(1)

	var side protocol.Side = protocol.Side_BUY
	var orderType protocol.OrderType = protocol.OrderType_LIMIT
	var price int64 = b.MidPrice
	var qty int64 = 100
	var isCancel bool
	var cancelID int64

	switch b.Strategy {
	case MarketMaker:
		orderType = protocol.OrderType_LIMIT
		if seq%2 == 0 {
			side = protocol.Side_BUY
			variation := b.rng.Int63n(b.Spread/10 + 1)
			price = b.MidPrice - b.Spread/2 - variation
		} else {
			side = protocol.Side_SELL
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
			orderType = protocol.OrderType_MARKET
			price = 0
			if b.rng.Float64() < 0.2 {
				orderType = protocol.OrderType_LIMIT
				variation := b.rng.Int63n(b.Spread*2 + 1)
				price = b.MidPrice - b.Spread + variation
			}
			qty = 2000 + b.rng.Int63n(3000)
			side = protocol.Side_BUY
			if b.rng.Intn(2) == 0 {
				side = protocol.Side_SELL
			}
		}

	case NoiseTrader:
		roll := b.rng.Float64()
		switch {
		case roll < 0.60:
			orderType = protocol.OrderType_LIMIT
			variation := b.rng.Int63n(b.MidPrice/10 + 1)
			if b.rng.Intn(2) == 0 {
				price = b.MidPrice + variation
			} else {
				price = b.MidPrice - variation
			}
		case roll < 0.85:
			orderType = protocol.OrderType_MARKET
		default:
			if len(b.activeOrders) > 0 {
				cancelID = b.activeOrders[0]
				b.activeOrders = b.activeOrders[1:]
				isCancel = true
			} else {
				orderType = protocol.OrderType_LIMIT
				price = b.MidPrice
			}
		}
		if !isCancel {
			side = protocol.Side_BUY
			if b.rng.Intn(2) == 0 {
				side = protocol.Side_SELL
			}
			qty = b.zipfQuantity()
		}
	}

	if isCancel {
		return &protocol.Order{
			BotId:   uint64(b.NumericID),
			OrderId: uint64(cancelID),
			Type:    protocol.OrderType_CANCEL,
		}
	}

	orderID := (int64(b.NumericID) << 32) | (seq & 0xFFFFFFFF)
	if orderType == protocol.OrderType_LIMIT {
		b.activeOrders = append(b.activeOrders, orderID)
	}

	return &protocol.Order{
		BotId:    uint64(b.NumericID),
		OrderId:  uint64(orderID),
		Type:     orderType,
		Side:     side,
		Price:    price,
		Quantity: uint64(qty),
	}
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

// RunPretestFleet executes a small deterministic pretest suite (5 bots, 100 orders each) over raw TCP
func RunPretestFleet(ctx context.Context, endpoint string, baseSeed int64) (PretestResults, error) {
	// Edge Case 2: TCP Liveness Probe/Retry Loop with exponential backoff
	log.Printf("[debug] Dialing TCP liveness probe on %s...\n", endpoint)
	var probeConn net.Conn
	var probeErr error
	backoff := 50 * time.Millisecond
	for start := time.Now(); time.Since(start) < 10*time.Second; {
		probeConn, probeErr = net.DialTimeout("tcp", endpoint, 500*time.Millisecond)
		if probeErr == nil {
			// Set a short read deadline to detect false positives from pre-emptive proxies (e.g. Docker Desktop)
			probeConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			oneByte := make([]byte, 1)
			_, readErr := probeConn.Read(oneByte)
			if readErr != nil {
				if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
					// Timeout is expected and indicates the container's matching engine has accepted the connection and is waiting for data
					probeConn.Close()
					probeErr = nil
					break
				}
				probeErr = readErr
			} else {
				// Read returned data successfully, also implies active container connection
				probeConn.Close()
				probeErr = nil
				break
			}
			probeConn.Close()
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 1*time.Second {
			backoff = 1 * time.Second
		}
	}
	if probeErr != nil {
		return PretestResults{}, fmt.Errorf("contestant TCP server failed to listen on port 8000 within 10 seconds: %w", probeErr)
	}
	log.Printf("[debug] TCP liveness probe succeeded on %s ✓\n", endpoint)

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
	var histMu sync.Mutex
	rttHist := hdrhistogram.New(1, 10000000000, 3)
	engineHist := hdrhistogram.New(1, 10000000000, 3)

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
		nextSeqID := int64(1)
		jitterBuffer := make(map[int64]PretestEvent)
		jitterGapThreshold := 50

		for {
			select {
			case <-gctx.Done():
				return nil
			case ev, ok := <-eventChan:
				if !ok {
					for len(jitterBuffer) > 0 {
						if item, exists := jitterBuffer[nextSeqID]; exists {
							processEvent(item, validator, acceptedOrders)
							delete(jitterBuffer, nextSeqID)
							nextSeqID++
						} else {
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

				jitterBuffer[int64(ev.Report.EngineSeqId)] = ev
				for {
					item, exists := jitterBuffer[nextSeqID]
					if !exists {
						if len(jitterBuffer) >= jitterGapThreshold {
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

	// 2. Spawn bots concurrently dialing TCP Port 8000
	for _, bot := range bots {
		b := bot
		g.Go(func() error {
			defer botRunnersDone.Done()
			conn, err := net.Dial("tcp", endpoint)
			if err != nil {
				return fmt.Errorf("TCP connection failed for %s: %v", b.StringID, err)
			}
			defer conn.Close()

			limiter := rate.NewLimiter(rate.Limit(b.RatePerSec), 5)
			
			// Setup receiver loop reading Little-Endian length-prefixed Protobuf messages
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					var length uint32
					err := binary.Read(conn, binary.LittleEndian, &length)
					receivedAt := time.Now().UnixNano()
					if err != nil {
						return
					}

					payload := make([]byte, length)
					_, err = io.ReadFull(conn, payload)
					if err != nil {
						return
					}

					var report protocol.ExecutionReport
					if err := proto.Unmarshal(payload, &report); err != nil {
						continue
					}

					b.sendTimesMu.RLock()
					sendTime, ok := b.SendTimes[int64(report.OrderId)]
					b.sendTimesMu.RUnlock()

					if ok {
						rttNs := receivedAt - sendTime
						engineNs := int64(report.ProcessingNs)

						histMu.Lock()
						_ = rttHist.RecordValue(rttNs)
						if engineNs > 0 {
							_ = engineHist.RecordValue(engineNs)
						}
						histMu.Unlock()

						latencyUs := rttNs / 1000
						breakdownMu.Lock()
						sm := strategyBreakdown[string(b.Strategy)]
						sm.TotalLatency += latencyUs
						sm.AckCount++
						if report.Status == protocol.ExecutionStatus_REJECTED {
							sm.OrdersFailed++
						}
						breakdownMu.Unlock()
					}

					select {
					case <-gctx.Done():
						return
					case eventChan <- PretestEvent{Report: &report, IsOwn: ok}:
					}
				}
			}()

			// Send orders loop writing Little-Endian length-prefixed Protobuf messages
			for i := 0; i < b.OrdersToSend; i++ {
				if err := limiter.Wait(gctx); err != nil {
					break
				}

				order := b.GenerateNextOrder()
				validator.ProcessOrder(int64(order.OrderId), order.Type.String(), order.Side.String(), order.Price, int64(order.Quantity))

				payload, err := proto.Marshal(order)
				if err != nil {
					continue
				}

				// Write 4-byte length prefix (Little-Endian)
				lengthPrefix := make([]byte, 4)
				binary.LittleEndian.PutUint32(lengthPrefix, uint32(len(payload)))

				b.sendTimesMu.Lock()
				b.SendTimes[int64(order.OrderId)] = time.Now().UnixNano()
				b.sendTimesMu.Unlock()

				_, err = conn.Write(lengthPrefix)
				if err == nil {
					_, err = conn.Write(payload)
				}

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

			sendLoopsDone.Done()
			sendLoopsDone.Wait()

			// Wait a brief grace period to drain remaining responses
			time.Sleep(500 * time.Millisecond)
			return nil
		})
	}

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
		quarter := len(samples) / 4
		var sumStart, sumEnd float64
		for i := 0; i < quarter; i++ {
			sumStart += samples[i]
			sumEnd += samples[len(samples)-1-i]
		}
		tpsStart = sumStart / float64(quarter)
		tpsEnd = sumEnd / float64(quarter)
	} else if len(samples) > 0 {
		tpsStart = samples[0]
		tpsEnd = samples[len(samples)-1]
	}

	if tpsStart == 0 {
		tpsStart = 100.0
		tpsEnd = 100.0
	}

	p50 := rttHist.ValueAtQuantile(50)
	p50Us := p50 / 1000
	if p50Us < 0 {
		p50Us = 0
	}

	p90 := rttHist.ValueAtQuantile(90)
	p90Us := p90 / 1000
	if p90Us < 0 {
		p90Us = 0
	}

	p99 := rttHist.ValueAtQuantile(99)
	p99Us := p99 / 1000
	if p99Us < 0 {
		p99Us = 0
	}

	engineP99 := engineHist.ValueAtQuantile(99)
	engineP99Us := engineP99 / 1000
	if engineP99Us < 0 {
		engineP99Us = 0
	}

	// Print all percentile metrics in the log
	log.Printf("[RunPretestFleet] Finished: RTT P50: %dµs | P90: %dµs | P99: %dµs | Engine Reported P99: %dµs\n", p50Us, p90Us, p99Us, engineP99Us)

	correctnessScore := validator.GetCorrectnessScore()

	return PretestResults{
		Correctness:        correctnessScore,
		P50Us:              p50Us,
		P90Us:              p90Us,
		P99Us:              p99Us,
		EngineP99Us:        engineP99Us,
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
	Report *protocol.ExecutionReport
	IsOwn  bool
}

func processEvent(ev PretestEvent, validator *shadow.Validator, acceptedOrders map[int64]bool) {
	report := ev.Report
	status := strings.ToLower(report.Status.String())
	orderID := int64(report.OrderId)
	if ev.IsOwn {
		if status == "accepted" {
			acceptedOrders[orderID] = true
			validator.ProcessAck(orderID, "accepted")
		} else if status == "filled" || status == "partial" {
			if !acceptedOrders[orderID] {
				acceptedOrders[orderID] = true
				validator.ProcessAck(orderID, "accepted")
			}
			validator.ProcessFill(orderID, int64(report.FilledQty), report.FilledPrice, int64(report.MatchedWith))
		} else if status == "cancelled" {
			validator.ProcessAck(orderID, "cancelled")
		} else {
			validator.ProcessAck(orderID, status)
		}
	} else {
		if status == "filled" || status == "partial" {
			validator.ProcessFill(orderID, int64(report.FilledQty), report.FilledPrice, int64(report.MatchedWith))
		}
	}
}
