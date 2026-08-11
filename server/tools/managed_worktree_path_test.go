package tools

import (
	"os"
	"path/filepath"
	"testing"

	"core/shared/config"
)

func TestManagedWorktreePathContextProtectsBaseWithoutKnownWorktrees(t *testing.T) {
	base := t.TempDir()
	workspaceRoot := filepath.Join(base, "ordinary-workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	context, err := NewManagedWorktreePathContext(base, nil, nil)
	if err != nil {
		t.Fatalf("new managed worktree path context: %v", err)
	}
	resolvedTarget, err := config.ResolveExistingAncestorRealPath(filepath.Join(workspaceRoot, "file.txt"))
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if !context.IsForeignManagedWorktreePath(resolvedTarget) {
		t.Fatal("path below managed base was not classified as foreign")
	}
}

func TestManagedWorktreePathContextRejectsOverlappingRoots(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("create nested roots: %v", err)
	}
	if _, err := NewManagedWorktreePathContext(base, nil, []string{outer, inner}); err == nil {
		t.Fatal("accepted nested managed roots")
	}
	if _, err := NewManagedWorktreePathContext(base, &inner, []string{outer}); err == nil {
		t.Fatal("accepted current root nested in a foreign root")
	}
}

func TestManagedWorktreePathContextRebindsOnlyToNonOverlappingRoot(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("create nested roots: %v", err)
	}
	context, err := NewManagedWorktreePathContext(base, &outer, []string{outer})
	if err != nil {
		t.Fatalf("new managed worktree path context: %v", err)
	}
	externalRoot := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(externalRoot, 0o755); err != nil {
		t.Fatalf("create external root: %v", err)
	}
	if _, err := context.WithCurrentWorktreeRoot(&externalRoot); err != nil {
		t.Fatalf("rebind to external current root: %v", err)
	}
	if _, err := context.WithCurrentWorktreeRoot(&inner); err == nil {
		t.Fatal("rebound to an unregistered nested root")
	}
}

func TestManagedWorktreePathContextDistinguishesCurrentForeignAndExternalPaths(t *testing.T) {
	base := t.TempDir()
	current := filepath.Join(base, "current")
	foreign := filepath.Join(base, "foreign")
	external := t.TempDir()
	for _, root := range []string{current, foreign} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create managed root %q: %v", root, err)
		}
	}
	context, err := NewManagedWorktreePathContext(base, &current, []string{current, foreign})
	if err != nil {
		t.Fatalf("new managed worktree path context: %v", err)
	}
	for _, test := range []struct {
		name    string
		path    string
		foreign bool
	}{
		{name: "current", path: filepath.Join(current, "file.txt")},
		{name: "foreign", path: filepath.Join(foreign, "file.txt"), foreign: true},
		{name: "external", path: filepath.Join(external, "file.txt")},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := config.ResolveExistingAncestorRealPath(test.path)
			if err != nil {
				t.Fatalf("resolve target: %v", err)
			}
			if got := context.IsForeignManagedWorktreePath(resolved); got != test.foreign {
				t.Fatalf("foreign classification = %t, want %t", got, test.foreign)
			}
		})
	}
}
