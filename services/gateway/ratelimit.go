package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CheckRateLimit checks if the contestant has submitted in the last 60 seconds.
// Returns (allowed, retryAfterSeconds, error).
func CheckRateLimit(ctx context.Context, rdb *redis.Client, contestantID string) (bool, int, error) {
	key := fmt.Sprintf("rate_limit:%s", contestantID)

	// Attempt to set the key only if it does not exist, with a 60-second expiration.
	// This operation is atomic.
	set, err := rdb.SetNX(ctx, key, "1", 60*time.Second).Result()
	if err != nil {
		return false, 0, err
	}

	if set {
		// Key was set successfully, so the submission is allowed
		return true, 0, nil
	}

	// Key already existed. Get the remaining TTL to inform the user when they can retry.
	ttl, err := rdb.TTL(ctx, key).Result()
	if err != nil {
		return false, 0, err
	}

	retryAfter := int(ttl.Seconds())
	if retryAfter <= 0 {
		retryAfter = 1 // Fallback in case of race conditions where key expires between calls
	}

	return false, retryAfter, nil
}
