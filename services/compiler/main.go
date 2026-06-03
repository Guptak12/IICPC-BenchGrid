package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/redis/go-redis/v9"
)

var (
	rdb          *redis.Client
	db           *sql.DB
	dockerClient *client.Client
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
	ctx := context.Background()
	if err := common.InitRedisQueues(ctx, rdb); err != nil {
		log.Fatalf("Redis Stream initialization failed: %v", err)
	}

	consumerName := "compiler-" + uuid.New().String()[:8]
	log.Printf("Compilation Worker %s started, listening on queue... ✓\n", consumerName)

	for {
		// Read new messages from group
		streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    common.CompilationGroup,
			Consumer: consumerName,
			Streams:  []string{common.CompilationQueue, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()

		if err == redis.Nil {
			// No new messages
			continue
		} else if err != nil {
			log.Printf("Error reading from stream: %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, message := range stream.Messages {
				processMessage(ctx, message)
			}
		}
	}
}

func processMessage(ctx context.Context, message redis.XMessage) {
	submissionID, ok1 := message.Values["submission_id"].(string)
	sourceCode, ok2 := message.Values["source_code"].(string)
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

	// 2. Perform compilation
	success, stderr, binaryBytes, err := CompileCode(ctx, dockerClient, []byte(sourceCode))
	if err != nil {
		log.Printf("[submission:%s] System error during compilation: %v\n", submissionID[:8], err)
		failSubmission(ctx, submissionID, "Compilation Failure", "Internal compilation agent error: "+err.Error())
		rdb.XAck(ctx, common.CompilationQueue, common.CompilationGroup, message.ID)
		return
	}

	if !success {
		log.Printf("[submission:%s] Compilation failed\n", submissionID[:8])
		failSubmission(ctx, submissionID, "Compilation Error", stderr)
		rdb.XAck(ctx, common.CompilationQueue, common.CompilationGroup, message.ID)
		return
	}

	// Encode compiled binary in base64
	base64Binary := base64.StdEncoding.EncodeToString(binaryBytes)

	log.Printf("[submission:%s] Compilation succeeded ✓\n", submissionID[:8])

	// 3. Update PostgreSQL status to 'compiled'
	_, err = db.ExecContext(ctx,
		"UPDATE submissions SET status = $1, updated_at = NOW() WHERE id = $2",
		"compiled", submissionID,
	)
	if err != nil {
		log.Printf("Failed to update status to compiled: %v\n", err)
	}

	// 4. Push job to pretest queue via XADD
	err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: common.PretestQueue,
		Values: map[string]interface{}{
			"submission_id": submissionID,
			"binary_data":   base64Binary,
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
