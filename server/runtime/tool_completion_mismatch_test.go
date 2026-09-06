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
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestToolCompletionDeletionMismatchPanicsBeforePersistenceInDebug(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Debug: true,
	})
	stepID := runtimeTestStepID("step-delete")
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	result := mismatchedDeletionCompletion(t, engine)

	defer func() {
		recovered := recover()
		failure, ok := recovered.(toolCompletionPresentationPanic)
		if !ok {
			t.Fatalf("panic = %#v, want typed tool completion presentation panic", recovered)
		}
		if failure.CallID != result.CallID ||
			failure.ToolName != result.Name ||
			failure.Mismatch == nil ||
			failure.Mismatch.Kind != patchformat.WholeFileDeletionFactMismatchUnexpectedOperation {
			t.Fatalf("typed panic context = %+v", failure)
		}

		window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
		if err != nil {
			t.Fatalf("read bounded mismatch records: %v", err)
		}
		for _, record := range window.Records {
			switch mustSessionEventPayload(record).(type) {
			case session.ToolCompletionRecord, session.LocalEntryRecord:
				t.Fatalf("debug mismatch persisted a completion or fallback entry: %+v", record)
			}
		}
		if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); !ok {
			t.Fatal("debug mismatch removed the live tool before persistence")
		}
	}()

	_, _ = engine.steerWithCommitReceipt(runtimeTestStepID("step-delete"), steerToolCompletionIntent(result))
}

func TestToolCompletionDeletionMismatchReleaseFallbackPersistsRecovery(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})
	stepID := runtimeTestStepID("step-delete")
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	result := mismatchedDeletionCompletion(t, engine)

	if err := engine.steer(runtimeTestStepID("step-delete"), steerToolCompletionIntent(result)); err != nil {
		t.Fatalf("persist release fallback: %v", err)
	}
	assertDeletionMismatchFallback(t, engine, store, result)

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})
	assertDeletionMismatchFallback(t, restored, reopened, result)
}

func TestToolCompletionDeletionMismatchDoesNotApplyUncommittedFallback(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var emitted []Event
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { emitted = append(emitted, event) },
	})
	restoreStep := setTestActiveStep(engine, "step-delete")
	defer restoreStep()
	result := mismatchedDeletionCompletion(t, engine)
	emitted = nil
	blocker := mustBlockTestEventLogAppends(t, store)

	receipt, err := engine.steerWithCommitReceipt(runtimeTestStepID("step-delete"), steerToolCompletionIntent(result))
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted fallback outcome: receipt=%+v err=%v", receipt, err)
	}
	if restoreErr := blocker.Restore(); restoreErr != nil {
		t.Fatalf("restore event-log blocker: %v", restoreErr)
	}
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded mismatch records: %v", readErr)
	}
	for _, record := range window.Records {
		switch mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord, session.LocalEntryRecord:
			t.Fatalf("uncommitted fallback persisted recovery data: %+v", record)
		}
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("uncommitted fallback projected rows: %+v", rows)
	}
	if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); !ok {
		t.Fatal("uncommitted fallback removed the live tool")
	}
	if len(emitted) != 0 {
		t.Fatalf("uncommitted fallback emitted client events: %+v", emitted)
	}
}

func TestToolCompletionDeletionMismatchAppliesCommittedFallbackAfterObserverError(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("mismatch observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	var emitted []Event
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { emitted = append(emitted, event) },
	})
	restoreStep := setTestActiveStep(engine, "step-delete")
	defer restoreStep()
	result := mismatchedDeletionCompletion(t, engine)
	emitted = nil
	gate.FailNext(observerErr)

	receipt, err := engine.steerWithCommitReceipt(runtimeTestStepID("step-delete"), steerToolCompletionIntent(result))
	if !receipt.Committed || !errors.Is(err, observerErr) {
		t.Fatalf("committed fallback outcome: receipt=%+v err=%v", receipt, err)
	}
	assertDeletionMismatchFallback(t, engine, store, result)
	if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); ok {
		t.Fatal("committed fallback retained the live tool")
	}
	if len(emitted) != 2 ||
		emitted[0].Kind != EventToolCallCompleted ||
		emitted[1].Kind != EventLocalEntryAdded {
		t.Fatalf("committed fallback events = %+v", emitted)
	}
}

type mismatchedDeletionTool struct{}

func (mismatchedDeletionTool) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	return mismatchedDeletionResult(call.ID), nil
}

