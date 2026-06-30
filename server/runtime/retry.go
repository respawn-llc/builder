package runtime

import (
	"context"
	"time"
)

var generateRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
var idleStallRetryDelays = []time.Duration{1 * time.Second}
var compactionRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

func waitForRetryDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
