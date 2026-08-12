package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
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
		newTestToolRegistry(t, tools.HandlerRegistration{
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

func TestDeclinedQuestionAllowsPreparedSuccessorToMaterialize(t *testing.T) {
	broker := tools.NewAskQuestionBroker()
	var materialized []string
	broker.SetAskHandler(func(_ context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
		materialized = append(materialized, req.ID)
		if req.ID == "question-1" {
			return nil, context.Canceled
		}
		return tools.AskQuestionAnswer{Freeform: textutil.Value("continue")}, nil
	})
	var skipped []tools.AskQuestionBatchMetadata
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
			AskQuestionBatchSkipped: func(batch tools.AskQuestionBatchMetadata) {
				skipped = append(skipped, batch)
			},
		},
	)

	results, err := engine.executeToolCalls(context.Background(), "step-1", []llm.ToolCall{
		{
			ID:    "question-1",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"First?"}`),
		},
		{
			ID:    "question-2",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Second?"}`),
		},
	})
	if err != nil {
		t.Fatalf("executeToolCalls: %v", err)
	}
	if len(results) != 2 || !results[0].IsError || results[1].IsError {
		t.Fatalf("decline/successor results = %+v", results)
	}
	if len(materialized) != 2 || materialized[0] != "question-1" || materialized[1] != "question-2" {
		t.Fatalf("materialized Questions = %v, want both prepared Questions in order", materialized)
	}
	if len(skipped) != 0 {
		t.Fatalf("decline marked prepared successor skipped: %+v", skipped)
	}
}
