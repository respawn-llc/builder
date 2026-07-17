package transcript

import (
	"errors"
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

	applied, err := ApplyToolResultPresentationDelta(ToolCallMeta{
		ToolName: "exec_command",
		IsShell:  true,
	}, delta, ToolResultPresentationOutcomeSuccessful)
	if err != nil {
		t.Fatalf("apply shell presentation delta: %v", err)
	}
	exitCode = 9

	if applied.ShellExitCode == nil || *applied.ShellExitCode != 7 {
		t.Fatalf("applied shell exit code = %v, want copied 7", applied.ShellExitCode)
	}
}

func TestApplyToolResultPresentationDeltaFinalizesDeletionFactsOnceWithoutMutatingCallMetadata(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	meta := ToolCallMeta{
		ToolName:     "patch",
		Command:      rendered.DetailText(),
		CompactText:  rendered.SummaryText(),
		PatchDetail:  rendered.DetailText(),
		PatchSummary: rendered.SummaryText(),
		PatchRender:  &rendered,
	}
	delta := &ToolResultPresentationDelta{
		WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
			ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
			Removed: 3,
		}},
	}

	applied, err := ApplyToolResultPresentationDelta(meta, delta, ToolResultPresentationOutcomeSuccessful)
	if err != nil {
		t.Fatalf("apply deletion presentation delta: %v", err)
	}
	if applied.PatchRender == nil || len(applied.PatchRender.Files) != 1 {
		t.Fatalf("finalized patch render = %+v, want one file", applied.PatchRender)
	}
	file := applied.PatchRender.Files[0]
	if file.Removed != 3 ||
		len(file.WholeFileDeletions) != 1 ||
		!file.WholeFileDeletions[0].CountKnown {
		t.Fatalf("finalized deletion metadata = %+v", file)
	}
	if applied.Command != applied.PatchDetail ||
		applied.CompactText != applied.PatchSummary ||
		applied.PatchDetail != applied.PatchRender.DetailText() ||
		applied.PatchSummary != applied.PatchRender.SummaryText() {
		t.Fatalf("finalized patch aliases are stale: %+v", applied)
	}
	if meta.PatchRender.Files[0].Removed != 0 ||
		meta.PatchRender.Files[0].WholeFileDeletions[0].CountKnown {
		t.Fatalf("authoritative call metadata was mutated: %+v", meta.PatchRender.Files[0])
	}

	_, err = ApplyToolResultPresentationDelta(applied, delta, ToolResultPresentationOutcomeSuccessful)
	var mismatch *patchformat.WholeFileDeletionFactMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("reapplied deletion delta error = %v, want typed mismatch", err)
	}
}

func TestApplyToolResultPresentationDeltaRequiresEverySuccessfulDeletionFact(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: first.txt\n*** Delete File: second.txt\n*** End Patch\n",
		"/workspace",
	)
	meta := ToolCallMeta{ToolName: "patch", PatchRender: &rendered}
	firstFact := patchformat.WholeFileDeletionFact{
		ID:      patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		Removed: 1,
	}

	tests := []struct {
		name  string
		delta *ToolResultPresentationDelta
	}{
		{name: "nil delta"},
		{name: "empty delta", delta: &ToolResultPresentationDelta{}},
		{name: "partial delta", delta: &ToolResultPresentationDelta{WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{firstFact}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ApplyToolResultPresentationDelta(meta, test.delta, ToolResultPresentationOutcomeSuccessful)
			if err == nil || err.Kind != patchformat.WholeFileDeletionFactMismatchMissing {
				t.Fatalf("error = %v, want typed missing deletion fact", err)
			}
		})
	}
}

func TestApplyToolResultPresentationDeltaPreservesUnknownCountsForFailedDeletion(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	meta := ToolCallMeta{ToolName: "patch", PatchRender: &rendered}

	applied, err := ApplyToolResultPresentationDelta(meta, nil, ToolResultPresentationOutcomeFailed)
	if err != nil {
		t.Fatalf("apply failed deletion presentation: %v", err)
	}
	if applied.PatchRender == nil ||
		applied.PatchRender.Files[0].Removed != 0 ||
		applied.PatchRender.Files[0].WholeFileDeletions[0].CountKnown {
		t.Fatalf("failed deletion presentation fabricated a count: %+v", applied.PatchRender)
	}
}
