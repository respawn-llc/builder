package worktree

import (
	"context"
	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/sessioncontract"
	"core/shared/worktreecontract"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeleteWorktreeBlocksWhenBackgroundProcessUsesDescendantPath(t *testing.T) {
	env := newServiceTestEnv(t)
	busy := mustCreateWorktree(t, env, "feature/delete-blocked-process")
	unrelated := mustCreateWorktree(t, env, "feature/delete-unrelated-process")
	busySession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, busySession.Meta().SessionID, env.binding.WorkspaceID, busy.WorktreeID, ".")
	state := captureDeleteTargetState(t, env, busySession.Meta().SessionID, busy)
	env.processes.snapshots = []shelltool.Snapshot{{ID: "proc-1", Command: "sleep 30", Workdir: filepath.Join(busy.CanonicalRoot, "tmp"), Running: true}}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, busy.WorktreeID))
	if !errors.Is(err, worktreecontract.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree error = %v, want ErrWorktreeBlocked", err)
	}
	snapshots := env.processes.CurrentSnapshots()
	if len(snapshots) != 1 || !snapshots[0].Running {
		t.Fatalf("background process snapshot changed after blocked delete: %+v", snapshots)
	}
	state.assertUnchanged(t, env, busySession.Meta().SessionID, busy.WorktreeID)

	result, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, unrelated.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree unrelated = %+v, %v; want completed", result, err)
	}
}

func TestBeginMutationSerializesMutationsByWorkspace(t *testing.T) {
	env := newServiceTestEnv(t)
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)

	firstRelease, _, err := env.service.beginWorkspaceMutation(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("beginMutation first: %v", err)
	}
	firstReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			firstRelease()
		}
	})

	type mutationResult struct {
		release func()
		err     error
	}
	resultCh := make(chan mutationResult, 1)
	go func() {
		release, _, err := env.service.beginWorkspaceMutation(env.ctx, otherSession.Meta().SessionID)
		resultCh <- mutationResult{release: release, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.release != nil {
			result.release()
		}
		t.Fatalf("expected second mutation to wait for workspace lock, got err=%v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	firstRelease()
	firstReleased = true
	var result mutationResult
	select {
	case result = <-resultCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second mutation")
	}
	if result.err != nil {
		t.Fatalf("beginMutation second: %v", result.err)
	}
	if result.release == nil {
		t.Fatal("expected second mutation lease")
	}
	result.release()
}

func TestBeginMutationReacquiresWorkspaceLaneWhenSessionWorkspaceChanges(t *testing.T) {
	env := newServiceTestEnv(t)
	secondWorkspace := t.TempDir()
	initGitRepo(t, secondWorkspace)
	secondCfg, err := config.Load(secondWorkspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load second workspace: %v", err)
	}
	secondBinding, err := env.store.AttachWorkspaceToProject(env.ctx, env.binding.ProjectID, secondCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject second workspace: %v", err)
	}
	secondSession := createServiceTestSession(t, env.store, secondCfg, secondBinding)

	firstWorkspaceLease, err := env.service.workspaceMutations.Acquire(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("acquire workspace mutation lease: %v", err)
	}
	firstLockReleased := false
	defer func() {
		if !firstLockReleased {
			firstWorkspaceLease.Release()
		}
	}()

	type mutationResult struct {
		release      func()
		workspaceCtx sessionWorkspaceContext
		err          error
	}
	firstCh := make(chan mutationResult, 1)
	go func() {
		release, workspaceCtx, err := env.service.beginWorkspaceMutation(env.ctx, env.session.Meta().SessionID)
		firstCh <- mutationResult{release: release, workspaceCtx: workspaceCtx, err: err}
	}()

	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, secondBinding.WorkspaceID, "", ".")
	firstWorkspaceLease.Release()
	firstLockReleased = true

	var first mutationResult
	select {
	case first = <-firstCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first mutation")
	}
	if first.err != nil {
		t.Fatalf("beginMutation first: %v", first.err)
	}
	if first.release == nil {
		t.Fatal("expected first mutation lease")
	}
	if first.workspaceCtx.workspaceID != secondBinding.WorkspaceID {
		first.release()
		t.Fatalf("first mutation workspace id = %q, want %q", first.workspaceCtx.workspaceID, secondBinding.WorkspaceID)
	}

	secondCh := make(chan mutationResult, 1)
	go func() {
		release, workspaceCtx, err := env.service.beginWorkspaceMutation(env.ctx, secondSession.Meta().SessionID)
		secondCh <- mutationResult{release: release, workspaceCtx: workspaceCtx, err: err}
	}()
	select {
	case result := <-secondCh:
		if result.release != nil {
			result.release()
		}
		first.release()
		t.Fatalf("expected second mutation to block on reacquired workspace lock, got %+v", result)
	case <-time.After(150 * time.Millisecond):
	}

	first.release()
	select {
	case result := <-secondCh:
		if result.err != nil {
			t.Fatalf("beginMutation second: %v", result.err)
		}
		if result.release == nil {
			t.Fatal("expected second mutation lease")
		}
		if result.workspaceCtx.workspaceID != secondBinding.WorkspaceID {
			result.release()
			t.Fatalf("second mutation workspace id = %q, want %q", result.workspaceCtx.workspaceID, secondBinding.WorkspaceID)
		}
		result.release()
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second mutation")
	}
}

func newServiceTestEnv(t *testing.T) *serviceTestEnv {
	return newServiceTestEnvWithResourceLifecycle(t, nil)
}

func newServiceTestEnvWithResourceLifecycle(t *testing.T, lifecycle sessionruntime.AgentResourceLifecycle) *serviceTestEnv {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, ".kent-test"))
	initGitRepo(t, workspace)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	store := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := store.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	sess := createServiceTestSession(t, store, cfg, binding)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot:   cfg.PersistenceRoot,
		StoreOptions:      store.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	publisher := &serviceTestPublisher{}
	processes := &serviceTestProcessSource{}
	service := NewService(store, nil, authority, publisher, processes, ServiceOptions{BaseDir: cfg.Settings.Worktrees.BaseDir})
	return &serviceTestEnv{
		t:             t,
		ctx:           ctx,
		store:         store,
		cfg:           cfg,
		binding:       binding,
		session:       sess,
		authority:     authority,
		publisher:     publisher,
		processes:     processes,
		service:       service,
		leaseID:       "lease-1",
		workspaceRoot: canonicalWorkspaceRoot,
		baseDir:       cfg.Settings.Worktrees.BaseDir,
	}
}

