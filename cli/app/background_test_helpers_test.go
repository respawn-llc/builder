package app

import (
	"testing"
	"time"

	shelltool "core/server/tools/shell"
)

const fastBackgroundTestYield = 20 * time.Millisecond

func newFastBackgroundTestManager(t *testing.T) *shelltool.Manager {
	t.Helper()
	manager, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(fastBackgroundTestYield),
		shelltool.WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new background manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}
