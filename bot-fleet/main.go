package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// FleetConfig is what the orchestrator POSTs to /run
type FleetConfig struct {
	Endpoint     string      `json:"endpoint"`
	NumBots      int         `json:"num_bots"`
	OrdersPerBot int         `json:"orders_per_bot"`
	MidPrice     float64     `json:"mid_price"`
	Spread       float64     `json:"spread"`
	RatePerSec   float64     `json:"rate_per_sec"`
	StrategyMix  StrategyMix `json:"strategy_mix"`
}

type StrategyMix struct {
	MarketMaker    float64 `json:"market_maker"`
	MomentumTrader float64 `json:"momentum_trader"`
	NoiseTrader    float64 `json:"noise_trader"`
}

// FleetReport is returned after the entire fleet finishes
type FleetReport struct {
	Status            string         `json:"status"`
	NumBots           int            `json:"num_bots"`
	TotalOrders       int            `json:"total_orders"`
	OrdersSent        int            `json:"orders_sent"`
	OrdersFailed      int            `json:"orders_failed"`
	DurationMs        int64          `json:"duration_ms"`
	StrategyBreakdown map[string]int `json:"strategy_breakdown"`
	// Step 5 will add latency histogram here
}

func main() {

	mux:= http.NewServeMux()
	mux.HandleFunc("/run", handleRun)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:    ":4000",
		Handler: mux,
		// No ReadTimeout — long-lived POST body
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  15 * time.Minute,
	}

	log.Println("Bot fleet service running on :4000")
	log.Fatal(server.ListenAndServe())

}

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var cfg FleetConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// --- Defaults ---
	if cfg.NumBots <= 0      { cfg.NumBots = 50 }
	if cfg.OrdersPerBot <= 0 { cfg.OrdersPerBot = 100 }
	if cfg.MidPrice <= 0     { cfg.MidPrice = 100.0 }
	if cfg.Spread <= 0       { cfg.Spread = 0.10 }
	if cfg.RatePerSec <= 0   { cfg.RatePerSec = 10.0 }
	if cfg.StrategyMix.MarketMaker+cfg.StrategyMix.MomentumTrader+cfg.StrategyMix.NoiseTrader == 0 {
		cfg.StrategyMix = StrategyMix{0.4, 0.3, 0.3}
	}
	if cfg.Endpoint == "" {
		http.Error(w, "endpoint is required", http.StatusBadRequest)
		return
	}

	// --- Build bots ---
	bots := buildBots(cfg)

	log.Printf("Starting fleet: %d bots × %d orders → %s\n",
		len(bots), cfg.OrdersPerBot, cfg.Endpoint)
	log.Printf("Strategy split: %d makers | %d momentum | %d noise\n",
		countStrategy(bots, MarketMaker),
		countStrategy(bots, MomentumTrader),
		countStrategy(bots, NoiseTrader),
	)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	start := time.Now()
	results := runFleet(ctx, bots, cfg)
	elapsed := time.Since(start)

	// --- Aggregate results ---
	var totalSent, totalFailed int
	for _, res := range results {
		totalSent += res.OrdersSent
		totalFailed += res.OrdersFailed
	}

	report := FleetReport{
		Status:      "completed",
		NumBots:     len(bots),
		TotalOrders: cfg.NumBots * cfg.OrdersPerBot,
		OrdersSent:  totalSent,
		OrdersFailed: totalFailed,
		DurationMs:  elapsed.Milliseconds(),
		StrategyBreakdown: map[string]int{
			"market_maker":    countStrategy(bots, MarketMaker),
			"momentum_trader": countStrategy(bots, MomentumTrader),
			"noise_trader":    countStrategy(bots, NoiseTrader),
		},
	}

	log.Printf("Fleet done: %d/%d orders sent in %s\n",
		totalSent, cfg.NumBots*cfg.OrdersPerBot, elapsed.Round(time.Millisecond))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// buildBots creates all bots with the correct strategy distribution.
func buildBots(cfg FleetConfig) []*Bot {
	bots := make([]*Bot, cfg.NumBots)

	numMakers   := int(float64(cfg.NumBots) * cfg.StrategyMix.MarketMaker)
	numMomentum := int(float64(cfg.NumBots) * cfg.StrategyMix.MomentumTrader)

	for i := 0; i < cfg.NumBots; i++ {
		var strategy StrategyType
		switch {
		case i < numMakers:
			strategy = MarketMaker
		case i < numMakers+numMomentum:
			strategy = MomentumTrader
		default:
			strategy = NoiseTrader
		}

		botCfg := NewBotConfig(
			int64(i+1),
			fmt.Sprintf("bot-%d", i+1),
			strategy,
			cfg.MidPrice,
			cfg.Spread,
			cfg.OrdersPerBot,
			cfg.RatePerSec,
		)
		bots[i] = NewBot(botCfg)
	}
	return bots
}

func countStrategy(bots []*Bot, s StrategyType) int {
	count := 0
	for _, b := range bots {
		if b.config.Strategy == s {
			count++
		}
	}
	return count
}