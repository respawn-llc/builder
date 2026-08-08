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

func TestGenerateWithRetryRetryPolicyByToolChoice(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0, 0, 0, 0, 0})
	withIdleStallRetryDelays(t, []time.Duration{0})

	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
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
			name: "required provider surfaces without retry",
			request: llm.Request{
				Model:          "gpt-5",
				ToolChoiceMode: llm.ToolChoiceModeRequired,
				Tools: []llm.Tool{{
					Name:   "complete_node",
					Schema: json.RawMessage(`{"type":"object"}`),
				}},
			},
			err:          &llm.APIStatusError{StatusCode: 503, Body: "overloaded"},
			wantAttempts: 1,
		},
		{
			name: "required stall surfaces without retry",
			request: llm.Request{
				Model:          "gpt-5",
				ToolChoiceMode: llm.ToolChoiceModeRequired,
				Tools: []llm.Tool{{
					Name:   "complete_node",
					Schema: json.RawMessage(`{"type":"object"}`),
				}},
			},
			err:          fmt.Errorf("model stream stalled: %w", llm.ErrModelStreamStalled),
			wantAttempts: 1,
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

func TestStatusFromRunErrorClassifiesStallAsFailed(t *testing.T) {
	t.Parallel()
	stall := fmt.Errorf("model generation failed after retries: %w", llm.ErrModelStreamStalled)
	if status := statusFromRunError(stall); status != RunStatusFailed {
		t.Fatalf("statusFromRunError(stall) = %v, want RunStatusFailed", status)
	}
}
