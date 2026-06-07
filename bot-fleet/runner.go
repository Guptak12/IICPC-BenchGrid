package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	pb "github.com/guptak12/bot-fleet/gen/fleet"
	"github.com/guptak12/bot-fleet/telemetry"
	protocol "iicpc-sandbox/pkg/protocol"
)

const (
	// maxConcurrentBots caps simultaneous connections
	maxConcurrentBots = 500
	dialTimeout       = 5 * time.Second
	ackTimeout        = 5 * time.Second
	readPollInterval  = 100 * time.Millisecond
	fillDrainGrace    = 250 * time.Millisecond
	debugLogs         = true
)

// Add worker addresses to config
type MasterConfig struct {
	WorkerAddresses []string // e.g. ["worker1:5001", "worker2:5002"]
}

var masterCfg = MasterConfig{
	WorkerAddresses: []string{
		"localhost:5001",
		"localhost:5002",
		"localhost:5003",
	},
}

func mapOrderType(t OrderType) protocol.OrderType {
	switch t {
	case Limit:
		return protocol.OrderType_LIMIT
	case Market:
		return protocol.OrderType_MARKET
	case Cancel:
		return protocol.OrderType_CANCEL
	default:
		return protocol.OrderType_LIMIT
	}
}

func mapSide(s Side) protocol.Side {
	switch s {
	case Buy:
		return protocol.Side_BUY
	case Sell:
		return protocol.Side_SELL
	default:
		return protocol.Side_BUY
	}
}

func cleanEndpoint(endpoint string) string {
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err == nil {
			return u.Host
		}
	}
	return endpoint
}

