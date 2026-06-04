package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
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
		SELECT contestant_id, id, verdict, composite_score, correctness_score, p50_us, p90_us, p99_us, actual_tps,
		       COALESCE((diagnostics->>'latency_score')::double precision, 0.0) AS latency_score,
		       COALESCE((diagnostics->>'throughput_score')::double precision, 0.0) AS throughput_score,
		       COALESCE(diagnostics->>'engine_archetype', 'Unclassified') AS engine_archetype,
		       updated_at
		FROM (
			SELECT DISTINCT ON (contestant_id) 
			       contestant_id, id, verdict, composite_score, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics, updated_at
			FROM submissions
			WHERE status = 'completed'
			ORDER BY contestant_id, composite_score DESC, updated_at ASC
		) AS sub
		ORDER BY composite_score DESC, updated_at ASC
	`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("[leaderboard] Database query failed: %v\n", err)
		return
	}
	defer rows.Close()

	entries := []LeaderboardEntry{}
	rank := 1

	for rows.Next() {
		var entry LeaderboardEntry
		err := rows.Scan(
			&entry.ContestantID,
			&entry.SubmissionID,
			&entry.Verdict,
			&entry.CompositeScore,
			&entry.CorrectnessScore,
			&entry.P50Us,
			&entry.P90Us,
			&entry.P99Us,
			&entry.ActualTps,
			&entry.LatencyScore,
			&entry.ThroughputScore,
			&entry.EngineArchetype,
			&entry.UpdatedAt,
		)
		if err != nil {
			log.Printf("[leaderboard] Scan row error: %v\n", err)
			continue
		}
		entry.Rank = rank
		entries = append(entries, entry)
		rank++
	}

	// Marshalling JSON
	dataBytes, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("[leaderboard] JSON marshalling failed: %v\n", err)
		return
	}

	// Write atomically using a temporary file to avoid partial read corruption
	tmpPath := targetPath + ".tmp"
	if err := ioutil.WriteFile(tmpPath, dataBytes, 0644); err != nil {
		log.Printf("[leaderboard] Failed to write temporary file: %v\n", err)
		return
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		log.Printf("[leaderboard] Atomic rename failed: %v\n", err)
		os.Remove(tmpPath)
		return
	}

	log.Printf("[leaderboard] Generated successfully with %d entries ✓\n", len(entries))
}
