package transcript

import (
	"testing"

	"core/shared/textutil"
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

func TestEntryPayloadEqualIncludesPatchPresentationMetadata(t *testing.T) {
	leftPresentation := patchformat.Render(
		"*** Begin Patch\n*** Add File: a.go\n+package a\n*** End Patch\n",
		"/workspace",
	)
	rightPresentation := patchformat.Render(
		"*** Begin Patch\n*** Add File: b.go\n+package b\n*** End Patch\n",
		"/workspace",
	)
	left := EntryPayload{
		Role:       "tool_call",
		Text:       "patch",
		ToolCallID: "call-1",
		ToolCall: &ToolCallMeta{
			ToolName:          "patch",
			PatchPresentation: &leftPresentation,
		},
	}
	right := EntryPayload{
		Role:       "tool_call",
		Text:       "patch",
		ToolCallID: "call-1",
		ToolCall: &ToolCallMeta{
			ToolName:          "patch",
			PatchPresentation: &rightPresentation,
		},
	}

	if EntryPayloadEqual(left, right) {
		t.Fatal("expected patch presentation change to make entries different")
	}
}

func TestToolCallMetaEqualDistinguishesWholeFileDeletionDispositionStates(t *testing.T) {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	group := patchformat.WholeFileDeletionGroupID{FirstOperation: id}
	pendingPresentation := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	pending := &ToolCallMeta{ToolName: "patch", PatchPresentation: &pendingPresentation}
	zeroPresentation, mismatch := patchformat.ApplyWholeFileDeletionFacts(
		pendingPresentation,
		[]patchformat.WholeFileDeletionFact{{
			PhysicalGroup: group,
			OperationIDs:  []patchformat.WholeFileDeletionOperationID{id},
			Removed:       0,
		}},
	)
	if mismatch != nil {
		t.Fatalf("finalize zero-line deletion: %v", mismatch)
	}
	zero := &ToolCallMeta{ToolName: "patch", PatchPresentation: &zeroPresentation}
	if ToolCallMetaEqual(pending, zero) {
		t.Fatal("absent disposition equals present zero disposition")
	}

	positive := cloneToolCallMetaForEqualityTest(zero)
	positive.PatchPresentation.Changes.Files[0].Removed = textutil.Value(4)
	positive.PatchPresentation.Changes.Files[0].Operations[0].Deletion.Disposition.Removed = 4
	if ToolCallMetaEqual(zero, positive) {
		t.Fatal("present zero equals present positive disposition")
	}

	otherGroup := cloneToolCallMetaForEqualityTest(positive)
	otherGroup.PatchPresentation.Changes.Files[0].Operations[0].Deletion.Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 1
	if ToolCallMetaEqual(positive, otherGroup) {
		t.Fatal("different physical group identity compares equal")
	}

	pendingCopy := cloneToolCallMetaForEqualityTest(pending)
	if !ToolCallMetaEqual(pending, pendingCopy) {
		t.Fatal("equivalent pending presentations compare different")
	}
}

func cloneToolCallMetaForEqualityTest(source *ToolCallMeta) *ToolCallMeta {
	cloned := *source
	cloned.PatchPresentation = patchformat.ClonePresentation(source.PatchPresentation)
	return &cloned
}
