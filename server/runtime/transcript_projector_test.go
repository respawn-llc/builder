package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func appendPersistedTranscriptRecord(t *testing.T, store *session.Store, payload any) session.EventRecord {
	t.Helper()
	event, _, err := appendTestEvent(t, store, "step", payload)
	if err != nil {
		t.Fatalf("append persisted transcript event %T: %v", payload, err)
	}
	return event
}

func applyPersistedTranscriptRecords(t *testing.T, scan *PersistedTranscriptScan, records []session.EventRecord) {
	t.Helper()
	for _, record := range records {
		if err := scan.ApplyPersistedEvent(record); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(record), err)
		}
	}
}

func TestPersistedTranscriptScanReconstructsPersistedTranscript(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	toolOutput, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("marshal tool output: %v", err)
	}
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleUser, Content: textutil.Value("hello")}),
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		appendPersistedTranscriptRecord(t, store, storedToolCompletion{
			CallID: "call-1",
			Name:   string(toolspec.ToolExecCommand),
			Output: toolOutput,
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value("call-1"),
				Name:   textutil.Value(string(toolspec.ToolExecCommand)),
				Output: toolOutput,
			}},
		}),
		appendPersistedTranscriptRecord(t, store, storedLocalEntry{Role: "system", Text: "persisted note"}),
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final answer"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 5 {
		t.Fatalf("entry count = %d, want 5", len(snapshot.Entries))
	}
	if got := snapshot.Entries[1]; got.Role != "tool_call" || got.Text != "pwd" ||
		got.ToolCall == nil || !got.ToolCall.IsShell || got.ToolCall.Command != "pwd" {
		t.Fatalf("entry[1] = %+v, want persisted shell-call projection", got)
	}
	if got := snapshot.Entries[2]; got.Role != "tool_result_ok" || got.Text == "" {
		t.Fatalf("entry[2] = %+v, want persisted synthesized tool result", got)
	}
	if snapshot.Entries[3].Role != "system" || snapshot.Entries[3].Text != "persisted note" {
		t.Fatalf("unexpected local entry: %+v", snapshot.Entries[3])
	}
	if got := scan.LastCommittedAssistantFinalAnswer(); got == nil || *got != "final answer" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %v, want final answer", got)
	}
}

func TestPersistedTranscriptScanBlankFinalClearsLastCommittedAnswer(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("previous answer"),
		}),
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(""),
		}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

	if got := scan.LastCommittedAssistantFinalAnswer(); got != nil {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %v, want absence after blank final", got)
	}
}

func TestPersistedTranscriptScanRestoresMaterializedToolResultFromCompletion(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	const callID = "call-materialized"
	output := json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`)
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, storedToolCompletion{
			CallID: callID, Name: string(toolspec.ToolExecCommand), Output: output,
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value(callID), Name: textutil.Value(string(toolspec.ToolExecCommand)),
				Output: output,
			}},
		}),
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID: callID, Name: string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"pwd"}`),
			}},
		}),
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role: llm.RoleTool, ToolCallID: textutil.Value(callID),
			Name: textutil.Value(string(toolspec.ToolExecCommand)),
		}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

	snapshot := scan.CollectedPageSnapshot()
	if got := len(snapshot.Entries); got != 2 {
		t.Fatalf("entry count = %d, want 2 (%+v)", got, snapshot.Entries)
	}
	if got := snapshot.Entries[1]; got.Role != "tool_result_ok" || got.Text == "" {
		t.Fatalf("materialized tool-result projection = %+v, want completed output", got)
	}
}

func TestPersistedTranscriptScanSurfacesPersistedCompactionSummaries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("user summary")}),
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("developer handoff")}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0]; got.Role != "compaction_summary" || got.Text != "user summary" {
		t.Fatalf("entry[0] = %+v, want user compaction summary", got)
	}
	if got := snapshot.Entries[1]; got.Role != "compaction_summary" || got.Text != "developer handoff" {
		t.Fatalf("entry[1] = %+v, want developer compaction summary", got)
	}
}

func TestPersistedTranscriptScanPreservesErrorLocalEntries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	record := appendPersistedTranscriptRecord(t, store, storedLocalEntry{Role: "error", Text: "Exact token counting failed"})
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, []session.EventRecord{record})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0]; got.Role != "error" || got.Text != "Exact token counting failed" {
		t.Fatalf("entry[0] = %+v, want persisted error entry", got)
	}
}

func TestPersistedTranscriptScanPreservesPersistedLocalEntryNoticeID(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	record := appendPersistedTranscriptRecord(t, store, storedLocalEntry{Role: "system", Text: "Mirrored notice", NoticeID: textutil.Value("notice-1")})
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, []session.EventRecord{record})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0].NoticeID; got != "notice-1" {
		t.Fatalf("notice id = %q, want notice-1", got)
	}
}

