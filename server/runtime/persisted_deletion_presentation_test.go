package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestPersistedToolCompletionRestoresDeletionDispositionWithoutFilesystemAccess(t *testing.T) {
	t.Parallel()
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
			if meta.PatchPresentation == nil || meta.PatchPresentation.Changes == nil {
				t.Fatalf("restored Patch presentation = %+v", meta)
			}
			removed := meta.PatchPresentation.Changes.Files[0].Removed
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

func TestPersistedToolCompletionNormalizesLegacyMissingDispositionAsPending(t *testing.T) {
	t.Parallel()
	meta := restoredDeletionPresentation(t, legacyDeletionMetadata(
		patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		nil,
		[]string{"-old"},
	))
	if meta.PatchPresentation == nil || meta.PatchPresentation.Changes == nil {
		t.Fatalf("restored Patch presentation = %+v", meta)
	}
	file := meta.PatchPresentation.Changes.Files[0]
	if file.Removed != nil ||
		len(file.Operations) != 2 ||
		file.Operations[0].Kind != patchformat.FileOperationUpdate ||
		file.Operations[1].Kind != patchformat.FileOperationDelete ||
		file.Operations[1].Deletion == nil ||
		file.Operations[1].Deletion.Disposition != nil {
		t.Fatalf("legacy pending deletion normalization = %+v", file)
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
				result := transcript.DecodeToolCallMeta(rawPresentation)
				if result.Kind != transcript.ToolCallMetaDecodeLegacyNormalized ||
					result.Meta == nil {
					t.Fatalf("decode presentation: %+v", result)
				}
				return result.Meta
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
			entry.ToolCall != nil && entry.ToolCall.PatchPresentation != nil {
			return entry.ToolCall
		}
	}
	t.Fatal("restored completion did not contain patch presentation")
	return nil
}

func deletionPersistenceMeta(
	id patchformat.WholeFileDeletionOperationID,
	disposition *patchformat.WholeFileDeletionDisposition,
) map[string]any {
	return legacyDeletionMetadata(id, disposition, nil)
}

func legacyDeletionMetadata(
	id patchformat.WholeFileDeletionOperationID,
	disposition *patchformat.WholeFileDeletionDisposition,
	changedDiff []string,
) map[string]any {
	diff := append([]string(nil), changedDiff...)
	diff = append(diff, "-<deleted file>")
	removed := 0
	for _, line := range changedDiff {
		if len(line) > 0 && line[0] == '-' {
			removed++
		}
	}
	oldRemoved := removed
	knownRemoved := removed > 0
	if disposition != nil {
		knownRemoved = true
		oldRemoved += disposition.Removed
	}
	summary := "./target.txt"
	if knownRemoved {
		summary += fmt.Sprintf(" -%d", oldRemoved)
	}
	detailLines := []any{
		map[string]any{
			"Kind":      "file",
			"Text":      "/workspace/target.txt",
			"FileIndex": 0,
			"Path":      "/workspace/target.txt",
		},
	}
	for _, line := range diff {
		detailLines = append(detailLines, map[string]any{
			"Kind":      "diff",
			"Text":      line,
			"FileIndex": 0,
			"Path":      "",
		})
	}
	detail := "/workspace/target.txt\n" + strings.Join(diff, "\n")
	return map[string]any{
		"ToolName":               "patch",
		"Presentation":           "default",
		"RenderBehavior":         "default",
		"IsShell":                false,
		"UserInitiated":          false,
		"Command":                detail,
		"CompactText":            summary,
		"InlineMeta":             "",
		"TimeoutLabel":           "",
		"PatchSummary":           summary,
		"PatchDetail":            detail,
		"Question":               "",
		"Suggestions":            nil,
		"RecommendedOptionIndex": 0,
		"OmitSuccessfulResult":   true,
		"RawOutputRequested":     false,
		"OutputTruncated":        false,
		"MovedToBackground":      false,
		"ShellExitCode":          nil,
		"RenderHint": map[string]any{
			"Kind":         "diff",
			"Path":         "",
			"ResultOnly":   false,
			"ShellDialect": "",
		},
		"PatchRender": map[string]any{
			"Files": []any{map[string]any{
				"AbsPath": "/workspace/target.txt",
				"RelPath": "./target.txt",
				"Added":   0,
				"Removed": removed,
				"Diff":    diff,
				"WholeFileDeletions": []any{map[string]any{
					"id":          id,
					"disposition": disposition,
				}},
			}},
			"SummaryLines": []any{map[string]any{
				"Kind":      "file",
				"Text":      summary,
				"FileIndex": 0,
				"Path":      "./target.txt",
			}},
			"DetailLines": detailLines,
		},
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
