package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
)

type callbackPersistenceObserver struct {
	delegate   session.PersistenceObserver
	reconciler session.EventLogReconciliationObserver

	mu       sync.Mutex
	callback func()
}

func newCallbackPersistenceObserver(
	delegate session.PersistenceObserver,
) *callbackPersistenceObserver {
	reconciler, _ := delegate.(session.EventLogReconciliationObserver)
	return &callbackPersistenceObserver{
		delegate:   delegate,
		reconciler: reconciler,
	}
}

func (o *callbackPersistenceObserver) Arm(callback func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.callback != nil {
		panic("callback persistence observer is already armed")
	}
	o.callback = callback
}

func (o *callbackPersistenceObserver) ObservePersistedStore(
	ctx context.Context,
	snapshot session.PersistedStoreSnapshot,
) error {
	if err := o.delegate.ObservePersistedStore(ctx, snapshot); err != nil {
		return err
	}
	o.mu.Lock()
	callback := o.callback
	o.callback = nil
	o.mu.Unlock()
	if callback != nil {
		callback()
	}
	return nil
}

func (o *callbackPersistenceObserver) ObserveEventLogReconciliation(
	ctx context.Context,
	reconciliation session.PersistedEventLogReconciliation,
) error {
	return o.reconciler.ObserveEventLogReconciliation(ctx, reconciliation)
}

func prepareSimpleResultGroupCall(
	t *testing.T,
	engine *Engine,
	stepID string,
	callID string,
) {
	t.Helper()
	call := normalizeToolCallForTranscript(llm.ToolCall{
		ID:    callID,
		Name:  string(toolspec.ToolExecCommand),
		Input: []byte(`{"cmd":"true"}`),
	}, engine.transcriptWorkingDir())
	if err := engine.steer(
		stepID,
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
		),
	); err != nil {
		t.Fatalf("persist result group call %s: %v", callID, err)
	}
	if err := engine.transcriptRuntimeState().RecordLiveToolStart(stepID, call); err != nil {
		t.Fatalf("record live result group call %s: %v", callID, err)
	}
}

func reportAndFlushSimpleResultGroup(
	engine *Engine,
	stepID string,
	collector *resultGroupCollector,
	callID string,
) error {
	outcome := resultGroupReportOutcome(0)
	return engine.steer(
		stepID,
		steerResultGroupReportIntent(
			collector,
			callID,
			testResultGroupUnit(callID),
			&outcome,
		),
		steerResultGroupFlushIntent(collector, ResultGroupFlushQuestion),
	)
}

func testResultGroupCollector(t *testing.T, callIDs ...string) *resultGroupCollector {
	t.Helper()
	roster := make([]resultGroupCallIdentity, len(callIDs))
	for index, callID := range callIDs {
		roster[index] = resultGroupCallIdentity{
			CallID:     callID,
			Name:       toolspec.ToolExecCommand,
			OutputKind: session.ToolOutputKindFunction,
		}
	}
	collector, err := newResultGroupCollector(roster)
	if err != nil {
		t.Fatalf("new result group collector: %v", err)
	}
	return collector
}

func testResultGroupUnit(callID string) resultGroupUnit {
	return resultGroupUnit{
		result: tools.Result{
			CallID: callID,
			Name:   toolspec.ToolExecCommand,
			Output: []byte(`{"ok":true}`),
		},
	}
}

func TestResultGroupCollectorEarlierEmptySlotBlocksReadyLaterResult(t *testing.T) {
	collector := testResultGroupCollector(t, "first", "second")
	if _, err := collector.report("second", testResultGroupUnit("second")); err != nil {
		t.Fatalf("report second result: %v", err)
	}

	if ready := collector.readyPrefix(); len(ready) != 0 {
		t.Fatalf("ready prefix = %+v, want empty", ready)
	}
}

