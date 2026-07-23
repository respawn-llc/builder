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

func TestToolCallMetaEqualDistinguishesWholeFileDeletionDispositionStates(t *testing.T) {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	group := patchformat.WholeFileDeletionGroupID{FirstOperation: id}
	legacy := &ToolCallMeta{ToolName: "patch", PatchRender: &patchformat.RenderedPatch{
		Files: []patchformat.RenderedFile{{RelPath: "target.txt"}},
	}}
	pending := &ToolCallMeta{ToolName: "patch", PatchRender: &patchformat.RenderedPatch{
		Files: []patchformat.RenderedFile{{
			RelPath: "target.txt",
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: id,
			}},
		}},
	}}
	zero := &ToolCallMeta{ToolName: "patch", PatchRender: &patchformat.RenderedPatch{
		Files: []patchformat.RenderedFile{{
			RelPath: "target.txt",
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: id,
				Disposition: &patchformat.WholeFileDeletionDisposition{
					PhysicalGroup: group,
					Removed:       0,
				},
			}},
		}},
	}}

	if ToolCallMetaEqual(legacy, pending) {
		t.Fatal("legacy missing operation metadata equals explicit pending operation")
	}
	if ToolCallMetaEqual(pending, zero) {
		t.Fatal("absent disposition equals present zero disposition")
	}

	positive := cloneToolCallMetaForEqualityTest(zero)
	positive.PatchRender.Files[0].WholeFileDeletions[0].Disposition.Removed = 4
	if ToolCallMetaEqual(zero, positive) {
		t.Fatal("present zero equals present positive disposition")
	}

	otherGroup := cloneToolCallMetaForEqualityTest(positive)
	otherGroup.PatchRender.Files[0].WholeFileDeletions[0].Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 1
	if ToolCallMetaEqual(positive, otherGroup) {
		t.Fatal("different physical group identity compares equal")
	}

	legacyCopy := cloneToolCallMetaForEqualityTest(legacy)
	if !ToolCallMetaEqual(legacy, legacyCopy) {
		t.Fatal("equivalent legacy renders compare different")
	}
}

func cloneToolCallMetaForEqualityTest(source *ToolCallMeta) *ToolCallMeta {
	cloned := *source
	cloned.PatchRender = patchformat.Clone(source.PatchRender)
	return &cloned
}
