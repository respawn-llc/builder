package worktree

import (
	"context"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type serviceTestRuntime struct {
	mu                     sync.Mutex
	rebindCalls            []serviceRuntimeCall
	reminderCalls          []session.WorktreeReminderState
	clearReminderSessions  []string
	activeSessions         map[string]bool
	runningSessions        map[string]bool
	syncErrSessions        map[string]error
	blockedRuns            map[string]int
	blockRunsHook          func([]string)
	rebindErr              error
	rebindErrRoot          string
	rebindHook             func(context.Context, string, string, string)
	transitionGate         <-chan struct{}
	transitionOutcomes     []clientui.WorktreeTransitionOutcome
	transitionOutcomeReady chan struct{}
	steeredFailures        []clientui.WorktreeTransitionOutcome
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

func (r *serviceTestRuntime) RunWorktreeTransition(
	ctx context.Context,
	sessionID string,
	fn func(context.Context, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) error {
	if r.transitionGate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.transitionGate:
		}
	}
	return fn(ctx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
		return r.SyncExecutionTarget(syncCtx, sessionID, target, reminder)
	})
}

func (r *serviceTestRuntime) PublishWorktreeTransitionOutcome(_ string, outcome clientui.WorktreeTransitionOutcome) {
	r.mu.Lock()
	r.transitionOutcomes = append(r.transitionOutcomes, outcome)
	if r.transitionOutcomeReady == nil {
		r.transitionOutcomeReady = make(chan struct{}, 1)
	}
	ready := r.transitionOutcomeReady
	r.mu.Unlock()
	select {
	case ready <- struct{}{}:
	default:
	}
}

func (r *serviceTestRuntime) SteerWorktreeTransitionFailure(_ context.Context, _ string, outcome clientui.WorktreeTransitionOutcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steeredFailures = append(r.steeredFailures, outcome)
	return nil
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
	hook := r.blockRunsHook
	r.mu.Unlock()
	if hook != nil {
		hook(append([]string(nil), blocked...))
	}
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
	createdView := worktreeViewFromListEntryForTest(resp.Worktree)
	if !createdView.CreatedBranch {
		t.Fatal("expected create to report created branch")
	}
	if !createdView.Managed {
		t.Fatal("expected worktree managed=true")
	}
	if sessionTargetWorktreeID(resp.Target) != "" {
		t.Fatalf("create changed session target to %q", sessionTargetWorktreeID(resp.Target))
	}
	if resp.Target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("create effective workdir = %q, want %q", resp.Target.EffectiveWorkdir, env.workspaceRoot)
	}
	if !createdView.CreatedBranch {
		t.Fatal("expected worktree created_branch=true")
	}
	if createdView.OriginSessionID != env.session.Meta().SessionID {
		t.Fatalf("origin session id = %q, want %q", createdView.OriginSessionID, env.session.Meta().SessionID)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, createdView.WorktreeID)
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
	if payload.SessionID == nil || *payload.SessionID != env.session.Meta().SessionID {
		t.Fatalf("setup payload session_id = %v, want %q", payload.SessionID, env.session.Meta().SessionID)
	}
	if payload.WorktreeID != createdView.WorktreeID {
		t.Fatalf("setup payload worktree_id = %q, want %q", payload.WorktreeID, createdView.WorktreeID)
	}
	if !payload.CreatedBranch {
		t.Fatal("expected setup payload created_branch=true")
	}
	if got := waitForFileText(t, cwdPath); got != createdView.CanonicalRoot {
		t.Fatalf("setup cwd = %q, want %q", got, createdView.CanonicalRoot)
	}
	if got := waitForFileLines(t, argsPath); len(got) != 3 || got[0] != env.workspaceRoot || got[1] != "feature/create-provenance" || got[2] != createdView.CanonicalRoot {
		t.Fatalf("setup args = %+v, want [%q %q %q]", got, env.workspaceRoot, "feature/create-provenance", createdView.CanonicalRoot)
	}
	if stdinPayload := waitForSetupPayload(t, stdinPath); !reflect.DeepEqual(stdinPayload, payload) {
		t.Fatalf("stdin payload = %+v, want %+v", stdinPayload, payload)
	}
	if len(env.runtime.rebindCalls) != 0 {
		t.Fatalf("create rebounded the runtime, got %+v", env.runtime.rebindCalls)
	}
	if len(env.runtime.reminderCalls) != 0 {
		t.Fatalf("create issued a worktree reminder, got %+v", env.runtime.reminderCalls)
	}
	worktrees := mustListWorktrees(t, env)
	created := findWorktreeByID(t, worktrees.Worktrees, createdView.WorktreeID)
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
	default:
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
	createdView := worktreeViewFromListEntryForTest(resp.Worktree)
	if createdView.CreatedBranch {
		t.Fatal("expected created_branch=false for existing ref")
	}
	if createdView.BranchName != "feature/existing-ref" {
		t.Fatalf("branch name = %q, want feature/existing-ref", createdView.BranchName)
	}
	if !createdView.Managed {
		t.Fatal("expected managed worktree for existing ref")
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, createdView.WorktreeID)
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
	mainGit, found, err := env.service.git.FindCreatedWorktree(env.ctx, env.workspaceRoot, env.workspaceRoot)
	if err != nil || !found {
		t.Fatalf("find main worktree: found=%v err=%v", found, err)
	}
	previousRecord, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	previous := syncedWorktree{record: previousRecord, git: GitWorktree{Root: created.CanonicalRoot, BranchName: created.BranchName}}
	respTarget, err := env.service.switchSessionTarget(env.ctx, sessionWorkspaceContext{
		target:        mustResolveServiceTestTarget(t, env),
		projectID:     env.binding.ProjectID,
		workspaceID:   env.binding.WorkspaceID,
		workspaceRoot: env.workspaceRoot,
		sessionID:     env.session.Meta().SessionID,
	}, &previous, syncedWorktree{
		record: metadata.WorktreeRecord{WorkspaceID: env.binding.WorkspaceID, CanonicalRoot: env.workspaceRoot},
		git:    mainGit,
	})
	if err != nil {
		t.Fatalf("switchSessionTarget: %v", err)
	}
	if sessionTargetWorktreeID(respTarget) != "" {
		t.Fatalf("target worktree id = %q, want main workspace", sessionTargetWorktreeID(respTarget))
	}
	if respTarget.CwdRelpath != "." {
		t.Fatalf("target cwd_relpath = %q, want .", respTarget.CwdRelpath)
	}
	if respTarget.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("effective workdir = %q, want %q", respTarget.EffectiveWorkdir, env.workspaceRoot)
	}
	if len(env.runtime.rebindCalls) == 0 || env.runtime.rebindCalls[len(env.runtime.rebindCalls)-1].root != env.workspaceRoot {
		t.Fatalf("expected rebind to main workspace, got %+v", env.runtime.rebindCalls)
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
