package scriptedllm_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
)

func TestClientStreamsDeltasFinalResponseAndReasoning(t *testing.T) {
	client := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{{
			StreamDeltas:    []llm.AssistantDelta{{Text: "hel", Phase: llm.MessagePhaseCommentary}, {Text: "lo", Phase: llm.MessagePhaseFinal}},
			ReasoningDeltas: []llm.ReasoningSummaryDelta{{Key: "r", Role: "assistant", Text: "because"}},
			Response:        scriptedllm.FinalAnswer("hello").Response,
		}},
	})

	var assistant []llm.AssistantDelta
	var reasoning []llm.ReasoningSummaryDelta
	resp, err := client.GenerateStreamWithEvents(context.Background(), llm.Request{Model: "m"}, llm.StreamCallbacks{
		OnAssistantDelta:        func(delta llm.AssistantDelta) { assistant = append(assistant, delta) },
		OnReasoningSummaryDelta: func(delta llm.ReasoningSummaryDelta) { reasoning = append(reasoning, delta) },
	})
	if err != nil {
		t.Fatalf("GenerateStreamWithEvents: %v", err)
	}
	if resp.Assistant.Content != "hello" {
		t.Fatalf("final assistant = %q, want %q", resp.Assistant.Content, "hello")
	}
	if len(assistant) != 2 || assistant[0].Text != "hel" || assistant[1].Text != "lo" {
		t.Fatalf("assistant deltas = %#v", assistant)
	}
	if len(reasoning) != 1 || reasoning[0].Text != "because" {
		t.Fatalf("reasoning deltas = %#v", reasoning)
	}
}

func TestClientValidatesExpectedToolResultAndReturnsToolCall(t *testing.T) {
	input := json.RawMessage(`{"cmd":"true"}`)
	client := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{scriptedllm.ToolBatch("tools", llm.ToolCall{ID: "call_1", Name: "exec_command", Input: input})},
	})
	resp, err := client.Generate(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Generate tool call: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}

	validator := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{{
			ExpectedToolResults: []scriptedllm.ExpectedToolResult{{CallID: "call_1", Name: "exec_command"}},
			Response:            scriptedllm.FinalAnswer("done").Response,
		}},
	})
	if _, err := validator.Generate(context.Background(), llm.Request{Model: "m"}); !errors.Is(err, scriptedllm.ErrUnexpectedToolResult) {
		t.Fatalf("missing tool result error = %v, want ErrUnexpectedToolResult", err)
	}
	successValidator := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{{
			ExpectedToolResults: []scriptedllm.ExpectedToolResult{{CallID: "call_1", Name: "exec_command"}},
			Response:            scriptedllm.FinalAnswer("done").Response,
		}},
	})
	_, err = successValidator.Generate(context.Background(), llm.Request{
		Model: "m",
		Items: []llm.ResponseItem{{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: "call_1", Name: "exec_command"}},
	})
	if err != nil {
		t.Fatalf("Generate with expected tool result: %v", err)
	}
}

func TestClientCompactionCapabilitiesTokensAndContextWindow(t *testing.T) {
	tokens := 42
	window := 128000
	trimmed := 3
	caps := llm.ProviderCapabilities{ProviderID: "scripted"}
	client := scriptedllm.NewClient(scriptedllm.Script{
		Capabilities:        &caps,
		InputTokenCount:     &tokens,
		ContextWindowTokens: &window,
		Compactions: []llm.CompactionResponse{{
			OutputItems:       []llm.ResponseItem{{Type: llm.ResponseItemTypeCompaction, Content: "summary"}},
			TrimmedItemsCount: &trimmed,
		}},
	})

	caps, err := client.ProviderCapabilities(context.Background())
	if err != nil || caps.ProviderID != "scripted" {
		t.Fatalf("ProviderCapabilities = %+v, %v", caps, err)
	}
	count, err := client.CountRequestInputTokens(context.Background(), llm.Request{Model: "m"})
	if err != nil || count != tokens {
		t.Fatalf("CountRequestInputTokens = %d, %v", count, err)
	}
	supported, err := client.SupportsRequestInputTokenCount(context.Background())
	if err != nil || !supported {
		t.Fatalf("SupportsRequestInputTokenCount = %v, %v", supported, err)
	}
	resolved, err := client.ResolveModelContextWindow(context.Background(), "m")
	if err != nil || resolved != window {
		t.Fatalf("ResolveModelContextWindow = %d, %v", resolved, err)
	}
	compaction, err := client.Compact(context.Background(), llm.CompactionRequest{Model: "m"})
	if err != nil || compaction.TrimmedItemsCount == nil || *compaction.TrimmedItemsCount != 3 {
		t.Fatalf("Compact = %+v, %v", compaction, err)
	}
}

func TestClientCompactionPreservesReportedZeroAndUnavailableCount(t *testing.T) {
	zero := 0
	client := scriptedllm.NewClient(scriptedllm.Script{
		Compactions: []llm.CompactionResponse{
			{TrimmedItemsCount: &zero},
			{TrimmedItemsCount: nil},
		},
	})

	reported, err := client.Compact(context.Background(), llm.CompactionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Compact reported zero: %v", err)
	}
	if reported.TrimmedItemsCount == nil || *reported.TrimmedItemsCount != 0 {
		t.Fatalf("reported trimmed count = %#v, want explicit zero", reported.TrimmedItemsCount)
	}

	unavailable, err := client.Compact(context.Background(), llm.CompactionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Compact unavailable: %v", err)
	}
	if unavailable.TrimmedItemsCount != nil {
		t.Fatalf("unavailable trimmed count = %#v, want nil", unavailable.TrimmedItemsCount)
	}
}

func TestClientExhaustedCancellationAndConcurrentCallErrors(t *testing.T) {
	client := scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{scriptedllm.Cancellation()}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Generate(ctx, llm.Request{Model: "m"}); !errors.Is(err, ctx.Err()) {
		t.Fatalf("cancellation error = %v, want %v", err, ctx.Err())
	}
	if _, err := client.Generate(context.Background(), llm.Request{Model: "m"}); !errors.Is(err, scriptedllm.ErrScriptExhausted) {
		t.Fatalf("exhausted error = %v, want ErrScriptExhausted", err)
	}

	block := make(chan struct{})
	concurrent := scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{
		{BeforeResponse: func(context.Context) error { <-block; return nil }, Response: scriptedllm.FinalAnswer("one").Response},
		{Response: scriptedllm.FinalAnswer("two").Response},
	}})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = concurrent.Generate(context.Background(), llm.Request{Model: "m"})
	}()
	if err := concurrent.WaitUntilActive(context.Background()); err != nil {
		t.Fatalf("WaitUntilActive: %v", err)
	}
	if _, err := concurrent.Generate(context.Background(), llm.Request{Model: "m"}); !errors.Is(err, scriptedllm.ErrConcurrentCall) {
		t.Fatalf("concurrent call error = %v, want ErrConcurrentCall", err)
	}
	close(block)
	wg.Wait()
}
