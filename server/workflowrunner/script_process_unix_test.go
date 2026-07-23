//go:build !windows

package workflowrunner

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func assertScriptProcessGroupGone(t *testing.T, processGroupID int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-processGroupID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect process group %d after script completion: %v", processGroupID, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("process group %d still exists after script completion: %v", processGroupID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
