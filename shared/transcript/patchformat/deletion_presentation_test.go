package patchformat

import (
	"slices"
	"testing"
)

func TestWholeFileDeletionPresentationFinalizesGroupedAliasesWithoutMutatingSource(t *testing.T) {
	rendered := Format(Document{Hunks: []any{
		DeleteFile{Path: "target.txt"},
		DeleteFile{Path: "target.txt"},
		DeleteFile{Path: "alias.txt"},
	}}, "/workspace")
	if len(rendered.Files) != 2 ||
		len(rendered.Files[0].WholeFileDeletions) != 2 ||
		len(rendered.Files[1].WholeFileDeletions) != 1 {
		t.Fatalf("pending operations = %+v", rendered.Files)
	}
	for ordinal, operation := range []WholeFileDeletionOperation{
		rendered.Files[0].WholeFileDeletions[0],
		rendered.Files[0].WholeFileDeletions[1],
		rendered.Files[1].WholeFileDeletions[0],
	} {
		if operation.ID.HunkOrdinal != ordinal || operation.Disposition != nil {
			t.Fatalf("pending operation %d = %+v", ordinal, operation)
		}
	}
	if RemovedLineCount(rendered.Files[0]) != nil {
		t.Fatal("pending deletion exposed a removal count")
	}

	first := WholeFileDeletionOperationID{HunkOrdinal: 0}
	finalized, mismatch := ApplyWholeFileDeletionFacts(rendered, []WholeFileDeletionFact{{
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
		if removed := RemovedLineCount(file); removed == nil || *removed != 7 {
			t.Fatalf("file %d removed count = %v, want known 7", index, removed)
		}
		for _, operation := range file.WholeFileDeletions {
			if operation.Disposition == nil ||
				operation.Disposition.PhysicalGroup.FirstOperation != first ||
				operation.Disposition.Removed != 7 {
				t.Fatalf("file %d operation = %+v", index, operation)
			}
		}
	}
	if rendered.Files[0].WholeFileDeletions[0].Disposition != nil {
		t.Fatal("fact application mutated its source")
	}
	if slices.Equal(rendered.SummaryLines, finalized.SummaryLines) ||
		finalized.SummaryText() != joinRenderedLines(finalized.SummaryLines) ||
		finalized.DetailText() != joinRenderedLines(finalized.DetailLines) {
		t.Fatal("fact application did not regenerate render aliases")
	}
}

func TestWholeFileDeletionPresentationPreservesKnownZero(t *testing.T) {
	rendered := Render(
		"*** Begin Patch\n*** Delete File: empty.txt\n*** End Patch\n",
		"/workspace",
	)
	id := WholeFileDeletionOperationID{HunkOrdinal: 0}
	finalized, mismatch := ApplyWholeFileDeletionFacts(rendered, []WholeFileDeletionFact{{
		PhysicalGroup: WholeFileDeletionGroupID{FirstOperation: id},
		OperationIDs:  []WholeFileDeletionOperationID{id},
	}})
	if mismatch != nil {
		t.Fatalf("apply zero fact: %+v", mismatch)
	}
	if removed := RemovedLineCount(finalized.Files[0]); removed == nil || *removed != 0 {
		t.Fatalf("removed count = %v, want present zero", removed)
	}
}

func TestWholeFileDeletionPresentationReturnsTypedMismatchContext(t *testing.T) {
	rendered := Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	received := []WholeFileDeletionOperationID{{HunkOrdinal: 9}}
	group, count := WholeFileDeletionGroupID{FirstOperation: received[0]}, 3
	_, mismatch := ApplyWholeFileDeletionFacts(rendered, []WholeFileDeletionFact{{
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
	rendered := Format(Document{Hunks: []any{
		DeleteFile{Path: "target.txt"},
		DeleteFile{Path: "target.txt"},
	}}, "/workspace")
	received := []WholeFileDeletionOperationID{
		{HunkOrdinal: 1},
		{HunkOrdinal: 0},
	}
	group := WholeFileDeletionGroupID{FirstOperation: received[0]}

	_, mismatch := ApplyWholeFileDeletionFacts(rendered, []WholeFileDeletionFact{{
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
	source := RenderedPatch{Files: []RenderedFile{{
		WholeFileDeletions: []WholeFileDeletionOperation{{
			ID: id,
			Disposition: &WholeFileDeletionDisposition{
				PhysicalGroup: WholeFileDeletionGroupID{FirstOperation: id},
				Removed:       2,
			},
		}},
	}}}
	cloned := Clone(&source)
	cloned.Files[0].WholeFileDeletions[0].ID.HunkOrdinal = 5
	cloned.Files[0].WholeFileDeletions[0].Disposition.PhysicalGroup.FirstOperation.HunkOrdinal = 6
	cloned.Files[0].WholeFileDeletions[0].Disposition.Removed = 7
	if operation := source.Files[0].WholeFileDeletions[0]; operation.ID != id ||
		operation.Disposition == nil ||
		operation.Disposition.PhysicalGroup.FirstOperation != id ||
		operation.Disposition.Removed != 2 {
		t.Fatalf("source aliased through clone: %+v", operation)
	}
}
