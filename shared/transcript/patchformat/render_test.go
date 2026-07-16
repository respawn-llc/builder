package patchformat

import (
	"errors"
	"testing"
)

func TestRenderFormatsSummaryAndDetailFromParsedPatch(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: dir/a.go\n-old\n+new\n*** Add File: b.go\n+hello\n*** End Patch\n"
	rendered := Render(patchText, "/workspace")

	if got := rendered.SummaryText(); got != "./dir/a.go +1 -1\n./b.go +1" {
		t.Fatalf("unexpected summary: %q", got)
	}
	if got := rendered.DetailText(); got != "/workspace/dir/a.go\n-old\n+new\n/workspace/b.go\n+hello" {
		t.Fatalf("unexpected detail: %q", got)
	}
	if len(rendered.DetailLines) != 5 {
		t.Fatalf("expected detail line metadata, got %+v", rendered.DetailLines)
	}
	if rendered.DetailLines[0].Kind != RenderedLineKindFile || rendered.DetailLines[0].Path != "/workspace/dir/a.go" {
		t.Fatalf("expected first detail file header metadata, got %+v", rendered.DetailLines[0])
	}
	if rendered.DetailLines[3].Kind != RenderedLineKindFile || rendered.DetailLines[3].Path != "/workspace/b.go" {
		t.Fatalf("expected second detail file header metadata, got %+v", rendered.DetailLines[3])
	}
}

func TestParseHeredocRequiresExactEOFDelimiter(t *testing.T) {
	patchText := "<<EOF\n*** Begin Patch\n*** Add File: eof.txt\n+MY_EOF\n*** End Patch\nEOF\n"
	doc, err := Parse(patchText)
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}
	add, ok := doc.Hunks[0].(AddFile)
	if !ok {
		t.Fatalf("expected add file hunk, got %+v", doc.Hunks)
	}
	if len(add.Content) != 1 || add.Content[0] != "MY_EOF" {
		t.Fatalf("expected body line ending in EOF preserved, got %+v", add.Content)
	}
}

func TestRenderFallsBackToRawForUnparseablePatch(t *testing.T) {
	rendered := Render("not a structured patch payload", "/workspace")

	if got := rendered.SummaryText(); got != "not a structured patch payload" {
		t.Fatalf("unexpected raw summary: %q", got)
	}
	if got := rendered.DetailText(); got != "not a structured patch payload" {
		t.Fatalf("unexpected raw detail: %q", got)
	}
	if len(rendered.Files) != 0 {
		t.Fatalf("expected raw fallback to omit file metadata, got %+v", rendered.Files)
	}
	if len(rendered.DetailLines) != 1 || rendered.DetailLines[0].Kind != RenderedLineKindRaw {
		t.Fatalf("expected raw detail line metadata, got %+v", rendered.DetailLines)
	}
}

