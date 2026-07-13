package worktree

import (
	"os"
	"path/filepath"
	"strings"
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
		{ID: "legacy-main", WorkspaceID: env.binding.WorkspaceID, CanonicalRoot: env.workspaceRoot, DisplayName: "main", OriginSessionID: "origin-session", CreatedAt: time.Now().UTC()},
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
	registered := entries[0].Registered
	if registered == nil || registered.Git.BranchRef == nil || registered.Git.BranchName == nil {
		t.Fatalf("registered Git facts = %+v, want branch ref and name", registered)
	}
	if registered.Kent.OriginSessionID == nil || *registered.Kent.OriginSessionID != "origin-session" {
		t.Fatalf("registered Kent facts = %+v, want origin session", registered.Kent)
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

func TestListWorktreesProjectsSelectorsAndCurrentStateWithoutReconcilingMissingMetadata(t *testing.T) {
	env := newServiceTestEnv(t)
	missingRoot := filepath.Join(t.TempDir(), "missing")
	record := metadata.WorktreeRecord{
		ID:            "legacy-missing",
		WorkspaceID:   env.binding.WorkspaceID,
		CanonicalRoot: missingRoot,
		DisplayName:   "missing",
		CreatedAt:     time.Now().UTC(),
	}
	if err := env.store.UpsertWorktreeRecord(env.ctx, record); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}

	response, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{
		SessionID: env.session.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(response.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want live main and missing metadata", response.Worktrees)
	}
	if !response.Worktrees[0].Projection.IsCurrent {
		t.Fatalf("main projection = %+v, want current", response.Worktrees[0].Projection)
	}
	if response.Worktrees[1].Topology.Variant != serverapi.WorktreeTopologyVariantMissing {
		t.Fatalf("missing topology = %+v", response.Worktrees[1].Topology)
	}
	for index, entry := range response.Worktrees {
		match, err := resolveTopologySelector(topologies(response.Worktrees), entry.Projection.Selector)
		if err != nil {
			t.Fatalf("selector %q: %v", entry.Projection.Selector, err)
		}
		if match.index != index {
			t.Fatalf("selector %q resolved to %d, want %d", entry.Projection.Selector, match.index, index)
		}
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, record.ID); err != nil {
		t.Fatalf("missing metadata was reconciled during list: %v", err)
	}
}

func TestProjectTopologyRejectsDuplicateGitAndKentRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktree")
	gitEntry := GitWorktree{Root: root, HeadOID: strings.Repeat("a", 40)}
	record := metadata.WorktreeRecord{
		ID:            "worktree-a",
		WorkspaceID:   "workspace-a",
		CanonicalRoot: root,
		DisplayName:   "worktree",
	}
	tests := []struct {
		name    string
		git     []GitWorktree
		records []metadata.WorktreeRecord
	}{
		{name: "git", git: []GitWorktree{gitEntry, gitEntry}, records: []metadata.WorktreeRecord{record}},
		{name: "kent", git: []GitWorktree{gitEntry}, records: []metadata.WorktreeRecord{record, record}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := projectTopologyEntries(test.git, test.records); err == nil {
				t.Fatal("projectTopologyEntries succeeded, want duplicate-root invariant error")
			}
		})
	}
}

func TestCreateRegistersOnlyTheCreatedWorktreeWithoutReconcilingOtherTopology(t *testing.T) {
	env := newServiceTestEnv(t)
	response, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID:  "create-without-reconcile",
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/explicit-register",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	records, err := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("ListWorktreeRecordsByWorkspaceID: %v", err)
	}
	if len(records) != 1 || records[0].ID != worktreeIDFromListEntry(response.Worktree) {
		t.Fatalf("records = %+v, want only created worktree", records)
	}
	list, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{SessionID: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(list.Worktrees) != 2 ||
		list.Worktrees[0].Topology.Variant != serverapi.WorktreeTopologyVariantExternal ||
		list.Worktrees[1].Topology.Variant != serverapi.WorktreeTopologyVariantRegistered {
		t.Fatalf("topology = %+v, want external main followed by registered created worktree", list.Worktrees)
	}
}

func topologies(entries []serverapi.WorktreeListEntry) []serverapi.WorktreeTopologyEntry {
	out := make([]serverapi.WorktreeTopologyEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Topology)
	}
	return out
}
