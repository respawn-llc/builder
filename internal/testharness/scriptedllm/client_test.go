package scriptedllm_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/shared/textutil"
)

func TestClientStreamsDeltasFinalResponseAndReasoning(t *testing.T) {
	client := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{{
			StreamDeltas: []llm.AssistantDelta{{Text: "hel", Phase: llm.MessagePhaseCommentary}, {Text: "lo", Phase: llm.MessagePhaseFinal}},
			ReasoningDeltas: []llm.ReasoningSummaryDelta{{
				SourceCoordinate: &llm.ReasoningSourceCoordinate{
					OutputIndex: func() *int64 { value := int64(0); return &value }(),
					PartIndex:   func() *int64 { value := int64(0); return &value }(),
				},
				Role: "assistant", Text: "because",
			}},
			Response: scriptedllm.FinalAnswer("hello").Response,
		}},
	})

	var assistant []llm.AssistantDelta
	var reasoning []llm.ReasoningSummaryDelta
	resp, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{
		OnAssistantDelta:        func(delta llm.AssistantDelta) { assistant = append(assistant, delta) },
		OnReasoningSummaryDelta: func(delta llm.ReasoningSummaryDelta) { reasoning = append(reasoning, delta) },
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Assistant.Content == nil || *resp.Assistant.Content != "hello" {
		t.Fatalf("final assistant = %#v, want %q", resp.Assistant.Content, "hello")
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
	resp, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{})
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
	if _, err := validator.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, scriptedllm.ErrUnexpectedToolResult) {
		t.Fatalf("missing tool result error = %v, want ErrUnexpectedToolResult", err)
	}
	successValidator := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{{
			ExpectedToolResults: []scriptedllm.ExpectedToolResult{{CallID: "call_1", Name: "exec_command"}},
			Response:            scriptedllm.FinalAnswer("done").Response,
		}},
	})
	_, err = successValidator.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Model: "m",
		Items: []llm.ResponseItem{{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call_1"), Name: textutil.Value("exec_command")}},
	}, llm.StreamCallbacks{})
	if err != nil {
		t.Fatalf("Generate with expected tool result: %v", err)
	}
}

func TestClientCompactionCapabilitiesAndContextWindow(t *testing.T) {
	window := 128000
	caps := llm.ProviderCapabilities{ProviderID: "scripted"}
	client := scriptedllm.NewClient(scriptedllm.Script{
		Capabilities:        &caps,
		ContextWindowTokens: &window,
		Compactions: []llm.CompactionResponse{{
			Checkpoint: llm.ResponseItem{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("encrypted")},
		}},
	})

	caps, err := client.ProviderCapabilities(context.Background())
	if err != nil || caps.ProviderID != "scripted" {
		t.Fatalf("ProviderCapabilities = %+v, %v", caps, err)
	}
	resolved, err := client.ResolveModelContextWindow(context.Background(), "m")
	if err != nil || resolved != window {
		t.Fatalf("ResolveModelContextWindow = %d, %v", resolved, err)
	}
	compaction, err := client.Compact(context.Background(), llm.CompactionRequest{Model: "m"})
	if err != nil || compaction.Checkpoint.Type != llm.ResponseItemTypeCompaction {
		t.Fatalf("Compact = %+v, %v", compaction, err)
	}
}

func TestClientExhaustedCancellationAndConcurrentCallErrors(t *testing.T) {
	client := scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{scriptedllm.Cancellation()}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Generate(ctx, llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, ctx.Err()) {
		t.Fatalf("cancellation error = %v, want %v", err, ctx.Err())
	}
	if _, err := client.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, scriptedllm.ErrScriptExhausted) {
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
		_, _ = concurrent.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{})
	}()
	if err := concurrent.WaitUntilActive(context.Background()); err != nil {
		t.Fatalf("WaitUntilActive: %v", err)
	}
	if _, err := concurrent.Generate(context.Background(), llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}, llm.StreamCallbacks{}); !errors.Is(err, scriptedllm.ErrConcurrentCall) {
		t.Fatalf("concurrent call error = %v, want ErrConcurrentCall", err)
	}
	close(block)
	wg.Wait()
}

func TestClientGenerationOutcomeReportsStepAdmission(t *testing.T) {
	block := make(chan struct{})
	client := scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{
		{BeforeResponse: func(context.Context) error { <-block; return errors.New("admitted failure") }},
		scriptedllm.FinalAnswer("unused"),
	}})
	admitted := make(chan scriptedllm.GenerationOutcome, 1)
	go func() {
		outcome, _ := client.GenerateOutcome(
			context.Background(),
			llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"},
			llm.StreamCallbacks{},
		)
		admitted <- outcome
	}()
	if err := client.WaitUntilActive(context.Background()); err != nil {
		t.Fatalf("WaitUntilActive: %v", err)
	}

	rejected, err := client.GenerateOutcome(
		context.Background(),
		llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"},
		llm.StreamCallbacks{},
	)
	if !errors.Is(err, scriptedllm.ErrConcurrentCall) {
		t.Fatalf("concurrent error = %v, want ErrConcurrentCall", err)
	}
	if rejected.Admission != scriptedllm.RequestNotAdmitted {
		t.Fatalf("concurrent admission = %v, want not admitted", rejected.Admission)
	}

	close(block)
	if outcome := <-admitted; outcome.Admission != scriptedllm.RequestAdmitted {
		t.Fatalf("consumed failing step admission = %v, want admitted", outcome.Admission)
	}
}

func TestClientGenerationOutcomeAdmitsEveryConsumedFailure(t *testing.T) {
	declared := errors.New("declared")
	cases := []struct {
		name string
		step scriptedllm.Step
		req  llm.Request
	}{
		{name: "validation", step: scriptedllm.Step{
			ExpectedToolResults: []scriptedllm.ExpectedToolResult{{CallID: "missing", Name: "exec_command"}},
		}, req: llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}},
		{name: "scripted error", step: scriptedllm.RuntimeError(declared), req: llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}},
		{name: "before response", step: scriptedllm.Step{
			BeforeResponse: func(context.Context) error { return declared },
		}, req: llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}},
		{name: "after response", step: scriptedllm.Step{
			AfterResponse: func(context.Context) error { return declared },
		}, req: llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}},
		{name: "cancellation", step: scriptedllm.Cancellation(), req: llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "m"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{testCase.step}})
			outcome, err := client.GenerateOutcome(context.Background(), testCase.req, llm.StreamCallbacks{})
			if err == nil {
				t.Fatal("consumed failing step returned no error")
			}
			if outcome.Admission != scriptedllm.RequestAdmitted {
				t.Fatalf("admission = %v, want admitted", outcome.Admission)
			}
		})
	}

	delay := time.Hour
	streaming := scriptedllm.FinalAnswer("unused")
	streaming.StreamDeltas = []llm.AssistantDelta{{Text: "first"}, {Text: "second"}}
	streaming.StreamDeltaDelay = &delay
	client := scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{streaming}})
	ctx, cancel := context.WithCancel(context.Background())
	outcome, err := client.GenerateOutcome(ctx, llm.Request{
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Model:          "m",
	}, llm.StreamCallbacks{OnAssistantDelta: func(llm.AssistantDelta) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("streaming error = %v, want context.Canceled", err)
	}
	if outcome.Admission != scriptedllm.RequestAdmitted {
		t.Fatalf("streaming admission = %v, want admitted", outcome.Admission)
	}
}
