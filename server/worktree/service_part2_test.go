package worktree

import (
	"context"
	"core/internal/testharness/worktreesetup"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeleteWorktreeBlocksWhenBackgroundProcessUsesDescendantPath(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-blocked-process")
	env.processes.snapshots = []shelltool.Snapshot{{ID: "proc-1", Command: "sleep 30", Workdir: filepath.Join(created.CanonicalRoot, "tmp"), Running: true}}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, created.WorktreeID))
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree error = %v, want ErrWorktreeBlocked", err)
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

func TestBeginMutationReacquiresWorkspaceLockWhenSessionWorkspaceChanges(t *testing.T) {
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

	firstWorkspaceLock := env.service.acquireWorkspaceMutationLock(env.binding.WorkspaceID)
	firstLockReleased := false
	defer func() {
		if !firstLockReleased {
			firstWorkspaceLock()
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
	firstWorkspaceLock()
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

func TestRetargetSessionsFromMissingWorktreeRollsBackActiveSessionMetadataOnRuntimeError(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/missing-runtime-error")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	record, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	activeTargetBefore, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget active before: %v", err)
	}
	env.runtime.rebindErrRoot = env.workspaceRoot
	env.runtime.rebindErr = errors.New("runtime rebind failed")
	env.runtime.activeSessions = map[string]bool{env.session.Meta().SessionID: true}
	env.runtime.rebindCalls = nil
	env.runtime.reminderCalls = nil

	err = env.service.retargetSessionsFromWorktree(env.ctx, env.binding.WorkspaceID, env.workspaceRoot, record, worktreeSessionRetargetOptions{reminder: worktreeReminderStateForExitedWorktree})
	if err == nil || !strings.Contains(err.Error(), "runtime rebind failed") {
		t.Fatalf("retargetSessionsFromMissingWorktree error = %v, want runtime rebind failed", err)
	}
	activeTargetAfter, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget active after: %v", err)
	}
	if sessionTargetWorktreeID(activeTargetAfter) != sessionTargetWorktreeID(activeTargetBefore) || activeTargetAfter.EffectiveWorkdir != activeTargetBefore.EffectiveWorkdir {
		t.Fatalf("expected active session target rolled back after runtime failure, before=%+v after=%+v", activeTargetBefore, activeTargetAfter)
	}
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if sessionTargetWorktreeID(otherTarget) != "" || otherTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected inactive session retargeted to main workspace, got %+v", otherTarget)
	}
	if len(env.runtime.rebindCalls) != 1 {
		t.Fatalf("expected one active runtime rebind attempt, got %+v", env.runtime.rebindCalls)
	}
	if len(env.runtime.reminderCalls) != 2 {
		t.Fatalf("expected reminder for both sessions, got %+v", env.runtime.reminderCalls)
	}
}

func TestRetargetSessionsFromWorktreeStopsBeforeLaterMutationWhenPlanningFails(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/retarget-plan-failure")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	record, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	blockers, err := env.store.ListSessionsTargetingWorktree(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("ListSessionsTargetingWorktree: %v", err)
	}
	if len(blockers) != 2 {
		t.Fatalf("targeting sessions = %+v, want two", blockers)
	}
	failedSessionID := blockers[0].SessionID
	laterSessionID := blockers[1].SessionID
	laterTargetBefore, err := env.store.ResolveSessionExecutionTarget(env.ctx, laterSessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget later before: %v", err)
	}
	env.runtime.blockRunsHook = func(blocked []string) {
		if !slices.Contains(blocked, failedSessionID) {
			t.Fatalf("blocked sessions = %+v, want failed session %q", blocked, failedSessionID)
		}
		if err := env.store.DeleteSessionRecordByID(env.ctx, failedSessionID); err != nil {
			t.Fatalf("DeleteSessionRecordByID: %v", err)
		}
	}
	err = env.service.retargetSessionsFromWorktree(env.ctx, env.binding.WorkspaceID, env.workspaceRoot, record, worktreeSessionRetargetOptions{
		reminder:        worktreeReminderStateForExitedWorktree,
		rollbackOnError: true,
	})
	if err == nil {
		t.Fatal("retargetSessionsFromWorktree succeeded after planned session disappeared")
	}
	laterTargetAfter, err := env.store.ResolveSessionExecutionTarget(env.ctx, laterSessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget later after: %v", err)
	}
	if sessionTargetWorktreeID(laterTargetAfter) != sessionTargetWorktreeID(laterTargetBefore) ||
		laterTargetAfter.EffectiveWorkdir != laterTargetBefore.EffectiveWorkdir {
		t.Fatalf("later session was mutated after planning failure: before=%+v after=%+v", laterTargetBefore, laterTargetAfter)
	}
	if len(env.runtime.rebindCalls) != 0 {
		t.Fatalf("runtime targets changed after planning failure: %+v", env.runtime.rebindCalls)
	}
}

func TestRetargetSessionsFromMissingWorktreeBlocksStartsUntilRuntimeSync(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/missing-block-runs")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	record, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	env.runtime.activeSessions = map[string]bool{otherSession.Meta().SessionID: true}
	checked := make(chan struct{})
	env.runtime.rebindHook = func(context.Context, string, string, string) {
		if got := env.runtime.blockedRunCount(otherSession.Meta().SessionID); got == 0 {
			t.Fatalf("session starts were not blocked while syncing retargeted runtime")
		}
		close(checked)
	}

	if err := env.service.retargetSessionsFromWorktree(env.ctx, env.binding.WorkspaceID, env.workspaceRoot, record, worktreeSessionRetargetOptions{reminder: worktreeReminderStateForExitedWorktree}); err != nil {
		t.Fatalf("retargetSessionsFromWorktree: %v", err)
	}
	select {
	case <-checked:
	default:
		t.Fatal("expected runtime sync hook to observe blocked session starts")
	}
	if got := env.runtime.blockedRunCount(otherSession.Meta().SessionID); got != 0 {
		t.Fatalf("session starts still blocked after retarget = %d, want 0", got)
	}
}

