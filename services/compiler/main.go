package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/signal"
	"syscall"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/moby/moby/client"
	"github.com/redis/go-redis/v9"
)

var (
	rdb          *redis.Client
	db           *sql.DB
	dockerClient *client.Client
	s3Client     *minio.Client
)

func main() {
	// Connect to Docker
	var err error
	dockerClient, err = client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Fatalf("Failed to initialize Docker client: %v", err)
	}
	defer dockerClient.Close()

	// Connect to Redis
	rdb = common.GetRedisClient()
	defer rdb.Close()

	// Connect to S3/MinIO
	ctx := context.Background()
	s3Client, err = common.GetS3Client()
	if err != nil {
		log.Fatalf("Failed to initialize S3 client: %v", err)
	}
	if err := common.EnsureS3Bucket(ctx, s3Client); err != nil {
		log.Fatalf("Failed to ensure S3 bucket: %v", err)
	}

	// Connect to PostgreSQL
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

	// Initialize queues
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		log.Fatalf("Redis Stream initialization failed: %v", err)
	}

	consumerName := "compiler-" + uuid.New().String()[:8]
	log.Printf("Compilation Worker %s started, listening on queue... ✓\n", consumerName)

	// Trap shutdown signals
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start PEL recovery for compiler group (claim messages idle > 2 minutes)
	common.StartPELRecovery(shutdownCtx, rdb, common.CompilationQueue, common.CompilationGroup, consumerName, 2*time.Minute)

	for {
		// Read new messages from group
		streams, err := rdb.XReadGroup(shutdownCtx, &redis.XReadGroupArgs{
			Group:    common.CompilationGroup,
			Consumer: consumerName,
			Streams:  []string{common.CompilationQueue, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()

		if shutdownCtx.Err() != nil {
			log.Println("Shutdown signal received, compiler worker shutting down...")
			return
		}

		if err == redis.Nil {
			continue
		} else if err != nil {
			log.Printf("Error reading from stream: %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, message := range stream.Messages {
				processMessage(shutdownCtx, message)
			}
		}
	}
}

func processMessage(ctx context.Context, message redis.XMessage) {
	submissionID, ok1 := message.Values["submission_id"].(string)
	s3Path, ok2 := message.Values["s3_path"].(string)
	contestantID, _ := message.Values["contestant_id"].(string)

	if !ok1 || !ok2 {
		log.Printf("Skipping invalid stream message: %v\n", message.ID)
		rdb.XAck(ctx, common.CompilationQueue, common.CompilationGroup, message.ID)
		return
	}

	log.Printf("[submission:%s] Starting compilation...\n", submissionID[:8])

	// 1. Update PostgreSQL status to 'compiling'
	_, err := db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, updated_at = NOW() WHERE id = $2",
		"compiling", submissionID,
	)
	if err != nil {
		log.Printf("Failed to update status to compiling: %v\n", err)
	}

	// 2. Download source from S3
	obj, err := s3Client.GetObject(ctx, common.S3Bucket, s3Path, minio.GetObjectOptions{})
	if err != nil {
		log.Printf("[submission:%s] Failed to get source from S3: %v\n", submissionID[:8], err)
		if common.ShouldRetry(ctx, rdb, common.CompilationQueue, common.CompilationGroup, message.ID, message.Values, err) {
			return
		}
		failSubmission(ctx, submissionID, "System Failure", "Failed to retrieve source code from storage: "+err.Error())
		return
	}
	defer obj.Close()
	srcBytes, err := io.ReadAll(obj)
	if err != nil {
		log.Printf("[submission:%s] Failed to read source from S3 object: %v\n", submissionID[:8], err)
		if common.ShouldRetry(ctx, rdb, common.CompilationQueue, common.CompilationGroup, message.ID, message.Values, err) {
			return
		}
		failSubmission(ctx, submissionID, "System Failure", "Failed to read source code: "+err.Error())
		return
	}

	// 3. Perform compilation
	success, stderr, binaryBytes, err := CompileCode(ctx, dockerClient, srcBytes)
	if err != nil {
		log.Printf("[submission:%s] System error during compilation: %v\n", submissionID[:8], err)
		if common.ShouldRetry(ctx, rdb, common.CompilationQueue, common.CompilationGroup, message.ID, message.Values, err) {
			return
		}
		failSubmission(ctx, submissionID, "Compilation Failure", "Internal compilation agent error: "+err.Error())
		return
	}

	if !success {
		log.Printf("[submission:%s] Compilation failed\n", submissionID[:8])
		failSubmission(ctx, submissionID, "Compilation Error", stderr)
		rdb.XAck(ctx, common.CompilationQueue, common.CompilationGroup, message.ID)
		return
	}

	// 4. Upload binary to S3
	binaryPath := fmt.Sprintf("submissions/%s/app", submissionID)
	_, err = s3Client.PutObject(ctx, common.S3Bucket, binaryPath, bytes.NewReader(binaryBytes), int64(len(binaryBytes)), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		log.Printf("[submission:%s] Failed to upload binary to S3: %v\n", submissionID[:8], err)
		if common.ShouldRetry(ctx, rdb, common.CompilationQueue, common.CompilationGroup, message.ID, message.Values, err) {
			return
		}
		failSubmission(ctx, submissionID, "System Failure", "Failed to store compiled binary: "+err.Error())
		return
	}

	log.Printf("[submission:%s] Compilation succeeded ✓\n", submissionID[:8])

	// 5. Update PostgreSQL status to 'compiled'
	_, err = db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, updated_at = NOW() WHERE id = $2",
		"compiled", submissionID,
	)
	if err != nil {
		log.Printf("Failed to update status to compiled: %v\n", err)
	}

	// 6. Push job to pretest queue via XADD
	err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: common.PretestQueue,
		Values: map[string]interface{}{
			"submission_id": submissionID,
			"binary_path":   binaryPath,
			"contestant_id": contestantID,
		},
	}).Err()
	if err != nil {
		log.Printf("[submission:%s] Failed to queue pretest job: %v\n", submissionID[:8], err)
		failSubmission(ctx, submissionID, "Queue Failure", "Failed to queue for pretests: "+err.Error())
	} else {
		// Acknowledge the compilation message in the group
		rdb.XAck(ctx, common.CompilationQueue, common.CompilationGroup, message.ID)
	}
}

func failSubmission(ctx context.Context, submissionID, verdict, stderr string) {
	diag := map[string]string{
		"error": stderr,
	}
	diagBytes, _ := json.Marshal(diag)

	_, err := db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, verdict = $2, diagnostics = $3, updated_at = NOW() WHERE id = $4",
		"failed", verdict, diagBytes, submissionID,
	)
	if err != nil {
		log.Printf("Failed to mark submission as failed: %v\n", err)
	}
}
