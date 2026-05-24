package main

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
	
)

const (
	// maxConcurrentBots caps simultaneous WebSocket connections
	// Prevents exhausting file descriptors on a single machine
	maxConcurrentBots = 500
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

			// runBot is defined in Step 2 (runner.go continued)
			results[idx] = runBot(ctx, b, cfg.Endpoint, &totalSent)
		}(i, bot)
	}

	wg.Wait()
	close(progressDone)

	return results
}

// runBot is the single-bot execution loop.
// Placeholder until Step 2 — returns empty result for now.
// Step 2 will replace this with the real WebSocket send/receive loop.
func runBot(ctx context.Context, b *Bot, endpoint string, totalSent *atomic.Int64) BotResult {
	result := BotResult{
		BotID:    b.config.StringID,
		Strategy: b.config.Strategy,
	}



	// TODO Step 2: dial WebSocket, send orders, record latency
	// For now just simulate work so the fleet wiring can be tested
	for i := 0; i < b.config.OrdersToSend; i++ {
		select {
		case <-ctx.Done():
			return result // context cancelled — stop cleanly
		default:
		}

		// Placeholder: generate order but don't send it yet
		// Step 2 replaces this with actual WS write + ack read
		_ = b.NextOrder()
		result.OrdersSent++
		totalSent.Add(1)

		// Simulate rate limiting
		time.Sleep(time.Duration(float64(time.Second) / b.config.RatePerSec))
	}

	return result
}