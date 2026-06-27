package worktree

import (
	"context"
	"core/server/metadata"
	"core/server/registry"
	runtimepkg "core/server/runtime"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type serviceTestRuntime struct {
	mu              sync.Mutex
	rebindCalls     []serviceRuntimeCall
	reminderCalls   []session.WorktreeReminderState
	activeSessions  map[string]bool
	runningSessions map[string]bool
	syncErrSessions map[string]error
	rebindErr       error
	rebindErrRoot   string
	rebindHook      func(context.Context, string, string, string)
}

type serviceRuntimeCall struct {
	sessionID string
	root      string
}

func (r *serviceTestRuntime) SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	r.mu.Lock()
	if reminder != nil {
		r.reminderCalls = append(r.reminderCalls, *reminder)
	}
	if !r.activeSessions[trimmedSessionID] {
		r.mu.Unlock()
		return nil
	}
	if err := r.syncErrSessions[trimmedSessionID]; err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	root := strings.TrimSpace(target.EffectiveWorkdir)
	if r.rebindHook != nil {
		r.rebindHook(ctx, sessionID, "", root)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebindCalls = append(r.rebindCalls, serviceRuntimeCall{sessionID: sessionID, root: root})
	if r.rebindErr != nil && (strings.TrimSpace(r.rebindErrRoot) == "" || strings.TrimSpace(r.rebindErrRoot) == root) {
		return r.rebindErr
	}
	return nil
}

func (r *serviceTestRuntime) HasActiveRun(_ context.Context, sessionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runningSessions[strings.TrimSpace(sessionID)], nil
}

func (r *serviceTestRuntime) IsSessionRuntimeActive(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeSessions[strings.TrimSpace(sessionID)]
}

type serviceTestProcessSource struct {
	snapshots []shelltool.Snapshot
}

func (s *serviceTestProcessSource) List() []shelltool.Snapshot {
	return append([]shelltool.Snapshot(nil), s.snapshots...)
}

type serviceTestLocalNotes struct {
	mu             sync.Mutex
	texts          []string
	sessionTexts   []string
	appendLocalErr error
}

type dirtyCountFailingGitRunner struct {
	base      gitCommandRunner
	dirtyRoot string
}

func (r *dirtyCountFailingGitRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	output, exitCode, err := r.Run(ctx, dir, args...)
	if err != nil {
		return nil, formatGitRunError(exitCode, err, output, args...)
	}
	return output, nil
}

func (r *dirtyCountFailingGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	if equalStrings(args, []string{"status", "--porcelain=v1", "-z"}) && strings.TrimSpace(dir) == strings.TrimSpace(r.dirtyRoot) {
		return []byte("status failed"), 1, errors.New("status failed")
	}
	return r.base.Run(ctx, dir, args...)
}

func (n *serviceTestLocalNotes) AppendCommittedEntry(_ context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	if n.appendLocalErr != nil {
		return n.appendLocalErr
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, req.Text)
	return nil
}

func (n *serviceTestLocalNotes) AppendSessionEntry(_ context.Context, _ string, _ string, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sessionTexts = append(n.sessionTexts, text)
	return nil
}

func (n *serviceTestLocalNotes) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	combined := append([]string(nil), n.texts...)
	combined = append(combined, n.sessionTexts...)
	return combined
}

type serviceTestEnv struct {
	t             *testing.T
	ctx           context.Context
	store         *metadata.Store
	cfg           config.App
	binding       metadata.Binding
	session       *session.Store
	runtime       *serviceTestRuntime
	processes     *serviceTestProcessSource
	localNotes    *serviceTestLocalNotes
	service       *Service
	leaseID       string
	workspaceRoot string
	baseDir       string
}

