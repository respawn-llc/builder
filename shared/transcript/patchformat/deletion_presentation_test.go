package patchformat

import (
	"slices"
	"testing"
)

func TestWholeFileDeletionPresentationFinalizesGroupedAliasesWithoutMutatingSource(t *testing.T) {
	changes := Format(Document{Hunks: []any{
		DeleteFile{Path: "target.txt"},
		DeleteFile{Path: "target.txt"},
		DeleteFile{Path: "alias.txt"},
	}}, "/workspace")
	if len(changes.Files) != 2 ||
		len(changes.Files[0].Operations) != 2 ||
		len(changes.Files[1].Operations) != 1 {
		t.Fatalf("pending operations = %+v", changes.Files)
	}
	for ordinal, operation := range []WholeFileDeletionOperation{
		*changes.Files[0].Operations[0].Deletion,
		*changes.Files[0].Operations[1].Deletion,
		*changes.Files[1].Operations[0].Deletion,
	} {
		if operation.ID.HunkOrdinal != ordinal || operation.Disposition != nil {
			t.Fatalf("pending operation %d = %+v", ordinal, operation)
		}
	}
	if changes.Files[0].Removed != nil {
		t.Fatal("pending deletion exposed a removal count")
	}

	first := WholeFileDeletionOperationID{HunkOrdinal: 0}
	finalized, mismatch := ApplyWholeFileDeletionFacts(Presentation{
		Variant: PresentationVariantChanges,
		Changes: &changes,
	}, []WholeFileDeletionFact{{
		PhysicalGroup: WholeFileDeletionGroupID{FirstOperation: first},
		OperationIDs: []WholeFileDeletionOperationID{
			first,
			{HunkOrdinal: 1},
			{HunkOrdinal: 2},
		},
		Removed: 7,
	}})
	if mismatch != nil {
		t.Fatalf("apply grouped fact: %+v", mismatch)
	}
	for index, file := range finalized.Files {
		if file.Removed == nil || *file.Removed != 7 {
			t.Fatalf("file %d removed count = %v, want known 7", index, file.Removed)
		}
		for _, operation := range file.Operations {
			if operation.Deletion == nil ||
				operation.Deletion.Disposition == nil ||
				operation.Deletion.Disposition.PhysicalGroup.FirstOperation != first ||
				operation.Deletion.Disposition.Removed != 7 {
				t.Fatalf("file %d operation = %+v", index, operation)
			}
		}
	}
	if changes.Files[0].Operations[0].Deletion.Disposition != nil {
		t.Fatal("fact application mutated its source")
	}
}

func TestWholeFileDeletionPresentationPreservesKnownZero(t *testing.T) {
	presentation := Render(
		"*** Begin Patch\n*** Delete File: empty.txt\n*** End Patch\n",
		"/workspace",
	)
	id := WholeFileDeletionOperationID{HunkOrdinal: 0}
	finalized, mismatch := ApplyWholeFileDeletionFacts(presentation, []WholeFileDeletionFact{{
		PhysicalGroup: WholeFileDeletionGroupID{FirstOperation: id},
		OperationIDs:  []WholeFileDeletionOperationID{id},
	}})
	if mismatch != nil {
		t.Fatalf("apply zero fact: %+v", mismatch)
	}
	if finalized.Files[0].Removed == nil || *finalized.Files[0].Removed != 0 {
		t.Fatalf("removed count = %v, want present zero", finalized.Files[0].Removed)
	}
}

func TestWholeFileDeletionPresentationReturnsTypedMismatchContext(t *testing.T) {
	presentation := Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	received := []WholeFileDeletionOperationID{{HunkOrdinal: 9}}
	group, count := WholeFileDeletionGroupID{FirstOperation: received[0]}, 3
	_, mismatch := ApplyWholeFileDeletionFacts(presentation, []WholeFileDeletionFact{{
		PhysicalGroup: group,
		OperationIDs:  received,
		Removed:       count,
	}})
	if mismatch == nil ||
		mismatch.Kind != WholeFileDeletionFactMismatchUnexpectedOperation ||
		!slices.Equal(mismatch.ExpectedOperationIDs, []WholeFileDeletionOperationID{{HunkOrdinal: 0}}) ||
		!slices.Equal(mismatch.ReceivedOperationIDs, received) ||
		mismatch.PhysicalGroup == nil || *mismatch.PhysicalGroup != group ||
		mismatch.Removed == nil || *mismatch.Removed != count {
		t.Fatalf("mismatch context = %+v", mismatch)
	}
}

func TestWholeFileDeletionPresentationRejectsNoncanonicalOperationOrder(t *testing.T) {
	changes := Format(Document{Hunks: []any{
		DeleteFile{Path: "target.txt"},
		DeleteFile{Path: "target.txt"},
	}}, "/workspace")
	received := []WholeFileDeletionOperationID{
		{HunkOrdinal: 1},
		{HunkOrdinal: 0},
	}
	group := WholeFileDeletionGroupID{FirstOperation: received[0]}

	_, mismatch := ApplyWholeFileDeletionFacts(Presentation{
		Variant: PresentationVariantChanges,
		Changes: &changes,
	}, []WholeFileDeletionFact{{
		PhysicalGroup: group,
		OperationIDs:  received,
		Removed:       3,
	}})
	if mismatch == nil ||
		mismatch.Kind != WholeFileDeletionFactMismatchInvalidGroup ||
		!slices.Equal(mismatch.ReceivedOperationIDs, received) ||
		mismatch.PhysicalGroup == nil ||
		*mismatch.PhysicalGroup != group {
		t.Fatalf("noncanonical ordering mismatch = %+v", mismatch)
	}
}

func TestCloneOwnsNestedWholeFileDeletionMetadata(t *testing.T) {
	id := WholeFileDeletionOperationID{HunkOrdinal: 0}
	removed := 2
	deletion := WholeFileDeletionOperation{
		ID: id,
		Disposition: &WholeFileDeletionDisposition{
			PhysicalGroup: WholeFileDeletionGroupID{FirstOperation: id},
			Removed:       2,
		},
	}
	source := Presentation{
		Variant: PresentationVariantChanges,
		Changes: &Changes{Files: []FileChange{{
			Path:    Path{Absolute: "/workspace/target", Relative: "./target"},
			Removed: &removed,
			Operations: []FileOperation{{
				Kind:     FileOperationDelete,
				Deletion: &deletion,
			}},
		}}},
	}
	cloned := ClonePresentation(&source)
	cloned.Changes.Files[0].Operations[0].Deletion.ID.HunkOrdinal = 5
	cloned.Changes.Files[0].Operations[0].Deletion.Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 6
	cloned.Changes.Files[0].Operations[0].Deletion.Disposition.Removed = 7
	if operation := source.Changes.Files[0].Operations[0].Deletion; operation.ID != id ||
		operation.Disposition == nil ||
		operation.Disposition.PhysicalGroup.FirstOperation != id ||
		operation.Disposition.Removed != 2 {
		t.Fatalf("source aliased through clone: %+v", operation)
	}
}
