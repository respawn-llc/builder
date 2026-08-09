package workflowrunner

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"core/server/launch"
	"core/server/tools"
	"core/server/workflowstore"
	"core/shared/config"
)

func TestCurrentNodeManagedWorktreePathContextProtectsNamespaceWithoutCurrentWorktree(t *testing.T) {
	baseDir := t.TempDir()
	persistenceRoot := t.TempDir()
	configText := "[worktrees]\nbase_dir = " + strconv.Quote(baseDir) + "\n"
	if err := os.WriteFile(filepath.Join(persistenceRoot, "config.toml"), []byte(configText), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	starter := &Starter{
		cfg: config.App{
			PersistenceRoot: persistenceRoot,
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
	if err := pathContext.CheckMutationPath(resolvedPath); !errors.Is(err, tools.ErrForeignManagedWorktreeEditDenied) {
		t.Fatalf("managed-worktree namespace path error = %v, want foreign Worktree denial", err)
	}
}
