package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"fmt"
	"sync/atomic"
	"sync"
	"time"

	
	"github.com/coder/websocket"
	gojson "github.com/goccy/go-json"
	"golang.org/x/sync/errgroup"
	"github.com/HdrHistogram/hdrhistogram-go"
	
)

const (
	// maxConcurrentBots caps simultaneous WebSocket connections
	// Prevents exhausting file descriptors on a single machine
	maxConcurrentBots = 500
	dialTimeout	  = 5 * time.Second
	debugLogs  = true
	channelBufferSize = 1024
)


// runFleet spawns all bots concurrently with a semaphore bound.
// Change :- Using errgroup instead of WaitGroup to capture any unexpected panics in bot goroutines.
func runFleet(ctx context.Context, bots []*Bot, cfg FleetConfig) []BotResult {
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

			results[idx] = runBot(gctx, b, cfg.Endpoint,strategy, &totalSent)
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

	return results
}

// runBot is the single-bot execution loop with the real WebSocket send/receive loop.
func runBot(ctx context.Context, b *Bot, endpoint string,strategy Strategy, totalSent *atomic.Int64) BotResult {
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