func TestCreateWorktreeMarksProvenanceAndRunsSetupScriptWithProjectID(t *testing.T) {
	env := newServiceTestEnv(t)
	payloadPath := filepath.Join(t.TempDir(), "worktree-payload.json")
	stdinPath := filepath.Join(t.TempDir(), "worktree-stdin.json")
	argsPath := filepath.Join(t.TempDir(), "worktree-args.txt")
	cwdPath := filepath.Join(t.TempDir(), "worktree-cwd.txt")
	scriptRelpath := filepath.Join("scripts", "setup-worktree.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\npwd > %q\nprintf '%%s\n%%s\n%%s\n' \"$1\" \"$2\" \"$3\" > %q\ncat > %q\nprintf '%%s' \"$KENT_WORKTREE_PAYLOAD_JSON\" > %q\n", cwdPath, argsPath, stdinPath, payloadPath))
	env.service.setupScript = scriptRelpath

	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID: "req-create",
		SessionID:       env.session.Meta().SessionID,
		BaseRef:         "HEAD",
		CreateBranch:    true,
		BranchName:      "feature/create-provenance",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if !resp.CreatedBranch {
		t.Fatal("expected create to report created branch")
	}
	if !resp.SetupScheduled {
		t.Fatal("expected setup script to be scheduled")
	}
	if !resp.Worktree.Managed {
		t.Fatal("expected worktree managed=true")
	}
	if resp.Target.WorktreeID != resp.Worktree.WorktreeID {
		t.Fatalf("create target worktree id = %q, want %q", resp.Target.WorktreeID, resp.Worktree.WorktreeID)
	}
	if resp.Target.EffectiveWorkdir != resp.Worktree.CanonicalRoot {
		t.Fatalf("create effective workdir = %q, want %q", resp.Target.EffectiveWorkdir, resp.Worktree.CanonicalRoot)
	}
	if !resp.Worktree.CreatedBranch {
		t.Fatal("expected worktree created_branch=true")
	}
	if resp.Worktree.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("origin session id = %q, want %q", resp.Worktree.OriginSessionID, env.session.Meta().SessionID)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, resp.Worktree.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if !record.Managed || !record.CreatedBranch || record.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("unexpected worktree record: %+v", record)
	}
	payload := waitForSetupPayload(t, payloadPath)
	if payload.ProjectID != env.binding.ProjectID {
		t.Fatalf("setup payload project_id = %q, want %q", payload.ProjectID, env.binding.ProjectID)
	}
	if payload.WorkspaceID != env.binding.WorkspaceID {
		t.Fatalf("setup payload workspace_id = %q, want %q", payload.WorkspaceID, env.binding.WorkspaceID)
	}
	if payload.SessionID != env.session.Meta().SessionID {
		t.Fatalf("setup payload session_id = %q, want %q", payload.SessionID, env.session.Meta().SessionID)
	}
	if payload.WorktreeID != resp.Worktree.WorktreeID {
		t.Fatalf("setup payload worktree_id = %q, want %q", payload.WorktreeID, resp.Worktree.WorktreeID)
	}
	if !payload.CreatedBranch {
		t.Fatal("expected setup payload created_branch=true")
	}
	if got := waitForFileText(t, cwdPath); got != resp.Worktree.CanonicalRoot {
		t.Fatalf("setup cwd = %q, want %q", got, resp.Worktree.CanonicalRoot)
	}
	if got := waitForFileLines(t, argsPath); len(got) != 3 || got[0] != env.workspaceRoot || got[1] != "feature/create-provenance" || got[2] != resp.Worktree.CanonicalRoot {
		t.Fatalf("setup args = %+v, want [%q %q %q]", got, env.workspaceRoot, "feature/create-provenance", resp.Worktree.CanonicalRoot)
	}
	if stdinPayload := waitForSetupPayload(t, stdinPath); stdinPayload != payload {
		t.Fatalf("stdin payload = %+v, want %+v", stdinPayload, payload)
	}
	if len(env.runtime.rebindCalls) != 1 || env.runtime.rebindCalls[0].root != resp.Worktree.CanonicalRoot {
		t.Fatalf("expected create-time rebind to created worktree, got %+v", env.runtime.rebindCalls)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no synthetic create-time switch notes, got %+v", notes)
	}
	if len(env.runtime.reminderCalls) == 0 {
		t.Fatal("expected create-time pending worktree reminder")
	}
	worktrees := mustListWorktrees(t, env)
	created := findWorktreeByID(t, worktrees.Worktrees, resp.Worktree.WorktreeID)
	if !created.Managed || !created.CreatedBranch || created.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("sync lost worktree provenance: %+v", created)
	}
}

