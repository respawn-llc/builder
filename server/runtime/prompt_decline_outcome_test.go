package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestDeclinedQuestionProducesErrorToolCompletionWithoutSyntheticUserMessage(t *testing.T) {
	broker := tools.NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
		return nil, context.Canceled
	})
	var eventMu sync.Mutex
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
			OnEvent: func(event Event) {
				eventMu.Lock()
				events = append(events, event)
				eventMu.Unlock()
			},
		},
	)

	results, err := engine.executeToolCalls(context.Background(), "step-1", []llm.ToolCall{{
		ID:    "question-1",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Proceed?"}`),
	}})
	if err != nil {
		t.Fatalf("executeToolCalls: %v", err)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("declined Question result = %+v, want one error result", results)
	}

	eventMu.Lock()
	defer eventMu.Unlock()
	completions := 0
	for _, event := range events {
		if event.Kind == EventUserMessageFlushed {
			t.Fatalf("declined Question emitted synthetic user message: %+v", event)
		}
		if event.Kind == EventToolCallCompleted && event.ToolResult != nil && event.ToolResult.CallID == "question-1" {
			completions++
			if !event.ToolResult.IsError {
				t.Fatalf("declined Question completion = %+v, want error", event.ToolResult)
			}
		}
	}
	if completions != 1 {
		t.Fatalf("declined Question completion count = %d, want 1", completions)
	}
}
