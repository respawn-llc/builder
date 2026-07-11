package transcript

import (
	"encoding/json"
	"testing"
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
	raw := EncodeToolCallMeta(ToolCallMeta{
		ToolName:           "exec_command",
		IsShell:            true,
		RawOutputRequested: true,
		OutputTruncated:    true,
		MovedToBackground:  true,
	})
	meta, ok := DecodeToolCallMeta(raw)
	if !ok {
		t.Fatal("expected tool metadata to decode successfully")
	}
	if !meta.RawOutputRequested || !meta.OutputTruncated || !meta.MovedToBackground {
		t.Fatalf("expected shell output status to round-trip, got %+v", meta)
	}
}
