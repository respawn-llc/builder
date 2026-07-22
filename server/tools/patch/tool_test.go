package patch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteParticipatesInAtomicPatchCommit(t *testing.T) {
	dir := t.TempDir()
	deleteTarget := filepath.Join(dir, "delete.txt")
	keepTarget := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(deleteTarget, []byte("delete me\n"), 0o644); err != nil {
		t.Fatalf("write delete target: %v", err)
	}
	if err := os.WriteFile(keepTarget, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write keep target: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "atomic-delete", "*** Begin Patch\n*** Delete File: delete.txt\n*** Add File: added.txt\n+hello\n*** Update File: keep.txt\n-two\n+two\n*** End Patch\n")
	if !result.IsError {
		t.Fatalf("expected tool error result")
	}

	deleted, err := os.ReadFile(deleteTarget)
	if err != nil {
		t.Fatalf("read delete target after failure: %v", err)
	}
	if string(deleted) != "delete me\n" {
		t.Fatalf("unexpected delete target contents after rollback: %q", string(deleted))
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected added file absent after rollback, stat err=%v", err)
	}
	kept, err := os.ReadFile(keepTarget)
	if err != nil {
		t.Fatalf("read keep target after failure: %v", err)
	}
	if string(kept) != "one\n" {
		t.Fatalf("unexpected keep target contents after rollback: %q", string(kept))
	}
}
