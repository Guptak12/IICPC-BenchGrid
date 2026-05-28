package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/coder/websocket"
	gojson "github.com/goccy/go-json"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/guptak12/bot-fleet/gen/fleet"
	"github.com/guptak12/bot-fleet/telemetry"
)

const (
	// maxConcurrentBots caps simultaneous WebSocket connections
	// Prevents exhausting file descriptors on a single machine
	maxConcurrentBots = 500
	dialTimeout	  = 5 * time.Second
	debugLogs  = true
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



// runFleet spawns all bots concurrently with a semaphore bound.
// Change :- Using errgroup instead of WaitGroup to capture any unexpected panics in bot goroutines.
func runFleet(ctx context.Context, bots []*Bot, cfg FleetConfig, producer *telemetry.Producer, jobID string, workerID string) []BotResult {
	results := make([]BotResult, len(bots))

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
		idx,b := i, bot // capture loop variables for closure

		// Acquire semaphore slot before launching goroutine
		// Blocks here if maxConcurrentBots goroutines are already running
		sem <- struct{}{}

		g.Go(func() error {
			defer func() { <-sem }() // release slot when done

			// Build the strategy for this bot
			// MarketMakerStrategy, MomentumStrategy, or NoiseStrategy
			strategy := newStrategy(b)

			results[idx] = runBot(gctx, b, cfg.Endpoint,strategy, &totalSent, producer, jobID, workerID)
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
			Type:        telemetry.EventWorkerDone,
			JobID:       jobID,
			WorkerID:    workerID,
			TotalSent:   int64(totalSent.Load()),
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

			// 1. Call the method, which now returns a STREAM, not a single result
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
			})

			if err != nil {
				resultCh <- shardResponse{err: fmt.Errorf("worker %s shard start failed: %v", workerAddr, err)}
				return
			}

			// 2. Loop and listen to the stream heartbeats
			var finalResult *pb.ShardResult
			for {
				res, err := stream.Recv()
				if err == io.EOF {
					// The worker closed the stream successfully
					break
				}
				if err != nil {
					resultCh <- shardResponse{err: fmt.Errorf("worker %s stream read error: %v", workerAddr, err)}
					return
				}

				// If you want to see live TPS from the workers, uncomment this:
				// log.Printf("[Master] Worker %s alive | TPS: %.2f", workerAddr, res.CurrentTps)

				// 3. Catch the final payload containing the heavy histogram bytes
				if res.IsFinal {
					finalResult = res
				}
			}

			if finalResult == nil {
				resultCh <- shardResponse{err: fmt.Errorf("worker %s closed stream without sending final result", workerAddr)}
				return
			}

			// Send the final result back to the Master aggregation channel
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

// runBot is the single-bot execution loop with the real WebSocket send/receive loop.
func runBot(ctx context.Context, b *Bot, endpoint string,strategy Strategy, totalSent *atomic.Int64,producer *telemetry.Producer, jobID string,workerID string) BotResult {
	result := BotResult{
		BotID:    b.config.StringID,
		Strategy: b.config.Strategy,
		//  Allocate dynamic range bounds (1ns min up to 1hr max) with 3 significant digits
		Histogram: hdrhistogram.New(1, 3600000000000, 3),
	}

	// -----------------------------------------------------------------
	// Phase 2 — Dial with a fail-fast timeout
	// -----------------------------------------------------------------
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
		Subprotocols: []string{"trading"},
	})
	dialCancel() // always release immediately after dial attempt

	if err != nil {
		log.Printf("[%s] dial failed: %v\n", b.config.StringID, err)
		result.OrdersFailed += b.config.OrdersToSend
		return result
	}
	defer conn.Close(websocket.StatusNormalClosure, "bot done")


	// -----------------------------------------------------------------
	// Phase 3 — Parallel sender + receiver
	//
	// sentSignal: sender → receiver, carries seq for latency lookup
	// Buffered to OrdersToSend so sender never blocks waiting for receiver
	//
	// latencyResult: receiver → collector
	// Buffered to OrdersToSend so receiver never blocks
	// -----------------------------------------------------------------
	sentSignal := make(chan int64, b.config.OrdersToSend) // seq numbers of sent orders, for latency correlation
	type latResult struct {
		latencyNs int64
		failed    bool
	}
	latencyCh := make(chan latResult, b.config.OrdersToSend)

	var sentCount atomic.Int64  // orders successfully written to socket
	var ackedCount atomic.Int64 // orders acked by server

	var wg sync.WaitGroup
	wg.Add(2)

	// --- Sender goroutine ---
	go func() {
		defer wg.Done()
		defer close(sentSignal)

		for i := 0; i < b.config.OrdersToSend; i++ {
			if err := strategy.Wait(ctx); err != nil {
				remaining := b.config.OrdersToSend - i
				for j := 0; j < remaining; j++ {
					latencyCh <- latResult{failed: true}
				}
				return
			}

			// Pain Point 2 Fix: Generate and Marshal JSON right here on the hot path
			msg := b.NextOrder()
			payload, err := b.MarshalOrder(msg)
			if err != nil {
				latencyCh <- latResult{failed: true}
				continue
			}
		seq := msg.OrderID & 0xFFFFFFFF
		b.RecordSendTime(seq)

		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				remaining := b.config.OrdersToSend - i
				for j := 0; j < remaining; j++ {
					latencyCh <- latResult{failed: true}
				}
				return
			}
		// Order successfully written to socket
			sentCount.Add(1)

			// Point A: non-blocking publish — bot never waits for Kafka
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

			// Select over channel send to respect context closure if consumer bottlenecks
			select {
			case <-ctx.Done():
				return
			case sentSignal <- seq:
			}
	}
}()

