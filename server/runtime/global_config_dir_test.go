package runtime

import (
	"path/filepath"
	"testing"
)

func TestAgentsInjectionPathsUsesGlobalConfigDirWhenSet(t *testing.T) {
	t.Parallel()
	globalDir := t.TempDir()
	workspace := t.TempDir()
	paths, err := agentsInjectionPaths(workspace, globalDir)
	if err != nil {
		t.Fatalf("agentsInjectionPaths: %v", err)
	}
	wantGlobal := filepath.Clean(filepath.Join(globalDir, agentsFileName))
	if len(paths) == 0 || paths[0] != wantGlobal {
		t.Fatalf("global AGENTS.md path = %v, want first entry %q", paths, wantGlobal)
	}
	wantWorkspace := filepath.Clean(filepath.Join(workspace, agentsFileName))
	if !containsPath(paths, wantWorkspace) {
		t.Fatalf("workspace AGENTS.md path missing from %v, want %q", paths, wantWorkspace)
	}
}

func TestAgentsInjectionPathsFallsBackToHomeWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	paths, err := agentsInjectionPaths(t.TempDir(), "")
	if err != nil {
		t.Fatalf("agentsInjectionPaths: %v", err)
	}
	wantGlobal := filepath.Clean(filepath.Join(home, agentsGlobalDirName, agentsFileName))
	if len(paths) == 0 || paths[0] != wantGlobal {
		t.Fatalf("default global AGENTS.md path = %v, want first entry %q", paths, wantGlobal)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
