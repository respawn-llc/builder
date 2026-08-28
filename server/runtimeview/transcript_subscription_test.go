package runtimeview

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"github.com/google/uuid"
)

func transcriptPayload[T any](t *testing.T, event clientui.TranscriptEvent) T {
	t.Helper()
	payload, ok := event.Payload().(T)
	if !ok {
		t.Fatalf("transcript payload type = %T, want %T", event.Payload(), *new(T))
	}
	return payload
}

func TestTranscriptProjectionOwnsNestedDeletionPresentation(t *testing.T) {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	source := &transcript.ToolCallMeta{
		ToolName: "patch",
		PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
			RelPath: "target.txt",
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: id,
				Disposition: &patchformat.WholeFileDeletionDisposition{
					PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
					Removed:       3,
				},
			}},
		}}},
	}

	cloned := cloneToolCallMeta(source)
	cloned.PatchRender.Files[0].WholeFileDeletions[0].Disposition.Removed = 9
	cloned.PatchRender.Files[0].WholeFileDeletions[0].Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 4

	disposition := source.PatchRender.Files[0].WholeFileDeletions[0].Disposition
	if disposition == nil || disposition.Removed != 3 ||
		disposition.PhysicalGroup.FirstOperation.HunkOrdinal != 0 {
		t.Fatalf("runtimeview clone aliased nested deletion metadata: %+v", disposition)
	}
}

func TestTranscriptCacheWarningProjectionPreservesAbsentTokenLoss(t *testing.T) {
	notice := transcriptNoticeFromFact(nil, &runtime.TranscriptNoticeRowFact{
		Reason:   transcript.NoticeReasonCacheWarning,
		Severity: transcript.NoticeSeverityWarning,
		CacheWarning: &runtime.TranscriptCacheWarningFact{
			Scope:      string(transcript.CacheWarningScopeConversation),
			Reason:     string(transcript.CacheWarningReasonCompaction),
			Visibility: transcript.EntryVisibilityOngoing,
		},
	})
	if notice.CacheWarning == nil {
		t.Fatal("cache-warning projection is absent")
	}
	if notice.CacheWarning.LostInputTokens != nil {
		t.Fatalf("projected absent token loss = %v, want nil", *notice.CacheWarning.LostInputTokens)
	}
}

func TestTranscriptProviderModelMismatchProjectionPreservesTypedFacts(t *testing.T) {
	mismatch := transcript.ProviderModelMismatchNotice{
		RequestedModel: "requested-model",
		ServedModel:    "served-model",
	}
	notice := transcriptNoticeFromFact(nil, &runtime.TranscriptNoticeRowFact{
		Reason:                transcript.NoticeReasonProviderModelMismatch,
		Severity:              transcript.NoticeSeverityWarning,
		ProviderModelMismatch: &mismatch,
	})
	if notice.ProviderModelMismatch == nil ||
		notice.ProviderModelMismatch.RequestedModel != mismatch.RequestedModel ||
		notice.ProviderModelMismatch.ServedModel != mismatch.ServedModel {
		t.Fatalf("provider-model mismatch projection = %+v, want %+v", notice.ProviderModelMismatch, mismatch)
	}
}

func TestTranscriptCompactionProjectionCarriesTypedFactsWithoutServerPresentation(t *testing.T) {
	count := 2
	detail := "provider summary"
	notice := transcriptNoticeFromFact(nil, &runtime.TranscriptNoticeRowFact{
		Reason:      transcript.NoticeReasonCompaction,
		Severity:    transcript.NoticeSeverityInfo,
		MessageType: llm.MessageTypeCompactionSummary,
		Compaction: &runtime.TranscriptCompactionNoticeFact{
			Count:  &count,
			Detail: &detail,
		},
	})
	if notice.Compaction == nil ||
		notice.Compaction.Count == nil ||
		*notice.Compaction.Count != count ||
		notice.Compaction.Detail == nil ||
		*notice.Compaction.Detail != detail {
		t.Fatalf("compaction projection = %+v", notice.Compaction)
	}
	if notice.CompactLabel != nil || notice.CondensedText != nil || notice.Diagnostic != nil {
		t.Fatalf("server-authored compaction presentation leaked into client contract: %+v", notice)
	}
}

func TestTranscriptCompactionStatusPreservesInitiatingRequestIdentity(t *testing.T) {
	requestID := runtimeids.NewCompactionRequestID()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCompactionCompleted,
		StepID: runtimeStepIDPointer(transcriptProjectionStepID),
		Compaction: &runtime.CompactionStatus{
			Mode:      "manual",
			RequestID: &requestID,
			Count:     1,
		},
	})
	if len(messages) != 1 {
		t.Fatalf("compaction messages = %+v, want one", messages)
	}
	status := transcriptPayload[clientui.TranscriptCompactionStatus](t, messages[0])
	if status.RequestID == nil || *status.RequestID != requestID {
		t.Fatalf("projected request identity = %v, want %s", status.RequestID, requestID.String())
	}
}

