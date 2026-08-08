package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
)

type questionBatchTestHandler struct{}

func (questionBatchTestHandler) Call(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, nil
}

func (questionBatchTestHandler) QuestionsEnabled() bool {
	return true
}

func TestPrepareExecutorToolCallsAssignsQuestionBatchOutsideWorkflow(t *testing.T) {
	engine := &Engine{
		registry: tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: questionBatchTestHandler{},
		}),
	}
	prepared, err := prepareExecutorToolCalls(engine, "step-1", "run-1", false, []llm.ToolCall{
		{ID: "ask-1", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"question":"First?"}`)},
		{ID: "ask-2", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"question":"Second?"}`)},
	})
	if err != nil {
		t.Fatalf("prepareExecutorToolCalls: %v", err)
	}
	for index, call := range prepared {
		if call.askQuestionBatch == nil {
			t.Fatalf("prepared call %d has no question batch", index)
		}
		if call.askQuestionBatch.StepID != "step-1" {
			t.Fatalf("prepared call %d step identity = %q, want step-1", index, call.askQuestionBatch.StepID)
		}
		if call.askQuestionBatch.CandidateOrdinal != index ||
			call.askQuestionBatch.PreparedPromptCount != 2 ||
			len(call.askQuestionBatch.BatchPromptIDs) != 2 {
			t.Fatalf("prepared call %d batch = %+v", index, call.askQuestionBatch)
		}
		if call.askQuestionBatch.BatchPromptIDs[index] != call.call.ID {
			t.Fatalf("prepared call %d prompt order = %v", index, call.askQuestionBatch.BatchPromptIDs)
		}
	}
}
