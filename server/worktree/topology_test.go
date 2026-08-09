package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/metadata"
	"core/shared/clientui"
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

func TestPreviewWorktreeDeleteResolvesCleanNonCurrentRegisteredTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-preview-clean")

	response, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  created.WorktreeID,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	if response.Worktree.Variant != serverapi.WorktreeTopologyVariantRegistered {
		t.Fatalf("preview topology = %+v, want registered", response.Worktree)
	}
	if response.DeletionSelector != created.WorktreeID {
		t.Fatalf("preview deletion selector = %q, want %q", response.DeletionSelector, created.WorktreeID)
	}
	if response.Cleanliness.Kind != clientui.WorktreeDirtyStateClean {
		t.Fatalf("preview cleanliness = %+v, want clean", response.Cleanliness)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("preview response validation: %v", err)
	}
}

func TestPreviewWorktreeDeleteBindsExternalConfirmationToCanonicalRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	branch := "feature/delete-preview-external-binding"
	rootA := filepath.Join(t.TempDir(), "external-a")
	rootB := filepath.Join(t.TempDir(), "external-b")
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", branch, rootA, "HEAD")
	t.Cleanup(func() {
		if _, err := os.Stat(rootA); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "remove", "--force", rootA)
		}
		if _, err := os.Stat(rootB); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "remove", "--force", rootB)
		}
	})

	preview, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  branch,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	canonicalRootA := canonicalTestPath(t, rootA)
	if preview.Worktree.Variant != serverapi.WorktreeTopologyVariantExternal ||
		preview.DeletionSelector != canonicalRootA {
		t.Fatalf("external preview = %+v, want root %q", preview, canonicalRootA)
	}

	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", rootA)
	runGit(t, env.workspaceRoot, "worktree", "add", rootB, branch)

	_, err = env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   env.session.Meta().SessionID,
		},
		Selector:            preview.DeletionSelector,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	var selectorErr *serverapi.WorktreeSelectorError
	if !errors.As(err, &selectorErr) || selectorErr.Kind != serverapi.WorktreeSelectorErrorKindNotFound {
		t.Fatalf("DeleteWorktree error = %v, want typed selector-not-found for root A", err)
	}
	if _, err := os.Stat(rootB); err != nil {
		t.Fatalf("replacement external root B = %v, want untouched", err)
	}
}

func TestPreviewWorktreeDeleteClassifiesModifiedUntrackedAndMixedDirtyStates(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T, string)
		wantCount int
	}{
		{
			name: "modified",
			prepare: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("modified\n"), 0o644); err != nil {
					t.Fatalf("WriteFile modified file: %v", err)
				}
			},
			wantCount: 1,
		},
		{
			name: "untracked",
			prepare: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
					t.Fatalf("WriteFile untracked file: %v", err)
				}
			},
			wantCount: 1,
		},
		{
			name: "mixed",
			prepare: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("modified\n"), 0o644); err != nil {
					t.Fatalf("WriteFile modified file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
					t.Fatalf("WriteFile untracked file: %v", err)
				}
			},
			wantCount: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newServiceTestEnv(t)
			created := mustCreateWorktree(t, env, "feature/delete-preview-"+test.name)
			test.prepare(t, created.CanonicalRoot)

			response, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
				SessionID: env.session.Meta().SessionID,
				Selector:  created.WorktreeID,
			})
			if err != nil {
				t.Fatalf("PreviewWorktreeDelete: %v", err)
			}
			if response.Cleanliness.Kind != clientui.WorktreeDirtyStateDirty ||
				response.Cleanliness.DirtyFileCount == nil ||
				*response.Cleanliness.DirtyFileCount != test.wantCount {
				t.Fatalf("preview cleanliness = %+v, want dirty count %d", response.Cleanliness, test.wantCount)
			}
		})
	}
}