func TestRunSetupScriptDoesNotAppendSuccessNote(t *testing.T) {
	notes := &serviceTestLocalNotes{}
	service := &Service{localNotes: notes}
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	writeExecutableFile(t, scriptPath, "#!/bin/sh\nexit 0\n")

	service.runSetupScript(scriptPath, "session-1", setupScriptPayload{WorktreeRoot: t.TempDir()})

	if got := notes.snapshot(); len(got) != 0 {
		t.Fatalf("expected no setup success note, got %+v", got)
	}
}

func TestCreateWorktreeAllowsExistingRefWithoutCreatingBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/existing-ref")

	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID: "req-create-existing-ref",
		SessionID:       env.session.Meta().SessionID,
		BaseRef:         "feature/existing-ref",
		CreateBranch:    false,
	})
	if err != nil {
		t.Fatalf("CreateWorktree existing ref: %v", err)
	}
	if resp.CreatedBranch {
		t.Fatal("expected created_branch=false for existing ref")
	}
	if resp.Worktree.BranchName != "feature/existing-ref" {
		t.Fatalf("branch name = %q, want feature/existing-ref", resp.Worktree.BranchName)
	}
	if !resp.Worktree.Managed {
		t.Fatal("expected managed worktree for existing ref")
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, resp.Worktree.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if record.CreatedBranch {
		t.Fatalf("expected created_branch=false in metadata, got %+v", record)
	}
}

func TestSyncWorkspaceClearsStaleManagedProvenanceWhenRootIsReused(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/provenance-stale")

	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", created.CanonicalRoot)
	runGit(t, env.workspaceRoot, "worktree", "add", "--detach", created.CanonicalRoot, "HEAD")

	worktrees := mustListWorktrees(t, env).Worktrees
	for _, worktree := range worktrees {
		if strings.TrimSpace(worktree.CanonicalRoot) != strings.TrimSpace(created.CanonicalRoot) {
			continue
		}
		if worktree.Managed || worktree.CreatedBranch || strings.TrimSpace(worktree.OriginSessionID) != "" {
			t.Fatalf("expected stale managed provenance cleared for reused root, got %+v", worktree)
		}
		return
	}
	t.Fatalf("expected reused worktree root %q in %+v", created.CanonicalRoot, worktrees)
}

func TestResolveWorktreeCreateTargetClassifiesBranchDetachedRefAndNewBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/existing-ref")

	existing, err := env.service.ResolveWorktreeCreateTarget(env.ctx, serverapi.WorktreeCreateTargetResolveRequest{SessionID: env.session.Meta().SessionID, Target: "feature/existing-ref"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget existing: %v", err)
	}
	if existing.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindExistingBranch {
		t.Fatalf("existing kind = %q, want existing_branch", existing.Resolution.Kind)
	}

	detached, err := env.service.ResolveWorktreeCreateTarget(env.ctx, serverapi.WorktreeCreateTargetResolveRequest{SessionID: env.session.Meta().SessionID, Target: "HEAD"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget detached: %v", err)
	}
	if detached.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindDetachedRef {
		t.Fatalf("detached kind = %q, want detached_ref", detached.Resolution.Kind)
	}

	newBranch, err := env.service.ResolveWorktreeCreateTarget(env.ctx, serverapi.WorktreeCreateTargetResolveRequest{SessionID: env.session.Meta().SessionID, Target: "feature/new-branch"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget new branch: %v", err)
	}
	if newBranch.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindNewBranch {
		t.Fatalf("new branch kind = %q, want new_branch", newBranch.Resolution.Kind)
	}
}

func TestDeleteWorktreeKeepsExistingBranchUnlessExplicitlyRequested(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/shared-branch")
	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID: "req-create-shared-branch",
		SessionID:       env.session.Meta().SessionID,
		BaseRef:         "feature/shared-branch",
		CreateBranch:    false,
	})
	if err != nil {
		t.Fatalf("CreateWorktree existing branch: %v", err)
	}
	env.localNotes = &serviceTestLocalNotes{}
	env.service.localNotes = env.localNotes

	deleteResp, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-shared-branch", resp.Worktree.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if deleteResp.BranchDeleted {
		t.Fatal("did not expect branch deletion without explicit confirmation")
	}
	if !strings.Contains(deleteResp.BranchCleanupMessage, "Kept branch feature/shared-branch") {
		t.Fatalf("unexpected branch cleanup message: %q", deleteResp.BranchCleanupMessage)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no transcript note for delete branch cleanup message, got %+v", notes)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", "feature/shared-branch"); !strings.Contains(got, "feature/shared-branch") {
		t.Fatalf("expected shared branch to remain, got %q", got)
	}
}

