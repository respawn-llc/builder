// Package filemode provides test-only filesystem permission assertions.
package filemode

import (
	"os"
	"runtime"
	"testing"
)

// AssertUnixPermissionMode asserts the complete Unix permission mode at path.
// Windows does not represent Unix permission bits, so the assertion is skipped there.
func AssertUnixPermissionMode(t testing.TB, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}
