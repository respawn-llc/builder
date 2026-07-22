package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/toolspec"
)

func TestCompletedResponseExternalWorkflowCompletionDiscardsActiveStreamWithoutPersistence(t *testing.T) {
	controller := &externallyCompletedWorkflowController{}
	step := scriptedllm.ToolBatch("", llm.ToolCall{
		ID:    "stale-call",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{}`),
	})
	step.StreamDeltas = []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseCommentary}}
	step.AfterResponse = func(context.Context) error {
		controller.completed.Store(true)
		return nil
	}
	var events []Event
	runID := workflow.RunID("workflow-run")
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		Config{
			Model: "gpt-5",
			WorkflowRun: &workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}

	var delta, reset *Event
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple assistant deltas: %+v", events)
			}
			delta = event
		case EventAssistantDeltaReset:
			if reset != nil {
				t.Fatalf("multiple assistant delta resets: %+v", events)
			}
			reset = event
		case EventToolCallStarted:
			t.Fatalf("externally completed workflow executed a stale tool call: %+v", event)
		}
	}
	if delta == nil || reset == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		reset.AssistantTranscriptStreamID == nil ||
		*delta.AssistantTranscriptStreamID != *reset.AssistantTranscriptStreamID ||
		reset.AssistantStreamAbortReason != string(AssistantStreamAbortSuperseded) {
		t.Fatalf("stale stream disposal = delta:%+v reset:%+v", delta, reset)
	}

	window, err := mustMaterializeTestEventLog(t, engine.store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded workflow records: %v", err)
	}
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID == "stale-call" {
				t.Fatalf("stale workflow response persisted a tool completion: %+v", payload)
			}
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore persisted message: %v", restoreErr)
			}
			for _, call := range message.ToolCalls {
				if call.ID == "stale-call" {
					t.Fatalf("stale workflow response persisted a tool call: %+v", message)
				}
			}
		}
	}
}

type externallyCompletedWorkflowController struct {
	completed atomic.Bool
}

func (c *externallyCompletedWorkflowController) CompleteWorkflowRun(
	context.Context,
	workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	return workflowruntime.CompletionResult{}, nil
}

func (c *externallyCompletedWorkflowController) RecordWorkflowProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{}, nil
}

func (c *externallyCompletedWorkflowController) ResetWorkflowProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return nil
}

func (c *externallyCompletedWorkflowController) ObserveWorkflowRunCompletion(
	context.Context,
	workflowruntime.CompletionObservationRequest,
) (workflowruntime.CompletionObservationResult, error) {
	return workflowruntime.CompletionObservationResult{Completed: c.completed.Load()}, nil
}
