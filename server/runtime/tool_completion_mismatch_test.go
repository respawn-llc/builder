package runtime

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestToolCompletionDeletionMismatchPanicsBeforePersistenceInDebug(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Debug: true,
	})
	result, mismatch := seedMismatchedDeletionCompletion(t, engine)

	defer func() {
		recovered := recover()
		failure, ok := recovered.(toolCompletionPresentationPanic)
		if !ok {
			t.Fatalf("panic = %#v, want typed tool completion presentation panic", recovered)
		}
		if failure.CallID != result.CallID ||
			failure.ToolName != result.Name ||
			failure.Mismatch == nil ||
			failure.Mismatch.Kind != mismatch.Kind ||
			!slices.Equal(failure.Mismatch.ExpectedOperationIDs, mismatch.ExpectedOperationIDs) ||
			!slices.Equal(failure.Mismatch.ReceivedOperationIDs, mismatch.ReceivedOperationIDs) {
			t.Fatalf("typed panic context = %+v, want call/tool/mismatch %+v", failure, mismatch)
		}
		events, err := sessiontest.CollectEvents(store)
		if err != nil {
			t.Fatalf("collect events after debug panic: %v", err)
		}
		if len(events) != 1 || events[0].Kind != "message" {
			t.Fatalf("debug mismatch persisted completion events: %+v", events)
		}
		if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); !ok {
			t.Fatal("debug mismatch removed the live tool before persistence")
		}
	}()

	_, _ = engine.steerWithCommitReceipt("step-delete", steerToolCompletionIntent(result))
}

