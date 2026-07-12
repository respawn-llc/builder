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
	"time"
)

type serviceTestRuntime struct {
	mu                    sync.Mutex
	rebindCalls           []serviceRuntimeCall
	reminderCalls         []session.WorktreeReminderState
	clearReminderSessions []string
	activeSessions        map[string]bool
	runningSessions       map[string]bool
	syncErrSessions       map[string]error
	blockedRuns           map[string]int
	rebindErr             error
	rebindErrRoot         string
	rebindHook            func(context.Context, string, string, string)
}

func sessionTargetWorktreeID(target clientui.SessionExecutionTarget) string {
	if target.Worktree == nil {
		return ""
	}
	return strings.TrimSpace(target.Worktree.ID)
}

func sessionTargetWorktreeRoot(target clientui.SessionExecutionTarget) string {
	if target.Worktree == nil {
		return ""
	}
	return strings.TrimSpace(target.Worktree.Root)
}

func (r *serviceTestRuntime) blockedRunCount(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blockedRuns[strings.TrimSpace(sessionID)]
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

func (r *serviceTestRuntime) ClearWorktreeReminder(_ context.Context, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearReminderSessions = append(r.clearReminderSessions, strings.TrimSpace(sessionID))
	return nil
}

func (r *serviceTestRuntime) HasBlockingRuntimeActivity(_ context.Context, sessionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runningSessions[strings.TrimSpace(sessionID)], nil
}

func (r *serviceTestRuntime) IsSessionRuntimeActive(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeSessions[strings.TrimSpace(sessionID)]
}

func (r *serviceTestRuntime) BlockSessionRuns(sessionIDs []string) func() {
	r.mu.Lock()
	if r.blockedRuns == nil {
		r.blockedRuns = make(map[string]int)
	}
	blocked := make([]string, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		trimmed := strings.TrimSpace(sessionID)
		if trimmed == "" {
			continue
		}
		r.blockedRuns[trimmed]++
		blocked = append(blocked, trimmed)
	}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, sessionID := range blocked {
			if r.blockedRuns[sessionID] <= 1 {
				delete(r.blockedRuns, sessionID)
				continue
			}
			r.blockedRuns[sessionID]--
		}
	}
}

func (r *serviceTestRuntime) runsBlocked(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blockedRuns[strings.TrimSpace(sessionID)] > 0
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
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-create",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/create-provenance",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if !resp.CreatedBranch {
		t.Fatal("expected create to report created branch")
	}
	if !resp.Worktree.Managed {
		t.Fatal("expected worktree managed=true")
	}
	if sessionTargetWorktreeID(resp.Target) != "" {
		t.Fatalf("create changed session target to %q", sessionTargetWorktreeID(resp.Target))
	}
	if resp.Target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("create effective workdir = %q, want %q", resp.Target.EffectiveWorkdir, env.workspaceRoot)
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
	if len(env.runtime.rebindCalls) != 0 {
		t.Fatalf("create rebounded the runtime, got %+v", env.runtime.rebindCalls)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no synthetic create-time switch notes, got %+v", notes)
	}
	if len(env.runtime.reminderCalls) != 0 {
		t.Fatalf("create issued a worktree reminder, got %+v", env.runtime.reminderCalls)
	}
	worktrees := mustListWorktrees(t, env)
	created := findWorktreeByID(t, worktrees.Worktrees, resp.Worktree.WorktreeID)
	if !created.Managed || !created.CreatedBranch || created.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("sync lost worktree provenance: %+v", created)
	}
}

