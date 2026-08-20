//go:build darwin || linux

package testprocess

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func WaitForExit(t testing.TB, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe process %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive after %v", pid, timeout)
}
