package patch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/tools"
	patchformat "core/shared/transcript/patchformat"
)

func TestDeleteFileReturnsLogicalLineCountFacts(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantRemoved int
	}{
		{name: "multi line", content: "first\r\nsecond\r\n", wantRemoved: 2},
		{name: "empty", content: "", wantRemoved: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			target := filepath.Join(workspace, "delete.txt")
			if err := os.WriteFile(target, []byte(test.content), 0o644); err != nil {
				t.Fatalf("seed delete target: %v", err)
			}

			result := callPatch(
				t,
				newPatchTestTool(t, workspace),
				"delete-count",
				"*** Begin Patch\n*** Delete File: delete.txt\n*** End Patch\n",
			)
			if result.IsError {
				t.Fatalf("delete failed: %s", string(result.Output))
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("delete target remains, stat err=%v", err)
			}
			facts := result.PresentationDelta.WholeFileDeletionFacts
			if len(facts) != 1 {
				t.Fatalf("deletion facts = %d, want one", len(facts))
			}
			wantID := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
			if facts[0].ID != wantID || facts[0].Removed != test.wantRemoved {
				t.Fatalf("deletion fact = %+v, want identity %+v and removed %d", facts[0], wantID, test.wantRemoved)
			}
		})
	}
}

func TestCommitStagedFilesCountsTheSnapshotItRemoves(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "delete.txt")
	if err := os.WriteFile(target, []byte("validated earlier\n"), 0o644); err != nil {
		t.Fatalf("seed delete target: %v", err)
	}
	if err := os.WriteFile(target, []byte("commit\nsnapshot\ncontent\n"), 0o644); err != nil {
		t.Fatalf("change target before commit: %v", err)
	}
	operationID := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 4}

	facts, err := commitStagedFiles(nil, map[string]patchformat.WholeFileDeletionOperationID{
		target: operationID,
	})
	if err != nil {
		t.Fatalf("commit deletion: %v", err)
	}
	if len(facts) != 1 || facts[0].ID != operationID || facts[0].Removed != 3 {
		t.Fatalf("commit deletion facts = %+v, want later three-line snapshot", facts)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete target remains, stat err=%v", err)
	}
}

func TestFailedDeleteOperationsReturnNoCountFacts(t *testing.T) {
	t.Run("path denied", func(t *testing.T) {
		workspace := t.TempDir()
		target := filepath.Join(workspace, "denied.txt")
		if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
			t.Fatalf("seed denied target: %v", err)
		}
		result := callPatch(
			t,
			newPatchTestTool(t, workspace, WithPathDenyPolicy(
				compileLiteralTreeDenyPolicy(t, target, "deny deletion"),
			)),
			"delete-denied",
			"*** Begin Patch\n*** Delete File: denied.txt\n*** End Patch\n",
		)
		if !result.IsError {
			t.Fatal("path-denied deletion succeeded")
		}
		assertNoWholeFileDeletionFacts(t, result)
		assertPatchFileContent(t, target, "keep\n")
	})

	t.Run("outside approval denied", func(t *testing.T) {
		workspace := t.TempDir()
		outsideRoot := outsideNonTempDir(t)
		target := filepath.Join(outsideRoot, "outside.txt")
		if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
			t.Fatalf("seed outside target: %v", err)
		}
		result := callPatch(
			t,
			newPatchTestTool(t, workspace, WithOutsideWorkspaceApprover(
				func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
					return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionDeny}, nil
				},
			)),
			"delete-outside-denied",
			"*** Begin Patch\n*** Delete File: "+target+"\n*** End Patch\n",
		)
		if !result.IsError {
			t.Fatal("unapproved outside deletion succeeded")
		}
		assertNoWholeFileDeletionFacts(t, result)
		assertPatchFileContent(t, target, "keep\n")
	})

	t.Run("missing", func(t *testing.T) {
		workspace := t.TempDir()
		result := callPatch(
			t,
			newPatchTestTool(t, workspace),
			"delete-missing",
			"*** Begin Patch\n*** Delete File: missing.txt\n*** End Patch\n",
		)
		if !result.IsError {
			t.Fatal("missing deletion succeeded")
		}
		assertNoWholeFileDeletionFacts(t, result)
	})

	t.Run("non regular", func(t *testing.T) {
		workspace := t.TempDir()
		target := filepath.Join(workspace, "directory")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("seed directory target: %v", err)
		}
		result := callPatch(
			t,
			newPatchTestTool(t, workspace),
			"delete-directory",
			"*** Begin Patch\n*** Delete File: directory\n*** End Patch\n",
		)
		if !result.IsError {
			t.Fatal("non-regular deletion succeeded")
		}
		assertNoWholeFileDeletionFacts(t, result)
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory target changed, info=%v err=%v", info, err)
		}
	})
}

