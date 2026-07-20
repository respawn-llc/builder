package appfixture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"core/server/llm"

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

func TestRuntimeMaterializesTokenPressureAndCompactionScript(t *testing.T) {
	inputTokens := 900
	contextWindow := 1000
	runtime, err := NewRuntime(
		writeRuntimeScriptFile(t, ScriptFile{
			Final:               "done",
			InputTokenCount:     &inputTokens,
			ContextWindowTokens: &contextWindow,
			Compactions: []CompactionFile{{
				Summary:           "compacted context",
				TrimmedItemsCount: 2,
				InputTokensAfter:  100,
			}},
		}),
		nil,
	)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if got, err := runtime.Client.CountRequestInputTokens(context.Background(), llm.Request{}); err != nil || got != inputTokens {
		t.Fatalf("input token count = %d, %v; want %d", got, err, inputTokens)
	}
	if got, err := runtime.Client.ResolveModelContextWindow(context.Background(), "gpt-5"); err != nil || got != contextWindow {
		t.Fatalf("context window = %d, %v; want %d", got, err, contextWindow)
	}
	compacted, err := runtime.Client.Compact(context.Background(), llm.CompactionRequest{})
	if err != nil {
		t.Fatalf("compact scripted runtime: %v", err)
	}
	if len(compacted.OutputItems) != 2 ||
		compacted.OutputItems[0].Type != llm.ResponseItemTypeMessage ||
		compacted.OutputItems[0].Role != llm.RoleUser ||
		compacted.OutputItems[0].Content != "compacted context" ||
		compacted.OutputItems[1].Type != llm.ResponseItemTypeCompaction ||
		compacted.OutputItems[1].EncryptedContent == "" ||
		compacted.TrimmedItemsCount == nil || *compacted.TrimmedItemsCount != 2 {
		t.Fatalf("scripted compaction = %+v", compacted)
	}
	if got, err := runtime.Client.CountRequestInputTokens(context.Background(), llm.Request{}); err != nil || got != 100 {
		t.Fatalf("post-compaction input token count = %d, %v; want 100", got, err)
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
