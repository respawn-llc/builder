package tools

import (
	"path/filepath"
	"testing"

	"core/shared/config"
)

func TestManagedWorktreePathContextResolvesMissingSecondaryRootByAncestor(t *testing.T) {
	currentRoot := t.TempDir()
	missingRoot := filepath.Join(t.TempDir(), "missing")
	context, err := NewManagedWorktreePathContextForRoots([]string{currentRoot, missingRoot}, &currentRoot)
	if err != nil {
		t.Fatalf("NewManagedWorktreePathContextForRoots: %v", err)
	}
	target := filepath.Join(missingRoot, "file.txt")
	resolved, err := config.ResolveExistingAncestorRealPath(target)
	if err != nil {
		t.Fatalf("ResolveExistingAncestorRealPath: %v", err)
	}
	if !context.IsForeignManagedWorktreePath(target, resolved) {
		t.Fatal("missing secondary managed root was not classified as foreign")
	}
}

func TestManagedWorktreePathContextRequiresExistingCurrentRoot(t *testing.T) {
	missingCurrent := filepath.Join(t.TempDir(), "missing")
	if _, err := NewManagedWorktreePathContextForRoots([]string{t.TempDir()}, &missingCurrent); err == nil {
		t.Fatal("missing current managed root was accepted")
	}
}
