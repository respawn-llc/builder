package transcript

import (
	"encoding/json"
	"testing"

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

func TestEncodeDecodeToolCallMetaRoundTripsKnownZeroDeletionCount(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: empty.txt\n*** End Patch\n",
		"/workspace",
	)
	rendered, err := patchformat.ApplyWholeFileDeletionFacts(
		rendered,
		[]patchformat.WholeFileDeletionFact{{
			ID: patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		}},
	)
	if err != nil {
		t.Fatalf("apply known-zero deletion fact: %v", err)
	}

	raw := EncodeToolCallMeta(ToolCallMeta{ToolName: "patch", PatchRender: &rendered})
	meta, ok := DecodeToolCallMeta(raw)
	if !ok || meta.PatchRender == nil || len(meta.PatchRender.Files) != 1 {
		t.Fatalf("decoded patch metadata = %+v, want one file", meta)
	}
	file := meta.PatchRender.Files[0]
	if file.Removed != 0 ||
		len(file.WholeFileDeletions) != 1 ||
		!file.WholeFileDeletions[0].CountKnown ||
		!patchformat.ShowsRemovedCount(file) {
		t.Fatalf("known-zero deletion did not round-trip: %+v", file)
	}
}
