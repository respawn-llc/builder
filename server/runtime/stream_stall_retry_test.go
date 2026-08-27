package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
)

type countingFailingStreamClient struct {
	calls atomic.Int32
	err   error
}

func (c *countingFailingStreamClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	c.calls.Add(1)
	return llm.Response{}, c.err
}

func TestGenerateWithRetryRetryPolicyByToolChoice(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0, 0, 0, 0, 0})
	withIdleStallRetryDelays(t, []time.Duration{0})

	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	tests := []struct {
		name         string
		request      llm.Request
		err          error
		wantAttempts int32
	}{
		{
			name:         "automatic stall uses reduced budget",
			request:      llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5"},
			err:          fmt.Errorf("model stream stalled: %w", llm.ErrModelStreamStalled),
			wantAttempts: int32(len(idleStallRetryDelays) + 1),
		},
		{
			name:         "automatic retriable uses full budget",
			request:      llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5"},
			err:          &llm.APIStatusError{StatusCode: 503, Body: "overloaded"},
			wantAttempts: int32(len(generateRetryDelays) + 1),
		},
		{
			name: "required provider uses full budget",
			request: llm.Request{
				Model:          "gpt-5",
				ToolChoiceMode: llm.ToolChoiceModeRequired,
				Tools: []llm.Tool{{
					Name:   "complete_node",
					Schema: mustTestFunctionSchema(t),
				}},
			},
			err:          &llm.ProviderAPIError{StatusCode: 503, Code: llm.UnifiedErrorCodeUnknown},
			wantAttempts: int32(len(generateRetryDelays) + 1),
		},
		{
			name: "required stall uses reduced budget",
			request: llm.Request{
				Model:          "gpt-5",
				ToolChoiceMode: llm.ToolChoiceModeRequired,
				Tools: []llm.Tool{{
					Name:   "complete_node",
					Schema: mustTestFunctionSchema(t),
				}},
			},
			err:          fmt.Errorf("model stream stalled: %w", llm.ErrModelStreamStalled),
			wantAttempts: int32(len(idleStallRetryDelays) + 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retriable := &countingFailingStreamClient{err: tt.err}

			if _, err := eng.generateWithRetryClient(context.Background(), "step-1", retriable, tt.request, nil, nil, nil); err == nil {
				t.Fatal("expected retry-policy failure to surface")
			}
			if got := retriable.calls.Load(); got != tt.wantAttempts {
				t.Fatalf("retry-policy attempts = %d, want %d", got, tt.wantAttempts)
			}
		})
	}
}

type retryingEventsClient struct {
	fakeClient
	attempts int
}

func (c *retryingEventsClient) Generate(_ context.Context, _ llm.Request, callbacks llm.StreamCallbacks) (llm.Response, error) {
	c.attempts++
	callbacks.OnAssistantDelta(llm.AssistantDelta{Text: "x"})
	callbacks.OnReasoningSummaryDelta(llm.ReasoningSummaryDelta{Text: "x"})
	if c.attempts == 1 {
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "incomplete", Name: "exec_command"}}}, &llm.ProviderAPIError{Code: llm.UnifiedErrorCodeUnknown}
	}
	return llm.Response{}, nil
}
func TestRequiredRetryClearsIncompleteAssistantReasoningAndTools(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0})
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	var sequence []EventKind
	resp, err := engine.generateWithRetryClient(context.Background(), "step", &retryingEventsClient{},
		llm.Request{Model: "gpt-5", ToolChoiceMode: llm.ToolChoiceModeRequired},
		func(_ llm.AssistantDelta) { sequence = append(sequence, EventAssistantDelta) },
		func(_ llm.ReasoningSummaryDelta) { sequence = append(sequence, EventReasoningDelta) },
		func() { sequence = append(sequence, EventAssistantDeltaReset, EventReasoningDeltaReset) },
	)
	if err != nil || len(resp.ToolCalls) != 0 || len(sequence) != 6 || sequence[0] != EventAssistantDelta || sequence[1] != EventReasoningDelta || sequence[2] != EventAssistantDeltaReset || sequence[3] != EventReasoningDeltaReset || sequence[4] != EventAssistantDelta || sequence[5] != EventReasoningDelta {
		t.Fatalf("required retry = response:%+v error:%v sequence:%v", resp, err, sequence)
	}
}
func TestRetryBudgetResetsAfterSuccessAndRetainsOverloadCause(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0, 0, 0, 0, 0})
	cause := &llm.ProviderAPIError{StatusCode: 200, Code: llm.UnifiedErrorCodeProviderOverload}
	client := &fakeClient{errors: []error{&llm.APIStatusError{StatusCode: 503}, nil, cause, cause, cause, cause, cause, cause}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	if _, err := engine.generateWithRetryClient(context.Background(), "first", client, llm.Request{Model: "gpt-5", ToolChoiceMode: llm.ToolChoiceModeAutomatic}, nil, nil, nil); err != nil {
		t.Fatalf("first generation: %v", err)
	}
	_, err := engine.generateWithRetryClient(context.Background(), "second", client, llm.Request{Model: "gpt-5", ToolChoiceMode: llm.ToolChoiceModeAutomatic}, nil, nil, nil)
	if !errors.Is(err, cause) || fakeClientCallCount(client) != 2+len(generateRetryDelays)+1 {
		t.Fatalf("exhausted overload = %v, calls = %d", err, fakeClientCallCount(client))
	}
}
func TestStatusFromRunErrorClassifiesStallAsFailed(t *testing.T) {
	t.Parallel()
	stall := fmt.Errorf("model generation failed after retries: %w", llm.ErrModelStreamStalled)
	if status := statusFromRunError(stall); status != RunStatusFailed {
		t.Fatalf("statusFromRunError(stall) = %v, want RunStatusFailed", status)
	}
}