// --- Receiver goroutine ---
	go func() {
		defer wg.Done()

		for seq := range sentSignal {
			_ = seq // seq available for future per-order timeout logic

		_, ackBytes, err := conn.Read(ctx)
		if err != nil {
				latencyCh <- latResult{failed: true}
				ackedCount.Add(1)
				if isTerminalError(err) || isContextError(err) {
					return
				}
				continue
			}	

		var ack OrderAck
		if err := gojson.Unmarshal(ackBytes, &ack); err != nil {
			latencyCh <- latResult{failed: true}
			ackedCount.Add(1)
			continue
		}

		latency := b.CalculateLatency(ack.OrderID)
		if latency < 0 {
			latencyCh <- latResult{failed: true}
			ackedCount.Add(1)
			continue
		}

		latencyCh <- latResult{latencyNs: latency}
		ackedCount.Add(1)

		if producer != nil {
			if ack.Status == "filled" {
				producer.PublishFillAsync(telemetry.FillEvent{
					Type:        telemetry.EventFill,
					JobID:       jobID,
					WorkerID:    workerID,
					OrderID:     ack.OrderID,
					FilledQty:   ack.FilledQty,
					FilledPrice: ack.FilledPrice,
					EngineSeqID: ack.EngineSeqID,
				})
			} else {
				producer.PublishAckAsync(telemetry.AckEvent{
					Type:       telemetry.EventOrderAck,
					JobID:      jobID,
					WorkerID:   workerID,
					BotID:      b.config.StringID,
					OrderID:    ack.OrderID,
					Status:     ack.Status,
					LatencyNs:  latency,
					EngineSeqID: ack.EngineSeqID,
				})
			}
		}
	}
}()

go func() {
		wg.Wait()
		unacked := sentCount.Load() - ackedCount.Load()
		for i := int64(0); i < unacked; i++ {
			latencyCh <- latResult{failed: true}
		}

		close(latencyCh) // now safe to close — all results are in
	}()
	
	// --- Collector — runs in the main bot goroutine ---
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

// isTerminalError returns true for errors that mean the connection is dead.
// Fix 2.1: on terminal errors we break out of the loop immediately instead
// of continuing to iterate and hitting timeouts on every remaining order.
func isTerminalError(err error) bool {
	if err == nil {
		return false
	}
	// EOF = server closed the connection
	if errors.Is(err, io.EOF) {
		return true
	}
	// Broken pipe / connection reset = network-level termination
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	// WebSocket close frame received
	var wsErr websocket.CloseError
	if errors.As(err, &wsErr) {
		return wsErr.Code != websocket.StatusNormalClosure
	}
	return false
}

// isContextError returns true if the error is a clean context shutdown.
func isContextError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var wsErr websocket.CloseError
	if errors.As(err, &wsErr) {
		return wsErr.Code == websocket.StatusNormalClosure ||
			wsErr.Code == websocket.StatusGoingAway
	}
	return false
}

// debugLog writes only when debugLogs is true.
// Fix 3.2: suppresses per-bot hot path logs at scale
// Flip debugLogs = true during development only
func debugLog(format string, args ...any) {
	if debugLogs {
		log.Printf(format, args...)
	}
}
