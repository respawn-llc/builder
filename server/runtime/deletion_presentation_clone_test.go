package runtime

import (
	"testing"

	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestRuntimeToolPresentationCloneBoundariesOwnDeletionMetadata(t *testing.T) {
	t.Parallel()
	source := deletionCloneTestMeta()
	clones := map[string]*transcript.ToolCallMeta{
		"live tool":      cloneTranscriptToolCallMeta(source),
		"persisted scan": clonePersistedToolCallMeta(source),
	}

	for name, cloned := range clones {
		t.Run(name, func(t *testing.T) {
			cloned.PatchRender.Files[0].WholeFileDeletions[0].ID.HunkOrdinal = 5
			cloned.PatchRender.Files[0].WholeFileDeletions[0].Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 6
			cloned.PatchRender.Files[0].WholeFileDeletions[0].Disposition.Removed = 7

			operation := source.PatchRender.Files[0].WholeFileDeletions[0]
			if operation.ID.HunkOrdinal != 0 ||
				operation.Disposition == nil ||
				operation.Disposition.PhysicalGroup.FirstOperation.HunkOrdinal != 0 ||
				operation.Disposition.Removed != 2 {
				t.Fatalf("source deletion metadata aliased through %s clone: %+v", name, operation)
			}
		})
	}
}

func deletionCloneTestMeta() *transcript.ToolCallMeta {
	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	return &transcript.ToolCallMeta{
		ToolName: "patch",
		PatchRender: &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
			RelPath: "target.txt",
			WholeFileDeletions: []patchformat.WholeFileDeletionOperation{{
				ID: id,
				Disposition: &patchformat.WholeFileDeletionDisposition{
					PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
					Removed:       2,
				},
			}},
		}}},
	}
}
