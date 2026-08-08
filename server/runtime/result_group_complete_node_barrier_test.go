package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
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
		State:        "applied",
	}, nil
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
			Model:                "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
			DurabilityObserver:   flushes,
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

func TestCompleteNodeControllerFailureRemainsSemanticAfterBarrier(t *testing.T) {
	flushes := &resultGroupFlushRecorder{}
	controllerErr := errors.New("Workflow completion unavailable")
	controller := &completeNodeBarrierController{completeError: controllerErr}
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
			json.RawMessage(`{"summary":"done"}`),
		),
	)
	if err != nil {
		t.Fatalf("controller failure escaped complete_node semantics: %v", err)
	}
	if mutatedBeforeDurability.Load() {
		t.Fatal("complete_node controller ran before the ready sibling committed")
	}
	if controller.completeCalls.Load() != 1 {
		t.Fatalf("Workflow completion calls = %d, want one", controller.completeCalls.Load())
	}
	if controller.violations.Load() != 1 {
		t.Fatalf("controller failure violations = %d, want one", controller.violations.Load())
	}
	if len(results) != 1 ||
		results[0].CallID != "complete-node" ||
		!results[0].IsError {
		t.Fatalf("controller failure results = %+v, want one semantic error", results)
	}
	observations := flushes.snapshot()
	if len(observations) != 2 ||
		observations[0].Reason != ResultGroupFlushCompleteNode ||
		observations[0].ResultCount != 1 ||
		observations[1].Reason != ResultGroupFlushStepBoundary {
		t.Fatalf(
			"controller failure flushes = %+v, want complete_node sibling flush then Step Boundary close",
			observations,
		)
	}
}

func TestCompleteNodeBarrierPreCommitFailureBlocksWorkflowMutationAndResult(t *testing.T) {
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(durability),
	)
	controller := &completeNodeBarrierController{}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:                "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
		},
	)
	persistAcceptedToolCallIntents(t, engine, "step", completeNodeBarrierAcceptedCalls(
		json.RawMessage(`{"summary":"done"}`),
	))
	appendsBefore, _ := durability.snapshot()
	blocker := mustBlockTestEventLogAppends(t, store)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		completeNodeBarrierAcceptedCalls(
			json.RawMessage(`{"summary":"done"}`),
		),
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || fatal.Committed {
		t.Fatalf("complete_node barrier error = %v, want uncommitted collector fatal", err)
	}
	if controller.completeCalls.Load() != 0 {
		t.Fatalf("uncommitted complete_node Workflow calls = %d, want zero", controller.completeCalls.Load())
	}
	if len(results) != 0 {
		t.Fatalf("uncommitted complete_node fatal results = %+v, want none", results)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log blocker: %v", err)
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"uncommitted complete_node append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); found {
		t.Fatal("uncommitted complete_node barrier projected the ready sibling")
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("complete-node"); found {
		t.Fatal("uncommitted complete_node barrier projected a fabricated result")
	}
}

func TestCompleteNodeBarrierCommittedObserverFailureRetainsPrefixAndBlocksMutation(t *testing.T) {
	observerErr := errors.New("complete_node barrier observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
		session.WithDurabilityObserver(durability),
	)
	controller := &completeNodeBarrierController{}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:                "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
		},
	)
	persistAcceptedToolCallIntents(t, engine, "step", completeNodeBarrierAcceptedCalls(
		json.RawMessage(`{"summary":"done"}`),
	))
	appendsBefore, _ := durability.snapshot()
	gate.FailNext(observerErr)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		completeNodeBarrierAcceptedCalls(
			json.RawMessage(`{"summary":"done"}`),
		),
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		!fatal.Committed ||
		!errors.Is(fatal.Cause, observerErr) {
		t.Fatalf("complete_node barrier error = %v, want committed observer fatal", err)
	}
	if controller.completeCalls.Load() != 0 {
		t.Fatalf("committed-observer complete_node Workflow calls = %d, want zero", controller.completeCalls.Load())
	}
	if len(results) != 0 {
		t.Fatalf("committed-observer complete_node fatal results = %+v, want none", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
		t.Fatal("committed observer failure did not project the ready sibling")
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("complete-node"); found {
		t.Fatal("committed observer failure projected a fabricated complete_node result")
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"committed-observer complete_node append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}
	assertFreshResourceRepairExactlyOnceWithHydratedPrefix(
		t,
		store,
		"complete-node",
		"hosted",
	)
}

func TestCompleteNodeBarrierCommittedProjectionFailureHydratesPrefixAndBlocksMutation(t *testing.T) {
	callbackObserver := newCallbackPersistenceObserver(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(callbackObserver),
		session.WithDurabilityObserver(durability),
	)
	controller := &completeNodeBarrierController{}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:                "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
		},
	)
	persistAcceptedToolCallIntents(t, engine, "step", completeNodeBarrierAcceptedCalls(
		json.RawMessage(`{"summary":"done"}`),
	))
	appendsBefore, _ := durability.snapshot()
	callbackObserver.Arm(func() {
		engine.transcriptRuntimeState().CompleteLiveTool("hosted")
	})

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		completeNodeBarrierAcceptedCalls(
			json.RawMessage(`{"summary":"done"}`),
		),
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || !fatal.Committed {
		t.Fatalf("complete_node barrier error = %v, want committed projection fatal", err)
	}
	if controller.completeCalls.Load() != 0 {
		t.Fatalf("committed-projection complete_node Workflow calls = %d, want zero", controller.completeCalls.Load())
	}
	if len(results) != 0 {
		t.Fatalf("committed-projection complete_node fatal results = %+v, want none", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); found {
		t.Fatal("committed projection failure partially projected the ready sibling")
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"committed-projection complete_node append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(
		t,
		reopened,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	if rows := countHydratedToolRows(
		mustTranscriptHydrationSnapshot(t, restored),
		"hosted",
	); rows != 1 {
		t.Fatalf("rehydrated complete_node sibling rows = %d, want one", rows)
	}
	assertFreshResourceRepairOnEngine(t, restored, reopened, "complete-node")
	assertFreshResourceRepairExactlyOnceWithHydratedPrefix(
		t,
		reopened,
		"complete-node",
		"hosted",
	)
}