func TestTranscriptHydrationPreservesDeletionDispositionPresence(t *testing.T) {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	tests := []struct {
		name        string
		disposition *patchformat.WholeFileDeletionDisposition
		wantRemoved *int
	}{
		{name: "absent"},
		{
			name: "present zero",
			disposition: &patchformat.WholeFileDeletionDisposition{
				PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
				Removed:       0,
			},
			wantRemoved: textutil.Value(0),
		},
		{
			name: "present positive",
			disposition: &patchformat.WholeFileDeletionDisposition{
				PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
				Removed:       4,
			},
			wantRemoved: textutil.Value(4),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hydration := mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{
				CommittedRows: []runtime.TranscriptCommittedRowFact{{
					StepID:     runtimeStepIDPointer(transcriptProjectionStepID),
					Visibility: transcript.EntryVisibilityOngoingCollapsed,
					Integrity:  transcript.RowIntegrityValid,
					Kind:       runtime.TranscriptCommittedRowFactTool,
					Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
					Provenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 1},
					Tool: &runtime.TranscriptToolRowFact{
						ToolCallID: "call-delete",
						ToolName:   "patch",
						Presentation: &transcript.ToolCallMeta{
							ToolName: "patch",
							PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
								RelPath: "target.txt",
								WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
									ID:          id,
									Disposition: test.disposition,
								}},
							}}},
						},
					},
				}},
			})
			if len(hydration.CommittedRows) != 1 ||
				hydration.CommittedRows[0].Tool == nil ||
				hydration.CommittedRows[0].Tool.Presentation == nil ||
				hydration.CommittedRows[0].Tool.Presentation.PatchRender == nil {
				t.Fatalf("projected deletion row = %+v", hydration.CommittedRows)
			}
			file := hydration.CommittedRows[0].Tool.Presentation.PatchRender.Files[0]
			removed := patchformat.RemovedLineCount(file)
			if test.wantRemoved == nil {
				if removed != nil {
					t.Fatalf("projected removed count = %d, want absent", *removed)
				}
				return
			}
			if removed == nil || *removed != *test.wantRemoved {
				t.Fatalf("projected removed count = %v, want %d", removed, *test.wantRemoved)
			}
		})
	}
}

const (
	transcriptProjectionRunID  = "10000000-0000-4000-8000-000000000011"
	transcriptProjectionStepID = "10000000-0000-4000-8000-000000000012"
)

func mustTranscriptHydration(t *testing.T, snapshot runtime.TranscriptHydrationSnapshot) clientui.TranscriptHydration {
	t.Helper()
	hydration, err := TranscriptHydrationFromSnapshotChecked(snapshot)
	if err != nil {
		t.Fatalf("TranscriptHydrationFromSnapshot: %v", err)
	}
	return hydration
}

func TestTranscriptHydrationCarriesRuntimeNativeAssistantStreamIdentity(t *testing.T) {
	streamID := uuid.MustParse("f84c7d21-4c94-4a54-87fd-b41f5bd01d38")
	hydration := mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{
		ActiveAssistantText:     "hello",
		ActiveAssistantMetadata: &runtime.AssistantStreamMetadata{StepID: transcriptProjectionStepID},
		ActiveAssistantStreamID: &streamID,
		ActiveAssistantPhase:    llm.MessagePhaseFinal,
	})
	if hydration.ActiveAssistant == nil {
		t.Fatal("expected active assistant stream in hydration")
	}
	if got := hydration.ActiveAssistant.StreamID.String(); got != streamID.String() {
		t.Fatalf("active assistant stream id = %q, want %q", got, streamID.String())
	}
	if hydration.ActiveAssistant.Text != "hello" {
		t.Fatalf("active assistant stream text = %q, want hello", hydration.ActiveAssistant.Text)
	}
	if hydration.ActiveAssistant.Phase != transcript.AssistantPhaseFinal {
		t.Fatalf("active assistant stream phase = %q, want final", hydration.ActiveAssistant.Phase)
	}
}

func TestTranscriptHydrationProjectsRuntimeOwnedFacts(t *testing.T) {
	queueItemID := runtimeids.NewQueueItemID()
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		ActiveThinkingStatus: &runtime.TranscriptThinkingStatusState{
			StepID: transcriptProjectionStepID, Text: "Planning",
		},
		ActiveReasoningTraces: []runtime.TranscriptReasoningTraceState{{
			StepID: transcriptProjectionStepID,
			Identity: runtime.TranscriptReasoningTraceIdentity{Kent: func() *runtimeids.ReasoningTraceID {
				id := runtimeids.NewReasoningTraceID()
				return &id
			}()},
			Text: "inspect",
		}},
		InFlightTools: []runtime.TranscriptLiveToolStart{{StepID: transcriptProjectionStepID, ToolCallID: "call-1", ToolName: "shell"}},
		PendingWork: runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{{
			ID: queueItemID, Lane: runtimeinput.PendingWorkLaneQueue,
			Kind: runtimeinput.PendingWorkItemKindMessage, State: runtimeinput.PendingWorkItemStatePending,
			Message: &runtimeinput.PendingWorkMessage{Text: "queued"},
		}}},
		ActiveCompaction: &runtime.TranscriptCompactionState{StepID: transcriptProjectionStepID, Mode: "auto", Count: 3},
		ContextUsage:     &runtime.ContextUsage{UsedTokens: 123, WindowTokens: 4000, CacheHitPercent: 25, HasCacheHitPercentage: true},
		Goal:             &session.GoalState{ID: "goal-1", Objective: "ship", Status: session.GoalStatusActive},
		GoalSuspended:    true,
	})
	if hydration.ActiveThinkingStatus == nil || hydration.ActiveThinkingStatus.Text != "Planning" ||
		len(hydration.ActiveReasoningTraces) != 1 || hydration.ActiveReasoningTraces[0].Text != "inspect" {
		t.Fatalf("reasoning = status %+v traces %+v", hydration.ActiveThinkingStatus, hydration.ActiveReasoningTraces)
	}
	if len(hydration.InFlightTools) != 1 || hydration.InFlightTools[0].ToolCallID != "call-1" {
		t.Fatalf("tools = %+v", hydration.InFlightTools)
	}
	if len(hydration.PendingWork.Items) != 1 || hydration.PendingWork.Items[0].ID != queueItemID {
		t.Fatalf("Pending Work = %+v", hydration.PendingWork)
	}
	if hydration.ActiveCompaction == nil || hydration.ActiveCompaction.Count != 3 {
		t.Fatalf("compaction = %+v", hydration.ActiveCompaction)
	}
	if hydration.ContextUsage == nil || hydration.ContextUsage.CacheHitPercent == nil ||
		*hydration.ContextUsage.CacheHitPercent != 25 || hydration.GoalStatus == nil ||
		hydration.GoalStatus.Goal == nil || !hydration.GoalStatus.Goal.Suspended {
		t.Fatalf("usage/goal = usage %+v goal %+v", hydration.ContextUsage, hydration.GoalStatus)
	}
}

