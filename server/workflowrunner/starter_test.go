package workflowrunner

import (
	"path/filepath"
	"testing"

	"core/server/launch"
	"core/server/workflowstore"
	"core/shared/config"
)

func TestCurrentNodeManagedWorktreePathContextProtectsNamespaceWithoutCurrentWorktree(t *testing.T) {
	baseDir := t.TempDir()
	starter := &Starter{
		cfg: config.App{
			Settings: config.Settings{
				Worktrees: config.WorktreeSettings{BaseDir: baseDir},
			},
		},
	}

	pathContext, err := starter.currentNodeManagedWorktreePathContext(launch.SessionPlan{
		ManagedWorktreeRoots: []string{filepath.Join(baseDir, "other-agent")},
	}, workflowstore.ExecutionRoot{})
	if err != nil {
		t.Fatalf("currentNodeManagedWorktreePathContext: %v", err)
	}
	if pathContext == nil {
		t.Fatal("currentNodeManagedWorktreePathContext returned nil with configured managed-worktree base")
	}
	foreignPath := filepath.Join(baseDir, "other-agent", "file.txt")
	resolvedPath, err := config.ResolveExistingAncestorRealPath(foreignPath)
	if err != nil {
		t.Fatalf("ResolveExistingAncestorRealPath: %v", err)
	}
	if !pathContext.IsForeignManagedWorktreePath(resolvedPath) {
		t.Fatal("managed-worktree namespace path was not classified as foreign without a current Worktree")
	}
}
