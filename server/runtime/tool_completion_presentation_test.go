package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestLiveToolCompletionPersistsAndProjectsDeletionMismatchDiagnosticAfterResult(t *testing.T) {
	store := mustCreateTestSession(t)
	emitted := make([]Event, 0, 1)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		OnEvent: func(event Event) {
			emitted = append(emitted, event)
		},
	})
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	call := llm.ToolCall{
		ID:   "call-live",
		Name: string(toolspec.ToolPatch),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:    string(toolspec.ToolPatch),
			PatchRender: &rendered,
		}),
	}
	receipt, err := eng.appendMessageRaw(
		"step",
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}},
		steeringMessageEventNone,
		true,
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("persist tool call: receipt=%+v error=%v", receipt, err)
	}
	receipt, err = eng.steerWithCommitReceipt("step", steerToolCompletionIntent(tools.Result{
		CallID: call.ID,
		Name:   toolspec.ToolPatch,
		Output: json.RawMessage(`{"ok":true}`),
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 1},
				Removed: 3,
			}},
		},
	}))
	if err != nil || !receipt.Committed {
		t.Fatalf("persist mismatched tool completion: receipt=%+v error=%v", receipt, err)
	}

	var completionEvent *Event
	for index := range emitted {
		if emitted[index].Kind == EventToolCallCompleted {
			completionEvent = &emitted[index]
		}
	}
	if completionEvent == nil || completionEvent.ToolResult == nil ||
		completionEvent.ToolResult.IsError ||
		completionEvent.ToolCompletionDiagnostic == nil {
		t.Fatalf("completion event = %+v, want successful result plus diagnostic", completionEvent)
	}
	facts := TranscriptCommittedRowFactsFromEvent(*completionEvent)
	if len(facts) != 2 ||
		facts[0].Kind != TranscriptCommittedRowFactTool ||
		facts[1].Kind != TranscriptCommittedRowFactNotice ||
		facts[1].Notice == nil ||
		!transcript.DeveloperDiagnosticEqual(facts[1].Notice.DeveloperDiagnostic, completionEvent.ToolCompletionDiagnostic) {
		t.Fatalf("completion facts = %+v, want result followed by typed diagnostic notice", facts)
	}
	var hydration TranscriptHydrationSnapshot
	if err := eng.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		hydration = snapshot
		return nil
	}); err != nil {
		t.Fatalf("hydrate live transcript: %v", err)
	}
	if len(hydration.CommittedRows) != 2 ||
		hydration.CommittedRows[0].Kind != TranscriptCommittedRowFactTool ||
		hydration.CommittedRows[1].Kind != TranscriptCommittedRowFactNotice ||
		hydration.CommittedRows[1].Notice == nil ||
		!transcript.DeveloperDiagnosticEqual(
			hydration.CommittedRows[1].Notice.DeveloperDiagnostic,
			completionEvent.ToolCompletionDiagnostic,
		) {
		t.Fatalf("live hydration rows = %+v, want result followed by typed diagnostic notice", hydration.CommittedRows)
	}

	persisted, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	var stored storedToolCompletion
	for _, event := range persisted {
		if event.Kind == "tool_completed" {
			if err := json.Unmarshal(event.Payload, &stored); err != nil {
				t.Fatalf("decode persisted completion: %v", err)
			}
		}
	}
	if stored.Diagnostic == nil ||
		!transcript.DeveloperDiagnosticEqual(stored.Diagnostic, completionEvent.ToolCompletionDiagnostic) {
		t.Fatalf("persisted completion diagnostic = %+v, want %+v", stored.Diagnostic, completionEvent.ToolCompletionDiagnostic)
	}

	page, err := eng.TranscriptNewestSegmentPage()
	if err != nil {
		t.Fatalf("newest segment page: %v", err)
	}
	if len(page.Snapshot.Entries) != 3 ||
		page.Snapshot.Entries[1].Role != "tool_result_ok" ||
		page.Snapshot.Entries[2].Role != string(transcript.EntryRoleDeveloperErrorFeedback) ||
		!transcript.DeveloperDiagnosticEqual(page.Snapshot.Entries[2].DeveloperDiagnostic, stored.Diagnostic) {
		t.Fatalf("persisted page entries = %+v, want call, result, diagnostic", page.Snapshot.Entries)
	}
}