func TestToolCompletionDeletionMismatchReleaseFallbackUsesCommitReceiptAuthority(t *testing.T) {
	t.Run("materialized tool output hydration", func(t *testing.T) {
		store := mustCreateTestSession(t)
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model: "gpt-5",
			Debug: false,
		})
		result, _ := seedMismatchedDeletionCompletion(t, engine)

		if err := engine.steer("step-delete", steerToolCompletionIntent(result)); err != nil {
			t.Fatalf("persist release fallback: %v", err)
		}
		if err := engine.steer(
			"step-delete",
			steerMessagesWithPersistenceIntent(
				steeringPriorityNormal,
				steeringMessageEventDefault,
				true,
				[]llm.Message{{
					Role:        llm.RoleTool,
					MessageType: llm.ToolOutputMessageType(true),
					ToolCallID:  result.CallID,
					Name:        string(result.Name),
					Content:     string(result.Output),
				}},
			),
		); err != nil {
			t.Fatalf("persist materialized tool output: %v", err)
		}

		assertDeletionFallbackHydration(t, engine, 2)
		durable, err := sessiontest.CollectEvents(store)
		if err != nil {
			t.Fatalf("collect durable events: %v", err)
		}
		if len(durable) != 4 ||
			durable[0].Kind != "message" ||
			durable[1].Kind != "tool_completed" ||
			durable[2].Kind != "local_entry" ||
			durable[3].Kind != "message" {
			t.Fatalf("durable fallback sequence = %+v", durable)
		}
		var feedback storedLocalEntry
		if err := json.Unmarshal(durable[2].Payload, &feedback); err != nil {
			t.Fatalf("decode fallback feedback: %v", err)
		}
		if feedback.AfterToolCallID == nil || *feedback.AfterToolCallID != result.CallID {
			t.Fatalf("fallback feedback attachment = %+v, want call %q", feedback.AfterToolCallID, result.CallID)
		}

		reopened := mustOpenTestSession(t, store.Dir())
		restored := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
			Model: "gpt-5",
		})
		assertDeletionFallbackHydration(t, restored, 2)
	})

	t.Run("uncommitted append", func(t *testing.T) {
		store := mustCreateTestSession(t)
		var emitted []Event
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			Debug:   false,
			OnEvent: func(event Event) { emitted = append(emitted, event) },
		})
		result, _ := seedMismatchedDeletionCompletion(t, engine)
		emitted = nil
		blocker := mustBlockTestEventLogAppends(t, store)

		receipt, err := engine.steerWithCommitReceipt(
			"step-delete",
			steerToolCompletionIntent(result),
		)
		if err == nil || receipt.Committed {
			t.Fatalf("uncommitted fallback outcome: receipt=%+v err=%v", receipt, err)
		}
		if restoreErr := blocker.Restore(); restoreErr != nil {
			t.Fatalf("restore event-log blocker: %v", restoreErr)
		}
		events, collectErr := sessiontest.CollectEvents(store)
		if collectErr != nil {
			t.Fatalf("collect durable events: %v", collectErr)
		}
		if len(events) != 1 || events[0].Kind != "message" {
			t.Fatalf("uncommitted fallback durable events: %+v", events)
		}
		if engine.transcriptRuntimeState().ToolCompletionCount() != 0 {
			t.Fatal("uncommitted fallback recorded a tool completion")
		}
		assertDeletionFallbackHydration(t, engine, 0)
		if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); !ok {
			t.Fatal("uncommitted fallback removed the live tool")
		}
		if len(emitted) != 0 {
			t.Fatalf("uncommitted fallback emitted client events: %+v", emitted)
		}
	})

	t.Run("committed observer error", func(t *testing.T) {
		observerErr := errors.New("fallback observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateNamedTestSession(
			t,
			"ws",
			t.TempDir(),
			session.WithPersistenceObserver(gate),
		)
		var emitted []Event
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			Debug:   false,
			OnEvent: func(event Event) { emitted = append(emitted, event) },
		})
		result, _ := seedMismatchedDeletionCompletion(t, engine)
		emitted = nil
		gate.FailNext(observerErr)

		receipt, err := engine.steerWithCommitReceipt(
			"step-delete",
			steerToolCompletionIntent(result),
		)
		if !receipt.Committed || !errors.Is(err, observerErr) {
			t.Fatalf("committed fallback outcome: receipt=%+v err=%v", receipt, err)
		}
		durable, collectErr := sessiontest.CollectEvents(store)
		if collectErr != nil {
			t.Fatalf("collect durable events: %v", collectErr)
		}
		if len(durable) != 3 ||
			durable[0].Kind != "message" ||
			durable[1].Kind != "tool_completed" ||
			durable[2].Kind != "local_entry" {
			t.Fatalf("durable fallback order = %+v, want tool completion then local entry", durable)
		}
		if engine.transcriptRuntimeState().ToolCompletionCount() != 1 {
			t.Fatal("committed fallback did not record exactly one tool completion")
		}
		assertDeletionFallbackHydration(t, engine, 2)
		if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); ok {
			t.Fatal("committed fallback retained the live tool")
		}
		if len(emitted) != 2 ||
			emitted[0].Kind != EventToolCallCompleted ||
			emitted[1].Kind != EventLocalEntryAdded ||
			emitted[0].ToolResult == nil ||
			emitted[0].ToolResult.IsError != result.IsError ||
			!slices.Equal(emitted[0].ToolResult.Output, result.Output) ||
			emitted[1].LocalEntry == nil ||
			emitted[1].LocalEntry.Role != string(transcript.EntryRoleDeveloperErrorFeedback) {
			t.Fatalf("fallback client event order/content = %+v", emitted)
		}
		assertFallbackPresentationIsPathOnly(t, emitted[0].ToolResult.Presentation)
		assertProviderHistoryExcludesOperatorFallback(t, engine, result)

		reopened := mustOpenTestSession(t, store.Dir())
		restored := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
			Model: "gpt-5",
		})
		assertDeletionFallbackHydration(t, restored, 2)
		assertProviderHistoryExcludesOperatorFallback(t, restored, result)
	})
}