func TestExecuteToolCallsCommitsCompletionDiagnosticInResultGroup(t *testing.T) {
	observer := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(observer),
	)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolPatch,
			Handler: mismatchedDeletionTool{},
		}),
		Config{Model: "gpt-5"},
	)
	stepID := runtimeTestStepID("step-delete")
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	call := llm.ToolCall{
		ID:          "deletion-call",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value("*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"),
	}

	results, err := engine.executeToolCalls(t.Context(), stepID, []llm.ToolCall{call})
	if err != nil {
		t.Fatalf("execute mismatched deletion tool: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("mismatched deletion results = %+v, want one", results)
	}
	assertDeletionMismatchFallback(t, engine, store, results[0])
	appends, syncs := observer.snapshot()
	if len(appends) != 1 ||
		appends[0].RecordCount != 3 ||
		len(syncs) != 1 {
		t.Fatalf(
			"diagnostic group durability = appends:%+v syncs:%+v, want one three-record append and sync",
			appends,
			syncs,
		)
	}
}

func mismatchedDeletionCompletion(t *testing.T, engine *Engine) tools.Result {
	t.Helper()
	call := llm.ToolCall{
		ID:          "deletion-call",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value("*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"),
	}
	normalized := normalizeToolCallForTranscript(call, engine.transcriptWorkingDir())
	if err := engine.steer(runtimeTestStepID("step-delete"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{normalized},
	}}),
	); err != nil {
		t.Fatalf("persist deletion call: %v", err)
	}
	if err := engine.transcriptRuntimeState().RecordLiveToolStart(runtimeTestStepID("step-delete"), normalized); err != nil {
		t.Fatalf("record live deletion call: %v", err)
	}
	return mismatchedDeletionResult(call.ID)
}

func mismatchedDeletionResult(callID string) tools.Result {
	received := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 9}
	group := patchformat.WholeFileDeletionGroupID{FirstOperation: received}
	return tools.Result{
		CallID: callID,
		Name:   toolspec.ToolPatch,
		Output: json.RawMessage(`{"ok":true}`),
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				PhysicalGroup: group,
				OperationIDs:  []patchformat.WholeFileDeletionOperationID{received},
				Removed:       1,
			}},
		},
	}
}

func assertDeletionMismatchFallback(t *testing.T, engine *Engine, store *session.Store, result tools.Result) {
	t.Helper()
	stepID := runtimeTestStepID("step-delete")
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded mismatch records: %v", err)
	}
	var completion *storedToolCompletion
	var feedback *storedLocalEntry
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			value, restoreErr := storedToolCompletionFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore tool completion: %v", restoreErr)
			}
			if value.CallID == result.CallID {
				completion = &value
			}
		case session.LocalEntryRecord:
			value, restoreErr := storedLocalEntryFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore local entry: %v", restoreErr)
			}
			if value.AfterToolCallID != nil && *value.AfterToolCallID == result.CallID {
				feedback = &value
			}
		}
	}
	if completion == nil || completion.Presentation == nil ||
		completion.Presentation.PatchPresentation == nil ||
		completion.Presentation.PatchPresentation.Changes == nil {
		t.Fatalf("missing fallback completion presentation: %+v", completion)
	}
	if feedback == nil || feedback.Role != string(transcript.EntryRoleDeveloperErrorFeedback) {
		t.Fatalf("missing typed mismatch feedback: %+v", feedback)
	}
	for _, file := range completion.Presentation.PatchPresentation.Changes.Files {
		for _, operation := range file.Operations {
			if operation.Deletion != nil && operation.Deletion.Disposition != nil {
				t.Fatalf("fallback fabricated deletion disposition: %+v", completion.Presentation)
			}
		}
	}

	var snapshot TranscriptHydrationSnapshot
	if err := engine.WithTranscriptHydrationSnapshot(func(value TranscriptHydrationSnapshot) error {
		snapshot = value
		return nil
	}); err != nil {
		t.Fatalf("read transcript hydration snapshot: %v", err)
	}
	var toolRow, noticeRow bool
	for _, row := range snapshot.CommittedRows {
		switch row.Kind {
		case TranscriptCommittedRowFactTool:
			toolRow = row.StepID != nil && *row.StepID == stepID
		case TranscriptCommittedRowFactNotice:
			noticeRow = row.StepID != nil && *row.StepID == stepID &&
				row.Notice != nil &&
				row.Notice.Reason == transcript.NoticeReasonRuntimeDiagnostic
		}
	}
	if !toolRow || !noticeRow {
		t.Fatalf("hydrated fallback rows missing tool or diagnostic: %+v", snapshot.CommittedRows)
	}

	outputCount := 0
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.Role != nil && *item.Role == llm.RoleDeveloper {
			t.Fatalf("operator feedback leaked into provider items: %+v", item)
		}
		if isToolOutputItem(item.Type) && item.CallID != nil && *item.CallID == result.CallID {
			outputCount++
		}
	}
	if outputCount != 1 {
		t.Fatalf("provider tool outputs = %d, want one", outputCount)
	}
}
