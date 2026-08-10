package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/runtimecommand"
)

func blockRuntimeEventAdmission(t *testing.T, queue *runtimecommand.Queue) func() {
	t.Helper()
	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseBlocker)
		})
	}
	if _, err := runtimecommand.Submit(context.Background(), queue, struct{}{}, func(
		_ runtimecommand.Admission,
		_ struct{},
		complete func(struct{}, error),
	) error {
		close(blockerStarted)
		<-releaseBlocker
		complete(struct{}{}, nil)
		return nil
	}); err != nil {
		t.Fatalf("submit Runtime Event blocker: %v", err)
	}
	select {
	case <-blockerStarted:
	case <-time.After(3 * time.Second):
		release()
		t.Fatal("Runtime Event blocker did not start")
	}
	return release
}
