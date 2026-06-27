package worktree

import (
	"context"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/config"
	"core/shared/serverapi"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeleteWorktreeBlocksWhenBackgroundProcessUsesDescendantPath(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-blocked-process")
	env.processes.snapshots = []shelltool.Snapshot{{ID: "proc-1", Command: "sleep 30", Workdir: filepath.Join(created.CanonicalRoot, "tmp"), Running: true}}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-blocked-process", created.WorktreeID))
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree error = %v, want ErrWorktreeBlocked", err)
	}
}

func TestDeleteWorktreeRebindsCurrentSessionToMainBeforeRemoval(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-current")
	if _, err := env.service.SwitchWorktree(env.ctx, worktreeSwitchRequest(env, "req-switch-delete-target", created.WorktreeID)); err != nil {
		t.Fatalf("SwitchWorktree: %v", err)
	}
	env.localNotes = &serviceTestLocalNotes{}
	env.service.localNotes = env.localNotes

	resp, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-current", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if resp.Target.WorktreeID != "" || resp.Target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("unexpected final delete target: %+v", resp.Target)
	}
	if len(env.runtime.rebindCalls) < 2 {
		t.Fatalf("expected switch to worktree and delete-time rebind back to main, got %+v", env.runtime.rebindCalls)
	}
	if got := env.runtime.rebindCalls[len(env.runtime.rebindCalls)-1].root; got != env.workspaceRoot {
		t.Fatalf("final rebind root = %q, want %q", got, env.workspaceRoot)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected worktree root removed, stat err=%v", err)
	}
	worktrees := mustListWorktrees(t, env).Worktrees
	for _, worktree := range worktrees {
		if worktree.WorktreeID == created.WorktreeID {
			t.Fatalf("expected deleted worktree to disappear from list, got %+v", worktree)
		}
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected delete path not to append transcript notes, got %+v", notes)
	}
}