func TestResultGroupCollectorReleasesContiguousReadyPrefix(t *testing.T) {
	collector := testResultGroupCollector(t, "first", "second", "third")
	if _, err := collector.report("first", testResultGroupUnit("first")); err != nil {
		t.Fatalf("report first result: %v", err)
	}
	if _, err := collector.report("second", testResultGroupUnit("second")); err != nil {
		t.Fatalf("report second result: %v", err)
	}

	ready := collector.readyPrefix()
	if len(ready) != 2 ||
		ready[0].result.CallID != "first" ||
		ready[1].result.CallID != "second" {
		t.Fatalf("ready prefix = %+v, want first and second", ready)
	}
}

func TestResultGroupCollectorCursorAdvanceStartsNextPrefix(t *testing.T) {
	collector := testResultGroupCollector(t, "first", "second", "third")
	for _, callID := range []string{"first", "second", "third"} {
		if _, err := collector.report(callID, testResultGroupUnit(callID)); err != nil {
			t.Fatalf("report %s result: %v", callID, err)
		}
	}
	if err := collector.advanceReadyPrefix(2); err != nil {
		t.Fatalf("advance ready prefix: %v", err)
	}

	ready := collector.readyPrefix()
	if len(ready) != 1 || ready[0].result.CallID != "third" {
		t.Fatalf("ready prefix after advance = %+v, want third", ready)
	}
}

func TestResultGroupCollectorRejectsDuplicateReport(t *testing.T) {
	collector := testResultGroupCollector(t, "first")
	if _, err := collector.report("first", testResultGroupUnit("first")); err != nil {
		t.Fatalf("report first result: %v", err)
	}

	if _, err := collector.report("first", testResultGroupUnit("first")); err == nil {
		t.Fatal("duplicate report succeeded")
	}
}

func TestResultGroupCollectorRejectsEmptySlotAtClose(t *testing.T) {
	collector := testResultGroupCollector(t, "first", "second")
	if _, err := collector.report("first", testResultGroupUnit("first")); err != nil {
		t.Fatalf("report first result: %v", err)
	}

	if err := collector.requireCompleteForClose(); err == nil {
		t.Fatal("close accepted an empty result slot")
	}
}

func TestResultGroupCollectorFatalAbortKeepsFirstCauseAndIgnoresReports(t *testing.T) {
	collector := testResultGroupCollector(t, "first")
	firstCause := errors.New("first durability failure")
	secondCause := errors.New("second durability failure")
	collector.abort(resultGroupFatal{Committed: false, Cause: firstCause})
	collector.abort(resultGroupFatal{Committed: true, Cause: secondCause})

	outcome, err := collector.report("first", testResultGroupUnit("first"))
	if err != nil {
		t.Fatalf("report after abort: %v", err)
	}
	if outcome != resultGroupReportIgnoredAfterAbort {
		t.Fatalf("report outcome = %d, want ignored after abort", outcome)
	}
	fatal := collector.fatalSnapshot()
	if fatal == nil || fatal.Committed || !errors.Is(fatal.Cause, firstCause) {
		t.Fatalf("collector fatal = %+v, want first uncommitted cause", fatal)
	}
	if ready := collector.readyPrefix(); len(ready) != 0 {
		t.Fatalf("aborted collector ready prefix = %+v, want empty", ready)
	}
}

func TestResultGroupCollectorRejectsReportAfterClose(t *testing.T) {
	collector := testResultGroupCollector(t, "first")
	if _, err := collector.report("first", testResultGroupUnit("first")); err != nil {
		t.Fatalf("report first result: %v", err)
	}
	if err := collector.advanceReadyPrefix(1); err != nil {
		t.Fatalf("advance ready prefix: %v", err)
	}
	if err := collector.close(); err != nil {
		t.Fatalf("close collector: %v", err)
	}

	if _, err := collector.report("first", testResultGroupUnit("first")); err == nil {
		t.Fatal("report after close succeeded")
	}
}

func TestResultGroupReportUsesCentralSteeringHandler(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	collector := testResultGroupCollector(t, "first")
	outcome := resultGroupReportOutcome(0)

	if err := engine.steer(
		"step",
		steerResultGroupReportIntent(
			collector,
			"first",
			testResultGroupUnit("first"),
			&outcome,
		),
	); err != nil {
		t.Fatalf("steer result group report: %v", err)
	}
	if outcome != resultGroupReportAccepted {
		t.Fatalf("report outcome = %d, want accepted", outcome)
	}
	ready := collector.readyPrefix()
	if len(ready) != 1 || ready[0].result.CallID != "first" {
		t.Fatalf("steered ready prefix = %+v, want first", ready)
	}
}

