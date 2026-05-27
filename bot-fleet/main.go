package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
pb "github.com/guptak12/bot-fleet/gen/fleet"// replace with actual import path
	"net"
	"os"
	"strings"
	"flag"
)

// ─────────────────────────────────────────────────────────────────────────────
// Job store
// ─────────────────────────────────────────────────────────────────────────────

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobAborted   JobStatus = "aborted"
)

// Job is immutable once written — fields are never mutated in place.
// Fix 2: we never write to job.Status or job.Report directly.
// Instead we build a new Job value and replace the pointer in the store.
// This means any reader holding the old pointer sees a consistent snapshot.
type Job struct {
	ID        string       `json:"job_id"`
	Status    JobStatus    `json:"status"`
	StartedAt time.Time    `json:"started_at"`
	EndedAt   *time.Time   `json:"ended_at,omitempty"`
	Report    *FleetReport `json:"report,omitempty"`
	Error     string       `json:"error,omitempty"`
	cancel    context.CancelFunc // not exported — never serialised
}

var (
	jobStore   = map[string]*Job{}
	jobStoreMu sync.RWMutex
)

// getJob returns the current Job pointer — caller gets a stable snapshot
func getJob(id string) (*Job, bool) {
	jobStoreMu.RLock()
	defer jobStoreMu.RUnlock()
	j, ok := jobStore[id]
	return j, ok
}

// replaceJob atomically replaces the job pointer in the store.
// Fix 2: never mutate fields on an existing *Job — always replace the pointer.
// Readers holding the old pointer continue to see a consistent snapshot.
func replaceJob(newJob *Job) {
	jobStoreMu.Lock()
	defer jobStoreMu.Unlock()
	jobStore[newJob.ID] = newJob
}

// ─────────────────────────────────────────────────────────────────────────────
// Config & Report types
// ─────────────────────────────────────────────────────────────────────────────

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

