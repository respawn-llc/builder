package runtime

import (
	"core/server/llm"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	"testing"
)

func TestNormalizeToolCallForTranscriptUsesCustomPatchInput(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: cli/app/ui_status.go\n@@\n type uiStatusAuthInfo struct {\n-\tSummary string\n+\tSummary string\n+\tReady bool\n }\n*** End Patch\n"
	call := llm.ToolCall{
		ID:          "call_patch",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value(patchText),
	}

	normalized := normalizeToolCallForTranscript(call, "/workspace")
	meta, ok := transcript.DecodeToolCallMeta(normalized.Presentation)
	if !ok || meta == nil {
		t.Fatalf("expected presentation metadata for custom patch call")
	}
	if !meta.HasPatchSummary() || !meta.HasPatchDetail() || meta.PatchRender == nil {
		t.Fatalf("expected patch summary/detail/render metadata, got %+v", meta)
	}
	if meta.PatchSummary != "./cli/app/ui_status.go +2 -1" {
		t.Fatalf("unexpected custom patch summary: %q", meta.PatchSummary)
	}
	if meta.Command == patchText {
		t.Fatalf("expected command to be rendered patch detail, not raw freeform payload")
	}
}