func TestDeleteWorktreeDeletesExistingBranchWhenExplicitlyRequested(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/shared-branch")
	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID: "req-create-shared-branch-explicit",
		SessionID:       env.session.Meta().SessionID,
		BaseRef:         "feature/shared-branch",
		CreateBranch:    false,
	})
	if err != nil {
		t.Fatalf("CreateWorktree existing branch: %v", err)
	}
	env.localNotes = &serviceTestLocalNotes{}
	env.service.localNotes = env.localNotes

	deleteReq := worktreeDeleteRequest(env, "req-delete-shared-branch-explicit", resp.Worktree.WorktreeID)
	deleteReq.DeleteBranch = true
	deleteResp, err := env.service.DeleteWorktree(env.ctx, deleteReq)
	if err != nil {
		t.Fatalf("DeleteWorktree explicit branch delete: %v", err)
	}
	if !deleteResp.BranchDeleted {
		t.Fatalf("expected branch deletion, got %+v", deleteResp)
	}
	if !strings.Contains(deleteResp.BranchCleanupMessage, "Deleted branch feature/shared-branch") {
		t.Fatalf("unexpected branch cleanup message: %q", deleteResp.BranchCleanupMessage)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no transcript note for delete branch cleanup message, got %+v", notes)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", "feature/shared-branch"); strings.Contains(got, "feature/shared-branch") {
		t.Fatalf("expected shared branch removed, got %q", got)
	}
}

func TestResolveRequestedWorktreeRootCreatesBaseDirAndAutoSuffixesCollisions(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "missing-base")
	service := &Service{baseDir: baseDir}
	firstRoot, err := defaultWorktreeRoot(baseDir, "workspace-1", "feature/collision")
	if err != nil {
		t.Fatalf("defaultWorktreeRoot: %v", err)
	}
	if err := os.MkdirAll(firstRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll collision root: %v", err)
	}
	firstRoot, err = config.CanonicalWorkspaceRoot(firstRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot collision root: %v", err)
	}

	resolvedRoot, err := service.resolveRequestedWorktreeRoot("", "workspace-1", CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/collision"})
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	if resolvedRoot == firstRoot {
		t.Fatalf("expected suffixed root after collision, got %q", resolvedRoot)
	}
	if !strings.HasPrefix(resolvedRoot, firstRoot+"-") {
		t.Fatalf("expected suffixed collision root, got %q (base %q)", resolvedRoot, firstRoot)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "workspace-1")); err != nil {
		t.Fatalf("expected workspace base dir created, stat err=%v", err)
	}
}