// FleetReport contains only aggregated scalar values — no raw latency slice.
// Fix 1: allLatencies []int64 removed entirely.
// Step 5 (HDR Histogram) will compute p50/p90/p99 per-bot and merge here.
type FleetReport struct {
	Status            string         `json:"status"`
	NumBots           int            `json:"num_bots"`
	TotalOrders       int            `json:"total_orders"`
	OrdersSent        int            `json:"orders_sent"`
	OrdersFailed      int            `json:"orders_failed"`
	DurationMs        int64          `json:"duration_ms"`
	TPS			   float64        `json:"tps"`
	StrategyBreakdown map[string]int `json:"strategy_breakdown"`
	// Step 5 Additions: Microsecond percentile representations
	P50Us             float64        `json:"p50_us"`
	P90Us             float64        `json:"p90_us"`
	P99Us             float64        `json:"p99_us"`
	MaxUs             float64        `json:"max_us"`
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP server
// ─────────────────────────────────────────────────────────────────────────────
var mode = flag.String("mode", "master", "master or worker")
var workerPort = flag.Int("port", 5001, "worker gRPC port")

func main() {
	 flag.Parse()

    switch *mode {
    case "worker":
        runWorkerMode(*workerPort)
    case "master":
        runMasterMode()
    default:
        log.Fatalf("unknown mode: %s", *mode)
    }
}
func runWorkerMode(port int) {
    lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
    if err != nil {
        log.Fatalf("listen failed: %v", err)
    }
    grpcServer := grpc.NewServer()
    pb.RegisterWorkerServiceServer(grpcServer, &workerServer{})
    log.Printf("Worker running on :%d\n", port)
    log.Fatal(grpcServer.Serve(lis))
}

func runMasterMode() {
    // Read worker addresses from env
    if w := os.Getenv("WORKERS"); w != "" {
        masterCfg.WorkerAddresses = strings.Split(w, ",")
    }
    // existing HTTP server setup
	mux := http.NewServeMux()
	mux.HandleFunc("/run",     handleRun)
	mux.HandleFunc("/status/", handleStatus)
	mux.HandleFunc("/cancel/", handleCancel)
	mux.HandleFunc("/health",  func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:         ":4000",
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Println("Bot fleet service running on :4000")
	log.Fatal(server.ListenAndServe())
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /run
// ─────────────────────────────────────────────────────────────────────────────

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

	if cfg.NumBots <= 0        { cfg.NumBots = 50 }
	if cfg.OrdersPerBot <= 0   { cfg.OrdersPerBot = 100 }
	if cfg.MidPrice <= 0       { cfg.MidPrice = 100.0 }
	if cfg.Spread <= 0         { cfg.Spread = 0.10 }
	if cfg.RatePerSec <= 0     { cfg.RatePerSec = 10.0 }
	if cfg.StrategyMix.MarketMaker+cfg.StrategyMix.MomentumTrader+cfg.StrategyMix.NoiseTrader == 0 {
		cfg.StrategyMix = StrategyMix{0.4, 0.3, 0.3}
	}
	if cfg.Endpoint == "" {
		http.Error(w, "endpoint is required", http.StatusBadRequest)
		return
	}

	// Fix 2: context.Background() — fleet outlives the HTTP request
	jobCtx, jobCancel := context.WithTimeout(context.Background(), 15*time.Minute)

	job := &Job{
		ID:        uuid.New().String(),
		Status:    JobPending,
		StartedAt: time.Now(),
		cancel:    jobCancel,
	}
	replaceJob(job)

	go func() {
		
		defer func() {
        if r := recover(); r != nil {
            now := time.Now()
            log.Printf("[job:%s] PANIC: %v\n", job.ID[:8], r)
            replaceJob(&Job{
                ID:        job.ID,
                Status:    JobAborted,
                StartedAt: job.StartedAt,
                EndedAt:   &now,
                Error:     fmt.Sprintf("internal panic: %v", r),
                cancel:    jobCancel,
            })
            jobCancel()
        }
    }()

		// Fix 2: build a new Job struct for each state transition
		// never mutate fields on the existing pointer
		replaceJob(&Job{
			ID:        job.ID,
			Status:    JobRunning,
			StartedAt: job.StartedAt,
			cancel:    jobCancel,
		})

		bots := buildBots(cfg)
		log.Printf("[job:%s] Starting: %d bots × %d orders → %s\n",
			job.ID[:8], len(bots), cfg.OrdersPerBot, cfg.Endpoint)

		start := time.Now()
		report, err := runDistributed(jobCtx, cfg, job.ID)
		elapsed := time.Since(start)

		// Step 5: Initialize global aggregator matrix (1ns to 1hr tracking bounds)
		if err != nil {
    now := time.Now()
    replaceJob(&Job{
        ID:        job.ID,
        Status:    JobAborted,
        StartedAt: job.StartedAt,
        EndedAt:   &now,
        Error:     err.Error(),
        cancel:    jobCancel,
    })
    jobCancel()
    return
}

// Fill in fields runDistributed doesn't set
report.DurationMs = elapsed.Milliseconds()
report.TPS = float64(report.OrdersSent) / (float64(elapsed.Milliseconds()) / 1000.0)
report.StrategyBreakdown = map[string]int{
    "market_maker":    countStrategy(bots, MarketMaker),
    "momentum_trader": countStrategy(bots, MomentumTrader),
    "noise_trader":    countStrategy(bots, NoiseTrader),
}

report.Status = "completed"

		now := time.Now()

		// Fix 2: replace with a new Job struct — old pointer untouched
		replaceJob(&Job{
			ID:        job.ID,
			Status:    JobCompleted,
			StartedAt: job.StartedAt,
			EndedAt:   &now,
			Report:    report,
			cancel:    jobCancel,
		})

		log.Printf("[job:%s] Done: %d/%d orders in %s\n",
			job.ID[:8], report.OrdersSent, cfg.NumBots*cfg.OrdersPerBot,
			elapsed.Round(time.Millisecond))

		jobCancel()
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"job_id":  job.ID,
		"status":  string(JobPending),
		"poll":    fmt.Sprintf("/status/%s", job.ID),
		"cancel":  fmt.Sprintf("/cancel/%s", job.ID),
		"message": "Fleet started. Poll /status/:job_id for results.",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /status/:jobID
// ─────────────────────────────────────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	jobID := r.URL.Path[len("/status/"):]
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	// Fix 2: getJob returns a pointer snapshot — safe to read without lock
	// because we never mutate fields on existing Job structs
	job, ok := getJob(jobID)
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /cancel/:jobID
// ─────────────────────────────────────────────────────────────────────────────

func handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}

	jobID := r.URL.Path[len("/cancel/"):]
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	job, ok := getJob(jobID)
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	if job.Status != JobRunning {
		http.Error(w, "job is not running", http.StatusConflict)
		return
	}

	job.cancel()

	now := time.Now()
	// Fix 2: new struct — don't mutate the existing job pointer
	replaceJob(&Job{
		ID:        job.ID,
		Status:    JobAborted,
		StartedAt: job.StartedAt,
		EndedAt:   &now,
		cancel:    job.cancel,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"job_id": jobID,
		"status": "aborted",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Bot building helpers
// ─────────────────────────────────────────────────────────────────────────────

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