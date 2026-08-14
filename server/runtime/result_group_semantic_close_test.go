package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
)

type semanticCloseBlockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *semanticCloseBlockingTool) Call(
	_ context.Context,
	call tools.Call,
) (tools.Result, error) {
	close(t.started)
	<-t.release
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

type semanticCloseWorkflowController struct {
	fakeWorkflowController
}

func TestSemanticCloseDoesNotRereportCompletedCellAndLeavesNoEmptySlot(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		activeStepID: "step",
		snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: "step"},
	}
	calls := []llm.ToolCall{
		{ID: "already-complete", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "needs-interruption", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
	}
	for _, call := range calls {
		normalized := normalizeToolCallForTranscript(call, engine.transcriptWorkingDir())
		if err := engine.steer("step", steerEventIntent(Event{
			Kind:                       EventToolCallStarted,
			StepID:                     "step",
			ToolCall:                   &normalized,
			CommittedTranscriptChanged: true,
		})); err != nil {
			t.Fatalf("start tool %q: %v", call.ID, err)
		}
	}
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{
		resultGroupIdentityFromToolCall(calls[0]),
		resultGroupIdentityFromToolCall(calls[1]),
	})
	if err != nil {
		t.Fatalf("new semantic close collector: %v", err)
	}
	first := tools.Result{
		CallID: calls[0].ID,
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"ok":true}`),
	}
	var outcome *resultGroupReportOutcome
	if err := engine.steer("step", steerResultGroupReportIntent(
		collector,
		first.CallID,
		resultGroupUnit{result: first},
		&outcome,
	)); err != nil || outcome == nil || *outcome != resultGroupReportAccepted {
		t.Fatalf("report completed cell = outcome:%v err:%v", outcome, err)
	}

	postJoin, err := engine.coordinateAcceptedResponsePostJoin(
		"step",
		[]executorToolCall{
			{call: calls[0], toolID: toolspec.ToolExecCommand, knownTool: true},
			{call: calls[1], toolID: toolspec.ToolExecCommand, knownTool: true},
		},
		collector,
		context.Canceled,
	)
	if err != nil || !errors.Is(postJoin.semanticErr, context.Canceled) {
		t.Fatalf(
			"semantic close errors = operational:%v semantic:%v, want nil/context.Canceled",
			err,
			postJoin.semanticErr,
		)
	}
	if collector.state != resultGroupCollectorClosed ||
		collector.cursor != len(collector.slots) {
		t.Fatalf(
			"semantic close collector = state:%d cursor:%d slots:%d",
			collector.state,
			collector.cursor,
			len(collector.slots),
		)
	}
	for index, slot := range collector.slots {
		if slot.result == nil {
			t.Fatalf("semantic close retained empty slot %d: %+v", index, slot)
		}
	}
	if len(postJoin.results) != 2 ||
		postJoin.results[0].CallID != calls[0].ID ||
		postJoin.results[1].CallID != calls[1].ID ||
		!postJoin.results[1].IsError {
		t.Fatalf("semantic close results = %+v", postJoin.results)
	}
}

func TestSemanticCloseSteeringFailureAbortsResultGroupWithoutPanicking(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	call := llm.ToolCall{
		ID:    "semantic-close-steer-failure",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	}
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{
		resultGroupIdentityFromToolCall(call),
	})
	if err != nil {
		t.Fatalf("new semantic-close collector: %v", err)
	}
	engine.closed.Store(true)

	_, err = engine.coordinateAcceptedResponsePostJoin(
		"step",
		[]executorToolCall{{call: call, toolID: toolspec.ToolExecCommand, knownTool: true}},
		collector,
		context.Canceled,
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, ErrEngineClosed) {
		t.Fatalf("semantic-close steering failure = %v, want uncommitted engine-closed fatal", err)
	}
}

type semanticCloseFailureKind uint8

const (
	semanticCloseFailureUncommitted semanticCloseFailureKind = iota + 1
	semanticCloseFailureObserver
	semanticCloseFailureProjection
)

func TestSemanticCloseFailureSkipsGoalAndWorkflowPostJoinOperations(t *testing.T) {
	tests := []struct {
		name      string
		kind      semanticCloseFailureKind
		committed bool
		projected bool
	}{
		{name: "uncommitted", kind: semanticCloseFailureUncommitted},
		{name: "committed observer", kind: semanticCloseFailureObserver, committed: true, projected: true},
		{name: "committed projection", kind: semanticCloseFailureProjection, committed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runSemanticCloseFailurePostJoinCase(
				t,
				test.kind,
				test.committed,
				test.projected,
			)
		})
	}
}

func runSemanticCloseFailurePostJoinCase(
	t *testing.T,
	kind semanticCloseFailureKind,
	wantCommitted bool,
	wantProjected bool,
) {
	t.Helper()
	durability := &toolDurabilityObservationRecorder{}
	var (
		store            *session.Store
		gate             *sessiontest.PersistenceGate
		callbackObserver *callbackPersistenceObserver
	)
	switch kind {
	case semanticCloseFailureUncommitted:
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithDurabilityObserver(durability),
		)
	case semanticCloseFailureObserver:
		gate = sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(gate),
			session.WithDurabilityObserver(durability),
		)
	case semanticCloseFailureProjection:
		callbackObserver = newCallbackPersistenceObserver(runtimeTestSessionPersistence)
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(callbackObserver),
			session.WithDurabilityObserver(durability),
		)
	default:
		t.Fatalf("unknown semantic close failure kind %d", kind)
	}

	handler := &semanticCloseBlockingTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := &semanticCloseWorkflowController{}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{
			Model: "gpt-5",
		},
	)
	publishTestWorkflowExecution(t, engine, testWorkflowConfig(controller, config.WorkflowCompletionModeTool))
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		activeStepID: "step",
		snapshot:     &RunSnapshot{RunID: "run", StepID: "step"},
	}
	calls := acceptedResponseCalls{
		local: []llm.ToolCall{{
			ID:    "close-failure",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}},
		order: []acceptedResponseCallRef{{
			source: acceptedResponseCallLocal,
			index:  0,
		}},
	}
	persistAcceptedToolCallIntents(t, engine, "step", calls)

	type outcome struct {
		terminal bool
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		_, terminal, err := (&defaultStepExecutor{engine: engine}).
			executeAcceptedToolCallsAndAppendResults(
				context.Background(),
				"step",
				calls,
			)
		done <- outcome{terminal: terminal, err: err}
	}()

	<-handler.started
	entry := newOutputSteeringQueueEntry("", false, steeringIntent{items: []steeringMutation{
		&steeringGoalMutation{mutation: GoalMutation{
			Kind: GoalMutationSet, Objective: "must remain queued", Actor: session.GoalActorUser,
		}},
	}})
	if _, err := engine.steering.append(entry); err != nil {
		t.Fatalf("queue Goal during tools: %v", err)
	}
	failure := errors.New("semantic close persistence failure")
	var restore func() error
	switch kind {
	case semanticCloseFailureUncommitted:
		blocker := mustBlockTestEventLogAppends(t, store)
		restore = blocker.Restore
	case semanticCloseFailureObserver:
		gate.FailNext(failure)
	case semanticCloseFailureProjection:
		callbackObserver.Arm(func() {
			engine.transcriptRuntimeState().CompleteLiveTool("close-failure")
		})
	}
	appendsBefore, _ := durability.snapshot()
	close(handler.release)
	got := <-done
	if restore != nil {
		if err := restore(); err != nil {
			t.Fatalf("restore event-log blocker: %v", err)
		}
	}

	var fatal *resultGroupFatal
	if !errors.As(got.err, &fatal) ||
		fatal.Committed != wantCommitted ||
		got.terminal {
		t.Fatalf(
			"semantic close failure = outcome:%+v fatal:%+v, want committed=%t",
			got,
			fatal,
			wantCommitted,
		)
	}
	if kind == semanticCloseFailureObserver && !errors.Is(fatal.Cause, failure) {
		t.Fatalf("observer fatal cause = %v, want %v", fatal.Cause, failure)
	}
	if goal := engine.Goal(); goal != nil {
		t.Fatalf("failed close drained Goal mutation: %+v", goal)
	}
	if !engine.HasPendingSteering() {
		t.Fatal("failed close removed the accepted Goal Steering tail")
	}
	_, projected := engine.transcriptRuntimeState().ToolCompletionSnapshot("close-failure")
	if projected != wantProjected {
		t.Fatalf("failed close projection = %t, want %t", projected, wantProjected)
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"failed close append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}
}