func TestPersistedTranscriptScanProjectsTypedDeveloperAndToolResultMetadata(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	const callID = "call-summary"
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment),
			Content: textutil.Value("environment state"),
		}),
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID: callID, Name: string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"cat secret"}`),
			}},
		}),
		appendPersistedTranscriptRecord(t, store, storedToolCompletion{
			CallID: callID, Name: string(toolspec.ToolExecCommand), IsError: true,
			Summary: textutil.Value("permission denied"), CondensedText: textutil.Value("permission denied compact"),
			Output: json.RawMessage(`{"error":"permission denied"}`),
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value(callID), Name: textutil.Value(string(toolspec.ToolExecCommand)),
				Output: json.RawMessage(`{"error":"permission denied"}`),
			}},
		}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

	snapshot := scan.CollectedPageSnapshot()
	if got := len(snapshot.Entries); got != 3 {
		t.Fatalf("entry count = %d, want 3 (%+v)", got, snapshot.Entries)
	}
	if got := snapshot.Entries[0]; got.Role != string(transcript.EntryRoleDeveloperContext) ||
		got.Visibility != transcript.EntryVisibilityDetail || got.MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("developer projection = %+v, want typed detail context", got)
	}
	if got := snapshot.Entries[2]; got.Role != "tool_result_error" ||
		got.ToolResultSummary != "permission denied" || got.CondensedText != "permission denied compact" {
		t.Fatalf("tool-result projection = %+v, want persisted error metadata", got)
	}
}

func TestPersistedTranscriptScanProjectsCacheWarningWithConfiguredVisibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode config.CacheWarningMode
		want transcript.EntryVisibility
	}{
		{name: "default", mode: config.CacheWarningModeDefault, want: transcript.EntryVisibilityDetail},
		{name: "verbose", mode: config.CacheWarningModeVerbose, want: transcript.EntryVisibilityOngoing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			warning := transcript.CacheWarning{
				Scope:  transcript.CacheWarningScopeConversation,
				Reason: transcript.CacheWarningReasonNonPostfix,
			}
			record := appendPersistedTranscriptRecord(t, store, warning)
			scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{CacheWarningMode: test.mode})
			applyPersistedTranscriptRecords(t, scan, []session.EventRecord{record})

			snapshot := scan.CollectedPageSnapshot()
			if got := len(snapshot.Entries); got != 1 {
				t.Fatalf("entry count = %d, want 1", got)
			}
			if got := snapshot.Entries[0]; got.Role != cacheWarningTranscriptRole ||
				got.Visibility != test.want || got.Text != transcript.CacheWarningText(warning) {
				t.Fatalf("cache-warning projection = %+v, want configured persisted warning", got)
			}
		})
	}
}

func TestPersistedTranscriptScanPreservesWholeFileDeletionDisposition(t *testing.T) {
	t.Parallel()
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	tests := []struct {
		name        string
		disposition *patchformat.WholeFileDeletionDisposition
		want        *int
	}{
		{name: "absent disposition"},
		{name: "zero removed", disposition: persistedDeletionDisposition(id, 0), want: textutil.Value(0)},
		{name: "positive removed", disposition: persistedDeletionDisposition(id, 5), want: textutil.Value(5)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := persistedDeletionPresentation(t, persistedDeletionMeta(id, test.disposition))
			removed := patchformat.RemovedLineCount(meta.PatchRender.Files[0])
			if test.want == nil {
				if removed != nil {
					t.Fatalf("restored removed count = %d, want absent", *removed)
				}
				return
			}
			if removed == nil || *removed != *test.want {
				t.Fatalf("restored removed count = %v, want %d", removed, *test.want)
			}
		})
	}
}

func TestPersistedTranscriptScanPreservesLegacyDeletionPresentation(t *testing.T) {
	t.Parallel()
	const legacySummary, legacyDetail = "legacy summary", "legacy detail"
	meta := persistedDeletionPresentation(t, transcript.ToolCallMeta{
		ToolName:     "patch",
		PatchSummary: legacySummary,
		PatchDetail:  legacyDetail,
		PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
			RelPath: "target.txt",
			Removed: 1,
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
			}},
		}}},
	})
	file := meta.PatchRender.Files[0]
	if meta.PatchSummary != legacySummary ||
		meta.PatchDetail != legacyDetail ||
		file.Removed != 1 ||
		len(file.WholeFileDeletions) != 1 ||
		file.WholeFileDeletions[0].Disposition != nil {
		t.Fatalf("legacy presentation was reclassified: %+v", meta)
	}
}

func persistedDeletionPresentation(t *testing.T, presentation transcript.ToolCallMeta) *transcript.ToolCallMeta {
	t.Helper()
	store := mustCreateTestSession(t)
	const callID = "call-delete"
	rawPresentation := transcript.EncodeToolCallMeta(presentation)
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID: callID, Name: string(toolspec.ToolPatch), Custom: true,
				CustomInput:  textutil.Value("*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"),
				Presentation: rawPresentation,
			}},
		}),
		appendPersistedTranscriptRecord(t, store, storedToolCompletion{
			CallID: callID, Name: string(toolspec.ToolPatch), Output: json.RawMessage(`{"ok":true}`),
			Presentation: &presentation,
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeCustomToolOutput,
				CallID: textutil.Value(callID),
				Name:   textutil.Value(string(toolspec.ToolPatch)),
				Output: json.RawMessage(`{"ok":true}`),
			}},
		}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)
	for _, entry := range scan.CollectedPageSnapshot().Entries {
		if entry.ToolCallID == callID && entry.Role == "tool_result_ok" && entry.ToolCall != nil {
			return entry.ToolCall
		}
	}
	t.Fatal("restored completion did not contain patch presentation")
	return nil
}

func persistedDeletionMeta(
	id patchformat.WholeFileDeletionOperationID,
	disposition *patchformat.WholeFileDeletionDisposition,
) transcript.ToolCallMeta {
	return transcript.ToolCallMeta{
		ToolName: "patch",
		PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: id, Disposition: disposition,
			}},
		}}},
	}
}

func persistedDeletionDisposition(
	id patchformat.WholeFileDeletionOperationID,
	removed int,
) *patchformat.WholeFileDeletionDisposition {
	return &patchformat.WholeFileDeletionDisposition{
		PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
		Removed:       removed,
	}
}