func TestBeginMutationSerializesMutationsByWorkspace(t *testing.T) {
	env := newServiceTestEnv(t)
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)

	firstRelease, _, err := env.service.beginMutation(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("beginMutation first: %v", err)
	}
	firstReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			firstRelease.Release()
		}
	})

	type mutationResult struct {
		release worktreeLease
		err     error
	}
	resultCh := make(chan mutationResult, 1)
	go func() {
		release, _, err := env.service.beginMutation(env.ctx, otherSession.Meta().SessionID)
		resultCh <- mutationResult{release: release, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.release != nil {
			result.release.Release()
		}
		t.Fatalf("expected second mutation to wait for workspace lock, got err=%v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	firstRelease.Release()
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
	result.release.Release()
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
			firstWorkspaceLock.Release()
		}
	}()

	type mutationResult struct {
		release      worktreeLease
		workspaceCtx sessionWorkspaceContext
		err          error
	}
	firstCh := make(chan mutationResult, 1)
	go func() {
		release, workspaceCtx, err := env.service.beginMutation(env.ctx, env.session.Meta().SessionID)
		firstCh <- mutationResult{release: release, workspaceCtx: workspaceCtx, err: err}
	}()

	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, secondBinding.WorkspaceID, "", ".")
	firstWorkspaceLock.Release()
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
		first.release.Release()
		t.Fatalf("first mutation workspace id = %q, want %q", first.workspaceCtx.workspaceID, secondBinding.WorkspaceID)
	}

	secondCh := make(chan mutationResult, 1)
	go func() {
		release, workspaceCtx, err := env.service.beginMutation(env.ctx, secondSession.Meta().SessionID)
		secondCh <- mutationResult{release: release, workspaceCtx: workspaceCtx, err: err}
	}()
	select {
	case result := <-secondCh:
		if result.release != nil {
			result.release.Release()
		}
		first.release.Release()
		t.Fatalf("expected second mutation to block on reacquired workspace lock, got %+v", result)
	case <-time.After(150 * time.Millisecond):
	}

	first.release.Release()
	select {
	case result := <-secondCh:
		if result.err != nil {
			t.Fatalf("beginMutation second: %v", result.err)
		}
		if result.release == nil {
			t.Fatal("expected second mutation lease")
		}
		if result.workspaceCtx.workspaceID != secondBinding.WorkspaceID {
			result.release.Release()
			t.Fatalf("second mutation workspace id = %q, want %q", result.workspaceCtx.workspaceID, secondBinding.WorkspaceID)
		}
		result.release.Release()
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
	if activeTargetAfter.WorktreeID != activeTargetBefore.WorktreeID || activeTargetAfter.EffectiveWorkdir != activeTargetBefore.EffectiveWorkdir {
		t.Fatalf("expected active session target rolled back after runtime failure, before=%+v after=%+v", activeTargetBefore, activeTargetAfter)
	}
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if otherTarget.WorktreeID != "" || otherTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected inactive session retargeted to main workspace, got %+v", otherTarget)
	}
	if len(env.runtime.rebindCalls) != 1 {
		t.Fatalf("expected one active runtime rebind attempt, got %+v", env.runtime.rebindCalls)
	}
	if len(env.runtime.reminderCalls) != 2 {
		t.Fatalf("expected reminder for both sessions, got %+v", env.runtime.reminderCalls)
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
	localNotes := &serviceTestLocalNotes{}
	service := NewService(store, nil, runtime, runtime, processes, localNotes, ServiceOptions{BaseDir: cfg.Settings.Worktrees.BaseDir})
	return &serviceTestEnv{
		t:             t,
		ctx:           ctx,
		store:         store,
		cfg:           cfg,
		binding:       binding,
		session:       sess,
		runtime:       runtime,
		processes:     processes,
		localNotes:    localNotes,
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
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-q", "-m", "init")
	canonicalRoot, err := config.CanonicalWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if got, want := currentGitTopLevel(t, root), canonicalRoot; got != want {
		t.Fatalf("git top-level = %q, want %q", got, want)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = appendTestGitCommitIdentityEnv(sanitizeTestGitEnv(os.Environ()))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func currentGitTopLevel(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, "rev-parse", "--show-toplevel")
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

func mustCreateWorktree(t *testing.T, env *serviceTestEnv, branchName string) serverapi.WorktreeView {
	t.Helper()
	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID: "req-create-" + strings.ReplaceAll(branchName, "/", "-"),
		SessionID:       env.session.Meta().SessionID,
		BaseRef:         "HEAD",
		CreateBranch:    true,
		BranchName:      branchName,
	})
	if err != nil {
		t.Fatalf("CreateWorktree(%s): %v", branchName, err)
	}
	return resp.Worktree
}

func worktreeSwitchRequest(env *serviceTestEnv, clientRequestID string, worktreeID string) serverapi.WorktreeSwitchRequest {
	return serverapi.WorktreeSwitchRequest{
		ClientRequestID: clientRequestID,
		SessionID:       env.session.Meta().SessionID,
		WorktreeID:      worktreeID,
	}
}

func worktreeDeleteRequest(env *serviceTestEnv, clientRequestID string, worktreeID string) serverapi.WorktreeDeleteRequest {
	return serverapi.WorktreeDeleteRequest{
		ClientRequestID: clientRequestID,
		SessionID:       env.session.Meta().SessionID,
		WorktreeID:      worktreeID,
	}
}

func updateServiceTestSessionTarget(t *testing.T, env *serviceTestEnv, sessionID, workspaceID, worktreeID, cwdRelpath string) {
	t.Helper()
	if err := env.store.UpdateSessionExecutionTargetByID(env.ctx, sessionID, workspaceID, worktreeID, cwdRelpath); err != nil {
		t.Fatalf("UpdateSessionExecutionTargetByID %s: %v", sessionID, err)
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

func findWorktreeByID(t *testing.T, worktrees []serverapi.WorktreeView, worktreeID string) serverapi.WorktreeView {
	t.Helper()
	for _, worktree := range worktrees {
		if worktree.WorktreeID == worktreeID {
			return worktree
		}
	}
	t.Fatalf("worktree %q not found in %+v", worktreeID, worktrees)
	return serverapi.WorktreeView{}
}

func findMainWorktreeView(t *testing.T, worktrees []serverapi.WorktreeView) serverapi.WorktreeView {
	t.Helper()
	for _, worktree := range worktrees {
		if worktree.IsMain {
			return worktree
		}
	}
	t.Fatalf("main worktree not found in %+v", worktrees)
	return serverapi.WorktreeView{}
}