func createServiceTestSession(t *testing.T, store *metadata.Store, cfg config.App, binding metadata.Binding) *session.Store {
	t.Helper()
	projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return sess
}

func initGitRepo(t *testing.T, root string) {
	t.Helper()
	testsetup.InitializeGitRepository(t, root)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return testsetup.RunGit(t, dir, args...)
}

func mustLocalBranch(t *testing.T, name string) *localBranch {
	t.Helper()
	branch, err := newLocalBranchName(name)
	if err != nil {
		t.Fatalf("newLocalBranchName(%q): %v", name, err)
	}
	return &branch
}

func writeExecutableFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func waitForSetupPayload(t *testing.T, path string) setupScriptPayload {
	t.Helper()
	var payload setupScriptPayload
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false
			}
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return false
		}
		return true
	}, "timed out waiting for setup payload at %s", path)
	return payload
}

func waitForFileText(t *testing.T, path string) string {
	t.Helper()
	var text string
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false
			}
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		text = strings.TrimSpace(string(body))
		return text != ""
	}, "timed out waiting for text file at %s", path)
	return text
}

func waitForFileLines(t *testing.T, path string) []string {
	t.Helper()
	text := waitForFileText(t, path)
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func nextSetupTerminalEvent(t *testing.T, sub SetupSubscription) *worktreepb.SetupEvent {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		evt, err := sub.Next(deadline)
		if err != nil {
			t.Fatalf("setup event: %v", err)
		}
		if evt.GetCompleted() != nil || evt.GetFailed() != nil {
			return evt
		}
	}
}

func assertServiceTestSessionTarget(t *testing.T, env *serviceTestEnv, worktreeID string, workdir string) {
	t.Helper()
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(target) != worktreeID || target.EffectiveWorkdir != workdir {
		t.Fatalf("session target = %+v, want worktree_id=%q workdir=%q", target, worktreeID, workdir)
	}
}

func mustResolveServiceTestTarget(t *testing.T, env *serviceTestEnv) clientui.SessionExecutionTarget {
	t.Helper()
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	return target
}

type serviceTestWorktree struct {
	WorktreeID      string
	DisplayName     string
	CanonicalRoot   string
	BranchRef       string
	BranchName      string
	Detached        bool
	IsMain          bool
	IsCurrent       bool
	Managed         bool
	CreatedBranch   bool
	OriginSessionID string
}