func TestSwitchWorktreeClampsCwdAndRecordsPendingReminder(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/switch-clamp")
	if err := os.MkdirAll(filepath.Join(created.CanonicalRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll pkg: %v", err)
	}
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, "pkg")
	main := findMainWorktreeView(t, mustListWorktrees(t, env).Worktrees)

	resp, err := env.service.SwitchWorktree(env.ctx, worktreeSwitchRequest(env, "req-switch-main", main.WorktreeID))
	if err != nil {
		t.Fatalf("SwitchWorktree: %v", err)
	}
	if resp.Target.WorktreeID != "" {
		t.Fatalf("target worktree id = %q, want main workspace", resp.Target.WorktreeID)
	}
	if resp.Target.CwdRelpath != "." {
		t.Fatalf("target cwd_relpath = %q, want .", resp.Target.CwdRelpath)
	}
	if resp.Target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("effective workdir = %q, want %q", resp.Target.EffectiveWorkdir, env.workspaceRoot)
	}
	if len(env.runtime.rebindCalls) == 0 || env.runtime.rebindCalls[len(env.runtime.rebindCalls)-1].root != env.workspaceRoot {
		t.Fatalf("expected rebind to main workspace, got %+v", env.runtime.rebindCalls)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no synthetic switch local notes, got %+v", notes)
	}
	if len(env.runtime.reminderCalls) == 0 {
		t.Fatal("expected pending worktree reminder")
	}
	reminder := env.runtime.reminderCalls[len(env.runtime.reminderCalls)-1]
	if reminder.Mode != session.WorktreeReminderModeExit || reminder.EffectiveCwd != env.workspaceRoot {
		t.Fatalf("unexpected reminder = %+v", reminder)
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if finalTarget.WorktreeID != "" || finalTarget.CwdRelpath != "." {
		t.Fatalf("unexpected final target after switch: %+v", finalTarget)
	}
}

func TestListWorktreesRetargetsMissingCurrentWorktreeBeforePruning(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/missing-current")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	if err := os.MkdirAll(filepath.Join(created.CanonicalRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll pkg: %v", err)
	}
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, "pkg")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, "pkg")
	env.runtime.rebindCalls = nil
	env.runtime.reminderCalls = nil
	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", created.CanonicalRoot)

	resp, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{SessionID: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if resp.Target.WorktreeID != "" {
		t.Fatalf("response target worktree id = %q, want main workspace", resp.Target.WorktreeID)
	}
	if resp.Target.CwdRelpath != "." {
		t.Fatalf("response target cwd_relpath = %q, want .", resp.Target.CwdRelpath)
	}
	if resp.Target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("response effective workdir = %q, want %q", resp.Target.EffectiveWorkdir, env.workspaceRoot)
	}
	for _, worktree := range resp.Worktrees {
		if worktree.WorktreeID == created.WorktreeID {
			t.Fatalf("expected missing worktree pruned from list, got %+v", worktree)
		}
	}
	resolved, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if resolved.WorktreeID != "" {
		t.Fatalf("stored target worktree id = %q, want main workspace", resolved.WorktreeID)
	}
	if resolved.WorktreeRoot != "" {
		t.Fatalf("stored target worktree root = %q, want empty", resolved.WorktreeRoot)
	}
	if resolved.CwdRelpath != "." {
		t.Fatalf("stored target cwd_relpath = %q, want .", resolved.CwdRelpath)
	}
	if resolved.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("stored effective workdir = %q, want %q", resolved.EffectiveWorkdir, env.workspaceRoot)
	}
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if otherTarget.WorktreeID != "" || otherTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected other session retargeted to main workspace, got %+v", otherTarget)
	}
	if len(env.runtime.rebindCalls) != 1 {
		t.Fatalf("expected exactly one active-runtime rebind, got %+v", env.runtime.rebindCalls)
	}
	if got := env.runtime.rebindCalls[0]; got.sessionID != env.session.Meta().SessionID || got.root != env.workspaceRoot {
		t.Fatalf("unexpected active-runtime rebind call: %+v", got)
	}
	if len(env.runtime.reminderCalls) != 2 {
		t.Fatalf("expected reminder for each retargeted session, got %+v", env.runtime.reminderCalls)
	}
	for _, reminder := range env.runtime.reminderCalls {
		if reminder.Mode != session.WorktreeReminderModeExit {
			t.Fatalf("reminder mode = %q, want exit", reminder.Mode)
		}
		if reminder.WorktreePath != created.CanonicalRoot {
			t.Fatalf("reminder worktree path = %q, want %q", reminder.WorktreePath, created.CanonicalRoot)
		}
		if reminder.EffectiveCwd != env.workspaceRoot {
			t.Fatalf("reminder effective cwd = %q, want %q", reminder.EffectiveCwd, env.workspaceRoot)
		}
	}
}

