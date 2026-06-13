package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"
)

var (
	lastMetricsTime = time.Now()
	lastHTTPCount   uint64
)

type PodList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	} `json:"items"`
}

type K8sPodStatus struct {
	GatewayPods   int  `json:"gateway_pods"`
	CompilerPods  int  `json:"compiler_pods"`
	PretestPods   int  `json:"pretest_pods"`
	PostgresPods  int  `json:"postgres_pods"`
	RedisPods     int  `json:"redis_pods"`
	TotalPods     int  `json:"total_pods"`
	IsClusterMode bool `json:"is_cluster_mode"`
}

func getK8sPods() (K8sPodStatus, error) {
	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		// Fallback for host development environment
		return K8sPodStatus{
			GatewayPods:   3,
			CompilerPods:  2,
			PretestPods:   0,
			PostgresPods:  1,
			RedisPods:     1,
			TotalPods:     7,
			IsClusterMode: false,
		}, nil
	}
	token := strings.TrimSpace(string(tokenBytes))

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}

	req, err := http.NewRequest("GET", "https://kubernetes.default.svc/api/v1/namespaces/default/pods", nil)
	if err != nil {
		return K8sPodStatus{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return K8sPodStatus{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return K8sPodStatus{}, fmt.Errorf("k8s api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return K8sPodStatus{}, err
	}

	var podList PodList
	if err := json.Unmarshal(body, &podList); err != nil {
		return K8sPodStatus{}, err
	}

	var status K8sPodStatus
	status.IsClusterMode = true

	for _, item := range podList.Items {
		if item.Status.Phase != "Running" {
			continue
		}
		status.TotalPods++
		appLabel := item.Metadata.Labels["app"]
		switch appLabel {
		case "submission-gateway":
			status.GatewayPods++
		case "compilation-worker":
			status.CompilerPods++
		case "pretest-worker":
			status.PretestPods++
		case "postgres":
			status.PostgresPods++
		case "redis":
			status.RedisPods++
		}
	}

	return status, nil
}

func handleDashboardPage(c fiber.Ctx) error {
	c.Set("Content-Type", "text/html")
	return c.SendString(dashboardHTML)
}

func handleDashboardMetrics(c fiber.Ctx) error {
	ctx := context.Background()

	// 1. Health checks
	dbHealthy := true
	if err := db.PingContext(ctx); err != nil {
		dbHealthy = false
	}

	redisHealthy := true
	if err := rdb.Ping(ctx).Err(); err != nil {
		redisHealthy = false
	}

	// 2. Metrics
	var totalSubs int
	var activeSubs int
	var maxScore float64

	if dbHealthy {
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions").Scan(&totalSubs)
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions WHERE status IN ('queued', 'compiling', 'running')").Scan(&activeSubs)
		_ = db.QueryRowContext(ctx, "SELECT COALESCE(MAX(composite_score), 0.0) FROM submissions").Scan(&maxScore)
	}

	var compileQueueDepth int64
	var pretestQueueDepth int64
	if redisHealthy {
		compileQueueDepth = rdb.XLen(ctx, common.CompilationQueue).Val()
		pretestQueueDepth = rdb.XLen(ctx, common.PretestQueue).Val()
	}

	// 3. Recent submissions (excluding source code reading)
	recentSubmissions := []fiber.Map{}
	if dbHealthy {
		rows, err := db.QueryContext(ctx,
			"SELECT id, contestant_id, status, verdict, diagnostics, composite_score, created_at FROM submissions ORDER BY created_at DESC LIMIT 30",
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var (
					id               string
					contestantID     string
					status           string
					verdict          string
					diagnosticsBytes []byte
					compositeScore   float64
					createdAt        time.Time
				)
				if err := rows.Scan(&id, &contestantID, &status, &verdict, &diagnosticsBytes, &compositeScore, &createdAt); err != nil {
					continue
				}

				var diagnostics map[string]interface{}
				_ = json.Unmarshal(diagnosticsBytes, &diagnostics)

				recentSubmissions = append(recentSubmissions, fiber.Map{
					"build_id":        id,
					"contestant_id":   contestantID,
					"status":          status,
					"verdict":         verdict,
					"diagnostics":     diagnostics,
					"composite_score": compositeScore,
					"submitted_at":    createdAt,
				})
			}
		}
	}

	// 4. Fetch Kubernetes Pod status metrics
	k8sStatus, err := getK8sPods()
	if err != nil {
		log.Printf("Failed to fetch k8s pod metrics: %v\n", err)
	}

	return c.JSON(fiber.Map{
		"db_healthy":              dbHealthy,
		"redis_healthy":           redisHealthy,
		"total_submissions":       totalSubs,
		"active_submissions":      activeSubs,
		"compilation_queue_depth": compileQueueDepth,
		"pretest_queue_depth":     pretestQueueDepth,
		"max_composite_score":     maxScore,
		"recent_submissions":      recentSubmissions,
		"k8s_status":              k8sStatus,
	})
}

func zipDirToBytes(dirPath string) ([]byte, error) {
	var zipBuf bytes.Buffer
	zipWriter := zip.NewWriter(&zipBuf)

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}
		f, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}
		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = f.Write(fileBytes)
		return err
	})
	if err != nil {
		return nil, err
	}
	err = zipWriter.Close()
	if err != nil {
		return nil, err
	}
	return zipBuf.Bytes(), nil
}

