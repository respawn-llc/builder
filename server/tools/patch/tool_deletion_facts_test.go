package patch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"core/server/tools"
	patchformat "core/shared/transcript/patchformat"
)

func TestDeleteFileReturnsAuthoritativeLogicalLineCountFacts(t *testing.T) {
	tests := []struct {
		name, content string
		want          int
	}{
		{name: "crlf final newline", content: "first\r\nsecond\r\n", want: 2},
		{name: "no final newline", content: "first\nsecond", want: 2},
		{name: "empty", content: "", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "delete.txt"), []byte(test.content), 0o644); err != nil {
				t.Fatalf("seed target: %v", err)
			}
			result := callPatch(t, newPatchTestTool(t, workspace), "delete-count",
				"*** Begin Patch\n*** Delete File: delete.txt\n*** End Patch\n")
			id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
			facts := deletionFacts(result)
			if result.IsError || len(facts) != 1 ||
				facts[0].PhysicalGroup.FirstOperation != id ||
				!slices.Equal(facts[0].OperationIDs, []patchformat.WholeFileDeletionOperationID{id}) ||
				facts[0].Removed != test.want {
				t.Fatalf("result error=%t facts=%+v, want operation %+v removed %d", result.IsError, facts, id, test.want)
			}
		})
	}
}

func TestRepeatedAndAliasDeleteHunksShareOnePhysicalFact(t *testing.T) {
	tests := []struct {
		name       string
		secondPath string
		wantFiles  int
	}{
		{name: "same path", secondPath: "target.txt", wantFiles: 1},
		{name: "symlink alias", secondPath: "alias.txt", wantFiles: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			target := filepath.Join(workspace, "target.txt")
			if err := os.WriteFile(target, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
				t.Fatalf("seed target: %v", err)
			}
			if test.secondPath == "alias.txt" {
				if err := os.Symlink(target, filepath.Join(workspace, test.secondPath)); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			patchText := "*** Begin Patch\n*** Delete File: target.txt\n*** Delete File: " +
				test.secondPath + "\n*** End Patch\n"
			result := callPatch(t, newPatchTestTool(t, workspace), "grouped-delete", patchText)
			wantIDs := []patchformat.WholeFileDeletionOperationID{{HunkOrdinal: 0}, {HunkOrdinal: 1}}
			facts := deletionFacts(result)
			if result.IsError || len(facts) != 1 ||
				!slices.Equal(facts[0].OperationIDs, wantIDs) ||
				facts[0].Removed != 3 {
				t.Fatalf("result error=%t facts=%+v, want one grouped count", result.IsError, facts)
			}
			finalized, mismatch := patchformat.ApplyWholeFileDeletionFacts(
				patchformat.Render(patchText, workspace),
				facts,
			)
			if mismatch != nil || len(finalized.Files) != test.wantFiles {
				t.Fatalf("finalized=%+v mismatch=%+v", finalized.Files, mismatch)
			}
			for index, file := range finalized.Files {
				if removed := patchformat.RemovedLineCount(file); removed == nil || *removed != 3 {
					t.Fatalf("file %d removed count = %v, want known 3", index, removed)
				}
			}
		})
	}
}

func TestCommitDeletionFactsUseRemovedSnapshotAndDisappearOnRollback(t *testing.T) {
	t.Run("commit snapshot", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "delete.txt")
		if err := os.WriteFile(target, []byte("commit\nsnapshot\ncontent\n"), 0o644); err != nil {
			t.Fatalf("seed target: %v", err)
		}
		id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 4}
		facts, err := commitStagedFiles(nil, map[string]wholeFileDeletionTarget{
			target: {OperationIDs: []patchformat.WholeFileDeletionOperationID{id}},
		})
		if err != nil || len(facts) != 1 ||
			facts[0].PhysicalGroup.FirstOperation != id ||
			facts[0].Removed != 3 {
			t.Fatalf("commit facts=%+v err=%v", facts, err)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		workspace := t.TempDir()
		deleteTarget := filepath.Join(workspace, "delete.txt")
		if err := os.WriteFile(deleteTarget, []byte("restore\nme\n"), 0o644); err != nil {
			t.Fatalf("seed delete target: %v", err)
		}
		blocker := filepath.Join(workspace, "blocking")
		if err := os.Mkdir(blocker, 0o755); err != nil {
			t.Fatalf("seed blocker: %v", err)
		}
		stage, err := createStagedFile(blocker, []byte("cannot commit\n"), 0o644)
		if err != nil {
			t.Fatalf("stage failing write: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(stage) })
		facts, err := commitStagedFiles(
			[]*patchFileState{{Exists: true, NewPath: blocker, Original: blocker, StagedPath: stage}},
			map[string]wholeFileDeletionTarget{
				deleteTarget: {OperationIDs: []patchformat.WholeFileDeletionOperationID{{HunkOrdinal: 0}}},
			},
		)
		if err == nil || len(facts) != 0 {
			t.Fatalf("rollback facts=%+v err=%v", facts, err)
		}
		assertPatchFileContent(t, deleteTarget, "restore\nme\n")
	})
}

func TestMoveAndFailedDeletionReturnNoFacts(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("move\n"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	move := callPatch(t, newPatchTestTool(t, workspace), "move",
		"*** Begin Patch\n*** Update File: source.txt\n*** Move to: destination.txt\n*** End Patch\n")
	missing := callPatch(t, newPatchTestTool(t, workspace), "missing",
		"*** Begin Patch\n*** Delete File: missing.txt\n*** End Patch\n")
	if move.IsError || len(deletionFacts(move)) != 0 ||
		!missing.IsError || len(deletionFacts(missing)) != 0 {
		t.Fatalf("move=%+v missing=%+v", move, missing)
	}
}

func deletionFacts(result tools.Result) []patchformat.WholeFileDeletionFact {
	if result.PresentationDelta == nil {
		return nil
	}
	return result.PresentationDelta.WholeFileDeletionFacts
}
