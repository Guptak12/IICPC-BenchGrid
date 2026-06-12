package common

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// Shared Constants for Redis Queues
const (
	CompilationQueue = "compilation_queue"
	CompilationGroup = "compiler_group"
	PretestQueue     = "pretest_queue"
	PretestGroup     = "pretest_group"
	SystestQueue     = "systest_queue"
	SystestGroup     = "systest_group"
)

// Sandbox Docker image and network names (dynamic via environment)
var (
	SandboxIsolatedNet = GetEnv("SANDBOX_ISOLATED_NET", "sandbox-net") // contestant containers only
)

// Shared Constants for Environment Overrides
const (
	EnvRedisAddr = "REDIS_ADDR"
	EnvDBAddr    = "DB_ADDR"
)

// GetEnv returns the value of the environment variable key, or fallback if unset.
func GetEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// GetRedisClient connects to Redis using environment variable or local default
func GetRedisClient() *redis.Client {
	addr := os.Getenv(EnvRedisAddr)
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	return rdb
}

// GetDB connects to PostgreSQL using environment variable or local default
func GetDB() (*sql.DB, error) {
	connStr := os.Getenv(EnvDBAddr)
	if connStr == "" {
		connStr = "postgres://iicpc:iicpc_secret@localhost:5432/iicpc_db?sslmode=disable"
	}
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}
	// Verify connection
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// InitRedisQueues ensures Redis streams and consumer groups exist
func InitRedisQueues(ctx context.Context, rdb *redis.Client) error {
	queues := []struct {
		stream string
		group  string
	}{
		{CompilationQueue, CompilationGroup},
		{PretestQueue, PretestGroup},
		{SystestQueue, SystestGroup},
	}

	for _, q := range queues {
		// Attempt to create consumer group. MKSTREAM ensures the stream is created too.
		err := rdb.XGroupCreateMkStream(ctx, q.stream, q.group, "$").Err()
		if err != nil {
			// If it already exists, ignore the error
			if err.Error() == "BUSYGROUP Consumer Group name already exists" {
				log.Printf("Redis Stream '%s' with Group '%s' already exists ✓\n", q.stream, q.group)
				continue
			}
			return fmt.Errorf("failed to create group %s on %s: %v", q.group, q.stream, err)
		}
		log.Printf("Created Redis Stream '%s' and Consumer Group '%s' ✓\n", q.stream, q.group)
	}

	return nil
}

// AckAndDel acknowledges a message in the consumer group and deletes it from the stream.
// This cleans up memory and ensures the stream length represents the current pending queue depth.
func AckAndDel(ctx context.Context, rdb *redis.Client, stream, group, msgID string) error {
	if err := rdb.XAck(ctx, stream, group, msgID).Err(); err != nil {
		return err
	}
	return rdb.XDel(ctx, stream, msgID).Err()
}
