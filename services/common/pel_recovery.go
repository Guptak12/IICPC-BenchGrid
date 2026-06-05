package common

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// StartPELRecovery periodically claims messages that have been pending
// in the stream for longer than maxPendingAge and re-delivers them to the group.
func StartPELRecovery(ctx context.Context, rdb *redis.Client, stream, group, consumer string, maxPendingAge time.Duration) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Find messages pending for too long
				pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
					Stream: stream,
					Group:  group,
					Start:  "-",
					End:    "+",
					Count:  10,
				}).Result()
				if err != nil {
					continue
				}
				for _, p := range pending {
					if p.Idle >= maxPendingAge {
						// Claim the message for this consumer
						claimed, err := rdb.XClaim(ctx, &redis.XClaimArgs{
							Stream:   stream,
							Group:    group,
							Consumer: consumer,
							MinIdle:  maxPendingAge,
							Messages: []string{p.ID},
						}).Result()
						if err == nil && len(claimed) > 0 {
							log.Printf("[PEL] Reclaimed stale message %s from stream %s (idle %v)\n", p.ID, stream, p.Idle)
						}
					}
				}
			}
		}
	}()
}
