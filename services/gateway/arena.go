package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Arena struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
}

type Badge struct {
	ArenaID   string `json:"arena_id"`
	Title     string `json:"title"`
	Type      string `json:"type"` // gold | silver | bronze
	Rank      int    `json:"rank"`
}

type ProfileStats struct {
	Handle         string  `json:"handle"`
	HighestScore   float64 `json:"highest_score"`
	LowestP99      int64   `json:"lowest_p99"`
	Trophies       []Badge `json:"trophies"`
	ContestsPlayed int     `json:"contests_played"`
}

// GET /api/v1/arena
func handleListArenas(c fiber.Ctx) error {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SELECT id, title, description, status, start_time, end_time FROM arenas ORDER BY start_time ASC")
	if err != nil {
		log.Printf("Failed to query arenas: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch arenas"})
	}
	defer rows.Close()

	arenas := []Arena{}
	for rows.Next() {
		var a Arena
		if err := rows.Scan(&a.ID, &a.Title, &a.Description, &a.Status, &a.StartTime, &a.EndTime); err != nil {
			continue
		}
		arenas = append(arenas, a)
	}

	return c.JSON(arenas)
}

// GET /api/v1/arena/:id
func handleGetArena(c fiber.Ctx) error {
	ctx := context.Background()
	id := c.Params("id")

	var a Arena
	err := db.QueryRowContext(ctx, "SELECT id, title, description, status, start_time, end_time FROM arenas WHERE id = $1", id).
		Scan(&a.ID, &a.Title, &a.Description, &a.Status, &a.StartTime, &a.EndTime)

	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Arena not found"})
	} else if err != nil {
		log.Printf("Failed to query arena details: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch arena details"})
	}

	return c.JSON(a)
}

// POST /api/v1/arena/:id/register
func handleRegisterArena(c fiber.Ctx) error {
	ctx := context.Background()
	arenaID := c.Params("id")
	userID := c.Locals("user_id").(string)

	// Verify arena exists
	var exists bool
	err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM arenas WHERE id = $1)", arenaID).Scan(&exists)
	if err != nil || !exists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Arena not found"})
	}

	// Insert registration
	_, err = db.ExecContext(ctx,
		"INSERT INTO arena_registrations (arena_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		arenaID, userID,
	)
	if err != nil {
		log.Printf("Failed to register contestant for arena: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to register for contest"})
	}

	return c.JSON(fiber.Map{"status": "registered"})
}

// GET /api/v1/arena/:id/registrations
func handleGetRegistrations(c fiber.Ctx) error {
	ctx := context.Background()
	arenaID := c.Params("id")

	rows, err := db.QueryContext(ctx,
		`SELECT u.handle FROM arena_registrations r
		 JOIN users u ON r.user_id = u.id
		 WHERE r.arena_id = $1 ORDER BY r.registered_at ASC`,
		arenaID,
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch registrations"})
	}
	defer rows.Close()

	handles := []string{}
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err == nil {
			handles = append(handles, handle)
		}
	}

	return c.JSON(handles)
}

