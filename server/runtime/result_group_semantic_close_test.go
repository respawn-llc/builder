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
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
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
	onObserve func()
}

func (c *semanticCloseWorkflowController) ObserveCurrentNodeCompletion(
	context.Context,
	workflowruntime.CompletionObservationRequest,
) (workflowruntime.CompletionObservationResult, error) {
	c.completionObservations.Add(1)
	if c.onObserve != nil {
		c.onObserve()
	}
	return workflowruntime.CompletionObservationResult{
		Completed: c.completedExternally.Load(),
	}, nil
}

func TestSemanticClosePrecedesGoalDrainAndWorkflowObservation(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
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
			CurrentNodeExecution: testWorkflowConfig(
				controller,
				config.WorkflowCompletionModeTool,
			),
		},
	)
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		activeStepID: "step",
		snapshot:     &RunSnapshot{RunID: "run", StepID: "step"},
	}
	calls := acceptedResponseCalls{
		local: []llm.ToolCall{{
			ID:    "close-success",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}},
		order: []acceptedResponseCallRef{{
			source: acceptedResponseCallLocal,
			index:  0,
		}},
	}
	persistAcceptedToolCallIntents(t, engine, "step", calls)

	var (
		observeErr error
		observed   atomic.Bool
	)
	controller.onObserve = func() {
		if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("close-success"); !found {
			observeErr = errors.New("Workflow completion was observed before Result Group projection")
			return
		}
		goal := engine.Goal()
		if goal == nil || goal.Objective != "queued during tools" {
			observeErr = errors.New("Workflow completion was observed before Goal drain")
			return
		}
		observed.Store(true)
	}

	type outcome struct {
		applied  bool
		terminal bool
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		applied, terminal, err := (&defaultStepExecutor{engine: engine}).
			executeAcceptedToolCallsAndAppendResults(
				context.Background(),
				"step",
				calls,
			)
		done <- outcome{applied: applied, terminal: terminal, err: err}
	}()

	<-handler.started
	if _, queued, err := engine.QueueGoalSetForActiveStep(
		"queued during tools",
		session.GoalActorUser,
	); err != nil || !queued {
		t.Fatalf("queue Goal during tools = queued:%t err:%v", queued, err)
	}
	controller.completedExternally.Store(true)
	close(handler.release)
	got := <-done
	if got.err != nil || got.applied || !got.terminal {
		t.Fatalf("semantic close outcome = %+v, want external Workflow terminal", got)
	}
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	if !observed.Load() || controller.completionObservations.Load() != 1 {
		t.Fatalf(
			"Workflow observation = observed:%t calls:%d, want true/1",
			observed.Load(),
			controller.completionObservations.Load(),
		)
	}
	toolOutputIndex := -1
	goalNoticeIndex := -1
	for index, message := range engine.transcriptRuntimeState().SnapshotMessages() {
		if message.Role == llm.RoleTool &&
			message.ToolCallID != nil &&
			*message.ToolCallID == "close-success" {
			toolOutputIndex = index
		}
		if message.Role == llm.RoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == llm.MessageTypeGoal {
			goalNoticeIndex = index
		}
	}
	if toolOutputIndex < 0 ||
		goalNoticeIndex < 0 ||
		toolOutputIndex >= goalNoticeIndex {
		t.Fatalf(
			"semantic close message order = tool:%d Goal:%d",
			toolOutputIndex,
			goalNoticeIndex,
		)
	}
}