func handleMockSubmission(c fiber.Ctx) error {
	ctx := context.Background()
	buildID := uuid.New().String()

	// 0. Enforce Submission Mutex (DB check) - Temporarily disabled for autoscaling test
	/*
	var activeCount int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions WHERE status IN ('queued', 'compiling', 'running')").Scan(&activeCount)
	if err == nil && activeCount > 0 {
		return c.Status(fiber.StatusLocked).SendString("Another benchmark is currently running. Please wait for it to complete.")
	}
	*/

	engine := c.Query("engine")
	if engine == "" {
		engine = "go_optimized"
	}

	allowedEngines := map[string]bool{
		"go_optimized": true,
		"python_slow":  true,
		"rust_crash":   true,
		"node_scammer": true,
		"cpp_basic":    true,
		"go_ws":        true,
		"go_rest":      true,
		"go_fix":       true,
	}
	if !allowedEngines[engine] {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid engine name")
	}

	dirPath := filepath.Join("test_payloads", engine)
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return c.Status(fiber.StatusBadRequest).SendString("Engine directory not found")
	}

	zipBytes, err := zipDirToBytes(dirPath)
	if err != nil {
		log.Printf("[mock] Failed to zip directory %s: %v\n", dirPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to package mock engine")
	}

	// 2. Upload to S3/MinIO
	s3Path := fmt.Sprintf("submissions/%s/submission.zip", buildID)
	_, err = s3Client.PutObject(ctx, common.S3Bucket, s3Path, bytes.NewReader(zipBytes), int64(len(zipBytes)), minio.PutObjectOptions{
		ContentType: "application/zip",
	})
	if err != nil {
		log.Printf("[mock] Failed to upload mock to S3: %v\n", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to store mock submission ZIP")
	}

	// 3. Save to database
	contestantID := fmt.Sprintf("mock-contestant-%d", rand.Intn(1000000))
	_, err = db.ExecContext(ctx,
		"INSERT INTO submissions (id, contestant_id, status, verdict, s3_path, arena_id) VALUES ($1, $2, $3, $4, $5, 'default')",
		buildID, contestantID, "queued", "Pending", s3Path,
	)
	if err != nil {
		log.Printf("[mock] Failed to insert into PostgreSQL: %v\n", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to save mock submission status")
	}

	_, err = db.ExecContext(ctx,
		"INSERT INTO submission_sources (submission_id, source_code) VALUES ($1, $2)",
		buildID, fmt.Sprintf("[ZIP Mock Submission: %s]", engine),
	)
	if err != nil {
		log.Printf("[mock] Failed to save mock submission source to DB: %v\n", err)
	}

	isSystestStr := c.Query("is_systest")
	if isSystestStr == "" {
		isSystestStr = "true"
	}

	// 4. Push to compilation queue
	err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: common.CompilationQueue,
		Values: map[string]interface{}{
			"submission_id": buildID,
			"s3_path":       s3Path,
			"contestant_id": contestantID,
			"is_systest":    isSystestStr,
		},
	}).Err()
	if err != nil {
		log.Printf("[mock] Failed to push to compilation stream: %v\n", err)
		db.ExecContext(ctx, "UPDATE submissions SET status = $1, verdict = $2, diagnostics = $3 WHERE id = $4", "failed", "Queue Failure", `{"error":"failed to queue job"}`, buildID)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to queue mock submission")
	}

	log.Printf("[mock] Triggered mock submission %s for %s ✓\n", buildID, contestantID)
	return c.JSON(fiber.Map{
		"build_id": buildID,
		"status":   "queued",
	})
}

func handleCleanDB(c fiber.Ctx) error {
	ctx := context.Background()

	// Truncate PG Table
	_, err := db.ExecContext(ctx, "TRUNCATE TABLE submissions RESTART IDENTITY CASCADE;")
	if err != nil {
		log.Printf("Failed to truncate submissions: %v\n", err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to truncate database submissions")
	}

	// Clear local submissions directory files to prevent disk clutter
	cwd, _ := os.Getwd()
	subsDir := filepath.Join(cwd, "submissions")
	_ = os.RemoveAll(subsDir)
	_ = os.MkdirAll(subsDir, 0777)

	// Flush Redis Streams
	if err := rdb.Del(ctx, common.CompilationQueue, common.PretestQueue, common.SystestQueue).Err(); err != nil {
		log.Printf("Failed to delete Redis streams: %v\n", err)
	}

	// Reinitialize consumer groups/streams
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		log.Printf("Failed to reinitialize Redis streams/groups: %v\n", err)
	}

	log.Println("Database cleaned and Redis streams flushed ✓")
	return c.JSON(fiber.Map{"status": "ok"})
}