// GET /api/v1/profile/:id
func handleGetProfile(c fiber.Ctx) error {
	ctx := context.Background()
	profileID := c.Params("id")

	var handle string
	err := db.QueryRowContext(ctx, "SELECT handle FROM users WHERE id = $1 OR handle = $1", profileID).Scan(&handle)
	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User profile not found"})
	} else if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch profile"})
	}

	var uID string
	_ = db.QueryRowContext(ctx, "SELECT id FROM users WHERE handle = $1", handle).Scan(&uID)

	var highestScore float64
	_ = db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(composite_score), 0.0) FROM submissions WHERE user_id = $1 AND status = 'completed'",
		uID,
	).Scan(&highestScore)

	var lowestP99 int64
	_ = db.QueryRowContext(ctx,
		"SELECT COALESCE(MIN(p99_us), 0) FROM submissions WHERE user_id = $1 AND status = 'completed' AND p99_us > 0",
		uID,
	).Scan(&lowestP99)

	// Get all ended contests this user submitted for
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT a.id, a.title, a.end_time
		 FROM submissions s
		 JOIN arenas a ON s.arena_id = a.id
		 WHERE s.user_id = $1 AND (a.status = 'ended' OR a.status = 'system_test')`,
		uID,
	)
	if err != nil {
		log.Printf("Profile query arenas error: %v", err)
	}

	trophies := []Badge{}
	contestsPlayed := 0

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var aID, aTitle string
			var aEndTime time.Time
			if err := rows.Scan(&aID, &aTitle, &aEndTime); err == nil {
				contestsPlayed++
				// Compute user's rank in this contest
				standingsQuery := `
					WITH standings AS (
						SELECT user_id, MAX(composite_score) as best_score, MIN(updated_at) as min_updated_at
						FROM submissions
						WHERE arena_id = $1 AND status = 'completed'
						GROUP BY user_id
					),
					ranked AS (
						SELECT user_id,
						       ROW_NUMBER() OVER (ORDER BY best_score DESC, min_updated_at ASC) as rank
						FROM standings
					)
					SELECT rank FROM ranked WHERE user_id = $2
				`
				var rank int
				err := db.QueryRowContext(ctx, standingsQuery, aID, uID).Scan(&rank)
				if err == nil {
					var badgeType string
					if rank == 1 {
						badgeType = "gold"
					} else if rank == 2 {
						badgeType = "silver"
					} else if rank == 3 {
						badgeType = "bronze"
					}

					if badgeType != "" {
						trophies = append(trophies, Badge{
							ArenaID:   aID,
							Title:     aTitle,
							Type:      badgeType,
							Rank:      rank,
						})
					}
				}
			}
		}
	}

	return c.JSON(ProfileStats{
		Handle:         handle,
		HighestScore:   highestScore,
		LowestP99:      lowestP99,
		Trophies:       trophies,
		ContestsPlayed: contestsPlayed,
	})
}

// POST /api/v1/admin/arena (Create Arena)
func handleCreateArena(c fiber.Ctx) error {
	ctx := context.Background()
	var body struct {
		Title       string    `json:"title"`
		Description string    `json:"description"`
		StartTime   time.Time `json:"start_time"`
		EndTime     time.Time `json:"end_time"`
	}

	if err := c.Bind().JSON(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	if body.Title == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Arena title is required"})
	}

	arenaID := uuid.New().String()
	_, err := db.ExecContext(ctx,
		"INSERT INTO arenas (id, title, description, start_time, end_time) VALUES ($1, $2, $3, $4, $5)",
		arenaID, body.Title, body.Description, body.StartTime, body.EndTime,
	)
	if err != nil {
		log.Printf("Failed to insert arena: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create arena"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":          arenaID,
		"title":       body.Title,
		"description": body.Description,
		"start_time":  body.StartTime,
		"end_time":    body.EndTime,
		"status":      "upcoming",
	})
}

// PUT /api/v1/admin/arena/:id (Update Arena & trigger system tests if status changes)
func handleUpdateArena(c fiber.Ctx) error {
	ctx := context.Background()
	arenaID := c.Params("id")

	var body struct {
		Title       string    `json:"title"`
		Description string    `json:"description"`
		StartTime   time.Time `json:"start_time"`
		EndTime     time.Time `json:"end_time"`
		Status      string    `json:"status"`
	}

	if err := c.Bind().JSON(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	// Fetch current status
	var currentStatus string
	err := db.QueryRowContext(ctx, "SELECT status FROM arenas WHERE id = $1", arenaID).Scan(&currentStatus)
	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Arena not found"})
	} else if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Database error"})
	}

	// Update arena
	_, err = db.ExecContext(ctx,
		`UPDATE arenas 
		 SET title = COALESCE(NULLIF($1, ''), title), 
		     description = COALESCE(NULLIF($2, ''), description),
		     start_time = COALESCE(NULLIF($3, '0001-01-01T00:00:00Z'::timestamptz), start_time),
		     end_time = COALESCE(NULLIF($4, '0001-01-01T00:00:00Z'::timestamptz), end_time),
		     status = COALESCE(NULLIF($5, ''), status)
		 WHERE id = $6`,
		body.Title, body.Description, body.StartTime, body.EndTime, body.Status, arenaID,
	)
	if err != nil {
		log.Printf("Failed to update arena: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update arena"})
	}

	// Trigger System Tests if status transitioned to 'system_test'
	if body.Status == "system_test" && currentStatus != "system_test" {
		go triggerSystemTestsForArena(arenaID)
	}

	return c.JSON(fiber.Map{"status": "ok"})
}

// Helper to trigger system tests
func triggerSystemTestsForArena(arenaID string) {
	ctx := context.Background()
	log.Printf("[systest] Triggering system tests for arena %s...", arenaID)

	// Fetch the best submission for each contestant in this arena
	query := `
		WITH ranked_submissions AS (
			SELECT s.id, s.contestant_id, s.s3_path, s.github_url, s.user_id,
			       ROW_NUMBER() OVER (PARTITION BY s.user_id ORDER BY s.composite_score DESC, s.created_at DESC) as rn
			FROM submissions s
			WHERE s.arena_id = $1 AND s.status = 'completed'
		)
		SELECT id, contestant_id, s3_path, github_url FROM ranked_submissions WHERE rn = 1
	`
	rows, err := db.QueryContext(ctx, query, arenaID)
	if err != nil {
		log.Printf("[systest] Failed to query submissions for system testing: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var subID, contestantID, s3Path, githubURL sql.NullString
		if err := rows.Scan(&subID, &contestantID, &s3Path, &githubURL); err != nil {
			continue
		}

		if !subID.Valid {
			continue
		}

		// Reset score and status of submission to queued/pending
		_, err := db.ExecContext(ctx,
			`UPDATE submissions 
			 SET status = 'queued', verdict = 'Pending', composite_score = 0, correctness_score = 0,
			     p50_us = 0, p90_us = 0, p99_us = 0, actual_tps = 0, diagnostics = '{}'::jsonb
			 WHERE id = $1`,
			subID.String,
		)
		if err != nil {
			log.Printf("[systest] Failed to reset submission %s: %v", subID.String, err)
			continue
		}

		// Push to system test queue Redis stream
		redisValues := map[string]interface{}{
			"submission_id": subID.String,
			"image_tag":     fmt.Sprintf("contestant-%s", subID.String),
			"contestant_id": contestantID.String,
		}

		err = rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: common.SystestQueue,
			Values: redisValues,
		}).Err()

		if err != nil {
			log.Printf("[systest] Failed to queue submission %s to Redis SystestStream: %v", subID.String, err)
		} else {
			log.Printf("[systest] Successfully queued submission %s to system test queue ✓", subID.String)
		}
	}
}

// POST /api/v1/admin/arena/:id/rejudge
func handleRejudgeArena(c fiber.Ctx) error {
	arenaID := c.Params("id")
	go triggerSystemTestsForArena(arenaID)
	return c.JSON(fiber.Map{"status": "rejudge_triggered"})
}

// GET /api/v1/admin/workers
func handleGetWorkersTelemetry(c fiber.Ctx) error {
	ctx := context.Background()
	compileLen := rdb.XLen(ctx, common.CompilationQueue).Val()
	pretestLen := rdb.XLen(ctx, common.PretestQueue).Val()
	systestLen := rdb.XLen(ctx, common.SystestQueue).Val()

	return c.JSON(fiber.Map{
		"compilation_queue_depth": compileLen,
		"pretest_queue_depth":     pretestLen,
		"systest_queue_depth":     systestLen,
	})
}
