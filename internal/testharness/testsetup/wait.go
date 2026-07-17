package testsetup

import (
	"testing"
	"time"
)

func Until(deadline time.Time, interval time.Duration, ready func() bool) bool {
	for time.Now().Before(deadline) {
		if ready() {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

func RequireUntil(t testing.TB, deadline time.Time, interval time.Duration, ready func() bool, failureFormat string, failureArgs ...any) {
	t.Helper()
	if !Until(deadline, interval, ready) {
		t.Fatalf(failureFormat, failureArgs...)
	}
}
