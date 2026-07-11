package transcript

import "testing"

func TestNormalizeToolCallMetaRecoversKnownShellToolsWithoutPresentationMetadata(t *testing.T) {
	tests := []struct {
		name          string
		toolName      string
		wantPlainHint bool
	}{
		{name: "exec command", toolName: "exec_command"},
		{name: "write stdin", toolName: "write_stdin", wantPlainHint: true},
		{name: "legacy shell alias", toolName: "shell"},
		{name: "legacy bash alias", toolName: "bash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := NormalizeToolCallMeta(ToolCallMeta{ToolName: tt.toolName})

			if meta.Presentation != ToolPresentationShell || meta.RenderBehavior != ToolCallRenderBehaviorShell || !meta.IsShell {
				t.Fatalf("expected known shell tool metadata recovered, got %+v", meta)
			}
			if tt.wantPlainHint && (meta.RenderHint == nil || meta.RenderHint.Kind != ToolRenderKindPlain) {
				t.Fatalf("expected write_stdin to use plain shell input rendering, got %+v", meta.RenderHint)
			}
		})
	}
}

func TestNormalizeToolCallMetaMarksWriteStdinShellAsPlainRenderHint(t *testing.T) {
	meta := NormalizeToolCallMeta(ToolCallMeta{
		ToolName:       "write_stdin",
		RenderBehavior: ToolCallRenderBehaviorShell,
	})

	if meta.RenderHint == nil || meta.RenderHint.Kind != ToolRenderKindPlain {
		t.Fatalf("expected write_stdin shell metadata to use plain render hint, got %+v", meta.RenderHint)
	}
}

func TestNormalizeToolCallMetaPreservesExplicitRenderHint(t *testing.T) {
	meta := NormalizeToolCallMeta(ToolCallMeta{
		ToolName:       "write_stdin",
		RenderBehavior: ToolCallRenderBehaviorShell,
		RenderHint:     &ToolRenderHint{Kind: ToolRenderKindShell, ShellDialect: ToolShellDialectPowerShell},
	})

	if meta.RenderHint == nil || meta.RenderHint.Kind != ToolRenderKindShell || meta.RenderHint.ShellDialect != ToolShellDialectPowerShell {
		t.Fatalf("expected explicit render hint preserved, got %+v", meta.RenderHint)
	}
}

func TestToolCallMetaValidityUsesAuthoritativeRenderHintValidation(t *testing.T) {
	valid := &ToolCallMeta{
		ToolName:       "exec_command",
		Presentation:   ToolPresentationShell,
		RenderBehavior: ToolCallRenderBehaviorShell,
		RenderHint:     &ToolRenderHint{Kind: ToolRenderKindSource, Path: "main.go"},
	}
	if !valid.Valid() {
		t.Fatalf("valid tool metadata rejected: %+v", valid)
	}

	invalid := *valid
	invalid.RenderHint = &ToolRenderHint{Kind: ToolRenderKindSource}
	if invalid.Valid() {
		t.Fatalf("tool metadata accepted invalid render hint: %+v", invalid)
	}
}

func TestToolCallMetaEqualIncludesShellOutputStatus(t *testing.T) {
	left := &ToolCallMeta{ToolName: "exec_command", IsShell: true, RawOutputRequested: true}
	right := &ToolCallMeta{ToolName: "exec_command", IsShell: true}

	if ToolCallMetaEqual(left, right) {
		t.Fatal("expected raw output status to affect tool metadata equality")
	}

	right.RawOutputRequested = true
	if !ToolCallMetaEqual(left, right) {
		t.Fatal("expected matching raw output status to be equal")
	}

	right.OutputTruncated = true
	if ToolCallMetaEqual(left, right) {
		t.Fatal("expected truncation status to affect tool metadata equality")
	}

	right.OutputTruncated = false
	right.MovedToBackground = true
	if ToolCallMetaEqual(left, right) {
		t.Fatal("expected backgrounded status to affect tool metadata equality")
	}
}
