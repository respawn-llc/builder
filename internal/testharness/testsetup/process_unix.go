//go:build aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package testsetup

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func ProcessGone(t testing.TB, pid int) bool {
	t.Helper()
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		t.Fatalf("inspect process %d: %v", pid, err)
	}
	return false
}

func RequireProcessGone(t testing.TB, deadline time.Time, pid int) {
	t.Helper()
	RequireUntil(t, deadline, 10*time.Millisecond, func() bool {
		return ProcessGone(t, pid)
	}, "process %d remained observable", pid)
}

func RequireProcessGoneNow(t testing.TB, pid int) {
	t.Helper()
	if !ProcessGone(t, pid) {
		t.Fatalf("process %d remained observable", pid)
	}
}
