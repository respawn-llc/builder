//go:build windows

package testsetup

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func ProcessGone(t testing.TB, pid int) bool {
	t.Helper()
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatalf("close inspected process handle: %v", err)
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
