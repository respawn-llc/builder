package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/workflowruntime"
	"core/shared/config"
)

func TestScriptedClientRecordsRequestsAndSteps(t *testing.T) {
	client := NewScriptedClient(
		llm.ProviderCapabilities{ProviderID: "legacy"},
		ScriptedFinalAnswer("done"),
		ScriptedToolBatch("tools", llm.ToolCall{ID: "call_1", Name: "exec_command", Input: json.RawMessage(`{"cmd":"true"}`)}),
		ScriptedRuntimeError(ErrScriptedRuntime),
		ScriptedCancellation(),
	)
	if _, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); err != nil {
		t.Fatalf("Generate final: %v", err)
	}
	toolResp, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{})
	if err != nil {
		t.Fatalf("Generate tools: %v", err)
	}
	if len(toolResp.ToolCalls) != 1 || toolResp.ToolCalls[0].Name != "exec_command" {
		t.Fatalf("tool response = %+v", toolResp)
	}
	if _, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, ErrScriptedRuntime) {
		t.Fatalf("runtime error = %v, want ErrScriptedRuntime", err)
	}
	if _, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	if got := len(client.Requests()); got != 4 {
		t.Fatalf("request count = %d, want 4", got)
	}
}

func TestScriptedClientProviderCapabilitiesDefault(t *testing.T) {
	client := NewDefaultScriptedClient()

	caps, err := client.ProviderCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ProviderCapabilities: %v", err)
	}
	if caps.ProviderID != "openai" || !caps.SupportsResponsesAPI || !caps.IsOpenAIFirstParty {
		t.Fatalf("caps = %+v, want openai defaults", caps)
	}
}

func TestForcedShellCompletionRequiresExecCommand(t *testing.T) {
	_, err := workflowruntime.SelectCompletionMode(workflowruntime.CompletionModeSelection{
		ConfiguredMode: config.WorkflowCompletionModeShellCommand,
	})
	if !errors.Is(err, workflowruntime.ErrShellCompletionUnavailable) {
		t.Fatalf("SelectCompletionMode error = %v, want unavailable shell completion", err)
	}
}

func TestScriptedClientCancellationReturnsContextErr(t *testing.T) {
	client := NewScriptedClient(llm.ProviderCapabilities{ProviderID: "legacy"}, ScriptedCancellation())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Generate(ctx, llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, ctx.Err()) {
		t.Fatalf("Generate error = %v, want %v", err, ctx.Err())
	}
}
