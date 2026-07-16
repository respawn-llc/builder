package app

import (
	"context"
	"time"
)

const subscriptionRetryDelay = 250 * time.Millisecond

func waitSubscriptionRetry(ctx context.Context) bool {
	timer := time.NewTimer(subscriptionRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
