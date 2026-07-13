package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
)

type countingFailingStreamClient struct {
	calls atomic.Int32
	err   error
}

func (c *countingFailingStreamClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	c.calls.Add(1)
	return llm.Response{}, c.err
}

func (c *countingFailingStreamClient) GenerateStreamWithEvents(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	c.calls.Add(1)
	return llm.Response{}, c.err
}

func TestGenerateWithRetryStalledStreamUsesReducedRetryBudget(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0, 0, 0, 0, 0})
	withIdleStallRetryDelays(t, []time.Duration{0})

	store := mustCreateTestSession(t)
	stall := &countingFailingStreamClient{err: fmt.Errorf("model stream stalled: %w", llm.ErrModelStreamStalled)}
	eng := mustNewTestEngine(t, store, stall, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", stall, llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5"}, nil, nil, nil); err == nil {
		t.Fatal("expected stall failure after reduced retries")
	}
	if got := stall.calls.Load(); got != int32(len(idleStallRetryDelays)+1) {
		t.Fatalf("stall attempts = %d, want %d", got, len(idleStallRetryDelays)+1)
	}
}

func TestGenerateWithRetryGenericRetriableUsesFullRetryBudget(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0, 0, 0, 0, 0})
	withIdleStallRetryDelays(t, []time.Duration{0})

	store := mustCreateTestSession(t)
	retriable := &countingFailingStreamClient{err: &llm.APIStatusError{StatusCode: 503, Body: "overloaded"}}
	eng := mustNewTestEngine(t, store, retriable, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", retriable, llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5"}, nil, nil, nil); err == nil {
		t.Fatal("expected failure after full retries")
	}
	if got := retriable.calls.Load(); got != int32(len(generateRetryDelays)+1) {
		t.Fatalf("generic retriable attempts = %d, want %d", got, len(generateRetryDelays)+1)
	}
}

func TestGenerateWithRetryRequiredChoiceSurfacesProviderAndTransportFailures(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0, 0, 0, 0, 0})
	withIdleStallRetryDelays(t, []time.Duration{0})

	tests := []struct {
		name string
		err  error
	}{
		{name: "provider", err: &llm.APIStatusError{StatusCode: 503, Body: "overloaded"}},
		{name: "transport stall", err: fmt.Errorf("model stream stalled: %w", llm.ErrModelStreamStalled)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			retriable := &countingFailingStreamClient{err: tt.err}
			eng := mustNewTestEngine(t, store, retriable, tools.NewRegistry(), Config{Model: "gpt-5"})
			request := llm.Request{
				Model:          "gpt-5",
				ToolChoiceMode: llm.ToolChoiceModeRequired,
				Tools: []llm.Tool{{
					Name:   "complete_node",
					Schema: json.RawMessage(`{"type":"object"}`),
				}},
			}

			if _, err := eng.generateWithRetryClient(context.Background(), "step-1", retriable, request, nil, nil, nil); err == nil {
				t.Fatal("expected required-choice failure to surface")
			}
			if got := retriable.calls.Load(); got != 1 {
				t.Fatalf("required-choice attempts = %d, want 1", got)
			}
		})
	}
}

func TestStatusFromRunErrorClassifiesStallAsFailed(t *testing.T) {
	stall := fmt.Errorf("model generation failed after retries: %w", llm.ErrModelStreamStalled)
	if status := statusFromRunError(stall); status != RunStatusFailed {
		t.Fatalf("statusFromRunError(stall) = %v, want RunStatusFailed", status)
	}
}
