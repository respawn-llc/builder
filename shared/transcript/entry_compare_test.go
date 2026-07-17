package transcript

import (
	"testing"

	patchformat "core/shared/transcript/patchformat"
)

func TestEntryPayloadEqualIncludesToolMetadata(t *testing.T) {
	left := EntryPayload{
		Role:       "tool_call",
		Text:       "pwd",
		ToolCallID: "call-1",
		ToolCall:   &ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
	}
	right := EntryPayload{
		Role:       "tool_call",
		Text:       "pwd",
		ToolCallID: "call-1",
		ToolCall:   &ToolCallMeta{ToolName: "shell", IsShell: true, Command: "ls"},
	}

	if EntryPayloadEqual(left, right) {
		t.Fatal("expected metadata command change to make entries different")
	}
}

func TestEntryPayloadEqualNormalizesDerivedToolMetadata(t *testing.T) {
	left := EntryPayload{
		Role:       "tool_call",
		Text:       "pwd",
		ToolCallID: " call-1 ",
		ToolCall:   &ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
	}
	right := EntryPayload{
		Role:       " TOOL_CALL ",
		Text:       "pwd",
		ToolCallID: "call-1",
		ToolCall:   &ToolCallMeta{ToolName: "shell", Presentation: ToolPresentationShell, RenderBehavior: ToolCallRenderBehaviorShell, IsShell: true, Command: "pwd", CompactText: "pwd"},
	}

	if !EntryPayloadEqual(left, right) {
		t.Fatal("expected normalized role/tool metadata to be equal")
	}
}

func TestEntryPayloadEqualTreatsEmptyToolMetadataAsAbsent(t *testing.T) {
	left := EntryPayload{Role: "tool_call", Text: "pwd", ToolCallID: "call-1"}
	right := EntryPayload{
		Role:       "tool_call",
		Text:       "pwd",
		ToolCallID: "call-1",
		ToolCall:   &ToolCallMeta{},
	}

	if !EntryPayloadEqual(left, right) {
		t.Fatal("expected empty tool metadata to equal absent metadata")
	}
}

func TestEntryPayloadEqualIncludesPatchRenderMetadata(t *testing.T) {
	left := EntryPayload{
		Role:       "tool_call",
		Text:       "patch",
		ToolCallID: "call-1",
		ToolCall: &ToolCallMeta{ToolName: "patch", PatchRender: &patchformat.RenderedPatch{
			SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "a.go"}},
		}},
	}
	right := EntryPayload{
		Role:       "tool_call",
		Text:       "patch",
		ToolCallID: "call-1",
		ToolCall: &ToolCallMeta{ToolName: "patch", PatchRender: &patchformat.RenderedPatch{
			SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "b.go"}},
		}},
	}

	if EntryPayloadEqual(left, right) {
		t.Fatal("expected patch render summary change to make entries different")
	}
}

func TestEntryPayloadEqualIncludesWholeFileDeletionMetadata(t *testing.T) {
	leftRender := patchformat.Render(
		"*** Begin Patch\n*** Delete File: a.go\n*** End Patch\n",
		"/workspace",
	)
	rightRender := patchformat.Clone(&leftRender)
	rightRender.Files[0].WholeFileDeletions[0].CountKnown = true

	left := EntryPayload{
		Role:       "tool_call",
		Text:       "patch",
		ToolCallID: "call-1",
		ToolCall:   &ToolCallMeta{ToolName: "patch", PatchRender: &leftRender},
	}
	right := left
	right.ToolCall = &ToolCallMeta{ToolName: "patch", PatchRender: rightRender}

	if EntryPayloadEqual(left, right) {
		t.Fatal("expected whole-file deletion metadata change to make entries different")
	}
}
