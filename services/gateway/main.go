package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"
	"github.com/gofiber/fiber/v3/middleware/sse"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"
)

var (
	rdb                 *redis.Client
	db                  *sql.DB
	s3Client            *minio.Client
	HTTPRequestsCount   uint64
	leaderboardJSONPath string
)

type ArenaSSEHub struct {
	mu          sync.Mutex
	subscribers map[string][]chan string // arena_id -> list of subscriber channels
}

var gatewayLeaderboardHub = &ArenaSSEHub{
	subscribers: make(map[string][]chan string),
}

func (h *ArenaSSEHub) subscribe(arenaID string) chan string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan string, 4)
	h.subscribers[arenaID] = append(h.subscribers[arenaID], ch)
	return ch
}

func (h *ArenaSSEHub) unsubscribe(arenaID string, ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.subscribers[arenaID]
	for i, s := range subs {
		if s == ch {
			h.subscribers[arenaID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

func (h *ArenaSSEHub) broadcast(arenaID string, payload string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subscribers[arenaID] {
		select {
		case ch <- payload:
		default:
		}
	}
}

func main() {
	// Connect to Redis
	rdb = common.GetRedisClient()
	defer rdb.Close()

	// Connect to PostgreSQL
	var err error
	for i := 0; i < 5; i++ {
		db, err = common.GetDB()
		if err == nil {
			break
		}
		log.Printf("Waiting for Postgres... error: %v\n", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("Postgres connection failed: %v", err)
	}
	defer db.Close()
	common.ConfigureDBPool(db)

	// Bootstrap Default Arena
	bootstrapDefaultArena()

	// Initialize Redis Streams and groups
	ctx := context.Background()
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		log.Fatalf("Redis Stream initialization failed: %v", err)
	}

	// Connect to S3/MinIO
	s3Client, err = common.GetS3Client()
	if err != nil {
		log.Fatalf("Failed to initialize S3 client: %v", err)
	}
	if err := common.EnsureS3Bucket(ctx, s3Client); err != nil {
		log.Fatalf("Failed to ensure S3 bucket: %v", err)
	}

	// Start metrics server
	go common.ServeMetrics(":9093")
	common.StartDBPoolCollector(ctx, db, "gateway", 5*time.Second)

	// Initialize Fiber app
	app := fiber.New(fiber.Config{
		BodyLimit: 10 * 1024 * 1024, // 10 MB limit
	})

	// Register HTTP telemetry middleware
	app.Use(func(c fiber.Ctx) error {
		atomic.AddUint64(&HTTPRequestsCount, 1)
		start := time.Now()
		err := c.Next()
		duration := time.Since(start).Seconds()

		method := c.Method()
		path := c.Path()
		statusCode := fmt.Sprintf("%d", c.Response().StatusCode())

		common.HTTPRequestsTotal.WithLabelValues(method, path, statusCode).Inc()
		common.HTTPRequestDuration.WithLabelValues(method, path).Observe(duration)

		return err
	})

	// Start periodic gateway leaderboard generator to broadcast active updates
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		for range ticker.C {
			broadcastActiveLeaderboards()
		}
	}()

	// Auth API
	app.Post("/api/v1/auth/register", handleRegister)
	app.Post("/api/v1/auth/login", handleLogin)
	app.Post("/api/v1/auth/logout", handleLogout)
	app.Get("/api/v1/auth/me", optionalAuth, handleMe)
	app.Get("/api/v1/auth/github", handleGitHubLogin)
	app.Get("/api/v1/auth/github/callback", handleGitHubCallback)

	// User Profile
	app.Get("/api/v1/profile/:id", handleGetProfile)

	// Arenas API
	app.Get("/api/v1/arena", handleListArenas)
	app.Get("/api/v1/arena/:id", handleGetArena)
	app.Post("/api/v1/arena/:id/register", requireAuth, handleRegisterArena)
	app.Get("/api/v1/arena/:id/registrations", requireAuth, handleGetRegistrations)

	// Submissions API
	app.Post("/api/v1/submit", optionalAuth, handleSubmission)
	app.Get("/api/v1/build/:id", handleBuildStatus)
	app.Get("/api/v1/submissions/:id/diagnostics", handleBuildStatus)
	app.Get("/api/v1/submissions/:id/source", optionalAuth, handleGetSource)
	app.Get("/api/v1/builds", handleListBuilds)

	// Leaderboards API
	app.Get("/api/v1/leaderboard/:arena_id", handleGetLeaderboard)
	app.Get("/api/v1/leaderboard/:arena_id/stream", handleGetLeaderboardStream)

	// Admin Console API
	app.Post("/api/v1/admin/arena", requireAuth, requireAdmin, handleCreateArena)
	app.Put("/api/v1/admin/arena/:id", requireAuth, requireAdmin, handleUpdateArena)
	app.Post("/api/v1/admin/arena/:id/rejudge", requireAuth, requireAdmin, handleRejudgeArena)
	app.Get("/api/v1/admin/workers", requireAuth, requireAdmin, handleGetWorkersTelemetry)

	// Dashboard
	app.Get("/dashboard", handleDashboardPage)
	app.Get("/api/v1/dashboard/metrics", handleDashboardMetrics)
	app.Post("/api/v1/dashboard/actions/mock-submission", handleMockSubmission)
	app.Post("/api/v1/dashboard/actions/clean-db", handleCleanDB)

	// Serve Leaderboard JSON
	leaderboardJSONPath = os.Getenv("LEADERBOARD_JSON_PATH")
	if leaderboardJSONPath == "" {
		leaderboardJSONPath = "./frontend/leaderboard.json"
	}
	app.Get("/leaderboard.json", func(c fiber.Ctx) error {
		c.Set("Content-Type", "application/json")
		return c.SendFile(leaderboardJSONPath)
	})

	// Serve Static Frontend
	app.Get("/*", static.New("./frontend"))

	log.Println("Submission Gateway API running on port 3000...")
	log.Fatal(app.Listen(":3000"))
}

func bootstrapDefaultArena() {
	var err error
	for i := 0; i < 5; i++ {
		var exists bool
		err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM arenas WHERE id = 'default')").Scan(&exists)
		if err == nil {
			if !exists {
				_, err = db.Exec(
					`INSERT INTO arenas (id, title, description, status, start_time, end_time) 
					 VALUES ('default', 'Default Arena', 'Default competitive programming sandbox arena.', 'active', NOW(), NOW() + INTERVAL '30 days')`,
				)
				if err != nil {
					log.Printf("Failed to bootstrap default arena: %v\n", err)
				} else {
					log.Println("Bootstrapped default arena ✓")
				}
			}
			break
		}
		log.Printf("Waiting for Postgres to bootstrap... %v\n", err)
		time.Sleep(1 * time.Second)
	}
}

func handleSubmission(c fiber.Ctx) error {
	ctx := context.Background()

	// 1. Extract credentials
	contestantID := c.FormValue("contestant_id")
	if contestantID == "" {
		contestantID = "anonymous"
	}

	var userID sql.NullString
	if c.Locals("user_id") != nil {
		userID.Valid = true
		userID.String = c.Locals("user_id").(string)
		if c.Locals("handle") != nil {
			contestantID = c.Locals("handle").(string)
		}
	}

	arenaID := c.FormValue("arena_id")
	if arenaID == "" {
		arenaID = "default"
	}

	// 2. Redis Rate Limiter
	rateLimitKey := contestantID
	if userID.Valid {
		rateLimitKey = userID.String
	}
	allowed, retryAfter, err := CheckRateLimit(ctx, rdb, rateLimitKey)
	if err != nil {
		log.Printf("Rate limit check failed for %s: %v\n", rateLimitKey, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Internal server error checking rate limit"})
	}
	if !allowed {
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error":       fmt.Sprintf("Too many requests. Please retry in %d seconds", retryAfter),
			"retry_after": retryAfter,
		})
	}

	githubURL := c.FormValue("github_url")
	var s3Path string
	var sourceCode string

	buildID := uuid.New().String()

	if githubURL != "" {
		sourceCode = fmt.Sprintf("[GitHub URL: %s]", githubURL)
	} else {
		// Expect ZIP file upload via form field "source_code"
		fileHeader, err := c.FormFile("source_code")
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing source_code ZIP file or github_url"})
		}

		if filepath.Ext(fileHeader.Filename) != ".zip" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Only .zip files accepted for multi-file submissions"})
		}

		file, err := fileHeader.Open()
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to open uploaded file"})
		}
		defer file.Close()

		srcBytes, err := io.ReadAll(file)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read uploaded file"})
		}
		// Normalize the zip and convert to tar.gz.
		// Kaniko requires tar.gz for S3 build context — a raw .zip gives
		// "gzip: invalid header" and fails immediately.
		tarGzBytes, err := normalizeZipToTarGz(srcBytes)
		if err != nil {
			log.Printf("Failed to normalize submission zip %s: %v\n", buildID, err)
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid zip archive: " + err.Error()})
		}
		sourceCode = "[ZIP Submission]"

		// Upload as tar.gz — Kaniko reads this directly as its S3 build context
		s3Path = fmt.Sprintf("submissions/%s/submission.tar.gz", buildID)
		_, err = s3Client.PutObject(ctx, common.S3Bucket, s3Path, bytes.NewReader(tarGzBytes), int64(len(tarGzBytes)), minio.PutObjectOptions{
			ContentType: "application/gzip",
		})
		if err != nil {
			log.Printf("Failed to upload submission %s to S3: %v\n", buildID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to store submission"})
		}
	}

	// 5. Save to PostgreSQL
	_, err = db.ExecContext(ctx,
		`INSERT INTO submissions (id, contestant_id, status, verdict, s3_path, github_url, user_id, arena_id) 
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		buildID, contestantID, "queued", "Pending", s3Path, githubURL, userID, arenaID,
	)
	if err != nil {
		log.Printf("Failed to insert submission %s into DB: %v\n", buildID, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to save submission status"})
	}

	_, err = db.ExecContext(ctx,
		"INSERT INTO submission_sources (submission_id, source_code) VALUES ($1, $2)",
		buildID, sourceCode,
	)
	if err != nil {
		log.Printf("Failed to save submission source %s to DB: %v\n", buildID, err)
	}

	// 6. Push job to compilation queue
	redisValues := map[string]interface{}{
		"submission_id": buildID,
		"contestant_id": contestantID,
	}
	if githubURL != "" {
		redisValues["github_url"] = githubURL
	} else {
		redisValues["s3_path"] = s3Path
	}

	err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: common.CompilationQueue,
		Values: redisValues,
	}).Err()
	if err != nil {
		log.Printf("Failed to queue build job for %s: %v\n", buildID, err)
		db.ExecContext(ctx, "UPDATE submissions SET status = $1, verdict = $2, diagnostics = $3 WHERE id = $4", "failed", "Queue Failure", `{"error":"failed to queue job"}`, buildID)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to queue submission"})
	}

	log.Printf("Submission %s for %s accepted and queued for building ✓\n", buildID, contestantID)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"build_id": buildID,
		"status":   "queued",
		"poll":     fmt.Sprintf("/api/v1/build/%s", buildID),
	})
}

func handleBuildStatus(c fiber.Ctx) error {
	ctx := context.Background()
	id := c.Params("id")

	var (
		buildID          string
		contestantID     string
		status           string
		verdict          string
		diagnosticsBytes []byte
		compositeScore   float64
		createdAt        time.Time
		githubURL        sql.NullString
		arenaID          sql.NullString
	)

	err := db.QueryRowContext(ctx,
		"SELECT id, contestant_id, status, verdict, diagnostics, composite_score, created_at, github_url, arena_id FROM submissions WHERE id = $1",
		id,
	).Scan(&buildID, &contestantID, &status, &verdict, &diagnosticsBytes, &compositeScore, &createdAt, &githubURL, &arenaID)

	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "build not found"})
	} else if err != nil {
		log.Printf("Query error for build status %s: %v\n", id, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal database error"})
	}

	var diagnostics map[string]interface{}
	if err := json.Unmarshal(diagnosticsBytes, &diagnostics); err != nil {
		diagnostics = map[string]interface{}{}
	}

	resMap := fiber.Map{
		"build_id":        buildID,
		"contestant_id":   contestantID,
		"status":          status,
		"verdict":         verdict,
		"diagnostics":     diagnostics,
		"composite_score": compositeScore,
		"submitted_at":    createdAt,
	}
	if githubURL.Valid {
		resMap["github_url"] = githubURL.String
	} else {
		resMap["github_url"] = ""
	}
	if arenaID.Valid {
		resMap["arena_id"] = arenaID.String
	} else {
		resMap["arena_id"] = "default"
	}

	return c.JSON(resMap)
}

func handleListBuilds(c fiber.Ctx) error {
	ctx := context.Background()
	arenaID := c.Query("arena_id")

	var rows *sql.Rows
	var err error

	if arenaID != "" {
		rows, err = db.QueryContext(ctx,
			`SELECT id, contestant_id, status, verdict, diagnostics, composite_score, created_at, github_url, arena_id 
			 FROM submissions WHERE arena_id = $1 ORDER BY created_at DESC LIMIT 50`,
			arenaID,
		)
	} else {
		rows, err = db.QueryContext(ctx,
			`SELECT id, contestant_id, status, verdict, diagnostics, composite_score, created_at, github_url, arena_id 
			 FROM submissions ORDER BY created_at DESC LIMIT 50`,
		)
	}

	if err != nil {
		log.Printf("Query error listing builds: %v\n", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal database error"})
	}
	defer rows.Close()

	builds := []fiber.Map{}
	for rows.Next() {
		var (
			buildID          string
			contestantID     string
			status           string
			verdict          string
			diagnosticsBytes []byte
			compositeScore   float64
			createdAt        time.Time
			githubURL        sql.NullString
			arenaID          sql.NullString
		)

		if err := rows.Scan(&buildID, &contestantID, &status, &verdict, &diagnosticsBytes, &compositeScore, &createdAt, &githubURL, &arenaID); err != nil {
			continue
		}

		var diagnostics map[string]interface{}
		json.Unmarshal(diagnosticsBytes, &diagnostics)

		resMap := fiber.Map{
			"build_id":        buildID,
			"contestant_id":   contestantID,
			"status":          status,
			"verdict":         verdict,
			"diagnostics":     diagnostics,
			"composite_score": compositeScore,
			"submitted_at":    createdAt,
		}
		if githubURL.Valid {
			resMap["github_url"] = githubURL.String
		} else {
			resMap["github_url"] = ""
		}
		if arenaID.Valid {
			resMap["arena_id"] = arenaID.String
		} else {
			resMap["arena_id"] = "default"
		}

		builds = append(builds, resMap)
	}

	return c.JSON(builds)
}

func handleGetSource(c fiber.Ctx) error {
	ctx := context.Background()
	id := c.Params("id")

	var subUserID sql.NullString
	var subArenaID sql.NullString
	err := db.QueryRowContext(ctx, "SELECT user_id, arena_id FROM submissions WHERE id = $1", id).Scan(&subUserID, &subArenaID)
	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "submission not found"})
	} else if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal database error"})
	}

	// Fetch current user from optionalAuth context
	currentUserID := ""
	currentUserRole := ""
	if c.Locals("user_id") != nil {
		currentUserID = c.Locals("user_id").(string)
	}
	if c.Locals("role") != nil {
		currentUserRole = c.Locals("role").(string)
	}

	isOwnerOrAdmin := currentUserRole == "admin" || (subUserID.Valid && subUserID.String == currentUserID)

	if !isOwnerOrAdmin {
		var arenaStatus string
		if subArenaID.Valid {
			_ = db.QueryRowContext(ctx, "SELECT status FROM arenas WHERE id = $1", subArenaID.String).Scan(&arenaStatus)
		}
		if arenaStatus != "ended" {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Source code is private until the contest ends"})
		}
	}

	var src string
	err = db.QueryRowContext(ctx,
		"SELECT source_code FROM submission_sources WHERE submission_id = $1", id,
	).Scan(&src)
	if err == sql.ErrNoRows {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "source not found"})
	} else if err != nil {
		log.Printf("Query error for submission source %s: %v\n", id, err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal database error"})
	}
	return c.JSON(fiber.Map{"source_code": src})
}

// GET /api/v1/leaderboard/:arena_id
func handleGetLeaderboard(c fiber.Ctx) error {
	arenaID := c.Params("arena_id")
	dataBytes, err := generateLeaderboardData(arenaID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to generate leaderboard"})
	}

	c.Set("Content-Type", "application/json")
	return c.Send(dataBytes)
}

// GET /api/v1/leaderboard/:arena_id/stream
func handleGetLeaderboardStream(c fiber.Ctx) error {
	arenaID := c.Params("arena_id")

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("Access-Control-Allow-Origin", "*")

	return sse.New(sse.Config{
		Handler: func(c fiber.Ctx, stream *sse.Stream) error {
			// Send current data immediately
			data, err := generateLeaderboardData(arenaID)
			if err == nil {
				_ = stream.Event(sse.Event{
					Data: data,
				})
			}

			ch := gatewayLeaderboardHub.subscribe(arenaID)
			defer gatewayLeaderboardHub.unsubscribe(arenaID, ch)

			keepAliveTicker := time.NewTicker(15 * time.Second)
			defer keepAliveTicker.Stop()

			for {
				select {
				case payload := <-ch:
					err := stream.Event(sse.Event{
						Data: []byte(payload),
					})
					if err != nil {
						return nil // client disconnected
					}
				case <-keepAliveTicker.C:
					// Send a keep-alive event
					err := stream.Event(sse.Event{
						Name: "ping",
						Data: []byte("keep-alive"),
					})
					if err != nil {
						return nil
					}
				case <-c.Context().Done():
					return nil
				}
			}
		},
	})(c)
}

func broadcastActiveLeaderboards() {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SELECT id FROM arenas WHERE status IN ('active', 'system_test')")
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var arenaID string
		if err := rows.Scan(&arenaID); err == nil {
			data, err := generateLeaderboardData(arenaID)
			if err == nil {
				gatewayLeaderboardHub.broadcast(arenaID, string(data))
				if arenaID == "default" {
					// Fallback file sync
					_ = os.MkdirAll(filepath.Dir(leaderboardJSONPath), 0755)
					_ = os.WriteFile(leaderboardJSONPath, data, 0644)
				}
			}
		}
	}
}

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
	Status           string    `json:"status"`
	UpdatedAt        time.Time `json:"updated_at"`
	DeltaScore       float64   `json:"delta_score"`
	DeltaP99         int64     `json:"delta_p99"`
	Reason           string    `json:"reason"`
}

func generateLeaderboardData(arenaID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	query := `
		WITH ranked_submissions AS (
			SELECT contestant_id, id, verdict, composite_score, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics, updated_at, status, COALESCE(user_id, contestant_id) as user_id,
			       ROW_NUMBER() OVER (PARTITION BY COALESCE(user_id, contestant_id) ORDER BY (verdict = 'Accepted') DESC, composite_score DESC, updated_at ASC) as rank_score
			FROM submissions
			WHERE arena_id = $1
		)
		SELECT contestant_id, id, verdict, composite_score, correctness_score, p50_us, p90_us, p99_us, actual_tps, diagnostics, updated_at, status, rank_score, user_id
		FROM ranked_submissions
		WHERE rank_score <= 2
		ORDER BY user_id, rank_score ASC
	`

	rows, err := db.QueryContext(ctx, query, arenaID)
	if err != nil {
		return nil, err
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
		status           string
		rankScore        int
		userID           string
	}

	userMap := make(map[string][]rawRow)
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
			&r.status,
			&r.rankScore,
			&r.userID,
		)
		if err != nil {
			continue
		}
		userMap[r.userID] = append(userMap[r.userID], r)
	}

	entries := []LeaderboardEntry{}

	for _, subs := range userMap {
		if len(subs) == 0 {
			continue
		}

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
			Status:           best.status,
			UpdatedAt:        best.updatedAt,
			Reason:           reason,
		}

		if time.Since(best.updatedAt) <= 10*time.Minute && len(subs) > 1 {
			secondBest := subs[1]
			entry.DeltaScore = best.compositeScore - secondBest.compositeScore
			entry.DeltaP99 = best.p99Us - secondBest.p99Us
		}

		entries = append(entries, entry)
	}

	// Sort leaderboard
	sort.Slice(entries, func(i, j int) bool {
		iAccepted := entries[i].Verdict == "Accepted"
		jAccepted := entries[j].Verdict == "Accepted"
		if iAccepted != jAccepted {
			return iAccepted // Accepted runs rank above failed runs
		}
		if entries[i].CompositeScore != entries[j].CompositeScore {
			return entries[i].CompositeScore > entries[j].CompositeScore
		}
		return entries[i].UpdatedAt.Before(entries[j].UpdatedAt)
	})

	for i := range entries {
		entries[i].Rank = i + 1
	}

	return json.Marshal(entries)
}
