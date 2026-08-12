package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
		registry: newTestToolRegistry(t, tools.HandlerRegistration{
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
		if call.askQuestionBatch.BatchPromptIDs[index] != call.executableCall.ID {
			t.Fatalf("prepared call %d prompt order = %v", index, call.askQuestionBatch.BatchPromptIDs)
		}
	}
}

func TestPrepareExecutorToolCallsExcludesRejectedQuestionsFromCanonicalBatch(t *testing.T) {
	engine := &Engine{
		registry: newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: questionBatchTestHandler{},
		}),
	}
	prepared, err := prepareExecutorToolCalls(engine, "step-1", "run-1", false, []llm.ToolCall{
		{
			ID:    "ask-1",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"First?","action":"legacy","unknown":true}`),
		},
		{
			ID:    "ask-invalid",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Invalid?","suggestions":null}`),
		},
		{
			ID:    "ask-2",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Second?","suggestions":["yes","no"],"recommended_option_index":1}`),
		},
	})
	if err != nil {
		t.Fatalf("prepareExecutorToolCalls: %v", err)
	}
	if prepared[0].inputErr != nil || prepared[2].inputErr != nil {
		t.Fatalf("valid question preparation errors = %v, %v", prepared[0].inputErr, prepared[2].inputErr)
	}
	if prepared[1].inputErr == nil {
		t.Fatal("null suggestions unexpectedly passed canonical ingress")
	}
	if prepared[1].askQuestionBatch != nil {
		t.Fatalf("rejected question entered candidate roster: %+v", prepared[1].askQuestionBatch)
	}
	for _, index := range []int{0, 2} {
		batch := prepared[index].askQuestionBatch
		if batch == nil {
			t.Fatalf("valid question %d has no batch", index)
		}
		if batch.PreparedPromptCount != 2 ||
			len(batch.BatchPromptIDs) != 2 ||
			batch.BatchPromptIDs[0] != "ask-1" ||
			batch.BatchPromptIDs[1] != "ask-2" {
			t.Fatalf("valid question %d batch = %+v", index, batch)
		}
	}
	if prepared[0].askQuestionBatch.CandidateOrdinal != 0 ||
		prepared[2].askQuestionBatch.CandidateOrdinal != 1 {
		t.Fatalf(
			"candidate ordinals = %d, %d",
			prepared[0].askQuestionBatch.CandidateOrdinal,
			prepared[2].askQuestionBatch.CandidateOrdinal,
		)
	}
	var canonical map[string]any
	if err := json.Unmarshal(prepared[0].executableCall.Input, &canonical); err != nil {
		t.Fatalf("decode canonical first question: %v", err)
	}
	if len(canonical) != 1 || canonical["question"] != "First?" {
		t.Fatalf("canonical first question retained legacy or unknown fields: %#v", canonical)
	}
}

func TestRejectedQuestionDoesNotPolluteLaterBatchSkipRoster(t *testing.T) {
	broker := tools.NewAskQuestionBroker()
	ctx, cancel := context.WithCancel(context.Background())
	broker.SetAskHandler(func(context.Context, tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
		cancel()
		return nil, context.Canceled
	})
	skipped := make(chan tools.AskQuestionBatchMetadata, 4)
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{
			Model: "gpt-5",
			AskQuestionBatchSkipped: func(batch tools.AskQuestionBatchMetadata) {
				skipped <- batch
			},
		},
	)
	results, err := engine.executeToolCalls(ctx, "step", []llm.ToolCall{
		{ID: "ask-1", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"question":"First?"}`)},
		{ID: "ask-invalid", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"question":"Invalid?","suggestions":null}`)},
		{ID: "ask-2", Name: string(toolspec.ToolAskQuestion), Input: json.RawMessage(`{"question":"Second?"}`)},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("execute question batch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("question batch results = %d, want 3", len(results))
	}
	close(skipped)
	var later *tools.AskQuestionBatchMetadata
	for batch := range skipped {
		if batch.PromptID == "ask-2" {
			copied := batch
			later = &copied
		}
	}
	if later == nil {
		t.Fatal("later valid candidate did not receive batch-skipped notification")
	}
	if later.CandidateOrdinal != 1 ||
		later.PreparedPromptCount != 2 ||
		len(later.BatchPromptIDs) != 2 ||
		later.BatchPromptIDs[0] != "ask-1" ||
		later.BatchPromptIDs[1] != "ask-2" {
		t.Fatalf("later skipped batch = %+v, want exact valid-candidate roster", *later)
	}
}