func TestResultGroupFlushAtomicallyPersistsCompletionOutputAndDiagnostic(t *testing.T) {
	observer := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(observer),
	)
	var events []Event
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	result := mismatchedDeletionCompletion(t, engine)
	appendsBefore, syncsBefore := observer.snapshot()
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{{
		CallID:     result.CallID,
		Name:       result.Name,
		OutputKind: session.ToolOutputKindCustom,
	}})
	if err != nil {
		t.Fatalf("new result group collector: %v", err)
	}
	outcome := resultGroupReportOutcome(0)
	if err := engine.steer(
		"step-delete",
		steerResultGroupReportIntent(
			collector,
			result.CallID,
			resultGroupUnit{result: result},
			&outcome,
		),
		steerResultGroupFlushIntent(collector, ResultGroupFlushQuestion),
	); err != nil {
		t.Fatalf("report and flush result group: %v", err)
	}
	if outcome != resultGroupReportAccepted || collector.cursor != 1 {
		t.Fatalf(
			"result group outcome=%d cursor=%d, want accepted cursor 1",
			outcome,
			collector.cursor,
		)
	}
	appendsAfter, syncsAfter := observer.snapshot()
	newAppends := appendsAfter[len(appendsBefore):]
	newSyncs := syncsAfter[len(syncsBefore):]
	if len(newAppends) != 1 || len(newSyncs) != 1 {
		t.Fatalf(
			"group durability observations = %d appends/%d syncs, want 1/1",
			len(newAppends),
			len(newSyncs),
		)
	}
	if newAppends[0].RecordCount != 3 {
		t.Fatalf("group append records = %d, want completion, diagnostic, and output", newAppends[0].RecordCount)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read result group records: %v", err)
	}
	var completionCount, diagnosticCount, outputCount int
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID == result.CallID {
				completionCount++
			}
		case session.LocalEntryRecord:
			if payload.AfterToolCallID != nil && *payload.AfterToolCallID == result.CallID {
				diagnosticCount++
			}
		case session.MessageRecord:
			if payload.Role == session.MessageRoleTool &&
				payload.ToolCallID != nil &&
				*payload.ToolCallID == result.CallID {
				outputCount++
			}
		}
	}
	if completionCount != 1 || diagnosticCount != 1 || outputCount != 1 {
		t.Fatalf(
			"group persisted completion/diagnostic/output = %d/%d/%d, want 1/1/1",
			completionCount,
			diagnosticCount,
			outputCount,
		)
	}
	if len(events) != 2 ||
		events[0].Kind != EventToolCallCompleted ||
		events[1].Kind != EventLocalEntryAdded {
		t.Fatalf("result group events = %+v, want completion then diagnostic", events)
	}
}

