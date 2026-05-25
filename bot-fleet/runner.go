package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	gojson "github.com/goccy/go-json"
	"golang.org/x/time/rate"
	
)

const (
	// maxConcurrentBots caps simultaneous WebSocket connections
	// Prevents exhausting file descriptors on a single machine
	maxConcurrentBots = 500
	dialTimeout	  = 5 * time.Second
	debugLogs  = true
)

// runFleet spawns all bots concurrently with a semaphore bound.
// Returns results only after every bot has finished or context is cancelled.
func runFleet(ctx context.Context, bots []*Bot, cfg FleetConfig) []BotResult {
	results := make([]BotResult, len(bots))

	// Semaphore: bufferred channel limits concurrent bots
	sem := make(chan struct{}, maxConcurrentBots)

	var wg sync.WaitGroup

	// Progress counter — incremented atomically by each bot per order sent
	var totalSent atomic.Int64

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
		wg.Add(1)

		// Acquire semaphore slot before launching goroutine
		// Blocks here if maxConcurrentBots goroutines are already running
		sem <- struct{}{}

		go func(idx int, b *Bot) {
			defer wg.Done()
			defer func() { <-sem }() // release slot when done

			results[idx] = runBot(ctx, b, cfg.Endpoint, &totalSent)
		}(i, bot)
	}

	wg.Wait()
	close(progressDone)

	return results
}

// runBot is the single-bot execution loop with the real WebSocket send/receive loop.
func runBot(ctx context.Context, b *Bot, endpoint string, totalSent *atomic.Int64) BotResult {
	result := BotResult{
		BotID:    b.config.StringID,
		Strategy: b.config.Strategy,
		Latencies: make([]int64, 0, b.config.OrdersToSend),
	}

	// -----------------------------------------------------------------
	// Phase 1 — Pre-compute all orders before touching the network
	// -----------------------------------------------------------------
	orders := make([][]byte, b.config.OrdersToSend)
	for i := 0; i < b.config.OrdersToSend; i++ {
		msg := b.NextOrder()
		payload, err := b.MarshalOrder(msg)
		if err != nil {
			result.OrdersFailed++
			continue
		}
		orders[i] = payload
	}

	// -----------------------------------------------------------------
	// Phase 2 — Dial with a fail-fast timeout
	// Fix 3.1: 5s sub-context so a dead server fails in 5s not 10min
	// -----------------------------------------------------------------
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
		Subprotocols: []string{"trading"},
	})
	dialCancel() // always release immediately after dial attempt

	if err != nil {
		// Fix 3.2: single log line per bot failure, not per-order
		log.Printf("[%s] dial failed: %v\n", b.config.StringID, err)
		result.OrdersFailed += b.config.OrdersToSend
		return result
	}
	defer conn.Close(websocket.StatusNormalClosure, "bot done")


	// -----------------------------------------------------------------
	// Phase 3 — Blast phase: send + receive
	//
	// NOTE: This is currently synchronous ping-pong (send → wait for ack).
	// Known limitation documented in analysis point 1 — at high TPS the
	// conn.Read() blocks and skews the rate limiter.
	// Step 3 will decouple into parallel sender + receiver goroutines.
	// -----------------------------------------------------------------
	limiter := rate.NewLimiter(rate.Limit(b.config.RatePerSec), 1)

	for i, payload := range orders {
		if payload == nil {
			continue
		}

		// Fix 2.2: account for unexecuted orders on context cancel
		if err := limiter.Wait(ctx); err != nil {
			result.OrdersFailed += b.config.OrdersToSend - i
			return result
		}

		seq := int64(i + 1)
		b.RecordSendTime(seq)

		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			if isContextError(err) {
				result.OrdersFailed += b.config.OrdersToSend - i
				return result
			}
			// Fix 2.1: terminal write error — stop immediately
			if isTerminalError(err) {
				result.OrdersFailed += b.config.OrdersToSend - i
				debugLog("[%s] terminal write error: %v — stopping bot\n", b.config.StringID, err)
				return result
			}
			result.OrdersFailed++
			continue
		}

		_, ackBytes, err := conn.Read(ctx)
		if err != nil {
			if isContextError(err) {
				result.OrdersFailed += b.config.OrdersToSend - i
				return result
			}
			// Fix 2.1: terminal read error — stop immediately
			if isTerminalError(err) {
				result.OrdersFailed += b.config.OrdersToSend - i
				debugLog("[%s] terminal read error: %v — stopping bot\n", b.config.StringID, err)
				return result
			}
			result.OrdersFailed++
			continue
		}

		var ack OrderAck
		if err := gojson.Unmarshal(ackBytes, &ack); err != nil {
			result.OrdersFailed++
			continue
		}

		latency := b.CalculateLatency(ack.OrderID)
		if latency < 0 {
			result.OrdersFailed++
			continue
		}

		result.Latencies = append(result.Latencies, latency)
		result.OrdersSent++
		totalSent.Add(1)
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