func TestFormatUsesMoveTargetForRenderedPaths(t *testing.T) {
	doc, err := Parse("*** Begin Patch\n*** Update File: src.txt\n*** Move to: dest.txt\n-old\n+new\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}

	rendered := Format(doc, "/workspace")
	if len(rendered.Files) != 1 {
		t.Fatalf("expected one rendered file, got %+v", rendered.Files)
	}
	if rendered.Files[0].AbsPath != "/workspace/dest.txt" || rendered.Files[0].RelPath != "./dest.txt" {
		t.Fatalf("expected move target paths, got %+v", rendered.Files[0])
	}
	if got := rendered.DetailText(); got != "/workspace/dest.txt\n-old\n+new" {
		t.Fatalf("unexpected moved detail: %q", got)
	}
}

func TestParseAllowsMoveOnlyUpdateFile(t *testing.T) {
	doc, err := Parse("*** Begin Patch\n*** Update File: src.txt\n*** Move to: dest.txt\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}
	update, ok := doc.Hunks[0].(UpdateFile)
	if !ok {
		t.Fatalf("expected update hunk, got %+v", doc.Hunks)
	}
	if update.Path != "src.txt" || update.MoveTo != "dest.txt" || len(update.Changes) != 0 {
		t.Fatalf("unexpected move-only update hunk: %+v", update)
	}
}

func TestFormatPreservesRelativeOutsideWorkspacePath(t *testing.T) {
	doc, err := Parse("*** Begin Patch\n*** Add File: ../outside.go\n+package outside\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}

	rendered := Format(doc, "/workspace/project")
	if len(rendered.Files) != 1 {
		t.Fatalf("expected one rendered file, got %+v", rendered.Files)
	}
	if rendered.Files[0].RelPath != "../outside.go" {
		t.Fatalf("expected outside-workspace relative path preserved, got %+v", rendered.Files[0])
	}
	if got := rendered.SummaryText(); got != "../outside.go +1" {
		t.Fatalf("unexpected summary: %q", got)
	}
}

func TestRenderWholeFileDeletionsTrackUnknownOperationsByHunkOrdinal(t *testing.T) {
	rendered := Render(
		"*** Begin Patch\n*** Delete File: duplicate.txt\n*** Delete File: duplicate.txt\n*** End Patch\n",
		"/workspace",
	)

	if len(rendered.Files) != 1 {
		t.Fatalf("rendered files = %d, want one lexical file", len(rendered.Files))
	}
	file := rendered.Files[0]
	if file.Removed != 0 {
		t.Fatalf("pending whole-file removal aggregate = %d, want unknown count omitted", file.Removed)
	}
	if len(file.WholeFileDeletions) != 2 {
		t.Fatalf("whole-file deletion operations = %d, want two", len(file.WholeFileDeletions))
	}
	for ordinal, operation := range file.WholeFileDeletions {
		wantID := WholeFileDeletionOperationID{HunkOrdinal: ordinal}
		if operation.ID != wantID {
			t.Fatalf("operation %d identity = %+v, want %+v", ordinal, operation.ID, wantID)
		}
		if operation.CountKnown {
			t.Fatalf("operation %d count unexpectedly known: %+v", ordinal, operation)
		}
	}
	if ShowsRemovedCount(file) {
		t.Fatal("pending whole-file deletion exposed a removal-count fact")
	}
	if len(rendered.DetailLines) != 3 ||
		rendered.DetailLines[0].Kind != RenderedLineKindFile ||
		rendered.DetailLines[1].Kind != RenderedLineKindDiff ||
		rendered.DetailLines[2].Kind != RenderedLineKindDiff {
		t.Fatalf("whole-file deletion detail structure changed: %+v", rendered.DetailLines)
	}
}

func TestApplyWholeFileDeletionFactsExposesPositiveAndKnownZeroCounts(t *testing.T) {
	rendered := Render(
		"*** Begin Patch\n*** Delete File: populated.txt\n*** Delete File: empty.txt\n*** End Patch\n",
		"/workspace",
	)
	facts := []WholeFileDeletionFact{
		{ID: WholeFileDeletionOperationID{HunkOrdinal: 0}, Removed: 3},
		{ID: WholeFileDeletionOperationID{HunkOrdinal: 1}, Removed: 0},
	}

	applied, err := ApplyWholeFileDeletionFacts(rendered, facts)
	if err != nil {
		t.Fatalf("apply deletion facts: %v", err)
	}
	if len(applied.Files) != 2 {
		t.Fatalf("rendered files = %d, want two", len(applied.Files))
	}
	for index, wantRemoved := range []int{3, 0} {
		file := applied.Files[index]
		if file.Removed != wantRemoved {
			t.Fatalf("file %d removed = %d, want %d", index, file.Removed, wantRemoved)
		}
		if len(file.WholeFileDeletions) != 1 {
			t.Fatalf("file %d operations = %d, want one", index, len(file.WholeFileDeletions))
		}
		operation := file.WholeFileDeletions[0]
		if operation.ID != facts[index].ID || !operation.CountKnown {
			t.Fatalf("file %d operation = %+v, want fact %+v marked known", index, operation, facts[index])
		}
		if !ShowsRemovedCount(file) {
			t.Fatalf("file %d did not expose its known removal-count fact", index)
		}
	}
}

func TestApplyWholeFileDeletionFactsRejectsDuplicateUnmatchedAndMissingIdentities(t *testing.T) {
	rendered := Render(
		"*** Begin Patch\n*** Delete File: first.txt\n*** Delete File: second.txt\n*** End Patch\n",
		"/workspace",
	)
	matchedID := WholeFileDeletionOperationID{HunkOrdinal: 0}

	tests := []struct {
		name     string
		facts    []WholeFileDeletionFact
		wantKind WholeFileDeletionFactMismatchKind
		wantID   WholeFileDeletionOperationID
	}{
		{
			name: "duplicate",
			facts: []WholeFileDeletionFact{
				{ID: matchedID, Removed: 1},
				{ID: matchedID, Removed: 1},
			},
			wantKind: WholeFileDeletionFactMismatchDuplicate,
			wantID:   matchedID,
		},
		{
			name:     "unmatched",
			facts:    []WholeFileDeletionFact{{ID: WholeFileDeletionOperationID{HunkOrdinal: 99}, Removed: 1}},
			wantKind: WholeFileDeletionFactMismatchUnmatched,
			wantID:   WholeFileDeletionOperationID{HunkOrdinal: 99},
		},
		{
			name:     "empty",
			wantKind: WholeFileDeletionFactMismatchMissing,
			wantID:   matchedID,
		},
		{
			name:     "partial",
			facts:    []WholeFileDeletionFact{{ID: matchedID, Removed: 1}},
			wantKind: WholeFileDeletionFactMismatchMissing,
			wantID:   WholeFileDeletionOperationID{HunkOrdinal: 1},
		},
		{
			name:     "negative count",
			facts:    []WholeFileDeletionFact{{ID: matchedID, Removed: -1}},
			wantKind: WholeFileDeletionFactMismatchInvalidCount,
			wantID:   matchedID,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ApplyWholeFileDeletionFacts(rendered, test.facts)
			var mismatch *WholeFileDeletionFactMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("error = %v, want typed whole-file deletion fact mismatch", err)
			}
			if mismatch.Kind != test.wantKind || mismatch.ID != test.wantID {
				t.Fatalf("mismatch = %+v, want kind=%q id=%+v", mismatch, test.wantKind, test.wantID)
			}
		})
	}
}