func TestTopLevelPreCommitCloseFatalMakesOneAppendAndNoDiagnostic(t *testing.T) {
	t.Parallel()
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(durability),
	)
	handler := &semanticCloseBlockingTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{commentaryResponse(
			"working",
			llm.ToolCall{
				ID:    "fatal-close",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{}`),
			},
		)}},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "continue")
		done <- err
	}()

	<-handler.started
	appendsBefore, _ := durability.snapshot()
	blocker := mustBlockTestEventLogAppends(t, store)
	close(handler.release)
	err := <-done
	if restoreErr := blocker.Restore(); restoreErr != nil {
		t.Fatalf("restore event-log blocker: %v", restoreErr)
	}
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || fatal.Committed {
		t.Fatalf("top-level close error = %v, want uncommitted collector fatal", err)
	}
	if store.Meta().PendingModelRecovery == nil {
		t.Fatal("top-level collector fatal cleared PendingModelRecovery")
	}
	if _, projected := engine.transcriptRuntimeState().ToolCompletionSnapshot("fatal-close"); projected {
		t.Fatal("uncommitted collector fatal projected a tool outcome")
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"top-level fatal append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if readErr != nil {
		t.Fatalf("read top-level fatal records: %v", readErr)
	}
	for _, record := range window.Records {
		entry, ok := mustSessionEventPayload(record).(session.LocalEntryRecord)
		if ok && entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			t.Fatalf("top-level collector fatal persisted semantic diagnostic: %+v", entry)
		}
	}
	if snapshot := engine.ChatSnapshot(); snapshot.StreamingError == "" {
		t.Fatal("top-level collector fatal did not publish transient failed live state")
	}
	if _, submitErr := engine.SubmitUserMessage(context.Background(), "later"); !errors.Is(submitErr, ErrEngineClosed) {
		t.Fatalf("later same-Engine submission error = %v, want ErrEngineClosed", submitErr)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatalf("close failed Engine: %v", closeErr)
	}
	appendsAfterClose, _ := durability.snapshot()
	if len(appendsAfterClose) != len(appendsAfter) {
		t.Fatalf(
			"Engine.Close append attempts = %d, want unchanged %d",
			len(appendsAfterClose),
			len(appendsAfter),
		)
	}
}

func TestTopLevelCommittedObserverCloseFatalRetainsProjectionAndClosesAdmission(t *testing.T) {
	t.Parallel()
	failure := errors.New("committed observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
		session.WithDurabilityObserver(durability),
	)
	handler := &semanticCloseBlockingTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{commentaryResponse(
			"working",
			llm.ToolCall{
				ID:    "observer-close",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{}`),
			},
		)}},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "continue")
		done <- err
	}()

	<-handler.started
	appendsBefore, _ := durability.snapshot()
	gate.FailNext(failure)
	close(handler.release)
	err := <-done

	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		!fatal.Committed ||
		!errors.Is(fatal.Cause, failure) {
		t.Fatalf("top-level observer close error = %v, want exact committed collector fatal", err)
	}
	if store.Meta().PendingModelRecovery == nil {
		t.Fatal("committed observer fatal cleared PendingModelRecovery")
	}
	if _, projected := engine.transcriptRuntimeState().ToolCompletionSnapshot("observer-close"); !projected {
		t.Fatal("committed observer fatal did not retain its projected group")
	}
	if _, submitErr := engine.SubmitUserMessage(context.Background(), "later"); !errors.Is(submitErr, ErrEngineClosed) {
		t.Fatalf("later same-Engine submission error = %v, want ErrEngineClosed", submitErr)
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf("observer fatal append attempts = %d, want one after %d", len(appendsAfter), len(appendsBefore))
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatalf("close failed Engine: %v", closeErr)
	}
	appendsAfterClose, _ := durability.snapshot()
	if len(appendsAfterClose) != len(appendsAfter) {
		t.Fatalf("Engine.Close append attempts = %d, want unchanged %d", len(appendsAfterClose), len(appendsAfter))
	}
}