func TestToolCompletionDiagnosticRejectsMismatchedCompletionCallID(t *testing.T) {
	diagnostic := transcript.NewDeletionFactMismatchDeveloperDiagnostic(
		"call-diagnostic",
		patchformat.WholeFileDeletionFactMismatchError{
			Kind: patchformat.WholeFileDeletionFactMismatchMissing,
			ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		},
	)
	payload, err := json.Marshal(storedToolCompletion{
		CallID:       "call-completion",
		Name:         string(toolspec.ToolPatch),
		Output:       json.RawMessage(`{"ok":true}`),
		Presentation: deletionDiagnosticTestPresentation(),
		Diagnostic:   &diagnostic,
	})
	if err != nil {
		t.Fatalf("marshal completion payload: %v", err)
	}
	chat := newChatStore()
	if err := chat.restoreToolCompletionPayload(payload); err == nil {
		t.Fatal("restore accepted a diagnostic for another tool completion")
	}
	if len(chat.toolCompletions) != 0 {
		t.Fatalf("rejected completion mutated runtime state: completions=%d", len(chat.toolCompletions))
	}

	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	receipt, err := eng.persistToolCompletionRaw("step", toolCompletionFinalization{
		Result: tools.Result{
			CallID:       "call-completion",
			Name:         toolspec.ToolPatch,
			Output:       json.RawMessage(`{"ok":true}`),
			Presentation: deletionDiagnosticTestPresentation(),
		},
		Diagnostic: &diagnostic,
	})
	if err == nil || receipt.Committed {
		t.Fatalf("persist accepted mismatched diagnostic: receipt=%+v error=%v", receipt, err)
	}
}

func deletionDiagnosticTestPresentation() *transcript.ToolCallMeta {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	return &transcript.ToolCallMeta{
		ToolName:    string(toolspec.ToolPatch),
		PatchRender: &rendered,
	}
}

func TestFailedToolCompletionDoesNotCreateDeletionMismatchDiagnostic(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	finalization := toolResultWithTranscriptPresentation(tools.Result{
		CallID:  "call-failed",
		Name:    toolspec.ToolPatch,
		IsError: true,
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 99},
				Removed: 1,
			}},
		},
	}, llm.ToolCall{
		ID:   "call-failed",
		Name: string(toolspec.ToolPatch),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:    string(toolspec.ToolPatch),
			PatchRender: &rendered,
		}),
	}, "/workspace")
	if finalization.Diagnostic != nil {
		t.Fatalf("failed completion created deletion mismatch diagnostic: %+v", finalization.Diagnostic)
	}
}

