package transcript

import (
	"encoding/json"
	"testing"

	"core/shared/textutil"
	patchformat "core/shared/transcript/patchformat"
)

func TestDecodeToolCallMetaTreatsEmptyObjectAsInvalid(t *testing.T) {
	result := DecodeToolCallMeta(json.RawMessage(`{}`))
	if result.Kind != ToolCallMetaDecodeInvalid || result.Cause == nil {
		t.Fatalf("expected empty tool metadata to decode as invalid, got %+v", result)
	}
}

func TestDecodeToolCallMetaRoundTripsNonEmptyMetadata(t *testing.T) {
	raw := EncodeToolCallMeta(ToolCallMeta{ToolName: "shell", Command: "echo hi"})
	result := DecodeToolCallMeta(raw)
	if result.Kind != ToolCallMetaDecodeCurrent || result.Meta == nil {
		t.Fatalf("expected current tool metadata, got %+v", result)
	}
	meta := result.Meta
	if meta.ToolName != "shell" || meta.Command != "echo hi" {
		t.Fatalf("unexpected decoded metadata: %+v", meta)
	}
}

func TestEncodeDecodeToolCallMetaRoundTripsShellDialect(t *testing.T) {
	raw := EncodeToolCallMeta(ToolCallMeta{
		ToolName: "exec_command",
		Command:  "copy /y C:\\src.txt C:\\dst.txt",
		RenderHint: &ToolRenderHint{
			Kind:         ToolRenderKindShell,
			ShellDialect: ToolShellDialectWindowsCommand,
		},
	})
	result := DecodeToolCallMeta(raw)
	if result.Kind != ToolCallMetaDecodeCurrent || result.Meta == nil {
		t.Fatalf("expected current tool metadata, got %+v", result)
	}
	meta := result.Meta
	if meta.RenderHint == nil {
		t.Fatalf("expected render hint, got %+v", meta)
	}
	if meta.RenderHint.ShellDialect != ToolShellDialectWindowsCommand {
		t.Fatalf("expected shell dialect to round-trip, got %+v", meta.RenderHint)
	}
}

func TestEncodeDecodeToolCallMetaRoundTripsShellOutputStatus(t *testing.T) {
	exitCode := 7
	raw := EncodeToolCallMeta(ToolCallMeta{
		ToolName:           "exec_command",
		IsShell:            true,
		RawOutputRequested: true,
		OutputTruncated:    true,
		MovedToBackground:  true,
		ShellExitCode:      &exitCode,
	})
	result := DecodeToolCallMeta(raw)
	if result.Kind != ToolCallMetaDecodeCurrent || result.Meta == nil {
		t.Fatalf("expected current tool metadata, got %+v", result)
	}
	meta := result.Meta
	if !meta.RawOutputRequested || !meta.OutputTruncated || !meta.MovedToBackground ||
		meta.ShellExitCode == nil || *meta.ShellExitCode != 7 {
		t.Fatalf("expected shell output status to round-trip, got %+v", meta)
	}
}

func TestEncodeDecodeToolCallMetaPreservesDeletionDispositionPresence(t *testing.T) {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	tests := []struct {
		name        string
		disposition *patchformat.WholeFileDeletionDisposition
		wantRemoved *int
	}{
		{name: "explicit null"},
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
				Removed:       8,
			},
			wantRemoved: textutil.Value(8),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation := patchformat.Render(
				"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
				"/workspace",
			)
			if test.disposition != nil {
				finalized, mismatch := patchformat.ApplyWholeFileDeletionFacts(
					presentation,
					[]patchformat.WholeFileDeletionFact{{
						PhysicalGroup: test.disposition.PhysicalGroup,
						OperationIDs:  []patchformat.WholeFileDeletionOperationID{id},
						Removed:       test.disposition.Removed,
					}},
				)
				if mismatch != nil {
					t.Fatalf("finalize deletion presentation: %v", mismatch)
				}
				presentation = finalized
			}
			raw := EncodeToolCallMeta(ToolCallMeta{
				ToolName:          "patch",
				PatchPresentation: &presentation,
			})
			result := DecodeToolCallMeta(raw)
			if result.Kind != ToolCallMetaDecodeCurrent ||
				result.Meta == nil ||
				result.Meta.PatchPresentation == nil ||
				result.Meta.PatchPresentation.Changes == nil {
				t.Fatalf("decode finalized patch metadata: %+v", result)
			}
			removed := result.Meta.PatchPresentation.Changes.Files[0].Removed
			if test.wantRemoved == nil {
				if removed != nil {
					t.Fatalf("removed count = %d, want absent", *removed)
				}
				return
			}
			if removed == nil || *removed != *test.wantRemoved {
				t.Fatalf("removed count = %v, want %d", removed, *test.wantRemoved)
			}
		})
	}
}

func TestDecodeToolCallMetaRejectsLegacyPatchMetadataBeforeNormalization(t *testing.T) {
	raw := json.RawMessage(`{
		"ToolName":"patch",
		"PatchRender":{
			"Files":[{
				"RelPath":"target.txt",
				"Removed":1,
				"WholeFileDeletions":[{"id":{"hunk_ordinal":0}}]
			}]
		}
	}`)

	result := DecodeToolCallMeta(raw)
	if result.Kind != ToolCallMetaDecodeInvalid || result.Cause == nil {
		t.Fatalf("legacy patch metadata decoded as current: %+v", result)
	}
}
