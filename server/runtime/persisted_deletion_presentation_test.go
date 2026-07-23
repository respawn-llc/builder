package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestPersistedToolCompletionRestoresDeletionDispositionWithoutFilesystemAccess(t *testing.T) {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	tests := []struct {
		name        string
		disposition *patchformat.WholeFileDeletionDisposition
		want        *int
	}{
		{name: "explicit null"},
		{name: "present zero", disposition: deletionDisposition(id, 0), want: textutil.Value(0)},
		{name: "present positive", disposition: deletionDisposition(id, 5), want: textutil.Value(5)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := restoredDeletionPresentation(t, deletionPersistenceMeta(id, test.disposition))
			removed := patchformat.RemovedLineCount(meta.PatchRender.Files[0])
			if test.want == nil {
				if removed != nil {
					t.Fatalf("restored removed count = %d, want absent", *removed)
				}
			} else if removed == nil || *removed != *test.want {
				t.Fatalf("restored removed count = %v, want %d", removed, *test.want)
			}
		})
	}
}

func TestPersistedToolCompletionPreservesLegacyMissingDispositionState(t *testing.T) {
	const legacySummary, legacyDetail = "legacy summary", "legacy detail"
	meta := restoredDeletionPresentation(t, map[string]any{
		"ToolName":     "patch",
		"PatchSummary": legacySummary,
		"PatchDetail":  legacyDetail,
		"PatchRender": map[string]any{
			"Files": []any{map[string]any{
				"RelPath": "target.txt",
				"Removed": 1,
				"WholeFileDeletions": []any{map[string]any{
					"id": map[string]any{"hunk_ordinal": 0},
				}},
			}},
		},
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

func restoredDeletionPresentation(t *testing.T, presentation any) *transcript.ToolCallMeta {
	t.Helper()
	const callID = "call-delete"
	rawPresentation, err := json.Marshal(presentation)
	if err != nil {
		t.Fatalf("marshal presentation: %v", err)
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID: callID, Name: "patch", Custom: true,
				CustomInput:  textutil.Value("*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"),
				Presentation: rawPresentation,
			}},
		}),
		mustPersistedScanEvent(t, "tool_completed", storedToolCompletion{
			CallID: callID, Name: "patch", Output: json.RawMessage(`{"ok":true}`),
			Presentation: func() *transcript.ToolCallMeta {
				decoded, ok := transcript.DecodeToolCallMeta(rawPresentation)
				if !ok {
					t.Fatal("decode presentation")
				}
				return decoded
			}(),
		}),
	}
	for _, event := range events {
		if err := scan.ApplyPersistedEvent(event); err != nil {
			t.Fatalf("restore %s: %v", mustSessionEventKind(event), err)
		}
	}
	for _, entry := range scan.CollectedPageSnapshot().Entries {
		if entry.ToolCallID == callID && entry.Role == "tool_result_ok" &&
			entry.ToolCall != nil && entry.ToolCall.PatchRender != nil {
			return entry.ToolCall
		}
	}
	t.Fatal("restored completion did not contain patch presentation")
	return nil
}

func deletionPersistenceMeta(
	id patchformat.WholeFileDeletionOperationID,
	disposition *patchformat.WholeFileDeletionDisposition,
) *transcript.ToolCallMeta {
	return &transcript.ToolCallMeta{
		ToolName: "patch",
		PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: id, Disposition: disposition,
			}},
		}}},
	}
}

func deletionDisposition(
	id patchformat.WholeFileDeletionOperationID,
	removed int,
) *patchformat.WholeFileDeletionDisposition {
	return &patchformat.WholeFileDeletionDisposition{
		PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
		Removed:       removed,
	}
}