func TestToolResultPresentationMismatchPreservesSuccessfulUncountedCompletion(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	call := llm.ToolCall{
		ID:   "call-1",
		Name: string(toolspec.ToolPatch),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:    string(toolspec.ToolPatch),
			PatchRender: &rendered,
		}),
	}
	result := tools.Result{
		CallID:  call.ID,
		Name:    toolspec.ToolPatch,
		Output:  json.RawMessage(`{"ok":true}`),
		IsError: false,
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 99},
				Removed: 3,
			}},
		},
	}

	finalization := toolResultWithTranscriptPresentation(result, call, "/workspace")

	if finalization.IsError {
		t.Fatal("presentation mismatch changed successful tool completion into an error")
	}
	if finalization.PresentationDelta != nil {
		t.Fatalf("presentation delta was not consumed: %+v", finalization.PresentationDelta)
	}
	if finalization.Presentation == nil ||
		finalization.Presentation.PatchRender == nil ||
		finalization.Presentation.PatchRender.Files[0].Removed != 0 ||
		finalization.Presentation.PatchRender.Files[0].WholeFileDeletions[0].CountKnown {
		t.Fatalf("completion did not preserve original uncounted presentation: %+v", finalization.Presentation)
	}
	if finalization.Diagnostic == nil ||
		finalization.Diagnostic.Kind() != transcript.DeveloperDiagnosticDeletionFactMismatch ||
		finalization.Diagnostic.DeletionFactMismatch == nil ||
		finalization.Diagnostic.DeletionFactMismatch.CallID != call.ID ||
		finalization.Diagnostic.DeletionFactMismatch.MismatchKind != patchformat.WholeFileDeletionFactMismatchUnmatched {
		t.Fatalf("typed mismatch diagnostic = %+v", finalization.Diagnostic)
	}
}

func TestToolResultPresentationMissingFactsPreserveSuccessfulUncountedCompletion(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: first.txt\n*** Delete File: second.txt\n*** End Patch\n",
		"/workspace",
	)
	call := llm.ToolCall{
		ID:   "call-missing",
		Name: string(toolspec.ToolPatch),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:    string(toolspec.ToolPatch),
			PatchRender: &rendered,
		}),
	}
	firstFact := patchformat.WholeFileDeletionFact{
		ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		Removed: 2,
	}
	tests := []struct {
		name  string
		delta *transcript.ToolResultPresentationDelta
	}{
		{name: "nil"},
		{name: "empty", delta: &transcript.ToolResultPresentationDelta{}},
		{name: "partial", delta: &transcript.ToolResultPresentationDelta{WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{firstFact}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalization := toolResultWithTranscriptPresentation(tools.Result{
				CallID:            call.ID,
				Name:              toolspec.ToolPatch,
				Output:            json.RawMessage(`{"ok":true}`),
				PresentationDelta: test.delta,
			}, call, "/workspace")

			if finalization.IsError || finalization.PresentationDelta != nil {
				t.Fatalf("successful completion changed on missing fact: %+v", finalization.Result)
			}
			for _, file := range finalization.Presentation.PatchRender.Files {
				if file.Removed != 0 || file.WholeFileDeletions[0].CountKnown {
					t.Fatalf("missing fact fabricated count: %+v", finalization.Presentation.PatchRender)
				}
			}
			context := finalization.Diagnostic.DeletionFactMismatch
			if context == nil ||
				context.MismatchKind != patchformat.WholeFileDeletionFactMismatchMissing {
				t.Fatalf("missing-fact diagnostic = %+v", finalization.Diagnostic)
			}
		})
	}
}

func TestToolResultPresentationNegativeFactPreservesSuccessfulUncountedCompletion(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	call := llm.ToolCall{
		ID:   "call-negative",
		Name: string(toolspec.ToolPatch),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName:    string(toolspec.ToolPatch),
			PatchRender: &rendered,
		}),
	}
	finalization := toolResultWithTranscriptPresentation(tools.Result{
		CallID: call.ID,
		Name:   toolspec.ToolPatch,
		Output: json.RawMessage(`{"ok":true}`),
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
				Removed: -1,
			}},
		},
	}, call, "/workspace")

	if finalization.IsError ||
		finalization.Presentation.PatchRender.Files[0].Removed != 0 ||
		finalization.Presentation.PatchRender.Files[0].WholeFileDeletions[0].CountKnown {
		t.Fatalf("negative fact changed successful uncounted completion: %+v", finalization)
	}
	context := finalization.Diagnostic.DeletionFactMismatch
	if context == nil ||
		context.MismatchKind != patchformat.WholeFileDeletionFactMismatchInvalidCount {
		t.Fatalf("negative-fact diagnostic = %+v", finalization.Diagnostic)
	}
}
