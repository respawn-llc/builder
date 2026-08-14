package architectureguard

import (
	"strings"
	"testing"
)

func TestCheckNoProductionPTYCheckpointProtocolRejectsAppDependency(t *testing.T) {
	root := t.TempDir()
	writeGuardFixture(
		t,
		root,
		"cli/app/bad.go",
		"package app\nimport \"core/internal/testharness/pty\"\nvar _ = pty.TerminalPhase{}\n",
	)

	err := CheckNoProductionPTYCheckpointProtocol(root)
	if err == nil || !strings.Contains(err.Error(), "cli/app/bad.go") {
		t.Fatalf("CheckNoProductionPTYCheckpointProtocol error = %v, want app violation", err)
	}
}

func TestCheckNoProductionPTYCheckpointProtocolRejectsTUILiteral(t *testing.T) {
	root := t.TempDir()
	writeGuardFixture(t, root, "cli/tui/bad.go", "package tui\nconst checkpoint = \"kent-pty-checkpoint\"\n")

	err := CheckNoProductionPTYCheckpointProtocol(root)
	if err == nil || !strings.Contains(err.Error(), "cli/tui/bad.go") {
		t.Fatalf("CheckNoProductionPTYCheckpointProtocol error = %v, want TUI violation", err)
	}
}

func TestCheckNoProductionPTYCheckpointProtocolAllowsTestHarnessAndTests(t *testing.T) {
	root := t.TempDir()
	writeGuardFixture(
		t,
		root,
		"internal/testharness/pty/protocol.go",
		"package pty\nconst TerminalPhaseMarker = \"kent-pty-checkpoint\"\n",
	)
	writeGuardFixture(
		t,
		root,
		"cli/app/allowed_test.go",
		"package app\nconst TerminalPhaseMarker = \"kent-pty-checkpoint\"\n",
	)

	if err := CheckNoProductionPTYCheckpointProtocol(root); err != nil {
		t.Fatalf("CheckNoProductionPTYCheckpointProtocol: %v", err)
	}
}
