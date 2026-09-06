package patchformat

import "testing"

func TestRenderBuildsTypedChangesFromParsedPatch(t *testing.T) {
	patchText := "*** Begin Patch\n*** Update File: dir/a.go\n-old\n+new\n*** Add File: b.go\n+hello\n*** End Patch\n"
	presentation := Render(patchText, "/workspace")

	if !presentation.Valid() || presentation.Variant != PresentationVariantChanges ||
		presentation.Changes == nil || len(presentation.Changes.Files) != 2 {
		t.Fatalf("unexpected presentation: %+v", presentation)
	}
	update := presentation.Changes.Files[0]
	if update.Path != (Path{Absolute: "/workspace/dir/a.go", Relative: "./dir/a.go"}) ||
		update.Added != 1 || update.Removed == nil || *update.Removed != 1 ||
		len(update.Operations) != 1 || update.Operations[0].Kind != FileOperationUpdate {
		t.Fatalf("unexpected update facts: %+v", update)
	}
	add := presentation.Changes.Files[1]
	if add.Path != (Path{Absolute: "/workspace/b.go", Relative: "./b.go"}) ||
		add.Added != 1 || add.Removed == nil || *add.Removed != 0 ||
		len(add.Operations) != 1 || add.Operations[0].Kind != FileOperationAdd {
		t.Fatalf("unexpected add facts: %+v", add)
	}
	if got := update.Operations[0].Groups; len(got) != 1 ||
		len(got[0].Lines) != 2 ||
		got[0].Lines[0] != (ChangedLine{Kind: ChangedLineRemoved, Content: "old"}) ||
		got[0].Lines[1] != (ChangedLine{Kind: ChangedLineAdded, Content: "new"}) {
		t.Fatalf("unexpected ordered changed lines: %+v", got)
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

func TestRenderClassifiesUnparseablePatchAsInvalidInput(t *testing.T) {
	presentation := Render("not a structured patch payload", "/workspace")

	if !presentation.Valid() || presentation.Variant != PresentationVariantInvalidInput ||
		presentation.InvalidInput == nil ||
		presentation.InvalidInput.InputDetail != "not a structured patch payload" {
		t.Fatalf("unexpected invalid-input presentation: %+v", presentation)
	}
}

func TestFormatUsesMoveTargetForRenderedPaths(t *testing.T) {
	doc, err := Parse("*** Begin Patch\n*** Update File: src.txt\n*** Move to: dest.txt\n-old\n+new\n*** End Patch\n")
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}

	changes := Format(doc, "/workspace")
	if len(changes.Files) != 1 {
		t.Fatalf("expected one changed file, got %+v", changes.Files)
	}
	file := changes.Files[0]
	if file.Path != (Path{Absolute: "/workspace/dest.txt", Relative: "./dest.txt"}) ||
		len(file.Operations) != 1 || file.Operations[0].Kind != FileOperationMove ||
		file.Operations[0].Source == nil ||
		*file.Operations[0].Source != (Path{Absolute: "/workspace/src.txt", Relative: "./src.txt"}) {
		t.Fatalf("expected move paths and operation, got %+v", file)
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

	changes := Format(doc, "/workspace/project")
	if len(changes.Files) != 1 {
		t.Fatalf("expected one changed file, got %+v", changes.Files)
	}
	if changes.Files[0].Path.Relative != "../outside.go" {
		t.Fatalf("expected outside-workspace relative path preserved, got %+v", changes.Files[0])
	}
}