func seedMismatchedDeletionCompletion(
	t *testing.T,
	engine *Engine,
) (tools.Result, *patchformat.WholeFileDeletionFactMismatch) {
	t.Helper()
	call := llm.ToolCall{
		ID:          "f3d2777d-4541-4bea-9270-d43efad59692",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: "*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
	}
	normalized := normalizeToolCallForTranscript(call, engine.transcriptWorkingDir())
	if err := engine.steer(
		"step-delete",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:      llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{normalized},
			}},
		),
	); err != nil {
		t.Fatalf("persist deletion call: %v", err)
	}
	if err := engine.transcriptRuntimeState().RecordLiveToolStart("step-delete", normalized); err != nil {
		t.Fatalf("record live deletion call: %v", err)
	}
	received := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 9}
	group := patchformat.WholeFileDeletionGroupID{FirstOperation: received}
	count := 3
	mismatch := &patchformat.WholeFileDeletionFactMismatch{
		Kind:                 patchformat.WholeFileDeletionFactMismatchUnexpectedOperation,
		ExpectedOperationIDs: []patchformat.WholeFileDeletionOperationID{{HunkOrdinal: 0}},
		ReceivedOperationIDs: []patchformat.WholeFileDeletionOperationID{received},
		PhysicalGroup:        &group,
		Removed:              &count,
	}
	return tools.Result{
		CallID:  call.ID,
		Name:    toolspec.ToolPatch,
		IsError: false,
		Output:  json.RawMessage(`{"ok":true}`),
		Summary: "applied",
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				PhysicalGroup: group,
				OperationIDs:  []patchformat.WholeFileDeletionOperationID{received},
				Removed:       count,
			}},
		},
	}, mismatch
}

func assertDeletionFallbackHydration(t *testing.T, engine *Engine, wantRows int) {
	t.Helper()
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		if len(snapshot.CommittedRows) != wantRows {
			t.Fatalf("hydration rows = %+v, want %d", snapshot.CommittedRows, wantRows)
		}
		if wantRows == 2 {
			if snapshot.CommittedRows[0].Kind != TranscriptCommittedRowFactTool ||
				snapshot.CommittedRows[0].StepID != "step-delete" ||
				snapshot.CommittedRows[1].Kind != TranscriptCommittedRowFactNotice ||
				snapshot.CommittedRows[1].StepID != "step-delete" ||
				snapshot.CommittedRows[1].Notice == nil ||
				snapshot.CommittedRows[1].Notice.Reason != transcript.NoticeReasonRuntimeDiagnostic ||
				snapshot.CommittedRows[1].Notice.DiagnosticCode != string(transcript.EntryRoleDeveloperErrorFeedback) {
				t.Fatalf("hydration fallback order = %+v", snapshot.CommittedRows)
			}
			assertFallbackPresentationIsPathOnly(
				t,
				snapshot.CommittedRows[0].Tool.Presentation,
			)
		}
		return nil
	}); err != nil {
		t.Fatalf("hydrate runtime transcript: %v", err)
	}
}

func assertFallbackPresentationIsPathOnly(t *testing.T, meta *transcript.ToolCallMeta) {
	t.Helper()
	if meta == nil || meta.PatchRender == nil ||
		len(meta.PatchRender.Files) != 1 ||
		len(meta.PatchRender.Files[0].WholeFileDeletions) != 1 ||
		meta.PatchRender.Files[0].WholeFileDeletions[0].Disposition != nil {
		t.Fatalf("fallback presentation = %+v, want prepared path-only deletion", meta)
	}
}

func assertProviderHistoryExcludesOperatorFallback(
	t *testing.T,
	engine *Engine,
	result tools.Result,
) {
	t.Helper()
	items := engine.transcriptRuntimeState().SnapshotItems()
	outputCount := 0
	for _, item := range items {
		if item.CallID == result.CallID && slices.Equal(item.Output, result.Output) {
			outputCount++
		}
		if item.Role == llm.RoleDeveloper {
			t.Fatalf("operator-only fallback leaked into provider items: %+v", item)
		}
	}
	if outputCount != 1 {
		t.Fatalf("provider output count = %d, want one ordinary successful tool output: %+v", outputCount, items)
	}
}
