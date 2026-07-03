package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
)

func TestScriptedClientRecordsRequestsAndSteps(t *testing.T) {
	client := NewScriptedClient(
		llm.ProviderCapabilities{ProviderID: "legacy"},
		ScriptedFinalAnswer("done"),
		ScriptedToolBatch("tools", llm.ToolCall{ID: "call_1", Name: "exec_command", Input: json.RawMessage(`{"cmd":"true"}`)}),
		ScriptedRuntimeError(ErrScriptedRuntime),
		ScriptedCancellation(),
	)
	if _, err := client.Generate(context.Background(), llm.Request{Model: "m"}); err != nil {
		t.Fatalf("Generate final: %v", err)
	}
	toolResp, err := client.Generate(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Generate tools: %v", err)
	}
	if len(toolResp.ToolCalls) != 1 || toolResp.ToolCalls[0].Name != "exec_command" {
		t.Fatalf("tool response = %+v", toolResp)
	}
	if _, err := client.Generate(context.Background(), llm.Request{Model: "m"}); !errors.Is(err, ErrScriptedRuntime) {
		t.Fatalf("runtime error = %v, want ErrScriptedRuntime", err)
	}
	if _, err := client.Generate(context.Background(), llm.Request{Model: "m"}); !errors.Is(err, context.Canceled) {
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

func TestScriptedClientCancellationReturnsContextErr(t *testing.T) {
	client := NewScriptedClient(llm.ProviderCapabilities{ProviderID: "legacy"}, ScriptedCancellation())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Generate(ctx, llm.Request{Model: "m"}); !errors.Is(err, ctx.Err()) {
		t.Fatalf("Generate error = %v, want %v", err, ctx.Err())
	}
}
