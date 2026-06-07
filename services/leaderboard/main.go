package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"iicpc-sandbox/services/common"
)

type LeaderboardEntry struct {
	Rank             int       `json:"rank"`
	ContestantID     string    `json:"contestant_id"`
	SubmissionID     string    `json:"submission_id"`
	Verdict          string    `json:"verdict"`
	CompositeScore   float64   `json:"composite_score"`
	CorrectnessScore float64   `json:"correctness_score"`
	P50Us            int64     `json:"p50_us"`
	P90Us            int64     `json:"p90_us"`
	P99Us            int64     `json:"p99_us"`
	ActualTps        float64   `json:"actual_tps"`
	LatencyScore     float64   `json:"latency_score"`
	ThroughputScore  float64   `json:"throughput_score"`
	EngineArchetype  string    `json:"engine_archetype"`
	UpdatedAt        time.Time `json:"updated_at"`
	
	// Deltas & Actionable Reasons
	DeltaScore       float64   `json:"delta_score"`
	DeltaP99         int64     `json:"delta_p99"`
	Reason           string    `json:"reason"`
}

type sseHub struct {
	mu          sync.Mutex
	subscribers []chan string
}

var (
	hub              = &sseHub{}
	globalTargetPath string
)

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
		default: // non-blocking for slow/inactive clients
		}
	}
}

func handleLeaderboardStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Send current file content immediately on connection
	data, err := os.ReadFile(globalTargetPath)
	if err == nil {
		fmt.Fprintf(w, "data: %s\n\n", string(data))
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
			return
		}
	}
}

func main() {
	// Connect to PostgreSQL
	var db *sql.DB
	var err error
	for i := 0; i < 5; i++ {
		db, err = common.GetDB()
		if err == nil {
			break
		}
		log.Printf("Leaderboard service: waiting for Postgres... error: %v\n", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("Postgres connection failed: %v", err)
	}
	defer db.Close()

	log.Println("Periodic Leaderboard Generator started successfully ✓")

	// Ensure frontend directory exists
	cwd, _ := os.Getwd()
	frontendDir := os.Getenv("FRONTEND_DIR")
	if frontendDir == "" {
		frontendDir = filepath.Join(cwd, "frontend")
	}
	if err := os.MkdirAll(frontendDir, 0755); err != nil {
		log.Fatalf("Failed to create frontend dir: %v", err)
	}

	targetPath := filepath.Join(frontendDir, "leaderboard.json")
	globalTargetPath = targetPath

	// Start internal SSE HTTP server
	go func() {
		http.HandleFunc("/leaderboard/stream", handleLeaderboardStream)
		log.Println("Leaderboard SSE server starting on :3001...")
		if err := http.ListenAndServe(":3001", nil); err != nil {
			log.Printf("Leaderboard SSE server exited: %v\n", err)
		}
	}()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		generateLeaderboard(db, targetPath)
	}
}

func generateLeaderboard(db *sql.DB, targetPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	query := `
		WITH ranked_submissions AS (
			SELECT contestant_id, id, verdict, composite_score, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics, updated_at,
			       ROW_NUMBER() OVER (PARTITION BY contestant_id ORDER BY composite_score DESC, updated_at ASC) as rank_score
			FROM submissions
			WHERE status = 'completed'
		)
		SELECT contestant_id, id, verdict, composite_score, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics, updated_at, rank_score
		FROM ranked_submissions
		WHERE rank_score <= 2
		ORDER BY contestant_id, rank_score ASC
	`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("[leaderboard] Database query failed: %v\n", err)
		return
	}
	defer rows.Close()

	type rawRow struct {
		contestantID     string
		submissionID     string
		verdict          string
		compositeScore   float64
		correctnessScore float64
		p50Us            int64
		p90Us            int64
		p99Us            int64
		actualTps        float64
		diagnosticsRaw   []byte
		updatedAt        time.Time
		rankScore        int
	}

	contestantMap := make(map[string][]rawRow)
	for rows.Next() {
		var r rawRow
		err := rows.Scan(
			&r.contestantID,
			&r.submissionID,
			&r.verdict,
			&r.compositeScore,
			&r.correctnessScore,
			&r.p50Us,
			&r.p90Us,
			&r.p99Us,
			&r.actualTps,
			&r.diagnosticsRaw,
			&r.updatedAt,
			&r.rankScore,
		)
		if err != nil {
			log.Printf("[leaderboard] Scan row error: %v\n", err)
			continue
		}
		contestantMap[r.contestantID] = append(contestantMap[r.contestantID], r)
	}

	entries := []LeaderboardEntry{}

	for _, subs := range contestantMap {
		if len(subs) == 0 {
			continue
		}
		
		// rank_score = 1 is the first entry due to ORDER BY rank_score ASC
		best := subs[0]

		var diag map[string]interface{}
		if len(best.diagnosticsRaw) > 0 {
			_ = json.Unmarshal(best.diagnosticsRaw, &diag)
		}
		if diag == nil {
			diag = make(map[string]interface{})
		}

		latScore, _ := diag["latency_score"].(float64)
		tpScore, _ := diag["throughput_score"].(float64)
		archetype, _ := diag["engine_archetype"].(string)
		if archetype == "" {
			archetype = "Unclassified"
		}
		reason, _ := diag["reason"].(string)
		if reason == "" {
			reason = "Optimal Execution (Passes all SLAs)"
		}

		entry := LeaderboardEntry{
			ContestantID:     best.contestantID,
			SubmissionID:     best.submissionID,
			Verdict:          best.verdict,
			CompositeScore:   best.compositeScore,
			CorrectnessScore: best.correctnessScore,
			P50Us:            best.p50Us,
			P90Us:            best.p90Us,
			P99Us:            best.p99Us,
			ActualTps:        best.actualTps,
			LatencyScore:     latScore,
			ThroughputScore:  tpScore,
			EngineArchetype:  archetype,
			UpdatedAt:        best.updatedAt,
			Reason:           reason,
		}

		// Calculate Deltas if the personal best high score is fresh (updated in the last 10 minutes)
		if time.Since(best.updatedAt) <= 10*time.Minute && len(subs) > 1 {
			secondBest := subs[1]
			entry.DeltaScore = best.compositeScore - secondBest.compositeScore
			entry.DeltaP99 = best.p99Us - secondBest.p99Us
		}

		entries = append(entries, entry)
	}

	// Sort leaderboard: best score first, tie-breaker oldest updatedAt first
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CompositeScore != entries[j].CompositeScore {
			return entries[i].CompositeScore > entries[j].CompositeScore
		}
		return entries[i].UpdatedAt.Before(entries[j].UpdatedAt)
	})

	// Assign Ranks
	for i := range entries {
		entries[i].Rank = i + 1
	}

	// Marshalling JSON
	dataBytes, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("[leaderboard] JSON marshalling failed: %v\n", err)
		return
	}

	// Write atomically using a temporary file to avoid partial read corruption
	tmpPath := targetPath + ".tmp"
	if err := os.WriteFile(tmpPath, dataBytes, 0644); err != nil {
		log.Printf("[leaderboard] Failed to write temporary file: %v\n", err)
		return
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		log.Printf("[leaderboard] Atomic rename failed: %v\n", err)
		os.Remove(tmpPath)
		return
	}

	log.Printf("[leaderboard] Generated successfully with %d entries ✓\n", len(entries))

	// Broadcast update to all live stream listeners
	hub.broadcast(string(dataBytes))
}