func TestCreateWorktreeBlocksUntilSetupCompletesBeforeSessionSwitch(t *testing.T) {
	env := newServiceTestEnv(t)
	startedPath := filepath.Join(t.TempDir(), "started")
	releasePath := filepath.Join(t.TempDir(), "release")
	markerPath := filepath.Join(t.TempDir(), "marker")
	scriptRelpath := filepath.Join("scripts", "blocking-setup.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nprintf started > %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nprintf marker > %q\n", startedPath, releasePath, markerPath))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	type createResult struct {
		resp serverapi.WorktreeCreateResponse
		err  error
	}
	resultCh := make(chan createResult, 1)
	go func() {
		resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
			SetupOperationID: setupID,
			ClientRequestID:  "req-create-blocking",
			SessionID:        env.session.Meta().SessionID,
			BaseRef:          "HEAD",
			CreateBranch:     true,
			BranchName:       "feature/create-blocking",
		})
		resultCh <- createResult{resp: resp, err: err}
	}()

	started := waitForFileText(t, startedPath)
	if started != "started" {
		t.Fatalf("started marker = %q, want started", started)
	}
	evt, err := sub.Next(env.ctx)
	if err != nil {
		t.Fatalf("setup event: %v", err)
	}
	if evt.Phase != serverapi.WorktreeSetupPhaseStarted || evt.SetupOperationID != setupID || evt.ScriptPath == "" || evt.WorktreeRoot == "" {
		t.Fatalf("started setup event = %+v", evt)
	}
	select {
	case result := <-resultCh:
		t.Fatalf("CreateWorktree returned before setup release: resp=%+v err=%v", result.resp, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("session target changed while setup blocked: %+v", target)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o644); err != nil {
		t.Fatalf("release setup: %v", err)
	}
	var result createResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for CreateWorktree")
	}
	if result.err != nil {
		t.Fatalf("CreateWorktree: %v", result.err)
	}
	if got := waitForFileText(t, markerPath); got != "marker" {
		t.Fatalf("setup marker = %q, want marker", got)
	}
	target, err = env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after: %v", err)
	}
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("session target after setup = %+v, want unchanged main target", target)
	}
}

func TestCreateWorktreeSetupFailureKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	scriptRelpath := filepath.Join("scripts", "fails.sh")
	longOutput := strings.Repeat("x", setupDiagnosticLimitBytes+128)
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q >&2\nexit 7\n", longOutput))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	expectedRoot, err := env.service.resolveRequestedWorktreeRoot("", env.binding.WorkspaceID, CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/setup-fails"})
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	_, err = env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: setupID,
		ClientRequestID:  "req-setup-fails",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/setup-fails",
	})
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want setup failure")
	}
	var retained *serverapi.WorktreeSetupRetainedError
	if !errors.As(err, &retained) || retained.Worktree.Registered == nil {
		t.Fatalf("CreateWorktree error = %T %v, want retained worktree facts", err, err)
	}
	if _, statErr := os.Stat(expectedRoot); statErr != nil {
		t.Fatalf("expected setup-failed worktree kept, stat err=%v", statErr)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.Phase != serverapi.WorktreeSetupPhaseFailed || evt.ExitCode == nil || *evt.ExitCode != 7 {
		t.Fatalf("failure setup event = %+v", evt)
	}
	if len(evt.Stderr) > setupDiagnosticLimitBytes {
		t.Fatalf("stderr diagnostic length = %d, want <= %d", len(evt.Stderr), setupDiagnosticLimitBytes)
	}
}

func TestCreateWorktreeSetupTimeoutKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	scriptRelpath := filepath.Join("scripts", "timeout.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), "#!/bin/sh\nsleep 10\n")
	env.service.setupScript = scriptRelpath
	env.service.setupTimeoutSeconds = 1
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	_, err = env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: setupID,
		ClientRequestID:  "req-setup-timeout",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/setup-timeout",
	})
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want setup timeout")
	}
	expectedScript, canonicalErr := config.CanonicalWorkspaceRoot(filepath.Join(env.workspaceRoot, scriptRelpath))
	if canonicalErr != nil {
		t.Fatalf("canonical expected script: %v", canonicalErr)
	}
	var setupErr *setupScriptError
	if !errors.As(err, &setupErr) {
		t.Fatalf("timeout error = %#v, want setupScriptError", err)
	}
	if !setupErr.Timeout || setupErr.TimeoutSeconds != 1 || setupErr.ScriptPath != expectedScript || setupErr.WorktreeRoot == "" {
		t.Fatalf("timeout setup error fields timeout=%t seconds=%d script=%q want=%q root=%q", setupErr.Timeout, setupErr.TimeoutSeconds, setupErr.ScriptPath, expectedScript, setupErr.WorktreeRoot)
	}
	if _, statErr := os.Stat(setupErr.WorktreeRoot); statErr != nil {
		t.Fatalf("expected timed-out setup worktree kept, stat err=%v", statErr)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.Phase != serverapi.WorktreeSetupPhaseFailed || !evt.Timeout {
		t.Fatalf("timeout setup event = %+v", evt)
	}
	if evt.ScriptPath != expectedScript || evt.WorktreeRoot != setupErr.WorktreeRoot || evt.Error != setupErr.Error() {
		t.Fatalf("timeout setup event = %+v, want timeout/script/worktree context", evt)
	}
}

func TestCreateWorktreeSetupCancellationKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	startedPath := filepath.Join(t.TempDir(), "started")
	scriptRelpath := filepath.Join("scripts", "cancel.sh")
	writeExecutableFile(t, filepath.Join(env.workspaceRoot, scriptRelpath), fmt.Sprintf("#!/bin/sh\nready_path=%q\nready_tmp=\"${ready_path}.$$\"\nprintf started > \"$ready_tmp\"\nmv \"$ready_tmp\" \"$ready_path\"\nexec sleep 10\n", startedPath))
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	expectedRoot, err := env.service.resolveRequestedWorktreeRoot("", env.binding.WorkspaceID, CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/setup-cancel"})
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	ctx, cancel := context.WithCancel(env.ctx)
	resultCh := make(chan error, 1)
	go func() {
		_, err := env.service.CreateWorktree(ctx, serverapi.WorktreeCreateRequest{
			SetupOperationID: setupID,
			ClientRequestID:  "req-setup-cancel",
			SessionID:        env.session.Meta().SessionID,
			BaseRef:          "HEAD",
			CreateBranch:     true,
			BranchName:       "feature/setup-cancel",
		})
		resultCh <- err
	}()
	if got := waitForFileText(t, startedPath); got != "started" {
		t.Fatalf("started marker = %q, want started", got)
	}
	cancel()
	select {
	case err = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for canceled create")
	}
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want cancellation error")
	}
	if _, statErr := os.Stat(expectedRoot); statErr != nil {
		t.Fatalf("expected canceled setup worktree kept, stat err=%v", statErr)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.Phase != serverapi.WorktreeSetupPhaseFailed || !evt.Canceled {
		t.Fatalf("canceled setup event = %+v", evt)
	}
}

func TestCreateWorktreeSetupDirectoryScriptKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	scriptRelpath := filepath.Join("scripts", "directory")
	if err := os.MkdirAll(filepath.Join(env.workspaceRoot, scriptRelpath), 0o755); err != nil {
		t.Fatalf("MkdirAll script dir: %v", err)
	}
	env.service.setupScript = scriptRelpath
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	_, err = env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: setupID,
		ClientRequestID:  "req-setup-directory",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/setup-directory",
	})
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want directory setup script error")
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.Phase != serverapi.WorktreeSetupPhaseFailed || strings.TrimSpace(evt.Error) == "" {
		t.Fatalf("directory setup event = %+v", evt)
	}
}

func TestCreateWorktreeMissingSetupScriptKeepsWorktreeAndSessionTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	env.service.setupScript = filepath.Join("scripts", "missing.sh")
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := env.service.SubscribeWorktreeSetup(env.ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("SubscribeWorktreeSetup: %v", err)
	}
	defer func() { _ = sub.Close() }()
	expectedRoot, err := env.service.resolveRequestedWorktreeRoot("", env.binding.WorkspaceID, CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/setup-missing"})
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	_, err = env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: setupID,
		ClientRequestID:  "req-setup-missing",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/setup-missing",
	})
	if err == nil {
		t.Fatal("CreateWorktree succeeded, want missing setup script error")
	}
	if _, statErr := os.Stat(expectedRoot); statErr != nil {
		t.Fatalf("expected missing-setup worktree kept, stat err=%v", statErr)
	}
	assertServiceTestSessionTarget(t, env, "", env.workspaceRoot)
	evt := nextSetupTerminalEvent(t, sub)
	if evt.Phase != serverapi.WorktreeSetupPhaseFailed || strings.TrimSpace(evt.Error) == "" {
		t.Fatalf("missing setup event = %+v", evt)
	}
}

func TestRunSetupScriptDoesNotAppendSuccessNote(t *testing.T) {
	notes := &serviceTestLocalNotes{}
	service := &Service{localNotes: notes}
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	writeExecutableFile(t, scriptPath, "#!/bin/sh\nexit 0\n")

	if err := service.runSetupScript(context.Background(), scriptPath, setupScriptPayload{WorktreeRoot: t.TempDir()}); err != nil {
		t.Fatalf("runSetupScript: %v", err)
	}

	if got := notes.snapshot(); len(got) != 0 {
		t.Fatalf("expected no setup success note, got %+v", got)
	}
}

