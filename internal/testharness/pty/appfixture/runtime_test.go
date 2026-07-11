package appfixture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestRuntimeDerivesTargetFinalAssistantOrdinalFromCompiledScript(t *testing.T) {
	var configuredTarget *ScriptFinalAssistantOrdinal
	runtime, err := NewRuntime(
		writeRuntimeScriptFile(t, ScriptFile{
			Steps: []StepFile{
				{Final: "first final"},
				{ToolCalls: []ToolCallFile{{
					ID:    uuid.NewString(),
					Name:  "exec_command",
					Input: json.RawMessage(`{"cmd":"true"}`),
				}}},
				{Final: "target final"},
			},
		}),
		func(target ScriptFinalAssistantOrdinal) func(context.Context) error {
			targetCopy := target
			configuredTarget = &targetCopy
			return nil
		},
	)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	want := ScriptFinalAssistantOrdinal(2)
	if configuredTarget == nil || *configuredTarget != want {
		t.Fatalf("configured target = %v, want %d", configuredTarget, want)
	}
	if got := runtime.TargetFinalAssistantOrdinal(); got != want {
		t.Fatalf("runtime target final assistant ordinal = %d, want %d", got, want)
	}
}

func TestRuntimeRejectsScriptWithoutAssistantFinal(t *testing.T) {
	_, err := NewRuntime(
		writeRuntimeScriptFile(t, ScriptFile{
			Steps: []StepFile{{
				ToolCalls: []ToolCallFile{{
					ID:    uuid.NewString(),
					Name:  "exec_command",
					Input: json.RawMessage(`{"cmd":"true"}`),
				}},
			}},
		}),
		nil,
	)
	if err == nil {
		t.Fatal("runtime accepted a script without an assistant final")
	}
}

func TestRuntimeRejectsScriptThatDoesNotEndWithAssistantFinal(t *testing.T) {
	_, err := NewRuntime(
		writeRuntimeScriptFile(t, ScriptFile{
			Steps: []StepFile{
				{Final: "earlier final"},
				{ToolCalls: []ToolCallFile{{
					ID:    uuid.NewString(),
					Name:  "exec_command",
					Input: json.RawMessage(`{"cmd":"true"}`),
				}}},
			},
		}),
		nil,
	)
	if err == nil {
		t.Fatal("runtime accepted a script whose final step is not an assistant final")
	}
}

func writeRuntimeScriptFile(t *testing.T, script ScriptFile) string {
	t.Helper()
	encoded, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal runtime script: %v", err)
	}
	path := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write runtime script: %v", err)
	}
	return path
}
