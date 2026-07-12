package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/shared/serverapi"
)

func TestProjectTopologyReturnsRegisteredExternalAndMissingInRequiredOrder(t *testing.T) {
	env := newServiceTestEnv(t)
	externalRoot := filepath.Join(t.TempDir(), "external")
	runGit(t, env.workspaceRoot, "worktree", "add", "--detach", externalRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", externalRoot) })
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if err := os.MkdirAll(missingRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll missing root: %v", err)
	}
	for _, record := range []metadata.WorktreeRecord{
		{ID: "legacy-main", WorkspaceID: env.binding.WorkspaceID, CanonicalRoot: env.workspaceRoot, DisplayName: "main", CreatedAt: time.Now().UTC()},
		{ID: "legacy-missing", WorkspaceID: env.binding.WorkspaceID, CanonicalRoot: missingRoot, DisplayName: "missing", CreatedAt: time.Now().UTC()},
	} {
		if err := env.store.UpsertWorktreeRecord(env.ctx, record); err != nil {
			t.Fatalf("UpsertWorktreeRecord: %v", err)
		}
	}
	entries, err := env.service.projectTopology(env.ctx, env.binding.WorkspaceID, env.workspaceRoot)
	if err != nil {
		t.Fatalf("projectTopology: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("topology entries = %+v", entries)
	}
	if entries[0].Variant != "registered" || entries[1].Variant != "external" || entries[2].Variant != "missing" {
		t.Fatalf("topology variants = %+v", entries)
	}
}

func TestResolveWorktreeSelectorUsesReadOnlyTopology(t *testing.T) {
	env := newServiceTestEnv(t)
	response, err := env.service.ResolveWorktreeSelector(env.ctx, serverapi.WorktreeSelectorPreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  env.workspaceRoot,
	})
	if err != nil {
		t.Fatalf("ResolveWorktreeSelector: %v", err)
	}
	if response.Worktree.Variant != serverapi.WorktreeTopologyVariantExternal || response.Selector == "" {
		t.Fatalf("selector preview = %+v", response)
	}
}
