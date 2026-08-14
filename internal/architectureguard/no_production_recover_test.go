package architectureguard

import (
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

func TestCheckNoProductionRecoverRejectsFilesOutsideHostBuildContext(t *testing.T) {
	tests := []struct {
		name string
		path string
		code string
	}{
		{
			name: "GOOS filename",
			path: "bad_windows.go",
			code: "package fixture\nfunc bad() { recover() }\n",
		},
		{
			name: "GOARCH filename",
			path: "bad_amd64.go",
			code: "package fixture\nfunc bad() { recover() }\n",
		},
		{
			name: "build constraint",
			path: "bad.go",
			code: "//go:build architecture_guard_fixture\n\npackage fixture\nfunc bad() { recover() }\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeGuardFixture(t, root, test.path, test.code)
			if err := CheckNoProductionRecover(root); err == nil {
				t.Fatal("production file outside the host build context was not inspected")
			}
		})
	}
}