func TestTranscriptReasoningHydrationAndLivePreserveOrderedIdentities(t *testing.T) {
	firstIndex, secondIndex := int64(0), int64(1)
	firstID := runtimeids.NewReasoningTraceID()
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		ActiveReasoningTraces: []runtime.TranscriptReasoningTraceState{
			{StepID: transcriptProjectionStepID, Identity: runtime.TranscriptReasoningTraceIdentity{Kent: &firstID}, Text: "first"},
			{StepID: transcriptProjectionStepID, Identity: runtime.TranscriptReasoningTraceIdentity{Provider: &llm.ReasoningItemIdentity{ItemID: "second", PartIndex: &secondIndex}}, Text: "second"},
		},
	})
	if len(hydration.ActiveReasoningTraces) != 2 || hydration.ActiveReasoningTraces[0].Identity.Kent == nil ||
		hydration.ActiveReasoningTraces[1].Identity.Provider == nil {
		t.Fatalf("hydrated reasoning order = %+v", hydration.ActiveReasoningTraces)
	}
	output := int64(0)
	live := append(
		TranscriptMessagesFromRuntimeEvent(runtime.Event{
			Kind: runtime.EventReasoningDelta, StepID: runtimeStepIDPointer(transcriptProjectionStepID),
			ReasoningDelta: &llm.ReasoningSummaryDelta{
				SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &output, PartIndex: &firstIndex},
				Text:             "first",
			},
			ReasoningTraceIdentity: &runtime.TranscriptReasoningTraceIdentity{Kent: &firstID},
		}),
		TranscriptMessagesFromRuntimeEvent(runtime.Event{
			Kind: runtime.EventReasoningDelta, StepID: runtimeStepIDPointer(transcriptProjectionStepID),
			ReasoningDelta: &llm.ReasoningSummaryDelta{
				SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &output, PartIndex: &secondIndex},
				Text:             "second",
			},
			ReasoningTraceIdentity: &runtime.TranscriptReasoningTraceIdentity{
				Provider: &llm.ReasoningItemIdentity{ItemID: "second", PartIndex: &secondIndex},
			},
		})...,
	)
	if len(live) != 2 {
		t.Fatalf("live reasoning messages = %+v", live)
	}
	for index, event := range live {
		update := transcriptPayload[clientui.TranscriptReasoningTraceUpdate](t, event)
		hydrated := hydration.ActiveReasoningTraces[index]
		if update.Identity.String() != hydrated.Identity.String() ||
			update.Text != hydrated.Text {
			t.Fatalf("live/hydration reasoning mismatch at %d: live=%+v hydration=%+v", index, update, hydrated)
		}
	}
}

func TestTranscriptCommittedRowsPreserveRuntimeVisibility(t *testing.T) {
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                runtime.EventLocalEntryAdded,
		StepID:              runtimeStepIDPointer(transcriptProjectionStepID),
		LocalEntryProjected: true,
		LocalEntry: &runtime.ChatEntry{
			Visibility: transcript.EntryVisibilityDetail,
			Role:       "user",
			Text:       "detail-only row",
			CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
				EventSequence: 1,
			},
		},
	})
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one committed row", messages)
	}
	row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
	if got := row.Visibility; got != transcript.EntryVisibilityDetail {
		t.Fatalf("committed row visibility = %q, want detail", got)
	}

	hydration := mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{{
			StepID:     runtimeStepIDPointer(transcriptProjectionStepID),
			Visibility: transcript.EntryVisibilityHidden,
			Kind:       runtime.TranscriptCommittedRowFactUser,
			Locator:    transcript.CommittedRowLocator{EventSequence: 2, RowOrdinal: 1},
			User:       &runtime.TranscriptUserRowFact{Text: "hidden row"},
			Provenance: &runtime.TranscriptCommittedRowProvenance{
				EventSequence: 2,
			},
		}},
	})
	if len(hydration.CommittedRows) != 1 {
		t.Fatalf("hydration rows = %+v, want one committed row", hydration.CommittedRows)
	}
	if got := hydration.CommittedRows[0].Visibility; got != clientui.EntryVisibilityHidden {
		t.Fatalf("hydration visibility = %q, want hidden", got)
	}
}

