package tools

import "testing"

func TestManagedWorktreePathContextRequiresLiveBaseRootResolver(t *testing.T) {
	if _, err := NewManagedWorktreePathContext(t.TempDir(), nil, nil, nil); err == nil {
		t.Fatal("managed Worktree path context accepted a missing live base-root resolver")
	}
}
