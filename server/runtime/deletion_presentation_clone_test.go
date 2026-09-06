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
			deletion := cloned.PatchPresentation.Changes.Files[0].Operations[0].Deletion
			deletion.ID.HunkOrdinal = 5
			deletion.Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 6
			deletion.Disposition.Removed = 7

			operation := source.PatchPresentation.Changes.Files[0].Operations[0].Deletion
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
	removed := 2
	return &transcript.ToolCallMeta{
		ToolName: "patch",
		PatchPresentation: &patchformat.Presentation{
			Variant: patchformat.PresentationVariantChanges,
			Changes: &patchformat.Changes{
				Files: []patchformat.FileChange{
					{
						Path:    patchformat.Path{Absolute: "/workspace/target.txt", Relative: "target.txt"},
						Removed: &removed,
						Operations: []patchformat.FileOperation{
							{
								Kind: patchformat.FileOperationDelete,
								Deletion: &patchformat.WholeFileDeletionOperation{
									ID: id,
									Disposition: &patchformat.WholeFileDeletionDisposition{
										PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
										Removed:       2,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