func TestTranscriptCommittedRowsProjectCommitTime(t *testing.T) {
	committedAt := transcript.CommittedAtUnixMs(123)
	stepID := runtimeStepIDPointer(transcriptProjectionStepID)
	for _, fact := range []runtime.TranscriptCommittedRowFact{
		{
			Kind: runtime.TranscriptCommittedRowFactUser,
			User: &runtime.TranscriptUserRowFact{
				Text:              "user",
				CommittedAtUnixMs: &committedAt,
			},
		},
		{
			StepID: stepID,
			Kind:   runtime.TranscriptCommittedRowFactAssistant,
			Assistant: &runtime.TranscriptAssistantRowFact{
				Text:              "assistant",
				Phase:             llm.MessagePhaseFinal,
				CommittedAtUnixMs: &committedAt,
			},
		},
	} {
		row := transcriptRowFromFact(fact)
		switch {
		case row.User != nil:
			if row.User.CommittedAtUnixMs == nil || row.User.CommittedAtUnixMs.UnixMs() != committedAt.UnixMs() {
				t.Fatalf("user committed time = %v, want %d", row.User.CommittedAtUnixMs, committedAt.UnixMs())
			}
		case row.Assistant != nil:
			if row.Assistant.CommittedAtUnixMs == nil || row.Assistant.CommittedAtUnixMs.UnixMs() != committedAt.UnixMs() {
				t.Fatalf("assistant committed time = %v, want %d", row.Assistant.CommittedAtUnixMs, committedAt.UnixMs())
			}
		default:
			t.Fatalf("projected row = %+v, want user or assistant", row)
		}
	}
}

func TestRuntimeScopedUserFlushProjectsWithoutExactStep(t *testing.T) {
	messages, err := TranscriptMessagesFromRuntimeEventChecked(runtime.Event{
		Kind:             runtime.EventUserMessageFlushed,
		UserMessage:      "idle input",
		UserMessageBatch: []string{"idle input"},
		UserMessageBatchQueuedItems: []runtime.QueuedUserMessageIdentity{{
			QueueItemID: "10000000-0000-4000-8000-000000000020",
		}},
		CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 1},
	})
	if err != nil {
		t.Fatalf("project Runtime user flush: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("Runtime user flush messages = %+v, want committed row and flush", messages)
	}
	var userRow *clientui.TranscriptUserRow
	var flushed *clientui.TranscriptUserMessageFlushed
	for index := range messages {
		if err := messages[index].Validate(); err != nil {
			t.Fatalf("Runtime user flush message %d failed validation: %v", index, err)
		}
		switch messages[index].Kind() {
		case clientui.TranscriptMessageCommittedRow:
			row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[index])
			userRow = row.User
		case clientui.TranscriptMessageUserMessageFlushed:
			payload := transcriptPayload[clientui.TranscriptUserMessageFlushed](t, messages[index])
			flushed = &payload
		}
	}
	if userRow == nil || userRow.StepID != nil || flushed == nil || flushed.StepID != nil {
		t.Fatalf("Runtime user/flush Step provenance = user:%+v flush:%+v, want absent", userRow, flushed)
	}
}