func mustCreateWorktree(t *testing.T, env *serviceTestEnv, branchName string) serviceTestWorktree {
	t.Helper()
	setupOperationID := worktreecontract.NewSetupOperationID()
	baseRef := "HEAD"
	resp, err := env.service.CreateWorktree(env.ctx, &worktreepb.CreateRequest{
		SetupOperationId: setupOperationID.String(),
		SessionId:        env.session.Meta().SessionID,
		Spec: &worktreepb.CreateSpec{
			BaseRef:      &baseRef,
			CreateBranch: true,
			BranchName:   &branchName,
		},
	})
	if err != nil {
		t.Fatalf("CreateWorktree(%s): %v", branchName, err)
	}
	return worktreeViewFromListEntryForTest(resp.Worktree)
}

func worktreeDeleteRequest(env *serviceTestEnv, worktreeID string) *worktreepb.DeleteRequest {
	return &worktreepb.DeleteRequest{
		SessionId:           env.session.Meta().SessionID,
		Selector:            worktreeID,
		BranchCleanupPolicy: worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN,
	}
}

func updateServiceTestSessionTarget(t *testing.T, env *serviceTestEnv, sessionID, workspaceID, worktreeID, cwdRelpath string) {
	t.Helper()
	var worktree *metadata.SessionExecutionTargetUpdateWorktree
	if strings.TrimSpace(worktreeID) != "" {
		worktree = &metadata.SessionExecutionTargetUpdateWorktree{ID: worktreeID}
	}
	if err := env.store.UpdateSessionExecutionTarget(env.ctx, metadata.SessionExecutionTargetUpdate{SessionID: sessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: workspaceID}, Worktree: worktree, CwdRelpath: cwdRelpath}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget %s: %v", sessionID, err)
	}
}

func mustListWorktrees(t *testing.T, env *serviceTestEnv) *worktreepb.ListSuccess {
	t.Helper()
	resp, err := env.service.ListWorktrees(env.ctx, &worktreepb.ListRequest{SessionId: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	return resp
}

func findWorktreeByID(t *testing.T, worktrees []*worktreepb.ListEntry, worktreeID string) serviceTestWorktree {
	t.Helper()
	for _, entry := range worktrees {
		if worktreeIDFromListEntry(entry) == worktreeID {
			return worktreeViewFromListEntryForTest(entry)
		}
	}
	t.Fatalf("worktree %q not found in %+v", worktreeID, worktrees)
	return serviceTestWorktree{}
}

func worktreeIDFromListEntry(entry *worktreepb.ListEntry) string {
	if registered := entry.GetTopology().GetRegistered(); registered != nil {
		return registered.GetKent().GetWorktreeId()
	}
	if missing := entry.GetTopology().GetMissing(); missing != nil {
		return missing.GetKent().GetWorktreeId()
	}
	return ""
}

func worktreeViewFromListEntryForTest(entry *worktreepb.ListEntry) serviceTestWorktree {
	view := serviceTestWorktree{IsCurrent: entry.GetProjection().GetIsCurrent()}
	if registered := entry.GetTopology().GetRegistered(); registered != nil {
		git := registered.GetGit()
		kent := registered.GetKent()
		view.WorktreeID = kent.GetWorktreeId()
		view.DisplayName = kent.GetDisplayName()
		view.CanonicalRoot = git.GetCanonicalRoot()
		view.BranchRef = git.GetBranchRef()
		view.BranchName = git.GetBranchName()
		view.Detached = git.GetDetached()
		view.IsMain = git.GetIsMainWorktree()
		view.Managed = kent.GetManaged()
		view.CreatedBranch = kent.GetCreatedBranch()
		view.OriginSessionID = kent.GetOriginSessionId()
	} else if external := entry.GetTopology().GetExternal(); external != nil {
		git := external.GetGit()
		view.CanonicalRoot = git.GetCanonicalRoot()
		view.DisplayName = filepath.Base(git.GetCanonicalRoot())
		view.BranchRef = git.GetBranchRef()
		view.BranchName = git.GetBranchName()
		view.Detached = git.GetDetached()
		view.IsMain = git.GetIsMainWorktree()
	} else if missing := entry.GetTopology().GetMissing(); missing != nil {
		kent := missing.GetKent()
		view.WorktreeID = kent.GetWorktreeId()
		view.DisplayName = kent.GetDisplayName()
		view.CanonicalRoot = kent.GetCanonicalRoot()
		view.Managed = kent.GetManaged()
		view.CreatedBranch = kent.GetCreatedBranch()
		view.OriginSessionID = kent.GetOriginSessionId()
	}
	return view
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