// runFleet spawns all bots concurrently with a semaphore bound.
func runFleet(ctx context.Context, bots []*Bot, cfg FleetConfig, producer *telemetry.Producer, jobID string, workerID string) []BotResult {
	results := make([]BotResult, len(bots))

	// Clean the endpoint and run a single TCP startup liveness probe/retry loop with exponential backoff
	cleanedAddr := cleanEndpoint(cfg.Endpoint)
	log.Printf("[debug] Dialing TCP liveness probe on %s...\n", cleanedAddr)
	var probeConn net.Conn
	var probeErr error
	backoff := 50 * time.Millisecond
	for start := time.Now(); time.Since(start) < 10*time.Second; {
		probeConn, probeErr = net.DialTimeout("tcp", cleanedAddr, 500*time.Millisecond)
		if probeErr == nil {
			probeConn.Close()
			break
		}
		select {
		case <-ctx.Done():
			probeErr = ctx.Err()
			break
		case <-time.After(backoff):
		}
		if probeErr == ctx.Err() {
			break
		}
		backoff *= 2
		if backoff > 1*time.Second {
			backoff = 1 * time.Second
		}
	}
	if probeErr != nil {
		log.Printf("[error] Liveness probe failed on %s: %v\n", cleanedAddr, probeErr)
		// Mark all bots as failed
		for i, b := range bots {
			results[i] = BotResult{
				BotID:        b.config.StringID,
				Strategy:     b.config.Strategy,
				Histogram:    newHistogram(),
				OrdersFailed: b.config.OrdersToSend,
			}
		}
		return results
	}
	log.Printf("[debug] TCP liveness probe succeeded on %s ✓\n", cleanedAddr)

	// Semaphore: bufferred channel limits concurrent bots
	sem := make(chan struct{}, maxConcurrentBots)

	// Progress counter — incremented atomically by each bot per order sent
	var totalSent atomic.Int64

	g, gctx := errgroup.WithContext(ctx)

	// Progress reporter — logs TPS every second until all bots finish
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		var lastSent int64
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				current := totalSent.Load()
				tps := current - lastSent
				lastSent = current
				log.Printf("Progress: %d orders sent | %d TPS\n", current, tps)
			}
		}
	}()

	for i, bot := range bots {
		idx, b := i, bot // capture loop variables for closure

		// Acquire semaphore slot before launching goroutine
		sem <- struct{}{}

		g.Go(func() error {
			defer func() { <-sem }() // release slot when done

			// Build the strategy for this bot
			strategy := newStrategy(b)

			results[idx] = runBot(gctx, b, cfg.Endpoint, strategy, &totalSent, producer, jobID, workerID)
			// All orders failed = fatal — cancel the fleet
			if results[idx].OrdersFailed == b.config.OrdersToSend &&
				b.config.OrdersToSend > 0 {
				return fmt.Errorf("[%s] all %d orders failed — aborting fleet",
					b.config.StringID, b.config.OrdersToSend)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		debugLog("Fleet aborted: %v\n", err)
	}

	close(progressDone)

	if producer != nil {
		producer.PublishWorkerDone(telemetry.WorkerDoneEvent{
			Type:      telemetry.EventWorkerDone,
			JobID:     jobID,
			WorkerID:  workerID,
			TotalSent: int64(totalSent.Load()),
		})
		producer.Close() // Flush the final buffers cleanly
	}

	return results
}

func runDistributed(ctx context.Context, cfg FleetConfig, jobID string) (*FleetReport, error) {
	workers := masterCfg.WorkerAddresses
	numWorkers := len(workers)

	if numWorkers == 0 {
		return nil, fmt.Errorf("no workers configured")
	}

	// Shard bots evenly across workers
	botsPerWorker := cfg.NumBots / numWorkers
	remainder := cfg.NumBots % numWorkers

	type shardResponse struct {
		result *pb.ShardResult
		err    error
	}

	resultCh := make(chan shardResponse, numWorkers)

	for i, addr := range workers {
		workerIdx := i
		workerAddr := addr

		// Calculate shard size — last worker gets remainder
		shardSize := botsPerWorker
		if workerIdx == numWorkers-1 {
			shardSize += remainder
		}

		// Bot ID offset ensures globally unique OrderIDs across workers
		botIDOffset := int64(workerIdx * botsPerWorker)

		go func() {
			conn, err := grpc.NewClient(workerAddr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				resultCh <- shardResponse{err: fmt.Errorf("worker %s connect failed: %v", workerAddr, err)}
				return
			}
			defer conn.Close()

			client := pb.NewWorkerServiceClient(conn)

			// Give each shard a generous timeout
			shardCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()

			stream, err := client.RunShard(shardCtx, &pb.ShardRequest{
				JobId:          jobID,
				Endpoint:       cfg.Endpoint,
				NumBots:        int32(shardSize),
				OrdersPerBot:   int32(cfg.OrdersPerBot),
				MidPrice:       cfg.MidPrice,
				Spread:         cfg.Spread,
				RatePerSec:     cfg.RatePerSec,
				MarketMakerPct: cfg.StrategyMix.MarketMaker,
				MomentumPct:    cfg.StrategyMix.MomentumTrader,
				NoisePct:       cfg.StrategyMix.NoiseTrader,
				BotIdOffset:    botIDOffset,
				Seed:		    cfg.Seed,
			})

			if err != nil {
				resultCh <- shardResponse{err: fmt.Errorf("worker %s shard start failed: %v", workerAddr, err)}
				return
			}

			var finalResult *pb.ShardResult
			for {
				res, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					resultCh <- shardResponse{err: fmt.Errorf("worker %s stream read error: %v", workerAddr, err)}
					return
				}

				if res.IsFinal {
					finalResult = res
				}
			}

			if finalResult == nil {
				resultCh <- shardResponse{err: fmt.Errorf("worker %s closed stream without sending final result", workerAddr)}
				return
			}

			resultCh <- shardResponse{result: finalResult, err: nil}
		}()
	}

	// Collect all shard results
	globalHist := newHistogram()
	var totalSent, totalFailed int
	var errs []string

	for i := 0; i < numWorkers; i++ {
		resp := <-resultCh
		if resp.err != nil {
			errs = append(errs, resp.err.Error())
			continue
		}

		totalSent += int(resp.result.OrdersSent)
		totalFailed += int(resp.result.OrdersFailed)

		if len(resp.result.Histogram) > 0 {
			h, err := deserialiseHistogram(resp.result.Histogram)
			if err == nil {
				globalHist.Merge(h)
			}
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("shard failures: %v", strings.Join(errs, ", "))
	}

	return &FleetReport{
		Status:       "completed",
		NumBots:      cfg.NumBots,
		TotalOrders:  cfg.NumBots * cfg.OrdersPerBot,
		OrdersSent:   totalSent,
		OrdersFailed: totalFailed,
		P50Us:        float64(globalHist.ValueAtQuantile(50)) / 1000.0,
		P90Us:        float64(globalHist.ValueAtQuantile(90)) / 1000.0,
		P99Us:        float64(globalHist.ValueAtQuantile(99)) / 1000.0,
		MaxUs:        float64(globalHist.Max()) / 1000.0,
	}, nil
}

// runBot is the single-bot execution loop with the real raw TCP send/receive loop.
func runBot(ctx context.Context, b *Bot, endpoint string, strategy Strategy, totalSent *atomic.Int64, producer *telemetry.Producer, jobID string, workerID string) BotResult {
	result := BotResult{
		BotID:    b.config.StringID,
		Strategy: b.config.Strategy,
		Histogram: newHistogram(),
	}

	cleanedAddr := cleanEndpoint(endpoint)

	// Dial with a fail-fast timeout
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", cleanedAddr)
	dialCancel()

	if err != nil {
		log.Printf("[%s] TCP dial failed: %v\n", b.config.StringID, err)
		result.OrdersFailed += b.config.OrdersToSend
		return result
	}
	defer conn.Close()

	type latResult struct {
		latencyNs int64
		failed    bool
	}
	latencyCh := make(chan latResult, b.config.OrdersToSend)

	var senderDone atomic.Bool
	var lastReportNs atomic.Int64
	lastReportNs.Store(time.Now().UnixNano())

	pendingMu := sync.Mutex{}
	pendingAcks := make(map[int64]int64, b.config.OrdersToSend) // orderID -> send time ns

	removePending := func(orderID int64) (int64, bool) {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		sendTime, ok := pendingAcks[orderID]
		if ok {
			delete(pendingAcks, orderID)
		}
		return sendTime, ok
	}

	pendingLen := func() int {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		return len(pendingAcks)
	}

	failExpiredPending := func(now time.Time) {
		deadlineNs := now.Add(-ackTimeout).UnixNano()
		var expired int

		pendingMu.Lock()
		for orderID, sentAt := range pendingAcks {
			if sentAt <= deadlineNs {
				delete(pendingAcks, orderID)
				expired++
			}
		}
		pendingMu.Unlock()

		for i := 0; i < expired; i++ {
			latencyCh <- latResult{failed: true}
		}
	}

	failAllPending := func() {
		pendingMu.Lock()
		n := len(pendingAcks)
		pendingAcks = make(map[int64]int64)
		pendingMu.Unlock()

		for i := 0; i < n; i++ {
			latencyCh <- latResult{failed: true}
		}
	}

	var wg sync.WaitGroup
	wg.Add(3)

	// --- Sender goroutine ---
	go func() {
		defer wg.Done()
		defer senderDone.Store(true)

		for i := 0; i < b.config.OrdersToSend; i++ {
			if err := strategy.Wait(ctx); err != nil {
				remaining := b.config.OrdersToSend - i
				for j := 0; j < remaining; j++ {
					latencyCh <- latResult{failed: true}
				}
				return
			}

			msg := b.NextOrder()
			
			// Map to protocol.Order
			protoOrder := &protocol.Order{
				BotId:    uint64(b.config.NumericID),
				OrderId:  uint64(msg.OrderID),
				Type:     mapOrderType(msg.Type),
				Side:     mapSide(msg.Side),
				Price:    msg.Price,
				Quantity: uint64(msg.Quantity),
			}

			payload, err := proto.Marshal(protoOrder)
			if err != nil {
				latencyCh <- latResult{failed: true}
				continue
			}

			seq := msg.OrderID & 0xFFFFFFFF
			b.RecordSendTime(seq)

			if msg.Type != Cancel {
				pendingMu.Lock()
				pendingAcks[msg.OrderID] = b.SendTimes[seq]
				pendingMu.Unlock()
			}

			lengthPrefix := make([]byte, 4)
			binary.LittleEndian.PutUint32(lengthPrefix, uint32(len(payload)))

			_, err = conn.Write(lengthPrefix)
			if err == nil {
				_, err = conn.Write(payload)
			}

			if err != nil {
				removePending(msg.OrderID)
				remaining := b.config.OrdersToSend - i
				for j := 0; j < remaining; j++ {
					latencyCh <- latResult{failed: true}
				}
				return
			}

			if producer != nil {
				producer.PublishOrderAsync(telemetry.OrderEvent{
					Type:      telemetry.EventOrderSent,
					JobID:     jobID,
					WorkerID:  workerID,
					BotID:     b.config.StringID,
					OrderID:   msg.OrderID,
					OrderType: string(msg.Type),
					Side:      string(msg.Side),
					Price:     msg.Price,
					Quantity:  msg.Quantity,
					SentAtNs:  b.SendTimes[seq],
				})
			}
		}
	}()

	// --- Receiver goroutine ---
	go func() {
		defer wg.Done()

		for {
			var length uint32
			err := binary.Read(conn, binary.LittleEndian, &length)
			receivedAt := time.Now().UnixNano()
			if err != nil {
				failAllPending()
				return
			}

			payload := make([]byte, length)
			_, err = io.ReadFull(conn, payload)
			if err != nil {
				failAllPending()
				return
			}
			lastReportNs.Store(receivedAt)

			var report protocol.ExecutionReport
			if err := proto.Unmarshal(payload, &report); err != nil {
				continue
			}

			status := strings.ToLower(report.Status.String())
			orderID := int64(report.OrderId)

			if isFillStatus(status) {
				sendTime, ok := removePending(orderID)
				if ok {
					latency := receivedAt - sendTime
					latencyCh <- latResult{latencyNs: latency}

					if producer != nil {
						producer.PublishAckAsync(telemetry.AckEvent{
							Type:            telemetry.EventOrderAck,
							JobID:           jobID,
							WorkerID:        workerID,
							BotID:           b.config.StringID,
							OrderID:         orderID,
							Status:          "accepted_and_filled",
							LatencyNs:       latency,
							ReceivedNs:      receivedAt,
							EngineSeqID:     int64(report.EngineSeqId),
							EngineLatencyNs: int64(report.ProcessingNs),
						})
					}
				}

				if producer != nil {
					producer.PublishFillAsync(telemetry.FillEvent{
						Type:        telemetry.EventFill,
						JobID:       jobID,
						WorkerID:    workerID,
						OrderID:     orderID,
						FilledQty:   int64(report.FilledQty),
						FilledPrice: report.FilledPrice,
						MatchedWith: int64(report.MatchedWith),
						EngineSeqID: int64(report.EngineSeqId),
					})
				}
				continue
			}

			if !isAckStatus(status) {
				continue
			}

			sendTime, ok := removePending(orderID)
			if !ok {
				continue
			}

			latency := receivedAt - sendTime
			latencyCh <- latResult{latencyNs: latency}

			if producer != nil {
				producer.PublishAckAsync(telemetry.AckEvent{
					Type:        telemetry.EventOrderAck,
					JobID:       jobID,
					WorkerID:    workerID,
					BotID:       b.config.StringID,
					OrderID:     orderID,
					Status:      status,
					LatencyNs:   latency,
					EngineSeqID: int64(report.EngineSeqId),
					EngineLatencyNs: int64(report.ProcessingNs),
				})
			}
		}
	}()

	// --- Control goroutine ---
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(readPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				failAllPending()
				return
			case <-ticker.C:
				failExpiredPending(time.Now())
				lastReport := time.Unix(0, lastReportNs.Load())
				if senderDone.Load() && pendingLen() == 0 && time.Since(lastReport) >= fillDrainGrace {
					return
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(latencyCh)
	}()

	// --- Collector ---
	for lr := range latencyCh {
		if lr.failed {
			result.OrdersFailed++
		} else {
			_ = result.Histogram.RecordValue(lr.latencyNs)
			result.OrdersSent++
			totalSent.Add(1)
		}
	}

	return result
}

func isAckStatus(status string) bool {
	switch status {
	case "accepted", "rejected", "cancelled":
		return true
	default:
		return false
	}
}

func isFillStatus(status string) bool {
	switch status {
	case "filled", "partial":
		return true
	default:
		return false
	}
}

func isTerminalError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

func isContextError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func debugLog(format string, args ...any) {
	if debugLogs {
		log.Printf(format, args...)
	}
}
