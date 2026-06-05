package common

import (
	"context"
	"log"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const (
	MaxRetries      = 3
	DeadLetterQueue = "dead_letter_queue"
)

// ShouldRetry checks the retry count, and either re-queues or dead-letters the message.
// It returns true if the message has been re-queued (and will be retried later),
// or false if the max retries was exceeded and the message was moved to the DLQ.
func ShouldRetry(ctx context.Context, rdb *redis.Client, stream, group, msgID string,
	values map[string]interface{}, err error) bool {

	retryStr, _ := values["retry_count"].(string)
	retryCount, _ := strconv.Atoi(retryStr)

	if retryCount >= MaxRetries {
		// Move to dead-letter queue
		values["original_stream"] = stream
		values["failure_reason"] = err.Error()
		rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: DeadLetterQueue,
			Values: values,
		})
		rdb.XAck(ctx, stream, group, msgID)
		log.Printf("[DLQ] Message %s moved to dead letter after %d retries\n", msgID, retryCount)
		return false
	}

	// Re-queue with incremented retry count
	values["retry_count"] = strconv.Itoa(retryCount + 1)
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: values,
	})
	rdb.XAck(ctx, stream, group, msgID)
	log.Printf("[retry] Message %s re-queued (attempt %d/%d)\n", msgID, retryCount+1, MaxRetries)
	return true
}
