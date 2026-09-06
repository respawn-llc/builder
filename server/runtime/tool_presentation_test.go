package runtime

import (
	"core/server/llm"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
	"testing"
)

func TestNormalizeToolCallForTranscriptUsesCustomPatchInput(t *testing.T) {
	t.Parallel()
	patchText := "*** Begin Patch\n*** Update File: cli/app/ui_status.go\n@@\n type uiStatusAuthInfo struct {\n-\tSummary string\n+\tSummary string\n+\tReady bool\n }\n*** End Patch\n"
	call := llm.ToolCall{
		ID:          "call_patch",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value(patchText),
	}

	normalized := normalizeToolCallForTranscript(call, "/workspace")
	decoded := transcript.DecodeToolCallMeta(normalized.Presentation)
	if decoded.Kind != transcript.ToolCallMetaDecodeCurrent || decoded.Meta == nil {
		t.Fatalf("expected current presentation metadata for custom patch call: %+v", decoded)
	}
	meta := decoded.Meta
	if meta.PatchPresentation == nil ||
		meta.PatchPresentation.Variant != patchformat.PresentationVariantChanges ||
		meta.PatchPresentation.Changes == nil ||
		len(meta.PatchPresentation.Changes.Files) != 1 {
		t.Fatalf("expected current patch change facts, got %+v", meta)
	}
	file := meta.PatchPresentation.Changes.Files[0]
	if file.Path.Relative != "./cli/app/ui_status.go" ||
		file.Added != 2 || file.Removed == nil || *file.Removed != 1 {
		t.Fatalf("unexpected custom patch facts: %+v", file)
	}
	if meta.Command != "" || meta.CompactText != "" {
		t.Fatalf("obsolete patch text projection retained: %+v", meta)
	}
}
