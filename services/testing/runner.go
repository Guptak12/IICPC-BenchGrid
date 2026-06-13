package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/coder/websocket"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/guptak12/bot-fleet/shadow"
	"google.golang.org/protobuf/proto"
	"iicpc-sandbox/pkg/protocol"
	"iicpc-sandbox/services/common"
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
	SendTimes    []int64
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
		SendTimes:    make([]int64, ordersToSend+1),
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

// RunFleet executes a customizable deterministic bot fleet over raw TCP
func RunFleet(ctx context.Context, endpoint string, baseSeed int64, numBots int, ordersPerBot int, protocolStr string, isSystest bool) (PretestResults, error) {
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

	// Create deterministic bots
	bots := make([]*PretestBot, numBots)
	strategies := []StrategyType{MarketMaker, MomentumTrader, NoiseTrader}

	ratePerSec := 50.0
	if val, err := strconv.ParseFloat(os.Getenv("BOT_RATE_PER_SEC"), 64); err == nil && val > 0 {
		ratePerSec = val
	}

	for i := 0; i < numBots; i++ {
		strat := strategies[i%len(strategies)]
		bots[i] = NewPretestBot(
			int64(i+1),
			fmt.Sprintf("bot-%d", i+1),
			strat,
			100.0, // Mid Price
			0.10,  // Spread
			ordersPerBot,
			ratePerSec,
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

	startTime := time.Now().UnixNano()
	type TimeWindow struct {
		Successes int64
		Failures  int64
	}
	bins := make(map[int64]*TimeWindow)
	getOrCreateBin := func(tNs int64) *TimeWindow {
		idx := (tNs - startTime) / 1_000_000_000
		if idx < 0 {
			idx = 0
		}
		w, exists := bins[idx]
		if !exists {
			w = &TimeWindow{}
			bins[idx] = w
		}
		return w
	}

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
				if curr < int64(numBots*ordersPerBot) || len(tpsSamples) == 0 || tpsSamples[len(tpsSamples)-1] > 0 {
					tpsSamples = append(tpsSamples, tps)
				}
				tpsSamplesMu.Unlock()

				// Update real-time Prometheus gauges
				common.FleetTPS.Set(tps)

				histMu.Lock()
				p99 := rttHist.ValueAtQuantile(99)
				histMu.Unlock()
				p99Us := p99 / 1000
				if p99Us < 0 {
					p99Us = 0
				}
				common.FleetP99Us.Set(float64(p99Us))
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

		for {
			select {
			case <-gctx.Done():
				return nil
			case ev, ok := <-eventChan:
				if !ok {
					for len(jitterBuffer) > 0 {
						if item, exists := jitterBuffer[nextSeqID]; exists {
							processEvent(item, validator, acceptedOrders)
							recordMetrics(item, bots, &histMu, rttHist, engineHist, &breakdownMu, strategyBreakdown)
							
							if item.Report != nil {
								status := strings.ToLower(item.Report.Status.String())
								if status == "rejected" {
									getOrCreateBin(item.ReceivedAt).Failures++
								} else {
									getOrCreateBin(item.ReceivedAt).Successes++
								}
							}

							delete(jitterBuffer, nextSeqID)
							nextSeqID++
						} else {
							minKey := int64(-1)
							for k := range jitterBuffer {
								if minKey == -1 || k < minKey {
									minKey = k
								}
							}
							if minKey != -1 && minKey > nextSeqID {
								nextSeqID = minKey
							} else {
								break
							}
						}
					}
					return nil
				}

				if ev.IsFailure {
					getOrCreateBin(ev.ReceivedAt).Failures++
					continue
				}

				if ev.Report == nil {
					continue
				}

				jitterBuffer[int64(ev.Report.EngineSeqId)] = ev
				for {
					item, exists := jitterBuffer[nextSeqID]
					if !exists {
						break
					}
					processEvent(item, validator, acceptedOrders)
					recordMetrics(item, bots, &histMu, rttHist, engineHist, &breakdownMu, strategyBreakdown)
					
					if item.Report != nil {
						status := strings.ToLower(item.Report.Status.String())
						if status == "rejected" {
							getOrCreateBin(item.ReceivedAt).Failures++
						} else {
							getOrCreateBin(item.ReceivedAt).Successes++
						}
					}

					delete(jitterBuffer, nextSeqID)
					nextSeqID++
				}
			}
		}
	})

	// 2. Spawn bots concurrently using the appropriate protocol adapter
	for _, bot := range bots {
		b := bot
		g.Go(func() error {
			defer botRunnersDone.Done()

			var adapter ProtocolAdapter
			switch strings.ToUpper(protocolStr) {
			case "WS":
				adapter = &WebSocketAdapter{}
			case "REST":
				adapter = &RESTAdapter{}
			case "FIX":
				adapter = &FIXAdapter{}
			default:
				adapter = &TCPProtobufAdapter{}
			}

			if err := adapter.Init(gctx, endpoint, b.NumericID); err != nil {
				return fmt.Errorf("adapter Init failed for %s using protocol %s: %w", b.StringID, protocolStr, err)
			}
			defer adapter.Close()

			if err := adapter.StartReceiver(gctx, eventChan); err != nil {
				return fmt.Errorf("adapter StartReceiver failed for %s: %w", b.StringID, err)
			}

			var scheduler *MMPPScheduler
			var limiter *rate.Limiter
			if !isSystest {
				botRate := 100.0 / float64(numBots)
				limiter = rate.NewLimiter(rate.Limit(botRate), 5)
			} else {
				scheduler = NewMMPPScheduler(b.RatePerSec, b.Seed, numBots, isSystest, b.NumericID)
			}

			// Send orders loop
			for i := 0; i < b.OrdersToSend; i++ {
				if !isSystest {
					if err := limiter.Wait(gctx); err != nil {
						return nil
					}
				} else {
					sleepDur := scheduler.NextSleep()
					if sleepDur > 0 {
						time.Sleep(sleepDur)
					}
					select {
					case <-gctx.Done():
						return nil
					default:
					}
				}

				order := b.GenerateNextOrder()
				validator.ProcessOrder(int64(order.OrderId), order.Type.String(), order.Side.String(), order.Price, int64(order.Quantity))

				seq := int64(order.OrderId & 0xFFFFFFFF)
				b.SendTimes[seq] = time.Now().UnixNano()

				err := adapter.SendOrder(gctx, order)
				if err != nil {
					totalFailed.Add(1)
					log.Printf("[%s] write error: %v\n", b.StringID, err)

					breakdownMu.Lock()
					strategyBreakdown[string(b.Strategy)].OrdersFailed++
					breakdownMu.Unlock()

					select {
					case <-gctx.Done():
					case eventChan <- PretestEvent{ReceivedAt: time.Now().UnixNano(), IsFailure: true}:
					}
					break
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

	// Map timeouts (sent orders that were never accepted) as failures in their respective send-time bins
	for _, b := range bots {
		for seq := 1; seq <= b.OrdersToSend; seq++ {
			sendTime := b.SendTimes[seq]
			if sendTime > 0 {
				orderID := (int64(b.NumericID) << 32) | int64(seq)
				if !acceptedOrders[orderID] {
					idx := (sendTime - startTime) / 1_000_000_000
					if idx < 0 {
						idx = 0
					}
					w, exists := bins[idx]
					if !exists {
						w = &TimeWindow{}
						bins[idx] = w
					}
					w.Failures++
				}
			}
		}
	}

	// Calculate MaxSustainedTPS: maximum successes count in any bin that contains exactly 0 failures
	maxSustainedTPS := 0.0
	for _, w := range bins {
		if w.Failures == 0 {
			if float64(w.Successes) > maxSustainedTPS {
				maxSustainedTPS = float64(w.Successes)
			}
		}
	}

	// Print all percentile metrics in the log
	log.Printf("[RunFleet] Finished: RTT P50: %dµs | P90: %dµs | P99: %dµs | Engine Reported P99: %dµs | Max Sustained TPS: %.2f\n", p50Us, p90Us, p99Us, engineP99Us, maxSustainedTPS)

	correctnessScore := validator.GetCorrectnessScore()

	return PretestResults{
		Protocol:           protocolStr,
		Correctness:        correctnessScore,
		P50Us:              p50Us,
		P90Us:              p90Us,
		P99Us:              p99Us,
		EngineP99Us:        engineP99Us,
		OrdersSent:         totalSent.Load(),
		OrdersFailed:       totalFailed.Load(),
		TpsStart:           math.Round(tpsStart*100) / 100,
		TpsEnd:             math.Round(tpsEnd*100) / 100,
		MaxSustainedTPS:    maxSustainedTPS,
		IsSystest:          isSystest,
		PhantomFills:       validator.GetPhantomFills(),
		PriorityViolations: validator.GetPriorityViolations(),
		StrategyBreakdown:  strategyBreakdown,
	}, nil
}

type PretestEvent struct {
	Report     *protocol.ExecutionReport
	IsOwn      bool
	ReceivedAt int64
	IsFailure  bool
}

// MMPPState represents the current volatility regime
type MMPPState int

const (
	CalmState MMPPState = iota
	ElevatedState
	PanicState
)

type MMPPScheduler struct {
	state      MMPPState
	baseRate   float64
	rng        *rand.Rand
	lastSwitch time.Time
	regimeDur  time.Duration
	numBots    int
	isSystest  bool
	botID      int64
	orderCount int
}

func NewMMPPScheduler(baseRate float64, seed int64, numBots int, isSystest bool, botID int64) *MMPPScheduler {
	rng := rand.New(rand.NewSource(seed))
	return &MMPPScheduler{
		state:      CalmState,
		baseRate:   baseRate,
		rng:        rng,
		lastSwitch: time.Now(),
		regimeDur:  time.Duration(100+rng.Intn(200)) * time.Millisecond,
		numBots:    numBots,
		isSystest:  isSystest,
		botID:      botID,
	}
}

func (s *MMPPScheduler) NextSleep() time.Duration {
	now := time.Now()
	if now.Sub(s.lastSwitch) >= s.regimeDur {
		s.lastSwitch = now
		s.regimeDur = time.Duration(100+s.rng.Intn(200)) * time.Millisecond
		roll := s.rng.Float64()
		switch s.state {
		case CalmState:
			if roll < 0.15 {
				s.state = ElevatedState
			} else if roll < 0.20 {
				s.state = PanicState
			}
		case ElevatedState:
			if roll < 0.20 {
				s.state = CalmState
			} else if roll < 0.30 {
				s.state = PanicState
			}
		case PanicState:
			if roll < 0.50 {
				s.state = ElevatedState
			} else if roll < 0.70 {
				s.state = CalmState
			}
		}
	}

	var rate float64
	if s.isSystest {
		s.orderCount++
		botsCount := s.numBots
		if botsCount <= 0 {
			botsCount = 1
		}
		if s.orderCount <= 100 {
			rate = 10000.0 / float64(botsCount)
		} else {
			if s.orderCount == 101 && s.botID == 1 {
				log.Printf("[MMPP_TRANSITION_TO_BURST] Transitioning from warm-up to MMPP burst scheduling at %s\n", time.Now().Format(time.RFC3339))
			}
			switch s.state {
			case CalmState:
				rate = 1000.0 / float64(botsCount)
			case ElevatedState:
				rate = 20000.0 / float64(botsCount)
			case PanicState:
				rate = 500000.0 / float64(botsCount)
			}
		}
	} else {
		switch s.state {
		case CalmState:
			rate = s.baseRate * 0.3
		case ElevatedState:
			rate = s.baseRate * 1.2
		case PanicState:
			rate = s.baseRate * 4.0
		}
	}

	if rate <= 0 {
		rate = 1.0
	}

	u := s.rng.Float64()
	if u >= 1.0 {
		u = 0.9999
	}
	deltaSeconds := -math.Log(1.0-u) / rate
	return time.Duration(deltaSeconds * float64(time.Second))
}

func recordMetrics(ev PretestEvent, bots []*PretestBot, histMu *sync.Mutex, rttHist, engineHist *hdrhistogram.Histogram, breakdownMu *sync.Mutex, strategyBreakdown map[string]*StrategyMetrics) {
	report := ev.Report
	if report == nil {
		return
	}

	botIDVal := int64(report.OrderId >> 32)
	if botIDVal <= 0 || botIDVal > int64(len(bots)) {
		return
	}
	b := bots[botIDVal-1]

	seq := int64(report.OrderId & 0xFFFFFFFF)
	var sendTime int64
	if seq > 0 && seq < int64(len(b.SendTimes)) {
		sendTime = b.SendTimes[seq]
	}

	if sendTime > 0 && ev.ReceivedAt > 0 {
		rttNs := ev.ReceivedAt - sendTime
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
		if sm != nil {
			sm.TotalLatency += latencyUs
			sm.AckCount++
			if report.Status == protocol.ExecutionStatus_REJECTED {
				sm.OrdersFailed++
			}
		}
		breakdownMu.Unlock()
	}
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

// ProtocolAdapter interface abstracts multi-protocol network stress clients
type ProtocolAdapter interface {
	Init(ctx context.Context, endpoint string, botID int64) error
	SendOrder(ctx context.Context, order *protocol.Order) error
	StartReceiver(ctx context.Context, eventChan chan<- PretestEvent) error
	Close() error
}

type TCPProtobufAdapter struct {
	conn  net.Conn
	botID int64
}

func (a *TCPProtobufAdapter) Init(ctx context.Context, endpoint string, botID int64) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	a.conn = conn
	a.botID = botID
	return nil
}

func (a *TCPProtobufAdapter) SendOrder(ctx context.Context, order *protocol.Order) error {
	payload, err := proto.Marshal(order)
	if err != nil {
		return err
	}
	lengthPrefix := make([]byte, 4)
	binary.LittleEndian.PutUint32(lengthPrefix, uint32(len(payload)))
	_ = a.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = a.conn.Write(lengthPrefix)
	if err == nil {
		_, err = a.conn.Write(payload)
	}
	return err
}

func (a *TCPProtobufAdapter) StartReceiver(ctx context.Context, eventChan chan<- PretestEvent) error {
	go func() {
		for {
			var length uint32
			err := binary.Read(a.conn, binary.LittleEndian, &length)
			receivedAt := time.Now().UnixNano()
			if err != nil {
				return
			}
			payload := make([]byte, length)
			_, err = io.ReadFull(a.conn, payload)
			if err != nil {
				return
			}
			var report protocol.ExecutionReport
			if err := proto.Unmarshal(payload, &report); err == nil {
				isOwn := int64(report.OrderId>>32) == a.botID
				select {
				case <-ctx.Done():
					return
				case eventChan <- PretestEvent{Report: &report, IsOwn: isOwn, ReceivedAt: receivedAt}:
				}
			}
		}
	}()
	return nil
}

func (a *TCPProtobufAdapter) Close() error {
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

type WebSocketAdapter struct {
	conn  *websocket.Conn
	ctx   context.Context
	botID int64
}

func (a *WebSocketAdapter) Init(ctx context.Context, endpoint string, botID int64) error {
	url := endpoint
	if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
		url = "ws://" + url
	}
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	a.conn = conn
	a.ctx = ctx
	a.botID = botID
	return nil
}

func (a *WebSocketAdapter) SendOrder(ctx context.Context, order *protocol.Order) error {
	orderMap := map[string]interface{}{
		"bot_id":   order.BotId,
		"order_id": order.OrderId,
		"type":     order.Type.String(),
		"side":     order.Side.String(),
		"price":    order.Price,
		"quantity": order.Quantity,
	}
	payload, err := json.Marshal(orderMap)
	if err != nil {
		return err
	}
	return a.conn.Write(ctx, websocket.MessageText, payload)
}

func (a *WebSocketAdapter) StartReceiver(ctx context.Context, eventChan chan<- PretestEvent) error {
	go func() {
		for {
			_, payload, err := a.conn.Read(a.ctx)
			receivedAt := time.Now().UnixNano()
			if err != nil {
				return
			}
			var repMap map[string]interface{}
			if err := json.Unmarshal(payload, &repMap); err == nil {
				report := mapJSONToExecutionReport(repMap)
				isOwn := int64(report.OrderId>>32) == a.botID
				select {
				case <-ctx.Done():
					return
				case eventChan <- PretestEvent{Report: report, IsOwn: isOwn, ReceivedAt: receivedAt}:
				}
			}
		}
	}()
	return nil
}

func (a *WebSocketAdapter) Close() error {
	if a.conn != nil {
		return a.conn.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}

func mapJSONToExecutionReport(repMap map[string]interface{}) *protocol.ExecutionReport {
	orderID, _ := getFloatAsUint64(repMap["order_id"])
	statusStr, _ := repMap["status"].(string)
	filledQty, _ := getFloatAsUint64(repMap["filled_qty"])
	filledPrice, _ := getFloatAsInt64(repMap["filled_price"])
	engineSeqID, _ := getFloatAsUint64(repMap["engine_seq_id"])
	processingNs, _ := getFloatAsUint64(repMap["processing_ns"])
	matchedWith, _ := getFloatAsUint64(repMap["matched_with"])

	var status protocol.ExecutionStatus
	switch strings.ToUpper(statusStr) {
	case "ACCEPTED":
		status = protocol.ExecutionStatus_ACCEPTED
	case "FILLED":
		status = protocol.ExecutionStatus_FILLED
	case "PARTIAL":
		status = protocol.ExecutionStatus_PARTIAL
	case "REJECTED":
		status = protocol.ExecutionStatus_REJECTED
	case "CANCELLED":
		status = protocol.ExecutionStatus_CANCELLED
	}

	return &protocol.ExecutionReport{
		OrderId:      orderID,
		Status:       status,
		FilledQty:    filledQty,
		FilledPrice:  filledPrice,
		EngineSeqId:  engineSeqID,
		ProcessingNs: processingNs,
		MatchedWith:  matchedWith,
	}
}

func getFloatAsUint64(v interface{}) (uint64, bool) {
	if f, ok := v.(float64); ok {
		return uint64(f), true
	}
	return 0, false
}

func getFloatAsInt64(v interface{}) (int64, bool) {
	if f, ok := v.(float64); ok {
		return int64(f), true
	}
	return 0, false
}

type RESTAdapter struct {
	client    *http.Client
	endpoint  string
	botID     int64
	sseConn   io.ReadCloser
	orderChan chan *protocol.Order
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
}

func (a *RESTAdapter) Init(ctx context.Context, endpoint string, botID int64) error {
	a.endpoint = endpoint
	a.botID = botID
	t := &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	a.client = &http.Client{Transport: t}

	a.orderChan = make(chan *protocol.Order, 50000)
	a.ctx, a.cancel = context.WithCancel(context.Background())

	// Start 50 concurrent worker goroutines
	const numWorkers = 50
	for i := 0; i < numWorkers; i++ {
		a.wg.Add(1)
		go a.worker()
	}

	return nil
}

func (a *RESTAdapter) worker() {
	defer a.wg.Done()

	url := a.endpoint
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	postURL := url + "/api/v1/orders"

	for order := range a.orderChan {
		orderMap := map[string]interface{}{
			"bot_id":   order.BotId,
			"order_id": order.OrderId,
			"type":     order.Type.String(),
			"side":     order.Side.String(),
			"price":    order.Price,
			"quantity": order.Quantity,
		}
		payload, err := json.Marshal(orderMap)
		if err != nil {
			continue
		}

		reqCtx, reqCancel := context.WithTimeout(a.ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, "POST", postURL, bytes.NewReader(payload))
		if err != nil {
			reqCancel()
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := a.client.Do(req)
		reqCancel()
		if err != nil {
			continue
		}
		resp.Body.Close()
	}
}

func (a *RESTAdapter) SendOrder(ctx context.Context, order *protocol.Order) error {
	select {
	case <-a.ctx.Done():
		return a.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case a.orderChan <- order:
		return nil
	default:
		return fmt.Errorf("RESTAdapter order channel buffer full")
	}
}

func (a *RESTAdapter) StartReceiver(ctx context.Context, eventChan chan<- PretestEvent) error {
	url := a.endpoint
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url+"/api/v1/events", nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	a.sseConn = resp.Body

	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			receivedAt := time.Now().UnixNano()
			if strings.HasPrefix(line, "data:") {
				dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				var repMap map[string]interface{}
				if err := json.Unmarshal([]byte(dataStr), &repMap); err == nil {
					report := mapJSONToExecutionReport(repMap)
					isOwn := int64(report.OrderId>>32) == a.botID
					if isOwn {
						select {
						case <-ctx.Done():
							return
						case eventChan <- PretestEvent{Report: report, IsOwn: isOwn, ReceivedAt: receivedAt}:
						}
					}
				}
			}
		}
	}()
	return nil
}

func (a *RESTAdapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.orderChan != nil {
		close(a.orderChan)
	}
	a.wg.Wait()

	if a.sseConn != nil {
		return a.sseConn.Close()
	}
	return nil
}

type FIXAdapter struct {
	conn     net.Conn
	botID    int64
	seqIn    int64
	seqOut   int64
	compID   string
	targetID string
}

func (a *FIXAdapter) Init(ctx context.Context, endpoint string, botID int64) error {
	a.botID = botID
	a.compID = fmt.Sprintf("BOT-%d", botID)
	a.targetID = "CONTESTANT"

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	a.conn = conn

	// Send Logon (MsgType=A)
	a.seqOut++
	logonFields := map[int]string{
		8:   "FIX.4.4",
		35:  "A",
		49:  a.compID,
		56:  a.targetID,
		34:  strconv.FormatInt(a.seqOut, 10),
		98:  "0",
		108: "30",
	}
	_, err = conn.Write(BuildFIX(logonFields))
	if err != nil {
		conn.Close()
		return err
	}

	// Read Logon reply
	reader := bufio.NewReader(conn)
	respBytes, err := reader.ReadBytes(1)
	if err != nil {
		conn.Close()
		return err
	}
	for {
		line, err := reader.ReadBytes(1)
		if err != nil {
			break
		}
		respBytes = append(respBytes, line...)
		if bytes.Contains(respBytes, []byte("\x0110=")) && bytes.HasSuffix(respBytes, []byte("\x01")) {
			break
		}
	}
	tags := ParseFIX(respBytes)
	if tags[35] != "A" {
		conn.Close()
		return fmt.Errorf("expected Logon reply A, got %s", tags[35])
	}
	a.seqIn = 1
	return nil
}

func (a *FIXAdapter) SendOrder(ctx context.Context, order *protocol.Order) error {
	a.seqOut++
	var sideVal string
	if order.Side == protocol.Side_BUY {
		sideVal = "1"
	} else {
		sideVal = "2"
	}

	var typeVal string
	if order.Type == protocol.OrderType_LIMIT {
		typeVal = "2"
	} else {
		typeVal = "1"
	}

	fields := map[int]string{
		8:   "FIX.4.4",
		35:  "D",
		49:  a.compID,
		56:  a.targetID,
		34:  strconv.FormatInt(a.seqOut, 10),
		11:  strconv.FormatUint(order.OrderId, 10),
		54:  sideVal,
		38:  strconv.FormatUint(order.Quantity, 10),
		44:  strconv.FormatInt(order.Price, 10),
		40:  typeVal,
		55:  "BTCUSD",
		1:   strconv.FormatInt(a.botID, 10),
	}
	if order.Type == protocol.OrderType_CANCEL {
		cancelClOrdID := (int64(a.botID) << 32) | (time.Now().UnixNano() & 0xFFFFFFFF)
		fields[35] = "F"
		fields[11] = strconv.FormatInt(cancelClOrdID, 10)
		fields[41] = strconv.FormatUint(order.OrderId, 10)
	}

	_ = a.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := a.conn.Write(BuildFIX(fields))
	return err
}

func (a *FIXAdapter) StartReceiver(ctx context.Context, eventChan chan<- PretestEvent) error {
	go func() {
		reader := bufio.NewReader(a.conn)
		for {
			respBytes, err := reader.ReadBytes(1)
			receivedAt := time.Now().UnixNano()
			if err != nil {
				return
			}
			for {
				line, err := reader.ReadBytes(1)
				if err != nil {
					return
				}
				respBytes = append(respBytes, line...)
				if bytes.Contains(respBytes, []byte("\x0110=")) && bytes.HasSuffix(respBytes, []byte("\x01")) {
					break
				}
			}
			tags := ParseFIX(respBytes)
			a.seqIn++

			if tags[35] == "8" {
				orderID, _ := strconv.ParseUint(tags[11], 10, 64)
				statusStr := tags[39]
				var status protocol.ExecutionStatus
				switch statusStr {
				case "0", "accepted":
					status = protocol.ExecutionStatus_ACCEPTED
				case "1", "partial":
					status = protocol.ExecutionStatus_PARTIAL
				case "2", "filled":
					status = protocol.ExecutionStatus_FILLED
				case "4", "cancelled":
					status = protocol.ExecutionStatus_CANCELLED
				case "8", "rejected":
					status = protocol.ExecutionStatus_REJECTED
				default:
					status = protocol.ExecutionStatus_ACCEPTED
				}

				qty, _ := strconv.ParseUint(tags[32], 10, 64)
				if qty == 0 {
					qty, _ = strconv.ParseUint(tags[38], 10, 64)
				}
				price, _ := strconv.ParseInt(tags[31], 10, 64)
				if price == 0 {
					price, _ = strconv.ParseInt(tags[44], 10, 64)
				}
				seqID, _ := strconv.ParseUint(tags[17], 10, 64)
				if seqID == 0 {
					seqID, _ = strconv.ParseUint(tags[34], 10, 64)
				}
				processingNs, _ := strconv.ParseUint(tags[9000], 10, 64)
				matchedWith, _ := strconv.ParseUint(tags[9001], 10, 64)

				report := &protocol.ExecutionReport{
					OrderId:      orderID,
					Status:       status,
					FilledQty:    qty,
					FilledPrice:  price,
					EngineSeqId:  seqID,
					ProcessingNs: processingNs,
					MatchedWith:  matchedWith,
				}
				isOwn := int64(report.OrderId>>32) == a.botID
				select {
				case <-ctx.Done():
					return
				case eventChan <- PretestEvent{Report: report, IsOwn: isOwn, ReceivedAt: receivedAt}:
				}
			}
		}
	}()
	return nil
}

func (a *FIXAdapter) Close() error {
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

func ParseFIX(msg []byte) map[int]string {
	fields := make(map[int]string)
	parts := bytes.Split(msg, []byte{1})
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		eqIdx := bytes.IndexByte(part, '=')
		if eqIdx == -1 {
			continue
		}
		tag, err := strconv.Atoi(string(part[:eqIdx]))
		if err != nil {
			continue
		}
		fields[tag] = string(part[eqIdx+1:])
	}
	return fields
}

func BuildFIX(fields map[int]string) []byte {
	var bodyBuf bytes.Buffer
	bodyBuf.WriteString(fmt.Sprintf("35=%s\x01", fields[35]))
	for tag, val := range fields {
		if tag == 8 || tag == 9 || tag == 35 || tag == 10 {
			continue
		}
		bodyBuf.WriteString(fmt.Sprintf("%d=%s\x01", tag, val))
	}
	body := bodyBuf.Bytes()

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("8=%s\x01", fields[8]))
	buf.WriteString(fmt.Sprintf("9=%d\x01", len(body)))
	buf.Write(body)

	checksum := CalculateFIXChecksum(buf.Bytes())
	buf.WriteString(fmt.Sprintf("10=%03d\x01", checksum))
	return buf.Bytes()
}

func CalculateFIXChecksum(data []byte) int {
	sum := 0
	for _, b := range data {
		sum += int(b)
	}
	return sum % 256
}

