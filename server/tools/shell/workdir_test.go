package shell

import "testing"

func TestResolveWorkdirUsesWorkspaceRootByDefault(t *testing.T) {
	workspace := t.TempDir()
	if got := ResolveWorkdir(workspace, ""); got != workspace {
		t.Fatalf("ResolveWorkdir(%q, empty) = %q, want %q", workspace, got, workspace)
	}
}

func TestResolveWorkdirResolvesRelativePathsFromWorkspaceRoot(t *testing.T) {
	workspace := t.TempDir()
	want := workspace + "/subdir"
	if got := ResolveWorkdir(workspace, "subdir"); got != want {
		t.Fatalf("ResolveWorkdir(%q, subdir) = %q, want %q", workspace, got, want)
	}
}

func TestResolveWorkdirPreservesAbsoluteOverride(t *testing.T) {
	workspace := t.TempDir()
	override := t.TempDir()
	if got := ResolveWorkdir(workspace, override); got != override {
		t.Fatalf("ResolveWorkdir(%q, %q) = %q, want %q", workspace, override, got, override)
	}
}