func TestSwitchWorktreeRollsBackExecutionTargetWhenRebindFails(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/rebind-fail")
	main := findMainWorktreeView(t, mustListWorktrees(t, env).Worktrees)
	if _, err := env.service.SwitchWorktree(env.ctx, worktreeSwitchRequest(env, "req-switch-reset-main", main.WorktreeID)); err != nil {
		t.Fatalf("SwitchWorktree main reset: %v", err)
	}
	env.localNotes = &serviceTestLocalNotes{}
	env.service.localNotes = env.localNotes
	env.runtime.rebindErrRoot = created.CanonicalRoot
	env.runtime.rebindErr = errors.New("boom")

	_, err := env.service.SwitchWorktree(env.ctx, worktreeSwitchRequest(env, "req-switch-fail", created.WorktreeID))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("SwitchWorktree error = %v, want rebind failure", err)
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if finalTarget.WorktreeID != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected execution target rollback to main workspace, got %+v", finalTarget)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no local notes on failed switch, got %+v", notes)
	}
}

func TestSwitchWorktreeRollsBackExecutionTargetWhenRequestContextCancelsDuringRebindFailure(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/rebind-canceled")
	main := findMainWorktreeView(t, mustListWorktrees(t, env).Worktrees)
	if _, err := env.service.SwitchWorktree(env.ctx, worktreeSwitchRequest(env, "req-switch-reset-main-canceled", main.WorktreeID)); err != nil {
		t.Fatalf("SwitchWorktree main reset: %v", err)
	}
	env.localNotes = &serviceTestLocalNotes{}
	env.service.localNotes = env.localNotes

	ctx, cancel := context.WithCancel(env.ctx)
	env.runtime.rebindErrRoot = created.CanonicalRoot
	env.runtime.rebindHook = func(rebindCtx context.Context, _ string, _ string, workspaceRoot string) {
		if err := rebindCtx.Err(); err != nil {
			t.Fatalf("unexpected rebind context canceled before rollback trigger: %v", err)
		}
		if strings.TrimSpace(workspaceRoot) == strings.TrimSpace(created.CanonicalRoot) {
			cancel()
		}
	}
	env.runtime.rebindErr = errors.New("boom")

	_, err := env.service.SwitchWorktree(ctx, worktreeSwitchRequest(env, "req-switch-fail-canceled", created.WorktreeID))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("SwitchWorktree error = %v, want rebind failure", err)
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if finalTarget.WorktreeID != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected execution target rollback to main workspace, got %+v", finalTarget)
	}
	if got := env.runtime.rebindCalls[len(env.runtime.rebindCalls)-1].root; got != env.workspaceRoot {
		t.Fatalf("expected final rollback rebind to main workspace, got %q calls=%+v", got, env.runtime.rebindCalls)
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected request context canceled, got %v", err)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no local notes on failed switch, got %+v", notes)
	}
}

func TestCreateWorktreeCleansUpCreatedStateWhenPostCreateSwitchFails(t *testing.T) {
	env := newServiceTestEnv(t)
	expectedRoot, err := env.service.resolveRequestedWorktreeRoot("", env.binding.WorkspaceID, CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/create-rollback"})
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	env.runtime.rebindErr = errors.New("boom")

	_, err = env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		ClientRequestID: "req-create-rollback",
		SessionID:       env.session.Meta().SessionID,
		BaseRef:         "HEAD",
		CreateBranch:    true,
		BranchName:      "feature/create-rollback",
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("CreateWorktree error = %v, want rebind failure", err)
	}
	if _, statErr := os.Stat(expectedRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected failed create worktree root removed, stat err=%v", statErr)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", "feature/create-rollback"); strings.Contains(got, "feature/create-rollback") {
		t.Fatalf("expected created branch cleaned up after failed create, got %q", got)
	}
	records, err := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("ListWorktreeRecordsByWorkspaceID: %v", err)
	}
	for _, record := range records {
		if strings.TrimSpace(record.CanonicalRoot) == strings.TrimSpace(expectedRoot) {
			t.Fatalf("expected failed create worktree record removed, got %+v", record)
		}
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if finalTarget.WorktreeID != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected session target unchanged after failed create, got %+v", finalTarget)
	}
}