func TestResultGroupFlushCommitsOutOfOrderReadyResultsInRosterOrder(t *testing.T) {
	observer := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(observer),
	)
	var events []Event
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	calls := []llm.ToolCall{
		{ID: "first", Name: string(toolspec.ToolExecCommand), Input: []byte(`{"cmd":"one"}`)},
		{ID: "second", Name: string(toolspec.ToolExecCommand), Input: []byte(`{"cmd":"two"}`)},
	}
	normalized := make([]llm.ToolCall, len(calls))
	for index, call := range calls {
		normalized[index] = normalizeToolCallForTranscript(call, engine.transcriptWorkingDir())
	}
	if err := engine.steer(
		"step",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: normalized}},
		),
	); err != nil {
		t.Fatalf("persist result group calls: %v", err)
	}
	for _, call := range normalized {
		if err := engine.transcriptRuntimeState().RecordLiveToolStart("step", call); err != nil {
			t.Fatalf("record live tool %s: %v", call.ID, err)
		}
	}
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{
		{CallID: "first", Name: toolspec.ToolExecCommand, OutputKind: session.ToolOutputKindFunction},
		{CallID: "second", Name: toolspec.ToolExecCommand, OutputKind: session.ToolOutputKindFunction},
	})
	if err != nil {
		t.Fatalf("new result group collector: %v", err)
	}
	appendsBefore, _ := observer.snapshot()
	secondOutcome := resultGroupReportOutcome(0)
	if err := engine.steer(
		"step",
		steerResultGroupReportIntent(
			collector,
			"second",
			resultGroupUnit{result: tools.Result{
				CallID: "second",
				Name:   toolspec.ToolExecCommand,
				Output: []byte(`{"second":true}`),
			}},
			&secondOutcome,
		),
		steerResultGroupFlushIntent(collector, ResultGroupFlushQuestion),
	); err != nil {
		t.Fatalf("report later result: %v", err)
	}
	appendsBlocked, _ := observer.snapshot()
	if len(appendsBlocked) != len(appendsBefore) || collector.cursor != 0 {
		t.Fatalf(
			"blocked prefix appended=%d before=%d cursor=%d",
			len(appendsBlocked),
			len(appendsBefore),
			collector.cursor,
		)
	}
	firstOutcome := resultGroupReportOutcome(0)
	if err := engine.steer(
		"step",
		steerResultGroupReportIntent(
			collector,
			"first",
			resultGroupUnit{result: tools.Result{
				CallID: "first",
				Name:   toolspec.ToolExecCommand,
				Output: []byte(`{"first":true}`),
			}},
			&firstOutcome,
		),
		steerResultGroupFlushIntent(collector, ResultGroupFlushQuestion),
	); err != nil {
		t.Fatalf("report first result and flush prefix: %v", err)
	}
	appendsAfter, _ := observer.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 ||
		appendsAfter[len(appendsBefore)].RecordCount != 4 ||
		collector.cursor != 2 {
		t.Fatalf(
			"committed prefix appends=%+v cursor=%d, want one four-record append and cursor 2",
			appendsAfter[len(appendsBefore):],
			collector.cursor,
		)
	}
	var completed []string
	for _, event := range events {
		if event.Kind == EventToolCallCompleted && event.ToolResult != nil {
			completed = append(completed, event.ToolResult.CallID)
		}
	}
	if len(completed) != 2 || completed[0] != "first" || completed[1] != "second" {
		t.Fatalf("completion event order = %v, want first then second", completed)
	}
}

func TestResultGroupFlushPreCommitFailureProjectsNothingAndDoesNotRetryOnClose(t *testing.T) {
	observer := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(observer),
	)
	var events []Event
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	prepareSimpleResultGroupCall(t, engine, "step", "failed")
	events = nil
	collector := testResultGroupCollector(t, "failed")
	appendsBefore, _ := observer.snapshot()
	blocker := mustBlockTestEventLogAppends(t, store)

	err := reportAndFlushSimpleResultGroup(engine, "step", collector, "failed")
	var fatal resultGroupFatal
	if !errors.As(err, &fatal) || fatal.Committed {
		t.Fatalf("pre-commit result group error = %v, want uncommitted fatal", err)
	}
	if collector.cursor != 0 || len(events) != 0 {
		t.Fatalf(
			"pre-commit result group cursor=%d events=%+v, want no projection",
			collector.cursor,
			events,
		)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log blocker: %v", err)
	}
	appendsAfterFailure, _ := observer.snapshot()
	if len(appendsAfterFailure) != len(appendsBefore)+1 {
		t.Fatalf(
			"pre-commit append attempts = %d, want one after %d",
			len(appendsAfterFailure),
			len(appendsBefore),
		)
	}
	var closeFatal *resultGroupFatal
	if closeErr := engine.steer(
		"step",
		steerResultGroupCloseIntent(collector),
	); !errors.As(closeErr, &closeFatal) {
		t.Fatalf("close aborted result group error = %v, want original fatal", closeErr)
	}
	appendsAfterClose, _ := observer.snapshot()
	if len(appendsAfterClose) != len(appendsAfterFailure) {
		t.Fatalf(
			"close retried aborted result group: appends=%d, want %d",
			len(appendsAfterClose),
			len(appendsAfterFailure),
		)
	}
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read pre-commit records: %v", readErr)
	}
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID == "failed" {
				t.Fatalf("pre-commit failure persisted completion: %+v", payload)
			}
		case session.MessageRecord:
			if payload.Role == session.MessageRoleTool &&
				payload.ToolCallID != nil &&
				*payload.ToolCallID == "failed" {
				t.Fatalf("pre-commit failure persisted output: %+v", payload)
			}
		}
	}
	if _, openErr := session.Open(
		store.Dir(),
		runtimeTestSessionPersistence.Options()...,
	); openErr != nil {
		t.Fatalf("reopen after pre-commit result group failure: %v", openErr)
	}
}

