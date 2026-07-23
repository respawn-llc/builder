package runtimeactivity

import (
	"testing"

	"core/server/runtime"
	"core/shared/clientui"
)

func TestClientActiveKindFromRuntimeMapsEveryRuntimeKind(t *testing.T) {
	tests := map[runtime.ActiveKind]clientui.RuntimeActivityActiveKind{
		runtime.ActiveKindUserTurn:            clientui.RuntimeActivityActiveKindUserTurn,
		runtime.ActiveKindWorkflowTurn:        clientui.RuntimeActivityActiveKindWorkflowTurn,
		runtime.ActiveKindGoalLoop:            clientui.RuntimeActivityActiveKindGoalLoop,
		runtime.ActiveKindCompaction:          clientui.RuntimeActivityActiveKindCompaction,
		runtime.ActiveKindPreSubmitCompaction: clientui.RuntimeActivityActiveKindPreSubmitCompaction,
		runtime.ActiveKindUserShell:           clientui.RuntimeActivityActiveKindUserShell,
		runtime.ActiveKindBackground:          clientui.RuntimeActivityActiveKindBackground,
		runtime.ActiveKindRuntimeMaintenance:  clientui.RuntimeActivityActiveKindRuntimeMaintenance,
	}
	for kind, want := range tests {
		got, err := ClientActiveKindFromRuntime(kind)
		if err != nil {
			t.Fatalf("ClientActiveKindFromRuntime(%q): %v", kind, err)
		}
		if got != want {
			t.Fatalf("ClientActiveKindFromRuntime(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestClientActiveKindFromRuntimeRejectsUnknownKind(t *testing.T) {
	if _, err := ClientActiveKindFromRuntime(runtime.ActiveKind("new_kind")); err == nil {
		t.Fatal("expected unknown runtime active kind to fail")
	}
}