func TestDeleteWorktreeBlocksWhenAnotherSessionTargetsIt(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-blocked-session")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	env.runtime.activeSessions[otherSession.Meta().SessionID] = true
	env.runtime.runningSessions = map[string]bool{otherSession.Meta().SessionID: true}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-blocked-session", created.WorktreeID))
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree error = %v, want ErrWorktreeBlocked", err)
	}
}

func TestDeleteWorktreeRetargetsActiveIdleSessionsTargetingIt(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-active-idle-session")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	dormantSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, dormantSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	env.runtime.activeSessions[otherSession.Meta().SessionID] = true

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-active-idle-session", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if target.WorktreeID != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected active idle session retargeted to main workspace, got %+v", target)
	}
	dormantTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, dormantSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget dormant session: %v", err)
	}
	if dormantTarget.WorktreeID != "" || dormantTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected dormant stale session retargeted by worktree deletion cleanup, got %+v", dormantTarget)
	}
	foundRebind := false
	for _, call := range env.runtime.rebindCalls {
		if call.sessionID == otherSession.Meta().SessionID && call.root == env.workspaceRoot {
			foundRebind = true
		}
	}
	if !foundRebind {
		t.Fatalf("expected active idle session runtime rebind to main workspace, got %+v", env.runtime.rebindCalls)
	}
}

func TestDeleteWorktreeRollsBackActiveIdleSessionRetargetsOnRuntimeSyncError(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-active-idle-rollback")
	firstSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	secondSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	for _, sess := range []*session.Store{firstSession, secondSession} {
		updateServiceTestSessionTarget(t, env, sess.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
		env.runtime.activeSessions[sess.Meta().SessionID] = true
	}
	env.runtime.syncErrSessions = map[string]error{secondSession.Meta().SessionID: errors.New("runtime sync failed")}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-active-idle-rollback", created.WorktreeID))
	if err == nil || !strings.Contains(err.Error(), "runtime sync failed") {
		t.Fatalf("DeleteWorktree error = %v, want runtime sync failed", err)
	}
	for _, sess := range []*session.Store{firstSession, secondSession} {
		target, err := env.store.ResolveSessionExecutionTarget(env.ctx, sess.Meta().SessionID)
		if err != nil {
			t.Fatalf("ResolveSessionExecutionTarget %s: %v", sess.Meta().SessionID, err)
		}
		if target.WorktreeID != created.WorktreeID || target.EffectiveWorkdir != created.CanonicalRoot {
			t.Fatalf("expected %s target rolled back to worktree, got %+v", sess.Meta().SessionID, target)
		}
	}
	if _, err := os.Stat(created.CanonicalRoot); err != nil {
		t.Fatalf("expected worktree root kept after retarget rollback, stat err=%v", err)
	}
	foundFirstRollback := false
	for _, call := range env.runtime.rebindCalls {
		if call.sessionID == firstSession.Meta().SessionID && call.root == created.CanonicalRoot {
			foundFirstRollback = true
		}
	}
	if !foundFirstRollback {
		t.Fatalf("expected first session runtime rolled back to deleted worktree, calls=%+v", env.runtime.rebindCalls)
	}
}

func TestDeleteWorktreeIgnoresDormantSessionsTargetingIt(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-dormant-session")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-dormant-session", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected worktree root removed, stat err=%v", err)
	}
}

