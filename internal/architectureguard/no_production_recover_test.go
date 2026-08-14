package architectureguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckNoProductionRecoverRejectsProductionCall(t *testing.T) {
	root := t.TempDir()
	writeGuardFixture(t, root, "bad.go", "package fixture\nfunc bad() { recover() }\n")

	err := CheckNoProductionRecover(root)
	if err == nil || !strings.Contains(err.Error(), "bad.go:2") {
		t.Fatalf("CheckNoProductionRecover error = %v, want bad.go violation", err)
	}
}

func TestCheckNoProductionRecoverAllowsTestCall(t *testing.T) {
	root := t.TempDir()
	writeGuardFixture(t, root, "allowed_test.go", "package fixture\nfunc allowed() { recover() }\n")

	if err := CheckNoProductionRecover(root); err != nil {
		t.Fatalf("CheckNoProductionRecover: %v", err)
	}
}

func writeGuardFixture(t *testing.T, root string, name string, content string) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
