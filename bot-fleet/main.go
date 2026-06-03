package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/guptak12/bot-fleet/gen/fleet" // replace with actual import path
	"github.com/guptak12/bot-fleet/telemetry"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	_ "github.com/lib/pq"
)

// LeaderboardEntry is what the frontend receives
type LeaderboardEntry struct {
	Rank             int       `json:"rank"`
	ContestantID     string    `json:"contestant_id"`
	CompositeScore   float64   `json:"composite_score"`
	CorrectnessScore float64   `json:"correctness_score"`
	PerfScore        float64   `json:"perf_score"`
	TPS              float64   `json:"tps"`
	P50Us            float64   `json:"p50_us"`
	P99Us            float64   `json:"p99_us"`
	JobID            string    `json:"job_id"`
	SubmittedAt      time.Time `json:"submitted_at"`
	KafkaUnavailable bool      `json:"kafka_unavailable"`
}

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
	ContestantID string    `json:"contestant_id"`
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

var rdb *redis.Client

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
	jobStore[newJob.ID] = newJob
	jobStoreMu.Unlock()

	 // Write-through to Redis — non-blocking, best-effort
    if rdb != nil {
        data, err := json.Marshal(newJob)
        if err == nil {
            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
            defer cancel()
            if err := rdb.HSet(ctx, "jobs", newJob.ID, data).Err(); err != nil {
                log.Printf("[redis] replaceJob write failed: %v\n", err)
            }
        }
    }

	// Broadcast leaderboard update to all SSE subscribers when a job finishes
    if newJob.Status == JobCompleted {
        go func() {
            if payload, err := buildLeaderboardJSON(); err == nil {
                hub.broadcast(payload)
            }
        }()
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Config & Report types
// ─────────────────────────────────────────────────────────────────────────────

type FleetConfig struct {
	JobID          string    `json:"job_id,omitempty"`
	ContestantID   string    `json:"contestant_id"`
	Endpoint       string    `json:"endpoint"`
	NumBots        int       `json:"num_bots"`
	OrdersPerBot   int       `json:"orders_per_bot"`
	MidPrice       float64   `json:"mid_price"`
	Spread         float64   `json:"spread"`
	RatePerSec     float64   `json:"rate_per_sec"`
	StrategyMix    StrategyMix `json:"strategy_mix"`
	Seed           int64     `json:"seed"` // seed for deterministic bot generation
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
	CorrectnessScore  float64        `json:"correctness_score"`
	KafkaUnavailable  bool           `json:"kafka_unavailable"`
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

	initRedis()
	rehydrateFromRedis()


    // existing HTTP server setup
	mux := http.NewServeMux()
	mux.HandleFunc("/run",     handleRun)
	mux.HandleFunc("/status/", handleStatus)
	mux.HandleFunc("/cancel/", handleCancel)
	mux.HandleFunc("/leaderboard",        handleLeaderboard)
	mux.HandleFunc("/leaderboard/stream", handleLeaderboardStream)
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
	log.Printf("==== MASTER RECEIVED ID: '%s' ====", cfg.ContestantID)

	jobID := cfg.JobID
	if jobID == "" {
		jobID = uuid.New().String()
	}

	// Fix 2: context.Background() — fleet outlives the HTTP request
	jobCtx, jobCancel := context.WithTimeout(context.Background(), 15*time.Minute)

	job := &Job{
		ID:           jobID,
		ContestantID: cfg.ContestantID,
		Status:       JobPending,
		StartedAt:    time.Now(),
		cancel:       jobCancel,
	}
	replaceJob(job)

	go func() {
		
		defer func() {
        if r := recover(); r != nil {
            now := time.Now()
            log.Printf("[job:%s] PANIC: %v\n", job.ID[:8], r)
            updatedJob := *job // <--- Copy all original fields (including ContestantID!)
            updatedJob.Status = JobAborted
            updatedJob.EndedAt = &now
            updatedJob.Error = fmt.Sprintf("internal panic: %v", r)
            replaceJob(&updatedJob)
            jobCancel()
        }
    }()

	// 2. The Running Update
    runningJob := *job
    runningJob.Status = JobRunning
    replaceJob(&runningJob)
		// Fix 2: build a new Job struct for each state transition
		// never mutate fields on the existing pointer
		// replaceJob(&Job{
		// 	ID:        job.ID,
		// 	Status:    JobRunning,
		// 	StartedAt: job.StartedAt,
		// 	cancel:    jobCancel,
		// })

		bots := buildBots(cfg)
		log.Printf("[job:%s] Starting: %d bots × %d orders → %s\n",
			job.ID[:8], len(bots), cfg.OrdersPerBot, cfg.Endpoint)

		brokers := kafkaBrokers() // inside iicpc-net
		consumer, err := telemetry.NewConsumer(brokers, job.ID, len(masterCfg.WorkerAddresses))
		if err != nil {
			log.Printf("[job:%s] Kafka consumer init failed: %v — running without telemetry\n",
				job.ID[:8], err)
			consumer = nil
		}

		// Run Consumer concurrently in background
		var telResult *telemetry.TelemetryResult
		var telErr error
		var wg sync.WaitGroup
		if consumer != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				telResult, telErr = consumer.Consume(jobCtx)
				consumer.Close()
			}()
		}

		start := time.Now()
		report, err := runDistributed(jobCtx, cfg, job.ID)
		elapsed := time.Since(start)

        if err != nil {
        now := time.Now()
        abortedJob := *job
        abortedJob.Status = JobAborted
        abortedJob.EndedAt = &now
        abortedJob.Error = err.Error()
        replaceJob(&abortedJob)
        jobCancel()
        return
    }

		if consumer != nil {
			wg.Wait()
			if telErr == nil && telResult != nil {
				// Override histogram with streaming data (more accurate)
				report.P50Us = float64(telResult.Histogram.ValueAtQuantile(50)) / 1000.0
				report.P90Us = float64(telResult.Histogram.ValueAtQuantile(90)) / 1000.0
				report.P99Us = float64(telResult.Histogram.ValueAtQuantile(99)) / 1000.0
				report.MaxUs = float64(telResult.Histogram.Max()) / 1000.0
				report.CorrectnessScore = telResult.Correctness

				log.Printf("[job:%s] Kafka: %d orders, %d fills processed, Correctness: %.2f%%\n",
					job.ID[:8], telResult.OrdersProcessed, telResult.FillsProcessed, telResult.Correctness)
			}
		}
		report.KafkaUnavailable = (consumer == nil)


			// Fill in fields runDistributed doesn't set
			report.DurationMs = elapsed.Milliseconds()
			report.TPS = float64(report.OrdersSent) / (float64(elapsed.Milliseconds()) / 1000.0)
			report.StrategyBreakdown = map[string]int{
				"market_maker":    countStrategy(bots, MarketMaker),
				"momentum_trader": countStrategy(bots, MomentumTrader),
				"noise_trader":    countStrategy(bots, NoiseTrader),
			}

			report.Status = "completed"

		// 2. The Completed Update (NO err != nil wrapper)
        now := time.Now()
        completedJob := *job
        completedJob.Status = JobCompleted
        completedJob.EndedAt = &now
        completedJob.Report = report
        replaceJob(&completedJob)

        // Persist official system test results to PostgreSQL database
        dbAddr := os.Getenv("DB_ADDR")
        if dbAddr == "" {
            dbAddr = "postgres://postgres:postgres@postgres:5432/postgres?sslmode=disable"
        }
        db, err := sql.Open("postgres", dbAddr)
        if err == nil {
            defer db.Close()
            
            // Calculate latencyScore based on P99Us
            latencyScore := 0.0
            if report.P99Us <= 500.0 {
                latencyScore = 100.0
            } else if report.P99Us >= 5000.0 {
                latencyScore = 0.0
            } else {
                latencyScore = 100.0 * (1.0 - (report.P99Us-500.0)/4500.0)
            }

            failRate := 0.0
            if report.OrdersSent > 0 {
                failRate = float64(report.OrdersFailed) / float64(report.OrdersSent)
            }
            throughputScore := (1.0 - failRate) * 100.0

            compositeScore := (throughputScore * 0.3) + (latencyScore * 0.3) + (report.CorrectnessScore * 0.4)
            compositeScore = math.Round(compositeScore*100) / 100

            // Deduce verdict based on correctness/latency/failures
            verdict := "Accepted"
            if report.CorrectnessScore < 50.0 {
                verdict = "Wrong Answer"
            } else if report.P99Us > 50000.0 {
                verdict = "Time Limit Exceeded"
            } else if failRate > 0.3 {
                verdict = "Throughput Exceeded"
            }

            diag := map[string]interface{}{
                "correctness":          report.CorrectnessScore,
                "p50_us":              report.P50Us,
                "p90_us":              report.P90Us,
                "p99_us":              report.P99Us,
                "orders_sent":         report.OrdersSent,
                "orders_failed":       report.OrdersFailed,
                "tps":                 report.TPS,
                "throughput_score":    throughputScore,
                "latency_score":       latencyScore,
            }
            diagBytes, _ := json.Marshal(diag)

            _, err = db.Exec(
                `UPDATE submissions 
                 SET status = $1, verdict = $2, composite_score = $3, correctness_score = $4,
                     p50_us = $5, p90_us = $6, p99_us = $7, actual_tps = $8, diagnostics = $9, updated_at = NOW()
                 WHERE id = $10`,
                "completed", verdict, compositeScore, report.CorrectnessScore,
                int64(report.P50Us), int64(report.P90Us), int64(report.P99Us), report.TPS, diagBytes, job.ID,
            )
            if err != nil {
                log.Printf("[master] Failed to save official results for job %s to DB: %v\n", job.ID, err)
            } else {
                log.Printf("[master] Successfully persisted official results for job %s to DB ✓\n", job.ID)
            }
        } else {
            log.Printf("[master] Failed to connect to DB to save results: %v\n", err)
        }
        
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
		ContestantID: job.ContestantID,
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

	// If no seed provided (manual testing), fall back to time-based
    baseSeed := cfg.Seed
    if baseSeed == 0 {
        baseSeed = time.Now().UnixNano()
    }

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
			baseSeed+int64(i), // ← each bot gets a unique but deterministic seed
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

// kafkaBrokers reads KAFKA_BROKERS env var, falls back to redpanda:9092
// Set KAFKA_BROKERS=broker1:9092,broker2:9092 for multi-broker clusters
func kafkaBrokers() []string {
    if v := os.Getenv("KAFKA_BROKERS"); v != "" {
        return strings.Split(v, ",")
    }
    return []string{"redpanda:9092"}
}

func redisAddr() string {
    if v := os.Getenv("REDIS_ADDR"); v != "" {
        return v
    }
    return "localhost:6379"
}

func initRedis() {
    rdb = redis.NewClient(&redis.Options{
        Addr: redisAddr(),
    })
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    if err := rdb.Ping(ctx).Err(); err != nil {
        // Non-fatal — degrade gracefully, leaderboard won't persist
        log.Printf("[redis] connection failed: %v — running without persistence\n", err)
        rdb = nil
    } else {
        log.Printf("[redis] connected at %s ✓\n", redisAddr())
    }
}

func rehydrateFromRedis() {
    if rdb == nil {
        return
    }

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    entries, err := rdb.HGetAll(ctx, "jobs").Result()
    if err != nil {
        log.Printf("[redis] rehydrate failed: %v\n", err)
        return
    }

    now := time.Now()
    rehydrated, zombies := 0, 0

    jobStoreMu.Lock()
    defer jobStoreMu.Unlock()

    for _, jsonStr := range entries {
        // Unmarshal into a value, not a pointer — avoids loop variable capture bug
        var job Job
        if err := json.Unmarshal([]byte(jsonStr), &job); err != nil {
            continue
        }

        // Upgrade 1: sanitize zombies — the goroutine that owned this job is dead
        if job.Status == JobRunning || job.Status == JobPending {
            job.Status = JobAborted
            job.Error = "system restarted while job was active"
            job.EndedAt = &now
            zombies++

            // Write sanitized state back to Redis immediately
            if fixed, err := json.Marshal(job); err == nil {
                rdb.HSet(ctx, "jobs", job.ID, fixed)
            }
        }

        // Store pointer to the local copy (safe — loop variable was value-copied above)
        jobCopy := job
        jobStore[job.ID] = &jobCopy
        rehydrated++
    }

    log.Printf("[redis] rehydrated %d jobs (%d zombies sanitized)\n", rehydrated, zombies)
}

// calculatePerfScore: 100% at ≤500µs, 0% at ≥5ms, linear decay between
func calculatePerfScore(p99Us float64) float64 {
    const targetUs  = 500.0
    const penaltyUs = 5000.0
    if p99Us <= targetUs  { return 100.0 }
    if p99Us >= penaltyUs { return 0.0 }
    return 100.0 * (1.0 - ((p99Us - targetUs) / (penaltyUs - targetUs)))
}

// calculateCompositeScore executes the benchmark formula.
func calculateCompositeScore(tps float64, p99Us float64, correctness float64) float64 {
	if correctness <= 0 {
		return 0.0
	}

	baseScore := tps
	baselineP99 := 5000.0
	latencyMultiplier := baselineP99 / p99Us
	if latencyMultiplier > 10.0 {
		latencyMultiplier = 10.0
	}

	correctnessRatio := correctness / 100.0
	penaltyFactor := 20.0
	correctnessMultiplier := math.Pow(correctnessRatio, penaltyFactor)

	return baseScore * latencyMultiplier * correctnessMultiplier
}

// SSE hub — broadcaster for live leaderboard updates
type sseHub struct {
    mu          sync.Mutex
    subscribers []chan string
}

var hub = &sseHub{}

func (h *sseHub) subscribe() chan string {
    ch := make(chan string, 4)
    h.mu.Lock()
    h.subscribers = append(h.subscribers, ch)
    h.mu.Unlock()
    return ch
}

func (h *sseHub) unsubscribe(ch chan string) {
    h.mu.Lock()
    defer h.mu.Unlock()
    for i, s := range h.subscribers {
        if s == ch {
            h.subscribers = append(h.subscribers[:i], h.subscribers[i+1:]...)
            return
        }
    }
}

func (h *sseHub) broadcast(payload string) {
    h.mu.Lock()
    defer h.mu.Unlock()
    for _, ch := range h.subscribers {
        select {
        case ch <- payload:
        default: // slow subscriber — skip, don't block
        }
    }
}

// buildLeaderboardJSON is the shared logic used by both endpoints
func buildLeaderboardJSON() (string, error) {
    jobStoreMu.RLock()
    defer jobStoreMu.RUnlock()

    bestRuns := make(map[string]LeaderboardEntry)

    for _, job := range jobStore {
        if job.Status != JobCompleted || job.Report == nil || job.ContestantID == "" {
            continue
        }

				perf := calculatePerfScore(job.Report.P99Us)
				composite := calculateCompositeScore(job.Report.TPS, job.Report.P99Us, job.Report.CorrectnessScore)

        entry := LeaderboardEntry{
            ContestantID:     job.ContestantID,
            CompositeScore:   math.Round(composite*100) / 100,
            CorrectnessScore: math.Round(job.Report.CorrectnessScore*100) / 100,
            PerfScore:        math.Round(perf*100) / 100,
            TPS:              math.Round(job.Report.TPS*10) / 10,
            P50Us:            job.Report.P50Us,
            P99Us:            job.Report.P99Us,
            JobID:            job.ID,
            SubmittedAt:      job.StartedAt,
				KafkaUnavailable: job.Report.KafkaUnavailable,
        }

        existing, exists := bestRuns[job.ContestantID]
        if !exists || entry.CompositeScore > existing.CompositeScore {
            bestRuns[job.ContestantID] = entry
        }
    }

    leaderboard := make([]LeaderboardEntry, 0, len(bestRuns))
    for _, e := range bestRuns {
        leaderboard = append(leaderboard, e)
    }

    sort.Slice(leaderboard, func(i, j int) bool {
        a, b := leaderboard[i], leaderboard[j]
        if a.CompositeScore != b.CompositeScore {
            return a.CompositeScore > b.CompositeScore
        }
        if a.CorrectnessScore != b.CorrectnessScore {
            return a.CorrectnessScore > b.CorrectnessScore
        }
        return a.TPS > b.TPS
    })

    // Assign ranks after sorting
    for i := range leaderboard {
        leaderboard[i].Rank = i + 1
    }

    data, err := json.Marshal(leaderboard)
    return string(data), err
}

func handleLeaderboard(w http.ResponseWriter, r *http.Request) {
    payload, err := buildLeaderboardJSON()
    if err != nil {
        http.Error(w, "failed to build leaderboard", http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Access-Control-Allow-Origin", "*")
    fmt.Fprint(w, payload)
}

func handleLeaderboardStream(w http.ResponseWriter, r *http.Request) {
    // SSE requires these exact headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("Access-Control-Allow-Origin", "*")

    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming unsupported", http.StatusInternalServerError)
        return
    }

    // Send current state immediately on connect
    if payload, err := buildLeaderboardJSON(); err == nil {
        fmt.Fprintf(w, "data: %s\n\n", payload)
        flusher.Flush()
    }

    ch := hub.subscribe()
    defer hub.unsubscribe(ch)

    for {
        select {
        case payload := <-ch:
            fmt.Fprintf(w, "data: %s\n\n", payload)
            flusher.Flush()
        case <-r.Context().Done():
            return // client disconnected
        }
    }
}