func TestListWorktreesReportsDirtyFileCount(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/dirty-count")
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "untracked.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	resp, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{SessionID: env.session.Meta().SessionID, IncludeDirtyCount: true})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	listed := findWorktreeByID(t, resp.Worktrees, created.WorktreeID)
	if listed.DirtyFileCount != 1 {
		t.Fatalf("dirty file count = %d, want 1", listed.DirtyFileCount)
	}
}

func TestListWorktreesDirtyCountProbeFailureIsBestEffort(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/dirty-probe-failure")
	env.service.git = NewGitInspector(&dirtyCountFailingGitRunner{base: execGitCommandRunner{}, dirtyRoot: created.CanonicalRoot})

	resp, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{SessionID: env.session.Meta().SessionID, IncludeDirtyCount: true})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	listed := findWorktreeByID(t, resp.Worktrees, created.WorktreeID)
	if listed.DirtyFileCount != -1 {
		t.Fatalf("dirty file count after failed probe = %d, want -1", listed.DirtyFileCount)
	}
}

func TestDeleteWorktreeForcesRemovalWhenDirty(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-dirty")
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "untracked.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-dirty", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected dirty worktree root removed, stat err=%v", err)
	}
}

func TestDeleteWorktreeDirtyCountProbeFailureIsBestEffort(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-dirty-probe-failure")
	env.service.git = NewGitInspector(&dirtyCountFailingGitRunner{base: execGitCommandRunner{}, dirtyRoot: created.CanonicalRoot})

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-dirty-probe-failure", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected worktree root removed, stat err=%v", err)
	}
}

func TestDeleteWorktreeBlocksOnlyActiveSessionsTargetingIt(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-mixed-session-blockers")
	dormantSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	activeSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	if err := dormantSession.SetName("dormant blocker"); err != nil {
		t.Fatalf("SetName dormant: %v", err)
	}
	if err := activeSession.SetName("active blocker"); err != nil {
		t.Fatalf("SetName active: %v", err)
	}
	if err := env.store.ImportSessionSnapshot(env.ctx, session.PersistedStoreSnapshot{SessionDir: dormantSession.Dir(), Meta: dormantSession.Meta()}); err != nil {
		t.Fatalf("ImportSessionSnapshot dormant: %v", err)
	}
	if err := env.store.ImportSessionSnapshot(env.ctx, session.PersistedStoreSnapshot{SessionDir: activeSession.Dir(), Meta: activeSession.Meta()}); err != nil {
		t.Fatalf("ImportSessionSnapshot active: %v", err)
	}
	updateServiceTestSessionTarget(t, env, dormantSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, activeSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	env.runtime.activeSessions[activeSession.Meta().SessionID] = true
	env.runtime.runningSessions = map[string]bool{activeSession.Meta().SessionID: true}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-mixed-session-blockers", created.WorktreeID))
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree error = %v, want ErrWorktreeBlocked", err)
	}
	message := err.Error()
	if !strings.Contains(message, "active blocker") {
		t.Fatalf("expected active blocker in error, got %q", message)
	}
	if strings.Contains(message, "dormant blocker") {
		t.Fatalf("did not expect dormant blocker in error, got %q", message)
	}
}

func TestDeleteWorktreeAllowsSessionAfterRuntimeRegistryCleanup(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-after-runtime-cleanup")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	runtimes := registry.NewRuntimeRegistry()
	engine := &runtimepkg.Engine{}
	runtimes.Register(otherSession.Meta().SessionID, engine)
	env.service.active = runtimes
	env.runtime.runningSessions = map[string]bool{otherSession.Meta().SessionID: true}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-before-runtime-cleanup", created.WorktreeID))
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree before runtime cleanup error = %v, want ErrWorktreeBlocked", err)
	}

	runtimes.Unregister(otherSession.Meta().SessionID, engine)
	_, err = env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-after-runtime-cleanup", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree after runtime cleanup: %v", err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected worktree root removed after runtime cleanup, stat err=%v", err)
	}
}
