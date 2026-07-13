package testsetup

import (
	"os"
	"path/filepath"
	"testing"
)

func WriteExecutable(t testing.TB, name string, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write executable test file: %v", err)
	}
	return path
}
