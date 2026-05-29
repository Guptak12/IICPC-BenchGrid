package main

import (
	"fmt"
	"log"
	"time"
	

	pb "github.com/guptak12/bot-fleet/gen/fleet"
	"github.com/guptak12/bot-fleet/telemetry"

)

// workerServer implements the gRPC WorkerService interface
type workerServer struct {
	pb.UnimplementedWorkerServiceServer
}

// Notice the new signature: no context.Context, and it returns an error instead of a Result pointer.
func (w *workerServer) RunShard(req *pb.ShardRequest, stream pb.WorkerService_RunShardServer) error {
	log.Printf("[worker] Shard received: %d bots × %d orders → %s\n",
		req.NumBots, req.OrdersPerBot, req.Endpoint)

	// Build bots — offset NumericID by bot_id_offset for unique bitwise OrderIDs
	bots := buildShardBots(req)

	cfg := FleetConfig{
		Endpoint:     req.Endpoint,
		NumBots:      int(req.NumBots),
		OrdersPerBot: int(req.OrdersPerBot),
		RatePerSec:   req.RatePerSec,
		StrategyMix: StrategyMix{
			MarketMaker:    req.MarketMakerPct,
			MomentumTrader: req.MomentumPct,
			NoiseTrader:    req.NoisePct,
		},
	}

	// --- HEARTBEAT GOROUTINE ---
	// This pulses every 2 seconds to keep the Docker/OS firewall from killing 
	// the idle connection during long load tests!
	done := make(chan bool)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				stream.Send(&pb.ShardResult{
					JobId:   req.JobId,
					IsFinal: false,
				})
			case <-done:
				return
			}
		}
	}()

	// 1. Initialize the Kafka Producer for this specific worker
	producer, err := telemetry.NewProducer(kafkaBrokers(), req.JobId)
	if err != nil {
		log.Printf("[worker] Failed to init Kafka producer: %v", err)
	}

	// 2. Generate a unique Worker ID (e.g., using the port it's running on)
	workerID := fmt.Sprintf("worker-%d", *workerPort)

	// Run the load test (this blocks until all bots finish)
	results := runFleet(stream.Context(), bots, cfg,producer, req.JobId, workerID)
	
	// Stop the heartbeat ping
	done <- true 

	// Aggregate shard results
	var sent, failed int
	globalHist := newHistogram()
	for _, r := range results {
		sent += r.OrdersSent
		failed += r.OrdersFailed
		globalHist.Merge(r.Histogram)
	}

	// Serialise histogram to bytes for transport
	histBytes, err := serialiseHistogram(globalHist)
	if err != nil {
		return fmt.Errorf("histogram serialise failed: %v", err)
	}

	log.Printf("[worker] Shard done: %d/%d sent\n", sent, int(req.NumBots)*int(req.OrdersPerBot))

	// Send the FINAL payload containing the heavy histogram bytes
	return stream.Send(&pb.ShardResult{
		JobId:        req.JobId,
		OrdersSent:   int64(sent),
		OrdersFailed: int64(failed),
		Histogram:    histBytes,
		IsFinal:      true, // Tells the Master we are officially done
	})
}

// buildShardBots creates bots with IDs offset by bot_id_offset
// so Worker 2's bot-1 has NumericID=1001, not 1 — prevents OrderID collisions
func buildShardBots(req *pb.ShardRequest) []*Bot {
	bots := make([]*Bot, req.NumBots)
	numMakers := int(float64(req.NumBots) * req.MarketMakerPct)
	numMomentum := int(float64(req.NumBots) * req.MomentumPct)

	 baseSeed := req.Seed
    if baseSeed == 0 {
        baseSeed = time.Now().UnixNano()
    }

	for i := 0; i < int(req.NumBots); i++ {
		var strategy StrategyType
		switch {
		case i < numMakers:
			strategy = MarketMaker
		case i < numMakers+numMomentum:
			strategy = MomentumTrader
		default:
			strategy = NoiseTrader
		}

		// Key: NumericID = offset + i+1 — globally unique across all workers
		botCfg := NewBotConfig(
			req.BotIdOffset+int64(i+1),
			fmt.Sprintf("worker%d-bot-%d", req.BotIdOffset/int64(req.NumBots)+1, i+1),
			strategy,
			req.MidPrice,
			req.Spread,
			int(req.OrdersPerBot),
			req.RatePerSec,
			// Key: seed = base + offset + i so Worker 2's bots don't
            // duplicate Worker 1's seeds but stay fully deterministic
            baseSeed+req.BotIdOffset+int64(i),
		)
		bots[i] = NewBot(botCfg)
	}
	return bots
}