func TestNextAvailableWorktreeRootFailsAfterCollisionCap(t *testing.T) {
	baseRoot := filepath.Join(t.TempDir(), "collision")
	for idx := 0; idx < 1024; idx++ {
		candidate := baseRoot
		if idx > 0 {
			candidate = baseRoot + "-" + strconv.Itoa(idx+1)
		}
		if err := os.MkdirAll(candidate, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", candidate, err)
		}
	}

	_, err := nextAvailableWorktreeRoot(baseRoot)
	if !errors.Is(err, ErrWorktreeRootCollisionCap) {
		t.Fatalf("nextAvailableWorktreeRoot error = %v, want capped collision error", err)
	}
}

func newServiceTestEnv(t *testing.T) *serviceTestEnv {
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
	store, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	binding, err := store.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	sess := createServiceTestSession(t, store, cfg, binding)
	runtime := &serviceTestRuntime{}
	runtime.activeSessions = map[string]bool{sess.Meta().SessionID: true}
	processes := &serviceTestProcessSource{}
	service := NewService(store, nil, runtime, runtime, processes, ServiceOptions{BaseDir: cfg.Settings.Worktrees.BaseDir})
	return &serviceTestEnv{
		t:             t,
		ctx:           ctx,
		store:         store,
		cfg:           cfg,
		binding:       binding,
		session:       sess,
		runtime:       runtime,
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
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, store.AuthoritativeSessionStoreOptions()...)
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
	worktreesetup.InitializeGitRepository(t, root)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return worktreesetup.RunGit(t, dir, args...)
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		var payload setupScriptPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		return payload
	}
	t.Fatalf("timed out waiting for setup payload at %s", path)
	return setupScriptPayload{}
}

func waitForFileText(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		return strings.TrimSpace(string(body))
	}
	t.Fatalf("timed out waiting for text file at %s", path)
	return ""
}

func waitForFileLines(t *testing.T, path string) []string {
	t.Helper()
	text := waitForFileText(t, path)
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func nextSetupTerminalEvent(t *testing.T, sub serverapi.WorktreeSetupSubscription) serverapi.WorktreeSetupEvent {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		evt, err := sub.Next(deadline)
		if err != nil {
			t.Fatalf("setup event: %v", err)
		}
		if evt.Phase == serverapi.WorktreeSetupPhaseCompleted || evt.Phase == serverapi.WorktreeSetupPhaseFailed {
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
	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-create-" + strings.ReplaceAll(branchName, "/", "-"),
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       branchName,
	})
	if err != nil {
		t.Fatalf("CreateWorktree(%s): %v", branchName, err)
	}
	return worktreeViewFromListEntryForTest(resp.Worktree)
}

func worktreeDeleteRequest(env *serviceTestEnv, worktreeID string) serverapi.WorktreeDeleteRequest {
	return serverapi.WorktreeDeleteRequest{
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            worktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
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

func mustListWorktrees(t *testing.T, env *serviceTestEnv) serverapi.WorktreeListResponse {
	t.Helper()
	resp, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{SessionID: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	return resp
}

func findWorktreeByID(t *testing.T, worktrees []serverapi.WorktreeListEntry, worktreeID string) serviceTestWorktree {
	t.Helper()
	for _, entry := range worktrees {
		if worktreeIDFromListEntry(entry) == worktreeID {
			return worktreeViewFromListEntryForTest(entry)
		}
	}
	t.Fatalf("worktree %q not found in %+v", worktreeID, worktrees)
	return serviceTestWorktree{}
}

func worktreeIDFromListEntry(entry serverapi.WorktreeListEntry) string {
	switch entry.Topology.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		return entry.Topology.Registered.Kent.WorktreeID
	case serverapi.WorktreeTopologyVariantMissing:
		return entry.Topology.Missing.Kent.WorktreeID
	default:
		return ""
	}
}

func worktreeViewFromListEntryForTest(entry serverapi.WorktreeListEntry) serviceTestWorktree {
	view := serviceTestWorktree{IsCurrent: entry.Projection.IsCurrent}
	switch entry.Topology.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		git := entry.Topology.Registered.Git
		kent := entry.Topology.Registered.Kent
		view.WorktreeID = kent.WorktreeID
		view.DisplayName = kent.DisplayName
		view.CanonicalRoot = git.CanonicalRoot
		view.BranchRef = pointerValue(git.BranchRef)
		view.BranchName = pointerValue(git.BranchName)
		view.Detached = git.Detached
		view.IsMain = git.IsMain
		view.Managed = kent.Managed
		view.CreatedBranch = kent.CreatedBranch
		view.OriginSessionID = pointerValue(kent.OriginSessionID)
	case serverapi.WorktreeTopologyVariantExternal:
		git := entry.Topology.External.Git
		view.CanonicalRoot = git.CanonicalRoot
		view.DisplayName = filepath.Base(git.CanonicalRoot)
		view.BranchRef = pointerValue(git.BranchRef)
		view.BranchName = pointerValue(git.BranchName)
		view.Detached = git.Detached
		view.IsMain = git.IsMain
	case serverapi.WorktreeTopologyVariantMissing:
		kent := entry.Topology.Missing.Kent
		view.WorktreeID = kent.WorktreeID
		view.DisplayName = kent.DisplayName
		view.CanonicalRoot = kent.CanonicalRoot
		view.Managed = kent.Managed
		view.CreatedBranch = kent.CreatedBranch
		view.OriginSessionID = pointerValue(kent.OriginSessionID)
	}
	return view
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