func TestResultGroupCommittedObserverFailureProjectsOnceAndStoresFatal(t *testing.T) {
	observerErr := errors.New("result group observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
		session.WithDurabilityObserver(durability),
	)
	var events []Event
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	prepareSimpleResultGroupCall(t, engine, "step", "observer")
	events = nil
	collector := testResultGroupCollector(t, "observer")
	appendsBefore, _ := durability.snapshot()
	gate.FailNext(observerErr)

	err := reportAndFlushSimpleResultGroup(engine, "step", collector, "observer")
	var fatal resultGroupFatal
	if !errors.As(err, &fatal) ||
		!fatal.Committed ||
		!errors.Is(fatal.Cause, observerErr) {
		t.Fatalf("committed observer result group error = %v", err)
	}
	if collector.cursor != 1 ||
		len(events) != 1 ||
		events[0].Kind != EventToolCallCompleted {
		t.Fatalf(
			"committed observer projection cursor=%d events=%+v",
			collector.cursor,
			events,
		)
	}
	appendsAfterFailure, _ := durability.snapshot()
	if len(appendsAfterFailure) != len(appendsBefore)+1 {
		t.Fatalf(
			"committed observer appends=%d, want one after %d",
			len(appendsAfterFailure),
			len(appendsBefore),
		)
	}
	var closeFatal *resultGroupFatal
	if closeErr := engine.steer(
		"step",
		steerResultGroupCloseIntent(collector),
	); !errors.As(closeErr, &closeFatal) {
		t.Fatalf("close committed-observer group error = %v", closeErr)
	}
	appendsAfterClose, _ := durability.snapshot()
	if len(appendsAfterClose) != len(appendsAfterFailure) {
		t.Fatalf(
			"close reappended committed group: appends=%d, want %d",
			len(appendsAfterClose),
			len(appendsAfterFailure),
		)
	}
}

func TestResultGroupCommittedProjectionFailureEmitsNothingAndHydratesOnce(t *testing.T) {
	callbackObserver := newCallbackPersistenceObserver(
		runtimeTestSessionPersistence,
	)
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(callbackObserver),
	)
	var events []Event
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	prepareSimpleResultGroupCall(t, engine, "step", "projection")
	events = nil
	collector := testResultGroupCollector(t, "projection")
	callbackObserver.Arm(func() {
		engine.transcriptRuntimeState().CompleteLiveTool("projection")
	})

	err := reportAndFlushSimpleResultGroup(engine, "step", collector, "projection")
	var fatal resultGroupFatal
	if !errors.As(err, &fatal) || !fatal.Committed {
		t.Fatalf("committed projection result group error = %v", err)
	}
	if collector.cursor != 1 || len(events) != 0 {
		t.Fatalf(
			"committed projection failure cursor=%d events=%+v, want cursor 1 and no events",
			collector.cursor,
			events,
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
	snapshot := mustTranscriptHydrationSnapshot(t, restored)
	toolRows := 0
	for _, row := range snapshot.CommittedRows {
		if row.Kind == TranscriptCommittedRowFactTool &&
			row.Tool != nil &&
			row.Tool.ToolCallID == "projection" {
			toolRows++
		}
	}
	if toolRows != 1 {
		t.Fatalf(
			"rehydrated projection-failure tool rows = %d, want 1: %+v",
			toolRows,
			snapshot.CommittedRows,
		)
	}
}