func TestTopLevelCommittedProjectionCloseFatalRehydratesOnceAndClosesAdmission(t *testing.T) {
	t.Parallel()
	callbackObserver := newCallbackPersistenceObserver(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(callbackObserver),
		session.WithDurabilityObserver(durability),
	)
	handler := &semanticCloseBlockingTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{commentaryResponse(
			"working",
			llm.ToolCall{
				ID:    "projection-close",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{}`),
			},
		)}},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "continue")
		done <- err
	}()

	<-handler.started
	appendsBefore, _ := durability.snapshot()
	callbackObserver.Arm(func() {
		engine.transcriptRuntimeState().CompleteLiveTool("projection-close")
	})
	close(handler.release)
	err := <-done

	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || !fatal.Committed {
		t.Fatalf("top-level projection close error = %v, want committed collector fatal", err)
	}
	if store.Meta().PendingModelRecovery == nil {
		t.Fatal("committed projection fatal cleared PendingModelRecovery")
	}
	if _, projected := engine.transcriptRuntimeState().ToolCompletionSnapshot("projection-close"); projected {
		t.Fatal("committed projection fatal partially projected its group")
	}
	if _, submitErr := engine.SubmitUserMessage(context.Background(), "later"); !errors.Is(submitErr, ErrEngineClosed) {
		t.Fatalf("later same-Engine submission error = %v, want ErrEngineClosed", submitErr)
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf("projection fatal append attempts = %d, want one after %d", len(appendsAfter), len(appendsBefore))
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatalf("close failed Engine: %v", closeErr)
	}
	appendsAfterClose, _ := durability.snapshot()
	if len(appendsAfterClose) != len(appendsAfter) {
		t.Fatalf("Engine.Close append attempts = %d, want unchanged %d", len(appendsAfterClose), len(appendsAfter))
	}

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(
		t,
		reopened,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	snapshot := mustTranscriptHydrationSnapshot(t, restored)
	if rows := countHydratedToolRows(snapshot, "projection-close"); rows != 1 {
		t.Fatalf("rehydrated projection-close tool rows = %d, want one", rows)
	}
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
		[]*completedToolResult{{result: first}, nil},
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
		[]*completedToolResult{nil},
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

func TestFinishRunErrorFeedbackPersistsOnlyUnpersistedLifecycleBranch(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	countFeedback := func() int {
		count := 0
		for _, entry := range engine.ChatSnapshot().Entries {
			if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				count++
			}
		}
		return count
	}
	callbackErr := errors.New("goal callback failed")
	if engine.persistRunErrorFeedback(callbackErr) == "" {
		t.Fatal("goal callback failure produced no durable feedback")
	}
	before := countFeedback()
	clearErr := &pendingModelRecoveryClearError{cause: errors.New("clear failed")}

	engine.finishRunErrorFeedback(errors.Join(
		&persistedRunCallbackError{cause: callbackErr},
		clearErr,
	))

	if after := countFeedback(); after != before+1 {
		t.Fatalf("goal final feedback records = %d after %d, want one lifecycle diagnostic", after, before)
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
			CurrentNodeExecution: testWorkflowConfig(
				controller,
				config.WorkflowCompletionModeTool,
			),
		},
	)
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
	if _, queued, err := engine.QueueGoalSetForActiveStep(
		"must remain queued",
		session.GoalActorUser,
	); err != nil || !queued {
		t.Fatalf("queue Goal during tools = queued:%t err:%v", queued, err)
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
	if controller.completionObservations.Load() != 0 {
		t.Fatalf(
			"failed close observed Workflow completion %d time(s)",
			controller.completionObservations.Load(),
		)
	}
	engine.activeStepGoalMutationsMu.Lock()
	pendingGoals := len(engine.activeStepGoalMutations["step"])
	engine.activeStepGoalMutationsMu.Unlock()
	if pendingGoals != 1 {
		t.Fatalf("failed close pending Goal mutations = %d, want one untouched", pendingGoals)
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

func TestProviderFailureBeforeCollectorPersistsDiagnosticBeforeReturning(t *testing.T) {
	t.Parallel()
	providerErr := &llm.APIStatusError{StatusCode: 401}
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{errors: []error{providerErr}},
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		},
	)
	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); !llm.HasHTTPStatus(err, 401) {
		t.Fatalf("submit error = %v, want provider failure", err)
	}
	assertRunFailureDiagnosticPrecedesTerminal(t, engine, events)
}

func TestAcceptedResponseValidationFailurePersistsDiagnosticBeforeTerminal(t *testing.T) {
	t.Parallel()
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:  llm.RoleAssistant,
				Phase: textutil.Value(llm.MessagePhaseCommentary),
				ToolCalls: []llm.ToolCall{{
					Name:  string(toolspec.ToolExecCommand),
					Input: json.RawMessage(`{}`),
				}},
			},
		}}},
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		},
	)
	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); err == nil {
		t.Fatal("accepted-response validation unexpectedly succeeded")
	}
	assertRunFailureDiagnosticPrecedesTerminal(t, engine, events)
}

func TestQueuedProviderFailurePersistsDiagnosticBeforeTerminal(t *testing.T) {
	t.Parallel()
	engine, events := newProviderFailureOrderingEngine(t, Config{Model: "gpt-5"})
	engine.QueueUserMessage("queued input")
	if _, err := engine.SubmitQueuedUserMessages(context.Background()); !llm.HasHTTPStatus(err, 401) {
		t.Fatalf("queued submission error = %v, want provider failure", err)
	}
	assertRunFailureDiagnosticPrecedesTerminal(t, engine, *events)
}

func TestQueuedFlushFailurePersistsDiagnosticBeforeTerminal(t *testing.T) {
	t.Parallel()
	flushErr := errors.New("queued user injection flush failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
	)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	if err := engine.ensureMetaContextForRequest(
		context.Background(),
		"queued-flush-failure",
	); err != nil {
		t.Fatalf("prepare queued submission context: %v", err)
	}
	engine.QueueUserMessage("queued input")
	gate.FailNext(flushErr)

	_, receipt, err := engine.SubmitQueuedUserMessagesWithActiveHook(
		context.Background(),
		nil,
	)
	if !receipt.Committed || !errors.Is(err, flushErr) {
		t.Fatalf(
			"queued flush failure receipt=%+v error=%v, want committed failure",
			receipt,
			err,
		)
	}
	diagnostics := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			diagnostics++
		}
	}
	if diagnostics != 1 {
		t.Fatalf(
			"queued flush failure diagnostics = %d, want one durable entry",
			diagnostics,
		)
	}
}

func TestGoalProviderFailurePersistsDiagnosticBeforeTerminal(t *testing.T) {
	t.Parallel()
	engine, events := newProviderFailureOrderingEngine(t, Config{
		Model:        "gpt-5",
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := engine.SetGoal("exercise Goal failure ordering", session.GoalActorUser); err != nil {
		t.Fatalf("set Goal: %v", err)
	}
	if _, err := engine.runGoalTurn(context.Background(), false); !llm.HasHTTPStatus(err, 401) {
		t.Fatalf("Goal turn error = %v, want provider failure", err)
	}
	assertRunFailureDiagnosticPrecedesTerminal(t, engine, *events)
}

func TestBackgroundProviderFailurePersistsDiagnosticBeforeTerminal(t *testing.T) {
	t.Parallel()
	engine, events := newProviderFailureOrderingEngine(t, Config{Model: "gpt-5"})
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: engine,
		steps:  engine.stepLifecycle,
	}
	scheduler.QueueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("background continuation"),
	})
	if _, err := scheduler.runQueuedNotices(context.Background()); !llm.HasHTTPStatus(err, 401) {
		t.Fatalf("background continuation error = %v, want provider failure", err)
	}
	assertRunFailureDiagnosticPrecedesTerminal(t, engine, *events)
}

func TestBackgroundLifecycleFailurePersistsDeveloperDiagnosticOutsideStepCallback(t *testing.T) {
	t.Parallel()
	lifecycleErr := errors.New("background step lifecycle failed")
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	steps := &stubExclusiveStepLifecycle{
		runFn: func(
			context.Context,
			exclusiveStepOptions,
			func(context.Context, string) error,
		) error {
			return lifecycleErr
		},
	}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}
	scheduler.queueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("queued background notice"),
	}, false)

	if _, err := scheduler.runQueuedNotices(context.Background()); !errors.Is(err, lifecycleErr) {
		t.Fatalf("background lifecycle error = %v, want injected failure", err)
	}
	entries := engine.ChatSnapshot().Entries
	if len(entries) != 1 ||
		entries[0].Role != string(transcript.EntryRoleDeveloperErrorFeedback) ||
		entries[0].Text == "" {
		t.Fatalf(
			"background lifecycle failure entries = %+v, want durable developer diagnostic",
			entries,
		)
	}
}

func TestBackgroundTerminalLifecycleFailurePersistsSeparatelyFromCallbackFailure(t *testing.T) {
	t.Parallel()
	providerErr := &llm.APIStatusError{StatusCode: 401}
	lifecycleErr := errors.New("background terminal lifecycle failed")
	lifecycle := &callbackStepLifecycleSink{
		onTransition: func(transition StepLifecycleTransition) error {
			if transition == StepLifecycleTransitionEnded {
				return lifecycleErr
			}
			return nil
		},
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{errors: []error{providerErr}},
		tools.NewRegistry(),
		Config{
			Model:         "gpt-5",
			StepLifecycle: lifecycle,
		},
	)
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: engine,
		steps:  engine.stepLifecycle,
	}
	scheduler.queueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("queued background notice"),
	}, false)

	if _, err := scheduler.runQueuedNotices(context.Background()); !errors.Is(
		err,
		providerErr,
	) || !errors.Is(err, lifecycleErr) {
		t.Fatalf(
			"background terminal errors = %v, want provider and lifecycle failures",
			err,
		)
	}
	diagnostics := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			diagnostics++
		}
	}
	if diagnostics != 2 {
		t.Fatalf(
			"background terminal developer diagnostics = %d, want callback and lifecycle entries",
			diagnostics,
		)
	}
}

func TestBackgroundPendingRecoveryClearFailurePersistsOneDeveloperDiagnostic(t *testing.T) {
	t.Parallel()
	clearErr := errors.New("background pending recovery clear failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
	)
	failureArmed := false
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("completed"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
		}}},
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				if event.Kind == EventAssistantMessage && !failureArmed {
					failureArmed = true
					gate.FailNext(clearErr)
				}
			},
		},
	)
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: engine,
		steps:  engine.stepLifecycle,
	}
	scheduler.queueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("queued background notice"),
	}, false)

	if _, err := scheduler.runQueuedNotices(context.Background()); !errors.Is(
		err,
		errPendingModelRecoveryClear,
	) || !errors.Is(err, clearErr) {
		t.Fatalf(
			"background pending recovery clear error = %v, want typed clear failure",
			err,
		)
	}
	if !failureArmed {
		t.Fatal("background assistant commit did not arm clear failure")
	}
	diagnostics := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			diagnostics++
		}
	}
	if diagnostics != 1 {
		t.Fatalf(
			"background pending recovery clear diagnostics = %d, want exactly one",
			diagnostics,
		)
	}
}

func TestBackgroundCallbackAndPendingRecoveryClearFailuresPersistSeparately(t *testing.T) {
	t.Parallel()
	providerErr := &llm.APIStatusError{StatusCode: 401}
	clearErr := errors.New("background callback pending recovery clear failed")
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{errors: []error{providerErr}},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	steps := &stubExclusiveStepLifecycle{
		runFn: func(
			ctx context.Context,
			_ exclusiveStepOptions,
			run func(context.Context, string) error,
		) error {
			callbackErr := run(ctx, "background-callback-clear")
			return errors.Join(
				callbackErr,
				&pendingModelRecoveryClearError{cause: clearErr},
			)
		},
	}
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: engine,
		steps:  steps,
	}
	scheduler.queueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("queued background notice"),
	}, false)

	if _, err := scheduler.runQueuedNotices(context.Background()); !errors.Is(
		err,
		providerErr,
	) || !errors.Is(err, errPendingModelRecoveryClear) ||
		!errors.Is(err, clearErr) {
		t.Fatalf(
			"background callback-plus-clear error = %v, want original combined failure",
			err,
		)
	}
	var diagnostics []string
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			diagnostics = append(diagnostics, entry.Text)
		}
	}
	if len(diagnostics) != 2 {
		t.Fatalf(
			"background callback-plus-clear diagnostics = %d, want one per failure",
			len(diagnostics),
		)
	}
	if diagnostics[0] == diagnostics[1] {
		t.Fatal("background pending-clear diagnostic duplicated callback feedback")
	}
}

func newProviderFailureOrderingEngine(
	t *testing.T,
	config Config,
) (*Engine, *[]Event) {
	t.Helper()
	var events []Event
	config.OnEvent = func(event Event) {
		events = append(events, event)
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{errors: []error{&llm.APIStatusError{StatusCode: 401}}},
		tools.NewRegistry(),
		config,
	)
	return engine, &events
}

func assertRunFailureDiagnosticPrecedesTerminal(
	t *testing.T,
	engine *Engine,
	events []Event,
) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, engine.store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read provider-failure records: %v", err)
	}
	durableDiagnostic := false
	for _, record := range window.Records {
		entry, ok := mustSessionEventPayload(record).(session.LocalEntryRecord)
		if ok &&
			entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) &&
			entry.Text != nil {
			durableDiagnostic = true
			break
		}
	}
	if !durableDiagnostic {
		t.Fatal("provider failure returned before its developer diagnostic was durable")
	}
	diagnosticIndex := -1
	failedRunIndex := -1
	for index, event := range events {
		if event.Kind == EventLocalEntryAdded &&
			event.LocalEntry != nil &&
			event.LocalEntry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			diagnosticIndex = index
		}
		if event.Kind == EventRunStateChanged &&
			event.RunState != nil &&
			event.RunState.Status == RunStatusFailed {
			failedRunIndex = index
		}
	}
	if diagnosticIndex < 0 ||
		failedRunIndex < 0 ||
		diagnosticIndex >= failedRunIndex {
		t.Fatalf(
			"provider failure event order = diagnostic:%d failed-run:%d events:%+v",
			diagnosticIndex,
			failedRunIndex,
			events,
		)
	}
}
