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
)

type completeNodeBarrierController struct {
	fakeWorkflowController
	beforeComplete func()
	completeError  error
	completeCalls  atomic.Int32
}

func (c *completeNodeBarrierController) CompleteAgentCurrentNode(
	_ context.Context,
	_ workflowruntime.AgentCompletionRequest,
) (workflowruntime.CompletionOutcome, error) {
	c.completeCalls.Add(1)
	if c.beforeComplete != nil {
		c.beforeComplete()
	}
	if c.completeError != nil {
		return workflowruntime.RejectedCompletionOutcome(c.completeError), c.completeError
	}
	return workflowruntime.AcceptedCompletionOutcome(workflowruntime.AcceptedCompletion{
		Result: workflowruntime.CompletionResult{
			TransitionID: "done",
			State:        "applied",
		},
	}), nil
}

func (c *completeNodeBarrierController) CompleteScriptCurrentNode(
	context.Context,
	workflowruntime.ScriptCompletionRequest,
) (workflowruntime.CompletionOutcome, error) {
	err := errors.New("unexpected Script completion")
	return workflowruntime.RejectedCompletionOutcome(err), err
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
	controller := &completeNodeBarrierController{}
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
			Model:              "gpt-5",
			DurabilityObserver: flushes,
		},
	)
	workflowConfig := testWorkflowConfig(controller, config.WorkflowCompletionModeTool)
	publishTestWorkflowExecution(t, engine, workflowConfig)
	publishTestWorkflowAgentAssociation(t, engine, workflowConfig)
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		snapshot: &RunSnapshot{
			RunID:  "11111111-1111-4111-8111-111111111111",
			StepID: "22222222-2222-4222-8222-222222222222",
		},
		activeStepID: "22222222-2222-4222-8222-222222222222",
	}

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"22222222-2222-4222-8222-222222222222",
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

func TestCompleteNodeValidatesBeforeEffectBarrier(t *testing.T) {
	flushes := &resultGroupFlushRecorder{}
	controller := &completeNodeBarrierController{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:              "gpt-5",
			DurabilityObserver: flushes,
		},
	)
	publishTestWorkflowExecution(t, engine, testWorkflowConfig(controller, config.WorkflowCompletionModeTool))

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