func TestRuntimeScopedToolCompletionProjectsLiveAndHydratedWithoutExactStep(t *testing.T) {
	result := tools.Result{
		CallID: "call-runtime",
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"output":"done"}`),
	}
	messages, err := TranscriptMessagesFromRuntimeEventChecked(runtime.Event{
		Kind:                runtime.EventToolCallCompleted,
		ToolResult:          &result,
		CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 2},
	})
	if err != nil {
		t.Fatalf("project Runtime tool completion: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Runtime tool completion messages = %+v, want one committed row", messages)
	}
	if err := messages[0].Validate(); err != nil {
		t.Fatalf("Runtime tool completion failed validation: %v", err)
	}
	liveRow := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
	if liveRow.Tool == nil || liveRow.Tool.StepID != nil {
		t.Fatalf("Runtime live tool Step provenance = %+v, want absent", liveRow.Tool)
	}

	hydration, err := TranscriptHydrationFromSnapshotChecked(runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{
			{
				Visibility: transcript.EntryVisibilityOngoing,
				Integrity:  transcript.RowIntegrityValid,
				Kind:       runtime.TranscriptCommittedRowFactUser,
				Locator:    transcript.CommittedRowLocator{EventSequence: 3, RowOrdinal: 1},
				Provenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 3},
				User:       &runtime.TranscriptUserRowFact{Text: "idle input"},
			},
			{
				Visibility: transcript.EntryVisibilityOngoingCollapsed,
				Integrity:  transcript.RowIntegrityValid,
				Kind:       runtime.TranscriptCommittedRowFactTool,
				Locator:    transcript.CommittedRowLocator{EventSequence: 4, RowOrdinal: 1},
				Provenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 4},
				Tool: &runtime.TranscriptToolRowFact{
					ToolCallID: "call-runtime",
					ToolName:   string(toolspec.ToolExecCommand),
					Text:       "done",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("hydrate Runtime-scoped rows: %v", err)
	}
	if len(hydration.CommittedRows) != 2 {
		t.Fatalf("Runtime hydration rows = %+v, want user and tool", hydration.CommittedRows)
	}
	for index := range hydration.CommittedRows {
		if err := hydration.CommittedRows[index].Validate(); err != nil {
			t.Fatalf("Runtime hydration row %d failed validation: %v", index, err)
		}
	}
	if hydration.CommittedRows[0].User == nil || hydration.CommittedRows[0].User.StepID != nil ||
		hydration.CommittedRows[1].Tool == nil || hydration.CommittedRows[1].Tool.StepID != nil {
		t.Fatalf("Runtime hydration Step provenance = %+v, want absent", hydration.CommittedRows)
	}
}

func TestTranscriptCommittedReasoningEventCarriesDedicatedPayload(t *testing.T) {
	part := int64(0)
	durationMs := int64(321)
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:   runtime.EventLocalEntryAdded,
		StepID: runtimeStepIDPointer(transcriptProjectionStepID),
		LocalEntry: &runtime.ChatEntry{
			Visibility: transcript.EntryVisibilityDetail,
			Role:       string(transcript.EntryRoleReasoning),
			Text:       "**Planning\nDetails**",
			DurationMs: &durationMs,
		},
		ReasoningTraceIdentity: &runtime.TranscriptReasoningTraceIdentity{
			Provider: &llm.ReasoningItemIdentity{ItemID: "reason_1", PartIndex: &part},
		},
		CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 1},
	})
	if len(messages) != 1 {
		t.Fatalf("reasoning messages = %+v, want one committed row", messages)
	}
	row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
	if row.Kind != clientui.TranscriptRowReasoningTrace ||
		row.ReasoningTrace == nil ||
		row.ReasoningTrace.Text != "Planning\nDetails" ||
		row.ReasoningTrace.CompactText != "Planning" ||
		row.ReasoningTrace.DurationMs == nil ||
		*row.ReasoningTrace.DurationMs != durationMs ||
		row.ReasoningTrace.ProvisionalIdentity == nil {
		t.Fatalf("projected reasoning row = %+v", row)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("projected reasoning row failed validation: %v", err)
	}
}

func TestTranscriptReasoningDurationProjectsHydrationAndBoundedPage(t *testing.T) {
	durationMs := int64(321)
	fact := runtime.TranscriptCommittedRowFact{
		StepID:  runtimeStepIDPointer(transcriptProjectionStepID),
		Kind:    runtime.TranscriptCommittedRowFactReasoningTrace,
		Locator: transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
		ReasoningTrace: &runtime.TranscriptReasoningTraceRowFact{
			Text:        "Planning\nDetails",
			CompactText: "Planning",
			DurationMs:  &durationMs,
		},
	}
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{fact},
	})
	if len(hydration.CommittedRows) != 1 || hydration.CommittedRows[0].ReasoningTrace == nil ||
		hydration.CommittedRows[0].ReasoningTrace.DurationMs == nil ||
		*hydration.CommittedRows[0].ReasoningTrace.DurationMs != durationMs {
		t.Fatalf("hydrated reasoning duration = %+v", hydration.CommittedRows)
	}

	page, err := TranscriptPageFromSegment(
		"58e121b5-30f7-4d0f-a1fa-fb3e6695e39c",
		"name",
		clientui.ConversationFreshnessEstablished,
		runtime.TranscriptSegmentPage{Snapshot: runtime.ChatSnapshot{Entries: []runtime.ChatEntry{{
			StepID:     runtimeStepIDPointer(transcriptProjectionStepID),
			Role:       string(transcript.EntryRoleReasoning),
			Text:       "Planning\nDetails",
			DurationMs: &durationMs,
			CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
				EventSequence: 1,
			},
		}}}},
	)
	if err != nil {
		t.Fatalf("project bounded transcript page: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].ReasoningTrace == nil ||
		page.Entries[0].ReasoningTrace.DurationMs == nil ||
		*page.Entries[0].ReasoningTrace.DurationMs != durationMs {
		t.Fatalf("paged reasoning duration = %+v", page.Entries)
	}
}

func TestUnknownToolExecutionProjectsFinalizedFailedInput(t *testing.T) {
	input := json.RawMessage(`{}`)
	call := llm.ToolCall{
		ID:    "call-unknown",
		Name:  "final_answer",
		Input: input,
	}
	client := scriptedllm.NewClient(scriptedllm.Script{
		Steps: []scriptedllm.Step{
			scriptedllm.ToolBatch("", call),
			{
				ExpectedToolResults: []scriptedllm.ExpectedToolResult{{
					CallID: call.ID,
					Name:   call.Name,
				}},
				Response: scriptedllm.FinalAnswer("done").Response,
			},
		},
	})
	store := newRuntimeViewStore(t)
	var completion runtime.Event
	engine := newRuntimeViewEngine(t, store, client, runtime.Config{
		Model: "gpt-5",
		OnEvent: func(evt runtime.Event) {
			if evt.Kind == runtime.EventToolCallCompleted && evt.ToolResult != nil {
				completion = evt
			}
		},
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "exercise unknown tool"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if remaining := client.RemainingSteps(); remaining != 0 {
		t.Fatalf("scripted LLM steps remaining = %d, want continuation after tool result consumed", remaining)
	}
	if completion.ToolResult == nil || !completion.ToolResult.IsError || completion.ToolResult.Presentation == nil {
		t.Fatalf("emitted completion = %+v, want finalized failed result", completion.ToolResult)
	}
	if completion.ToolResult.Presentation.Command != string(input) ||
		completion.ToolResult.Presentation.CompactText != string(input) {
		t.Fatalf("emitted presentation = %+v, want original input preserved", completion.ToolResult.Presentation)
	}

	messages := TranscriptMessagesFromRuntimeEvent(completion)
	var row *clientui.TranscriptCommittedRow
	for _, message := range messages {
		if message.Kind() == clientui.TranscriptMessageCommittedRow {
			candidate := transcriptPayload[clientui.TranscriptCommittedRow](t, message)
			if candidate.Tool == nil {
				continue
			}
			if row != nil {
				t.Fatalf("client transcript messages = %+v, want exactly one committed tool row", messages)
			}
			row = &candidate
		}
	}
	if row == nil {
		t.Fatalf("client transcript messages = %+v, want one committed tool row", messages)
	}
	if row.Kind != clientui.TranscriptRowTool || !row.Tool.IsError {
		t.Fatalf("client transcript row = %+v, want failed tool row", row)
	}
	if row.Tool.Presentation == nil ||
		row.Tool.Presentation.Command != string(input) ||
		row.Tool.Presentation.CompactText != string(input) {
		t.Fatalf("projected tool presentation = %+v, want original input preserved", row.Tool.Presentation)
	}
}

func TestTranscriptPagePreservesRollbackTargetIdentity(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(91)
	locator := &rollbacktarget.CandidateLocator{
		UserMessageSeq:       91,
		CandidatePageEndByte: 2048,
	}
	page, err := TranscriptPageFromSegment(
		"58e121b5-30f7-4d0f-a1fa-fb3e6695e39c",
		"name",
		clientui.ConversationFreshnessEstablished,
		runtime.TranscriptSegmentPage{
			LatestRollbackCandidate: locator,
			Snapshot: runtime.ChatSnapshot{Entries: []runtime.ChatEntry{{
				Role:             "user",
				StepID:           runtimeStepIDPointer(transcriptProjectionStepID),
				Text:             "persisted user prompt",
				RollbackTargetID: &targetID,
				CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
					EventSequence: 5,
				},
			}}},
		},
	)
	if err != nil {
		t.Fatalf("project page: %v", err)
	}

	if len(page.Entries) != 1 || page.Entries[0].User == nil {
		t.Fatalf("transcript page rows = %#v, want one user row", page.Entries)
	}
	if got := page.Entries[0].User.RollbackTargetID; got == nil || *got != targetID {
		t.Fatalf("projected rollback target = %v, want exact target %q", got, targetID)
	}
	if page.LatestRollbackCandidate == nil || *page.LatestRollbackCandidate != *locator {
		t.Fatalf("projected rollback candidate locator = %#v, want %#v", page.LatestRollbackCandidate, locator)
	}
}

func TestTranscriptProjectionCanonicalizesBlankPersistedAssistantPhase(t *testing.T) {
	hydration := mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{{
			StepID:  runtimeStepIDPointer(transcriptProjectionStepID),
			Kind:    runtime.TranscriptCommittedRowFactAssistant,
			Locator: transcript.CommittedRowLocator{EventSequence: 3, RowOrdinal: 1},
			Assistant: &runtime.TranscriptAssistantRowFact{
				Text: "legacy final answer",
			},
		}},
	})
	if len(hydration.CommittedRows) != 1 || hydration.CommittedRows[0].Assistant == nil {
		t.Fatalf("hydration rows = %+v, want one assistant row", hydration.CommittedRows)
	}
	if got := hydration.CommittedRows[0].Assistant.Phase; got != transcript.AssistantPhaseFinal {
		t.Fatalf("persisted assistant phase = %q, want canonical final phase", got)
	}
}

func TestTranscriptPageProjectsReviewerAndBackgroundMetadata(t *testing.T) {
	exitCode := 9
	activityID := uuid.New()
	page, err := TranscriptPageFromSegment("58e121b5-30f7-4d0f-a1fa-fb3e6695e39c", "name", clientui.ConversationFreshnessEstablished, runtime.TranscriptSegmentPage{
		Snapshot: runtime.ChatSnapshot{Entries: []runtime.ChatEntry{
			{Role: "reviewer_status", Text: "review complete", CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 6}},
			{
				Role:                 "system",
				Text:                 "background failed",
				MessageType:          llm.MessageTypeBackgroundNotice,
				BackgroundActivityID: activityID.String(),
				BackgroundProcessID:  "process-1",
				BackgroundExitCode:   &exitCode,
				CommittedProvenance:  &runtime.TranscriptCommittedRowProvenance{EventSequence: 7},
			},
		}},
	})
	if err != nil {
		t.Fatalf("project page: %v", err)
	}

	if len(page.Entries) != 2 {
		t.Fatalf("page entries = %+v", page.Entries)
	}
	reviewerStatus := page.Entries[0]
	if reviewerStatus.Visibility != clientui.EntryVisibilityOngoingCollapsed ||
		reviewerStatus.Kind != clientui.TranscriptRowNotice ||
		reviewerStatus.Notice == nil ||
		reviewerStatus.Notice.Diagnostic == nil ||
		reviewerStatus.Notice.Diagnostic.Code != clientui.TranscriptDiagnosticCode(transcript.EntryRoleReviewerStatus) {
		t.Fatalf("reviewer status row = %+v, want collapsed reviewer status notice", reviewerStatus)
	}
	backgroundRow := page.Entries[1]
	if backgroundRow.Kind != clientui.TranscriptRowNotice || backgroundRow.Notice == nil {
		t.Fatalf("background row = %+v, want notice", backgroundRow)
	}
	if background := backgroundRow.Notice.Background; background == nil || background.ExitCode == nil || *background.ExitCode != exitCode {
		t.Fatalf("background notice = %+v, want exit code %d", background, exitCode)
	}
}

func TestTranscriptHydrationRejectsAssistantStreamWithoutRuntimeIdentity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected partial assistant stream identity panic")
		}
	}()
	_ = mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{
		ActiveAssistantText:     "hello",
		ActiveAssistantMetadata: &runtime.AssistantStreamMetadata{StepID: transcriptProjectionStepID},
	})
}

func TestTranscriptMessagesIgnoreEmptyAssistantDelta(t *testing.T) {
	streamID := uuid.New()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                        runtime.EventAssistantDelta,
		AssistantDelta:              "",
		AssistantTranscriptStreamID: &streamID,
	})
	if len(messages) != 0 {
		t.Fatalf("empty assistant delta messages = %+v, want none", messages)
	}
}

func TestTranscriptMessagesIgnoreNoopAssistantResetWithoutStream(t *testing.T) {
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventAssistantDeltaReset,
		AssistantStreamAbortReason: string(runtime.AssistantStreamAbortSuperseded),
	})
	if len(messages) != 0 {
		t.Fatalf("noop assistant reset messages = %+v, want none", messages)
	}
}

func TestTranscriptMessagesIgnoreFinalizedAssistantReset(t *testing.T) {
	streamID := uuid.New()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                        runtime.EventAssistantDeltaReset,
		StepID:                      runtimeStepIDPointer(transcriptProjectionStepID),
		AssistantTranscriptStreamID: &streamID,
	})
	if len(messages) != 0 {
		t.Fatalf("finalized assistant reset messages = %+v, want committed assistant row to remain the sole terminal", messages)
	}
}

func TestTranscriptBackgroundActivityUsesRuntimeActivityID(t *testing.T) {
	activityID := uuid.New()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:   runtime.EventBackgroundUpdated,
		StepID: runtimeStepIDPointer(transcriptProjectionStepID),
		Background: &runtime.BackgroundShellEvent{
			Type:        runtime.BackgroundShellEventBackgrounded,
			ID:          uuid.NewString(),
			ActivityID:  activityID,
			OwnerRunID:  transcriptProjectionRunID,
			OwnerStepID: transcriptProjectionStepID,
			State:       "running",
			Command:     "go test ./...",
			Workdir:     "/tmp/workspace",
			Preview:     "tests",
		},
	})
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one background activity", messages)
	}
	background := transcriptPayload[clientui.TranscriptBackgroundActivity](t, messages[0])
	if got := background.ActivityID.String(); got != activityID.String() {
		t.Fatalf("background transcript id = %q, want activity id %q", got, activityID)
	}
}

func TestTranscriptBackgroundActivityLifecycleIgnoresPreviewTruncation(t *testing.T) {
	tests := []struct {
		name           string
		eventType      runtime.BackgroundShellEventType
		previewRemoved int
		wantLifecycle  clientui.BackgroundLifecycle
	}{
		{name: "running truncated preview remains live", eventType: runtime.BackgroundShellEventBackgrounded, previewRemoved: 2, wantLifecycle: clientui.BackgroundLifecycleBackgrounded},
		{name: "completed activity is terminal", eventType: runtime.BackgroundShellEventCompleted, wantLifecycle: clientui.BackgroundLifecycleCompleted},
		{name: "killed activity is terminal", eventType: runtime.BackgroundShellEventKilled, wantLifecycle: clientui.BackgroundLifecycleKilled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
				Kind:   runtime.EventBackgroundUpdated,
				StepID: runtimeStepIDPointer(transcriptProjectionStepID),
				Background: &runtime.BackgroundShellEvent{
					Type:           tt.eventType,
					ID:             uuid.NewString(),
					ActivityID:     uuid.New(),
					OwnerRunID:     transcriptProjectionRunID,
					OwnerStepID:    transcriptProjectionStepID,
					State:          string(tt.eventType),
					Command:        "sleep 2",
					Workdir:        "/tmp/workspace",
					PreviewRemoved: tt.previewRemoved,
				},
			})
			if len(messages) != 1 {
				t.Fatalf("messages = %+v, want one background activity", messages)
			}
			background := transcriptPayload[clientui.TranscriptBackgroundActivity](t, messages[0])
			if got := background.Lifecycle; got != tt.wantLifecycle {
				t.Fatalf("background activity lifecycle = %q, want %q", got, tt.wantLifecycle)
			}
		})
	}
}

func TestTranscriptBackgroundNoticeCarriesTypedExitCode(t *testing.T) {
	exitCode := 3
	activityID := uuid.New()
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:   runtime.EventConversationUpdated,
		StepID: runtimeStepIDPointer(transcriptProjectionStepID),
		CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
			EventSequence: 1,
		},
		Message: llm.Message{
			Role:                 llm.RoleDeveloper,
			Name:                 textutil.Value("process-1"),
			MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
			Content:              textutil.Value("background failed"),
			CompactContent:       textutil.Value("background failed"),
			BackgroundActivityID: textutil.Value(activityID.String()),
			BackgroundExitCode:   &exitCode,
		},
	})

	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one background notice", messages)
	}
	row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
	if row.Notice == nil {
		t.Fatal("background notice row is missing notice payload")
	}
	background := row.Notice.Background
	if background == nil || background.ExitCode == nil || *background.ExitCode != exitCode {
		t.Fatalf("background notice = %+v, want exit code %d", background, exitCode)
	}
}

func TestTranscriptWorktreeNoticeCarriesTypedContextWithoutServerPresentation(t *testing.T) {
	target := session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/transcript"),
			WorktreePath:  "/tmp/worktree",
			WorkspaceRoot: "/tmp/workspace",
			EffectiveCwd:  "/tmp/worktree/pkg",
		},
	}
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventConversationUpdated,
		CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
			EventSequence: 2,
		},
		Message: llm.Message{
			Role:            llm.RoleDeveloper,
			MessageType:     textutil.Value(llm.MessageTypeWorktreeMode),
			SourcePath:      textutil.Value(target.EffectiveCwd),
			WorktreeContext: &target.WorktreeContext,
			Content:         textutil.Value("model-visible worktree context"),
		},
	})

	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one worktree notice", messages)
	}
	row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
	if row.Notice == nil {
		t.Fatal("worktree notice row is missing notice payload")
	}
	notice := row.Notice
	if notice.Worktree == nil {
		t.Fatal("worktree transcript context is missing")
	}
	wantContext := &clientui.TranscriptWorktreeContext{
		Branch:        target.Branch,
		WorktreePath:  target.WorktreePath,
		WorkspaceRoot: target.WorkspaceRoot,
		EffectiveCwd:  target.EffectiveCwd,
	}
	if !reflect.DeepEqual(notice.Worktree, wantContext) {
		t.Fatalf("worktree transcript context = %+v, want %+v", notice.Worktree, wantContext)
	}
	if notice.CondensedText != nil || notice.CompactLabel != nil {
		t.Fatalf("server-authored worktree presentation leaked into client contract: %+v", notice)
	}
}

func TestTranscriptWorktreeNoticeKeepsMissingBranchNullable(t *testing.T) {
	context := session.WorktreeContext{
		WorktreePath:  "/tmp/detached-worktree",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/detached-worktree",
	}
	messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind: runtime.EventConversationUpdated,
		CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
			EventSequence: 3,
		},
		Message: llm.Message{
			Role:            llm.RoleDeveloper,
			MessageType:     textutil.Value(llm.MessageTypeWorktreeMode),
			WorktreeContext: &context,
			Content:         textutil.Value("model-visible detached worktree context"),
		},
	})
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one typed worktree notice", messages)
	}
	row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
	if row.Notice == nil || row.Notice.Worktree == nil {
		t.Fatal("typed worktree notice is missing projected context")
	}
	if branch := row.Notice.Worktree.Branch; branch != nil {
		t.Fatalf("projected detached worktree branch = %v, want null", branch)
	}
}

func TestTranscriptBackgroundActivityRejectsMissingRuntimeActivityID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected missing background activity id panic")
		}
	}()

	_ = TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:   runtime.EventBackgroundUpdated,
		StepID: runtimeStepIDPointer(transcriptProjectionStepID),
		Background: &runtime.BackgroundShellEvent{
			Type:        runtime.BackgroundShellEventBackgrounded,
			ID:          uuid.NewString(),
			OwnerRunID:  transcriptProjectionRunID,
			OwnerStepID: transcriptProjectionStepID,
			State:       "running",
			Command:     "go test ./...",
			Workdir:     "/tmp/workspace",
			Preview:     "tests",
		},
	})
}

func TestAssistantTranscriptMessagesDoNotReemitLiveToolStarts(t *testing.T) {
	for _, kind := range []runtime.EventKind{runtime.EventAssistantMessage, runtime.EventConversationUpdated} {
		t.Run(string(kind), func(t *testing.T) {
			messages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
				Kind:   kind,
				StepID: runtimeStepIDPointer(transcriptProjectionStepID),
				CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{
					EventSequence: 4,
				},
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value("checking the repo"),
					Phase:   textutil.Value(llm.MessagePhaseCommentary),
					ToolCalls: []llm.ToolCall{{
						ID:   "call-1",
						Name: "shell",
					}},
				},
			})
			if len(messages) != 1 {
				t.Fatalf("messages = %+v, want only assistant committed row", messages)
			}
			if messages[0].Kind() != clientui.TranscriptMessageCommittedRow {
				t.Fatalf("message = %+v, want assistant committed row", messages[0])
			}
			row := transcriptPayload[clientui.TranscriptCommittedRow](t, messages[0])
			if row.Assistant == nil {
				t.Fatalf("message = %+v, want assistant committed row", messages[0])
			}
		})
	}
}

func TestInFlightClearFailureIsOperationalDiagnosticOnly(t *testing.T) {
	event := runtime.Event{
		Kind:   runtime.EventInFlightClearFailed,
		StepID: runtimeStepIDPointer(transcriptProjectionStepID),
	}
	if facts := runtime.TranscriptCommittedRowFactsFromEvent(event); len(facts) != 0 {
		t.Fatalf("in-flight clear failure committed facts = %+v, want none", facts)
	}
	messages := TranscriptMessagesFromRuntimeEvent(event)
	if len(messages) != 1 || messages[0].Kind() != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("in-flight clear failure messages = %+v, want one operational diagnostic", messages)
	}
	diagnostic := transcriptPayload[clientui.TranscriptOperationalDiagnostic](t, messages[0])
	if diagnostic.Code != clientui.OperationalDiagnosticInFlightClearFailed {
		t.Fatalf("diagnostic code = %q, want %q", diagnostic.Code, clientui.OperationalDiagnosticInFlightClearFailed)
	}
}

func TestContextFactPersistenceFailureIsOperationalDiagnosticOnly(t *testing.T) {
	stepID := mustTranscriptStepID(transcriptProjectionStepID, "test Context-fact diagnostic")
	event := runtime.Event{
		Kind:   runtime.EventContextFactsPersistFailed,
		StepID: textutil.Value(stepID.String()),
		Error:  "persist Session Context manual Compact eligibility: disk full",
	}
	if facts := runtime.TranscriptCommittedRowFactsFromEvent(event); len(facts) != 0 {
		t.Fatalf("Context-fact persistence failure committed facts = %+v, want none", facts)
	}
	messages := TranscriptMessagesFromRuntimeEvent(event)
	if len(messages) != 1 || messages[0].Kind() != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("Context-fact persistence failure messages = %+v, want one operational diagnostic", messages)
	}
	diagnostic := transcriptPayload[clientui.TranscriptOperationalDiagnostic](t, messages[0])
	if diagnostic.Code != clientui.OperationalDiagnosticContextFactsPersistFailed ||
		diagnostic.Detail == "" ||
		diagnostic.StepID == nil ||
		*diagnostic.StepID != stepID {
		t.Fatalf("Context-fact persistence diagnostic = %+v", diagnostic)
	}
}
