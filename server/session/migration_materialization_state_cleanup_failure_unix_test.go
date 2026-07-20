//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package session

import (
	"os"
	"testing"
)

func armEventLogWorkspaceCleanupFailure(t *testing.T, workspace string) func() {
	t.Helper()
	if err := os.Chmod(workspace, 0o500); err != nil {
		t.Fatalf("make event-log migration workspace non-writable: %v", err)
	}
	return func() {
		if err := os.Chmod(workspace, 0o700); err != nil {
			t.Errorf("restore event-log migration workspace permissions: %v", err)
		}
	}
}