func TestCreateWorktreeAllowsExistingRefWithoutCreatingBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/existing-ref")

	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-create-existing-ref",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "feature/existing-ref",
		CreateBranch:     false,
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

func TestListWorktreesDoesNotResetManagedProvenanceWhenRootIsReused(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/provenance-stale")

	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", created.CanonicalRoot)
	runGit(t, env.workspaceRoot, "worktree", "add", "--detach", created.CanonicalRoot, "HEAD")

	worktrees := mustListWorktrees(t, env).Worktrees
	for _, entry := range worktrees {
		worktree := worktreeViewFromListEntryForTest(entry)
		if strings.TrimSpace(worktree.CanonicalRoot) != strings.TrimSpace(created.CanonicalRoot) {
			continue
		}
		if !worktree.Managed || !worktree.CreatedBranch || strings.TrimSpace(worktree.OriginSessionID) == "" {
			t.Fatalf("list mutated managed provenance for reused root: %+v", worktree)
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
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-create-shared-branch",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "feature/shared-branch",
		CreateBranch:     false,
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
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-create-shared-branch-explicit",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "feature/shared-branch",
		CreateBranch:     false,
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
	if sessionTargetWorktreeID(resp.Target) != "" {
		t.Fatalf("target worktree id = %q, want main workspace", sessionTargetWorktreeID(resp.Target))
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
	if sessionTargetWorktreeID(finalTarget) != "" || finalTarget.CwdRelpath != "." {
		t.Fatalf("unexpected final target after switch: %+v", finalTarget)
	}
}

func TestListWorktreesReportsMissingCurrentWorktreeWithoutRetargeting(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/missing-current")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	env.runtime.rebindCalls = nil
	env.runtime.reminderCalls = nil
	runGit(t, env.workspaceRoot, "worktree", "remove", "--force", created.CanonicalRoot)

	resp, err := env.service.ListWorktrees(env.ctx, serverapi.WorktreeListRequest{SessionID: env.session.Meta().SessionID})
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if sessionTargetWorktreeID(resp.Target) != created.WorktreeID {
		t.Fatalf("response target worktree id = %q, want %q", sessionTargetWorktreeID(resp.Target), created.WorktreeID)
	}
	foundMissing := false
	for _, entry := range resp.Worktrees {
		if worktreeIDFromListEntry(entry) == created.WorktreeID {
			foundMissing = entry.Topology.Variant == serverapi.WorktreeTopologyVariantMissing && entry.Projection.IsCurrent
		}
	}
	if !foundMissing {
		t.Fatalf("missing current worktree not projected: %+v", resp.Worktrees)
	}
	resolved, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(resolved) != created.WorktreeID {
		t.Fatalf("stored target worktree id = %q, want %q", sessionTargetWorktreeID(resolved), created.WorktreeID)
	}
	otherTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other session: %v", err)
	}
	if sessionTargetWorktreeID(otherTarget) != created.WorktreeID {
		t.Fatalf("other session was retargeted during list: %+v", otherTarget)
	}
	if len(env.runtime.rebindCalls) != 0 {
		t.Fatalf("list rebound runtimes: %+v", env.runtime.rebindCalls)
	}
	if len(env.runtime.reminderCalls) != 0 {
		t.Fatalf("list emitted reminders: %+v", env.runtime.reminderCalls)
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
	if sessionTargetWorktreeID(finalTarget) != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected execution target rollback to main workspace, got %+v", finalTarget)
	}
	if notes := env.localNotes.snapshot(); len(notes) != 0 {
		t.Fatalf("expected no local notes on failed switch, got %+v", notes)
	}
}

func TestSwitchSessionTargetRejectsInvalidPreviousTargetBeforeMetadataMutation(t *testing.T) {
	env := newServiceTestEnv(t)
	invalidPrevious := clientui.SessionExecutionTarget{
		WorkspaceID:      env.binding.WorkspaceID,
		WorkspaceRoot:    env.workspaceRoot,
		Worktree:         &clientui.SessionExecutionWorktreeTarget{},
		CwdRelpath:       ".",
		EffectiveWorkdir: env.workspaceRoot,
	}
	workspaceCtx := sessionWorkspaceContext{
		sessionID:     env.session.Meta().SessionID,
		workspaceID:   env.binding.WorkspaceID,
		workspaceRoot: env.workspaceRoot,
		target:        invalidPrevious,
		projectID:     env.binding.ProjectID,
	}
	next := syncedWorktree{
		record: metadata.WorktreeRecord{
			ID:            "worktree-next",
			WorkspaceID:   env.binding.WorkspaceID,
			CanonicalRoot: filepath.Join(env.workspaceRoot, "next"),
		},
		git: GitWorktree{},
	}

	if _, err := env.service.switchSessionTarget(env.ctx, workspaceCtx, nil, next); err == nil {
		t.Fatal("expected switchSessionTarget to reject previous target with present empty worktree id")
	}
	target, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("execution target mutated despite invalid previous target: %+v", target)
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
	if sessionTargetWorktreeID(finalTarget) != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
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

func TestCreateWorktreeKeepsCreatedStateWhenPostSetupSwitchFails(t *testing.T) {
	env := newServiceTestEnv(t)
	expectedRoot, err := env.service.resolveRequestedWorktreeRoot("", env.binding.WorkspaceID, CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/create-rollback"})
	if err != nil {
		t.Fatalf("resolveRequestedWorktreeRoot: %v", err)
	}
	env.runtime.rebindErr = errors.New("boom")

	resp, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "req-create-rollback",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/create-rollback",
	})
	if err != nil {
		t.Fatalf("CreateWorktree error = %v, want successful create without switching", err)
	}
	if sessionTargetWorktreeID(resp.Target) != "" || len(env.runtime.rebindCalls) != 0 {
		t.Fatalf("create changed target despite no-enter contract: target=%+v rebinds=%+v", resp.Target, env.runtime.rebindCalls)
	}
	if _, statErr := os.Stat(expectedRoot); statErr != nil {
		t.Fatalf("expected failed create worktree root kept, stat err=%v", statErr)
	}
	expectedRoot, err = config.CanonicalWorkspaceRoot(expectedRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot expected: %v", err)
	}
	if got := runGit(t, env.workspaceRoot, "branch", "--list", "feature/create-rollback"); strings.Contains(got, "feature/create-rollback") {
		// Branch remains because setup has completed and the worktree is inspectable.
	} else {
		t.Fatalf("expected created branch kept after post-setup switch failure, got %q", got)
	}
	records, err := env.store.ListWorktreeRecordsByWorkspaceID(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("ListWorktreeRecordsByWorkspaceID: %v", err)
	}
	recordKept := false
	for _, record := range records {
		if strings.TrimSpace(record.CanonicalRoot) == strings.TrimSpace(expectedRoot) {
			recordKept = true
		}
	}
	if !recordKept {
		t.Fatalf("expected failed create worktree record kept for root %q", expectedRoot)
	}
	finalTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if sessionTargetWorktreeID(finalTarget) != "" || finalTarget.EffectiveWorkdir != env.workspaceRoot {
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
	if sessionTargetWorktreeID(target) != "" || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("expected active idle session retargeted to main workspace, got %+v", target)
	}
	dormantTarget, err := env.store.ResolveSessionExecutionTarget(env.ctx, dormantSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget dormant session: %v", err)
	}
	if sessionTargetWorktreeID(dormantTarget) != "" || dormantTarget.EffectiveWorkdir != env.workspaceRoot {
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
		if sessionTargetWorktreeID(target) != created.WorktreeID || target.EffectiveWorkdir != created.CanonicalRoot {
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
	claim, _, _ := runtimes.AcquireRuntimeClaim(otherSession.Meta().SessionID, "test-owner")
	claim.Resolve(engine, nil, nil)
	env.service.active = runtimes
	env.runtime.runningSessions = map[string]bool{otherSession.Meta().SessionID: true}

	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-before-runtime-cleanup", created.WorktreeID))
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorktree before runtime cleanup error = %v, want ErrWorktreeBlocked", err)
	}

	if claim := runtimes.RuntimeClaimFor(otherSession.Meta().SessionID); claim != nil {
		_, _ = claim.Close(env.ctx, nil)
	}
	_, err = env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, "req-delete-after-runtime-cleanup", created.WorktreeID))
	if err != nil {
		t.Fatalf("DeleteWorktree after runtime cleanup: %v", err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected worktree root removed after runtime cleanup, stat err=%v", err)
	}
}
