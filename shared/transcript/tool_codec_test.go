package transcript

import (
	"encoding/json"
	"testing"

	"core/shared/textutil"
	patchformat "core/shared/transcript/patchformat"
)

func TestDecodeToolCallMetaTreatsEmptyObjectAsAbsent(t *testing.T) {
	meta, ok := DecodeToolCallMeta(json.RawMessage(`{}`))
	if ok {
		t.Fatalf("expected empty tool metadata to decode as absent, got %+v", meta)
	}
}

func TestDecodeToolCallMetaRoundTripsNonEmptyMetadata(t *testing.T) {
	raw := EncodeToolCallMeta(ToolCallMeta{ToolName: "shell", Command: "echo hi"})
	meta, ok := DecodeToolCallMeta(raw)
	if !ok {
		t.Fatal("expected tool metadata to decode successfully")
	}
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
	meta, ok := DecodeToolCallMeta(raw)
	if !ok {
		t.Fatal("expected tool metadata to decode successfully")
	}
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
	meta, ok := DecodeToolCallMeta(raw)
	if !ok {
		t.Fatal("expected tool metadata to decode successfully")
	}
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
			raw := EncodeToolCallMeta(ToolCallMeta{
				ToolName: "patch",
				PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
					RelPath: "target.txt",
					WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
						ID:          id,
						Disposition: test.disposition,
					}},
				}}},
			})
			meta, ok := DecodeToolCallMeta(raw)
			if !ok || meta.PatchRender == nil {
				t.Fatalf("decode finalized patch metadata: ok=%t meta=%+v", ok, meta)
			}
			removed := patchformat.RemovedLineCount(meta.PatchRender.Files[0])
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

func TestDecodeToolCallMetaAcceptsLegacyDeletionOperationWithoutDisposition(t *testing.T) {
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

	meta, ok := DecodeToolCallMeta(raw)
	if !ok || meta.PatchRender == nil {
		t.Fatalf("decode legacy patch metadata: ok=%t meta=%+v", ok, meta)
	}
	file := meta.PatchRender.Files[0]
	if file.Removed != 1 ||
		len(file.WholeFileDeletions) != 1 ||
		file.WholeFileDeletions[0].Disposition != nil {
		t.Fatalf("legacy patch metadata was reclassified: %+v", file)
	}
}