func TestPreviewWorktreeDeleteHandlesInspectionFailureCancellationAndMainWorkspace(t *testing.T) {
	t.Run("inspection failure becomes unknown", func(t *testing.T) {
		env := newServiceTestEnv(t)
		createExternalWorktree(t, env, "feature/delete-preview-unknown")
		env.service.git = NewGitInspector(&previewStatusRunner{
			listOutput: []byte(runGit(t, env.workspaceRoot, "worktree", "list", "--porcelain")),
			outputErr:  errors.New("status inspection failed"),
		})

		response, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
			SessionID: env.session.Meta().SessionID,
			Selector:  "feature/delete-preview-unknown",
		})
		if err != nil {
			t.Fatalf("PreviewWorktreeDelete: %v", err)
		}
		if response.Cleanliness.Kind != clientui.WorktreeDirtyStateUnknown ||
			response.Cleanliness.UnknownCause == nil ||
			strings.TrimSpace(*response.Cleanliness.UnknownCause) == "" {
			t.Fatalf("preview cleanliness = %+v, want unknown diagnostic", response.Cleanliness)
		}
	})

	t.Run("cancellation remains an error", func(t *testing.T) {
		env := newServiceTestEnv(t)
		ctx, cancel := context.WithCancel(env.ctx)
		defer cancel()
		createExternalWorktree(t, env, "feature/delete-preview-canceled")
		env.service.git = NewGitInspector(&previewStatusRunner{
			listOutput: []byte(runGit(t, env.workspaceRoot, "worktree", "list", "--porcelain")),
			outputErr:  errors.New("status inspection canceled"),
			cancel:     cancel,
		})

		_, err := env.service.PreviewWorktreeDelete(ctx, serverapi.WorktreeDeletePreviewRequest{
			SessionID: env.session.Meta().SessionID,
			Selector:  "feature/delete-preview-canceled",
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PreviewWorktreeDelete error = %v, want context.Canceled", err)
		}
	})

	t.Run("main workspace is blocked", func(t *testing.T) {
		env := newServiceTestEnv(t)
		_, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
			SessionID: env.session.Meta().SessionID,
			Selector:  env.workspaceRoot,
		})
		if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
			t.Fatalf("PreviewWorktreeDelete error = %v, want ErrWorktreeBlocked", err)
		}
	})
}

func TestPreviewWorktreeDeleteLeavesTopologyAndSubsequentOperationsUnchanged(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-preview-read-only")
	before, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{
		SessionID: env.session.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("ListWorktrees before preview: %v", err)
	}

	if _, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  created.WorktreeID,
	}); err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}

	after, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{
		SessionID: env.session.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("ListWorktrees after preview: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("worktree list changed after read-only preview:\nbefore=%+v\nafter=%+v", before, after)
	}
	if _, err := env.service.ResolveWorktreeSelector(env.ctx, serverapi.WorktreeSelectorPreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  created.WorktreeID,
	}); err != nil {
		t.Fatalf("ResolveWorktreeSelector after preview: %v", err)
	}
}

