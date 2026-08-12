package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type completeNodeBarrierController struct {
	fakeWorkflowController
	beforeComplete func()
	completeError  error
	committedError error
	completeCalls  atomic.Int32
}

func (c *completeNodeBarrierController) CompleteCurrentNode(
	_ context.Context,
	_ workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	c.completeCalls.Add(1)
	if c.beforeComplete != nil {
		c.beforeComplete()
	}
	if c.completeError != nil {
		return workflowruntime.CompletionResult{}, c.completeError
	}
	return workflowruntime.CompletionResult{
		TransitionID: "done",
		State:        workflowruntime.CompletionStateApplied,
	}, c.committedError
}

func completeNodeBarrierAcceptedCalls(input json.RawMessage) acceptedResponseCalls {
	calls := questionBarrierAcceptedCalls()
	calls.local[0] = llm.ToolCall{
		ID:    "complete-node",
		Name:  string(toolspec.ToolCompleteNode),
		Input: input,
	}
	return calls
}

func TestCompleteNodeBarrierCommitsReadySiblingBeforeWorkflowMutation(t *testing.T) {
	store := mustCreateTestSession(t)
	flushes := &resultGroupFlushRecorder{}
	var events []Event
	diagnostic := errors.New("successor assignment observer failed")
	controller := &completeNodeBarrierController{committedError: diagnostic}
	var (
		engine                  *Engine
		mutatedBeforeDurability atomic.Bool
	)
	controller.beforeComplete = func() {
		if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
			mutatedBeforeDurability.Store(true)
		}
	}
	engine = mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:                "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
			DurabilityObserver:   flushes,
			OnEvent:              func(event Event) { events = append(events, event) },
		},
	)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		completeNodeBarrierAcceptedCalls(
			json.RawMessage(`{"summary":"done"}`),
		),
	)
	if err != nil {
		t.Fatalf("execute complete_node result group: %v", err)
	}
	if mutatedBeforeDurability.Load() {
		t.Fatal("complete_node mutated Workflow before the ready sibling committed")
	}
	if controller.completeCalls.Load() != 1 {
		t.Fatalf("Workflow completion calls = %d, want one", controller.completeCalls.Load())
	}
	if len(results) != 1 ||
		results[0].CallID != "complete-node" ||
		results[0].IsError ||
		!results[0].Terminal {
		t.Fatalf("complete_node results = %+v, want one terminal success", results)
	}
	var output map[string]any
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatalf("decode complete_node output: %v", err)
	}
	if _, present := output["diagnostic"]; present {
		t.Fatalf("complete_node output exposed a diagnostic field: %s", results[0].Output)
	}
	assertWorkflowCompletionOperatorDiagnostic(t, events, diagnostic)
	observations := flushes.snapshot()
	if len(observations) != 2 ||
		observations[0].Reason != ResultGroupFlushCompleteNode ||
		observations[0].ResultCount != 1 ||
		observations[1].Reason != ResultGroupFlushStepBoundary {
		t.Fatalf(
			"result group flushes = %+v, want complete_node sibling flush then Step Boundary close",
			observations,
		)
	}
}

func assertWorkflowCompletionOperatorDiagnostic(
	t *testing.T,
	events []Event,
	diagnostic error,
) {
	t.Helper()
	for _, event := range events {
		if event.Kind == EventLocalEntryAdded &&
			event.CommittedTranscriptChanged &&
			event.LocalEntry != nil &&
			event.LocalEntry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) &&
			event.LocalEntry.Text == diagnostic.Error() {
			return
		}
	}
	t.Fatal("typed workflow completion operator diagnostic was not published")
}

func TestCompleteNodeValidatesBeforeEffectBarrier(t *testing.T) {
	flushes := &resultGroupFlushRecorder{}
	controller := &completeNodeBarrierController{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:                "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
			DurabilityObserver:   flushes,
		},
	)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		completeNodeBarrierAcceptedCalls(
			json.RawMessage(`{"summary":""}`),
		),
	)
	if err != nil {
		t.Fatalf("execute invalid complete_node result group: %v", err)
	}
	if controller.completeCalls.Load() != 0 {
		t.Fatalf("invalid complete_node Workflow calls = %d, want zero", controller.completeCalls.Load())
	}
	if controller.violations.Load() != 1 {
		t.Fatalf("invalid complete_node violations = %d, want one", controller.violations.Load())
	}
	if len(results) != 1 ||
		results[0].CallID != "complete-node" ||
		!results[0].IsError {
		t.Fatalf("invalid complete_node results = %+v, want one semantic error", results)
	}
	observations := flushes.snapshot()
	if len(observations) != 1 ||
		observations[0].Reason != ResultGroupFlushStepBoundary ||
		observations[0].ResultCount != 2 {
		t.Fatalf(
			"invalid complete_node flushes = %+v, want only Step Boundary close",
			observations,
		)
	}
}
