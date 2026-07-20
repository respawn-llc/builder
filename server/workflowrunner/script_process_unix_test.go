//go:build aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package workflowrunner

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func assertScriptProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect process %d after script completion: %v", pid, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("process %d still exists after script completion: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertScriptProcessAlive(t *testing.T, pid int) {
	t.Helper()
	err := syscall.Kill(pid, 0)
	if err != nil {
		t.Fatalf("inspect process %d during cancellation grace: %v", pid, err)
	}
}