func TestPreviewWorktreeDeleteDoesNotHoldMutationLane(t *testing.T) {
	env := newServiceTestEnv(t)
	target := mustCreateWorktree(t, env, "feature/delete-preview-mutation-lane")
	runner := newPreviewMutationStatusRunner()
	env.service.git = NewGitInspector(runner)

	type previewResult struct {
		response serverapi.WorktreeDeletePreviewResponse
		err      error
	}
	previewDone := make(chan previewResult, 1)
	go func() {
		response, err := env.service.PreviewWorktreeDelete(env.ctx, serverapi.WorktreeDeletePreviewRequest{
			SessionID: env.session.Meta().SessionID,
			Selector:  target.WorktreeID,
		})
		previewDone <- previewResult{response: response, err: err}
	}()

	select {
	case <-runner.statusStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("preview did not reach cleanliness inspection")
	}

	createDone := make(chan struct {
		response serverapi.WorktreeCreateResponse
		err      error
	}, 1)
	go func() {
		response, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			SessionID:        env.session.Meta().SessionID,
			BaseRef:          "HEAD",
			CreateBranch:     true,
			BranchName:       "feature/delete-preview-independent-mutation",
		})
		createDone <- struct {
			response serverapi.WorktreeCreateResponse
			err      error
		}{response: response, err: err}
	}()

	var created serverapi.WorktreeCreateResponse
	select {
	case result := <-createDone:
		if result.err != nil {
			t.Fatalf("independent CreateWorktree: %v", result.err)
		}
		created = result.response
	case <-time.After(3 * time.Second):
		t.Fatal("independent mutation waited for preview mutation lane")
	}
	if created.Worktree.Topology.Variant != serverapi.WorktreeTopologyVariantRegistered {
		t.Fatalf("independent mutation worktree = %+v, want registered", created.Worktree)
	}

	runner.ReleaseStatus()
	select {
	case result := <-previewDone:
		if result.err != nil {
			t.Fatalf("PreviewWorktreeDelete: %v", result.err)
		}
		if result.response.Cleanliness.Kind != clientui.WorktreeDirtyStateClean {
			t.Fatalf("preview cleanliness = %+v, want clean", result.response.Cleanliness)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preview did not complete after cleanliness inspection release")
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

func TestListWorkspaceWorktreesProjectsMarkerlessTopology(t *testing.T) {
	env := newServiceTestEnv(t)

	response, err := env.service.ListWorkspaceWorktrees(env.ctx, serverapi.WorktreeWorkspaceListRequest{
		ProjectID:   env.binding.ProjectID,
		WorkspaceID: env.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("ListWorkspaceWorktrees: %v", err)
	}
	if response.WorkspaceID != env.binding.WorkspaceID || len(response.Worktrees) != 1 {
		t.Fatalf("workspace list = %+v", response)
	}
	if response.Worktrees[0].Projection.IsCurrent {
		t.Fatalf("workspace projection = %+v, want no session marker", response.Worktrees[0].Projection)
	}
	if strings.TrimSpace(response.Worktrees[0].Projection.Selector) == "" {
		t.Fatalf("workspace projection = %+v, want selector", response.Worktrees[0].Projection)
	}
}

func TestListWorkspaceWorktreesRejectsWorkspaceFromAnotherProject(t *testing.T) {
	env := newServiceTestEnv(t)

	_, err := env.service.ListWorkspaceWorktrees(env.ctx, serverapi.WorktreeWorkspaceListRequest{
		ProjectID:   "another-project",
		WorkspaceID: env.binding.WorkspaceID,
	})
	if err == nil {
		t.Fatal("ListWorkspaceWorktrees succeeded for a workspace from another project")
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

type previewStatusRunner struct {
	listOutput []byte
	outputErr  error
	cancel     context.CancelFunc
}

type previewMutationStatusRunner struct {
	statusStarted chan struct{}
	releaseStatus chan struct{}
	startOnce     sync.Once
	releaseOnce   sync.Once
}

func newPreviewMutationStatusRunner() *previewMutationStatusRunner {
	return &previewMutationStatusRunner{
		statusStarted: make(chan struct{}),
		releaseStatus: make(chan struct{}),
	}
}

func (r *previewMutationStatusRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) == 3 && args[0] == "status" && args[1] == "--porcelain=v1" && args[2] == "-z" {
		r.startOnce.Do(func() { close(r.statusStarted) })
		select {
		case <-r.releaseStatus:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return execGitCommandRunner{}.Output(ctx, dir, args...)
}

func (r *previewMutationStatusRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	return execGitCommandRunner{}.Run(ctx, dir, args...)
}

func (r *previewMutationStatusRunner) ReleaseStatus() {
	r.releaseOnce.Do(func() { close(r.releaseStatus) })
}

func (r *previewStatusRunner) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) == 3 && args[0] == "status" {
		if r.cancel != nil {
			r.cancel()
		}
		return nil, r.outputErr
	}
	return append([]byte(nil), r.listOutput...), nil
}

func (r *previewStatusRunner) Run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	if len(args) == 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		return append([]byte(nil), r.listOutput...), 0, nil
	}
	return nil, 1, errors.New("unexpected Git command")
}
