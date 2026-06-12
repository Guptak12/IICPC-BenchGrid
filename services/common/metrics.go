package common

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// ─────────────────────────────────────────────────────────────────────────────
// Submission Pipeline Metrics
// ─────────────────────────────────────────────────────────────────────────────

// SubmissionsTotal counts submissions entering each pipeline stage.
// Labels: status = queued | building | running | completed | failed
var SubmissionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "iicpc",
	Name:      "submissions_total",
	Help:      "Total submissions by pipeline stage.",
}, []string{"status"})

// CompilationDuration tracks Docker build time per submission.
var CompilationDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "iicpc",
	Name:      "compilation_duration_seconds",
	Help:      "Time to compile a submission (Docker build).",
	Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
})

// PretestDuration tracks total pretest/systest execution time (all K runs).
var PretestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "iicpc",
	Name:      "pretest_duration_seconds",
	Help:      "Total pretest or systest evaluation time (all K runs).",
	Buckets:   []float64{5, 10, 20, 30, 60, 120, 180},
}, []string{"test_type"})

// PretestRunDuration tracks a single K-run execution time.
var PretestRunDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "iicpc",
	Name:      "pretest_run_duration_seconds",
	Help:      "Duration of a single pretest K-run (sandbox + fleet).",
	Buckets:   []float64{2, 5, 10, 15, 20, 30, 60},
})

// ─────────────────────────────────────────────────────────────────────────────
// Queue Depth Gauges
// ─────────────────────────────────────────────────────────────────────────────

// QueueDepth reports current Redis Stream length.
// Labels: queue = compilation_queue | pretest_queue | systest_queue
var QueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "queue_depth",
	Help:      "Current number of messages in each Redis Stream queue.",
}, []string{"queue"})

// ─────────────────────────────────────────────────────────────────────────────
// Worker Health
// ─────────────────────────────────────────────────────────────────────────────

// WorkerActiveJobs tracks how many jobs a worker is currently processing.
// Labels: worker_type = compiler | pretest, worker_id = unique instance name
var WorkerActiveJobs = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "worker_active_jobs",
	Help:      "Number of jobs currently being processed by a worker.",
}, []string{"worker_type", "worker_id"})

// ─────────────────────────────────────────────────────────────────────────────
// Database Connection Pool
// ─────────────────────────────────────────────────────────────────────────────

// DBPoolActive reports active DB connections.
var DBPoolActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "db_pool_active_connections",
	Help:      "Current number of active PostgreSQL connections.",
}, []string{"service"})

// DBPoolIdle reports idle DB connections.
var DBPoolIdle = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "db_pool_idle_connections",
	Help:      "Current number of idle PostgreSQL connections.",
}, []string{"service"})

// ─────────────────────────────────────────────────────────────────────────────
// HTTP Traffic (Gateway only)
// ─────────────────────────────────────────────────────────────────────────────

// HTTPRequestsTotal counts HTTP requests to the gateway.
var HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "iicpc",
	Name:      "http_requests_total",
	Help:      "Total HTTP requests handled by the gateway.",
}, []string{"method", "path", "status_code"})

// HTTPRequestDuration tracks HTTP request latency.
var HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "iicpc",
	Name:      "http_request_duration_seconds",
	Help:      "HTTP request duration in seconds.",
	Buckets:   prometheus.DefBuckets,
}, []string{"method", "path"})

// ─────────────────────────────────────────────────────────────────────────────
// Bot Fleet Telemetry (embedded in pretest)
// ─────────────────────────────────────────────────────────────────────────────

// FleetOrdersSentTotal counts bot fleet orders sent across all evaluations.
var FleetOrdersSentTotal = promauto.NewCounter(prometheus.CounterOpts{
	Namespace: "iicpc",
	Name:      "fleet_orders_sent_total",
	Help:      "Total orders sent by the embedded bot fleet across all evaluations.",
})

// FleetTPS reports the most recent bot fleet TPS.
var FleetTPS = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "fleet_tps",
	Help:      "Most recent TPS achieved by the bot fleet.",
})

// FleetCorrectness reports the most recent correctness score.
var FleetCorrectness = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "fleet_correctness",
	Help:      "Most recent correctness score from the bot fleet (0-100).",
})

// FleetP99Us reports the most recent P99 latency in microseconds.
var FleetP99Us = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "iicpc",
	Name:      "fleet_p99_us",
	Help:      "Most recent P99 latency from the bot fleet in microseconds.",
})

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// ServeMetrics starts a Prometheus HTTP metrics server on the given port.
// This runs on a separate port to avoid interfering with the main application server.
// Call this as a goroutine: go common.ServeMetrics(":9090")
func ServeMetrics(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	log.Printf("[prometheus] Metrics server listening on %s/metrics\n", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[prometheus] Metrics server fatal error: %v\n", err)
	}
}

// StartQueueDepthCollector polls Redis XLEN every interval and updates QueueDepth gauges.
func StartQueueDepthCollector(ctx context.Context, rdb *redis.Client, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, q := range []string{CompilationQueue, PretestQueue, SystestQueue} {
					length := rdb.XLen(ctx, q).Val()
					QueueDepth.WithLabelValues(q).Set(float64(length))
				}
			}
		}
	}()
}

// StartDBPoolCollector periodically exports DB connection pool stats to Prometheus.
func StartDBPoolCollector(ctx context.Context, db *sql.DB, serviceName string, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stats := db.Stats()
				DBPoolActive.WithLabelValues(serviceName).Set(float64(stats.InUse))
				DBPoolIdle.WithLabelValues(serviceName).Set(float64(stats.Idle))
			}
		}
	}()
}

// ConfigureDBPool sets production-safe connection pool limits on a *sql.DB.
func ConfigureDBPool(db *sql.DB) {
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
}