func TestRolledBackDeletionReturnsNoCountFacts(t *testing.T) {
	workspace := t.TempDir()
	deleteTarget := filepath.Join(workspace, "delete.txt")
	if err := os.WriteFile(deleteTarget, []byte("restore\nme\n"), 0o644); err != nil {
		t.Fatalf("seed delete target: %v", err)
	}
	blockingDirectory := filepath.Join(workspace, "blocking")
	if err := os.Mkdir(blockingDirectory, 0o755); err != nil {
		t.Fatalf("seed blocking directory: %v", err)
	}
	stage, err := createStagedFile(blockingDirectory, []byte("cannot commit\n"), 0o644)
	if err != nil {
		t.Fatalf("stage failing write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(stage) })

	facts, err := commitStagedFiles(
		[]*patchFileState{{
			Exists:     true,
			NewPath:    blockingDirectory,
			Original:   blockingDirectory,
			StagedPath: stage,
		}},
		map[string]patchformat.WholeFileDeletionOperationID{
			deleteTarget: {HunkOrdinal: 0},
		},
	)
	if err == nil {
		t.Fatal("expected commit failure")
	}
	if len(facts) != 0 {
		t.Fatalf("rolled-back deletion returned facts: %+v", facts)
	}
	assertPatchFileContent(t, deleteTarget, "restore\nme\n")
}

func TestDuplicateCanonicalDeleteTargetsAreRejectedBeforeCommit(t *testing.T) {
	tests := []struct {
		name       string
		secondPath func(*testing.T, string, string) string
	}{
		{
			name: "same path",
			secondPath: func(_ *testing.T, _ string, _ string) string {
				return "target.txt"
			},
		},
		{
			name: "symlink alias",
			secondPath: func(t *testing.T, workspace, target string) string {
				alias := filepath.Join(workspace, "alias.txt")
				if err := os.Symlink(target, alias); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				return "alias.txt"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			target := filepath.Join(workspace, "target.txt")
			if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
				t.Fatalf("seed duplicate target: %v", err)
			}
			secondPath := test.secondPath(t, workspace, target)
			result := callPatch(
				t,
				newPatchTestTool(t, workspace),
				"duplicate-delete",
				"*** Begin Patch\n*** Delete File: target.txt\n*** Delete File: "+secondPath+"\n*** End Patch\n",
			)
			if !result.IsError {
				t.Fatal("duplicate canonical deletion succeeded")
			}
			if payload := toolFailurePayload(t, result); payload.Kind != string(failureKindMalformedSyntax) {
				t.Fatalf("duplicate canonical deletion kind = %q, want malformed", payload.Kind)
			}
			assertNoWholeFileDeletionFacts(t, result)
			assertPatchFileContent(t, target, "keep\n")
		})
	}
}

func assertNoWholeFileDeletionFacts(t *testing.T, result tools.Result) {
	t.Helper()
	if result.PresentationDelta != nil &&
		len(result.PresentationDelta.WholeFileDeletionFacts) != 0 {
		t.Fatalf("failed deletion returned facts: %+v", result.PresentationDelta.WholeFileDeletionFacts)
	}
}
