package transcript

import (
	"testing"

	patchformat "core/shared/transcript/patchformat"
)

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

	exitCode := 7
	right.MovedToBackground = false
	right.ShellExitCode = &exitCode
	if ToolCallMetaEqual(left, right) {
		t.Fatal("expected shell exit status to affect tool metadata equality")
	}
}

func TestApplyToolResultPresentationDeltaCopiesShellExitCode(t *testing.T) {
	exitCode := 7
	delta := &ToolResultPresentationDelta{ShellExitCode: &exitCode}

	applied, mismatch := ApplyToolResultPresentationDelta(ToolCallMeta{
		ToolName: "exec_command",
		IsShell:  true,
	}, delta, ToolResultPresentationOutcomeSuccessful)
	if mismatch != nil {
		t.Fatalf("apply shell presentation delta: %+v", mismatch)
	}
	exitCode = 9

	if applied.ShellExitCode == nil || *applied.ShellExitCode != 7 {
		t.Fatalf("applied shell exit code = %v, want copied 7", applied.ShellExitCode)
	}
}

func TestApplyToolResultPresentationDeltaFinalizesGroupedDeletionFactsWithoutMutatingCallMetadata(t *testing.T) {
	presentation := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	meta := ToolCallMeta{
		ToolName:          "patch",
		PatchPresentation: &presentation,
	}
	first := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	delta := &ToolResultPresentationDelta{
		WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
			PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: first},
			OperationIDs: []patchformat.WholeFileDeletionOperationID{
				first,
				{HunkOrdinal: 1},
			},
			Removed: 5,
		}},
	}

	applied, mismatch := ApplyToolResultPresentationDelta(
		meta,
		delta,
		ToolResultPresentationOutcomeSuccessful,
	)
	if mismatch != nil {
		t.Fatalf("apply deletion presentation delta: %+v", mismatch)
	}
	if applied.PatchPresentation == nil || applied.PatchPresentation.Changes == nil {
		t.Fatal("finalized patch change facts are absent")
	}
	file := applied.PatchPresentation.Changes.Files[0]
	if file.Removed == nil || *file.Removed != 5 {
		t.Fatalf("finalized removed count = %v, want known 5", file.Removed)
	}
	if applied.Command != "" || applied.CompactText != "" {
		t.Fatalf("finalized patch retained obsolete text projections: %+v", applied)
	}
	if meta.PatchPresentation.Changes.Files[0].Operations[0].Deletion.Disposition != nil {
		t.Fatalf("authoritative call metadata was mutated: %+v", meta.PatchPresentation)
	}
}

func TestApplyToolResultPresentationDeltaPreservesPendingDispositionForFailedDeletion(t *testing.T) {
	presentation := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)

	applied, mismatch := ApplyToolResultPresentationDelta(
		ToolCallMeta{ToolName: "patch", PatchPresentation: &presentation},
		nil,
		ToolResultPresentationOutcomeFailed,
	)
	if mismatch != nil {
		t.Fatalf("apply failed deletion presentation: %+v", mismatch)
	}
	if applied.PatchPresentation == nil || applied.PatchPresentation.Changes == nil {
		t.Fatal("failed deletion lost prepared patch change facts")
	}
	if removed := applied.PatchPresentation.Changes.Files[0].Removed; removed != nil {
		t.Fatalf("failed deletion fabricated removed count %d", *removed)
	}
}

func TestApplyToolResultPresentationDeltaReturnsTypedMissingFactMismatch(t *testing.T) {
	presentation := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)

	_, mismatch := ApplyToolResultPresentationDelta(
		ToolCallMeta{ToolName: "patch", PatchPresentation: &presentation},
		&ToolResultPresentationDelta{},
		ToolResultPresentationOutcomeSuccessful,
	)
	if mismatch == nil || mismatch.Kind != patchformat.WholeFileDeletionFactMismatchMissingOperation {
		t.Fatalf("mismatch = %+v, want typed missing operation", mismatch)
	}
}
