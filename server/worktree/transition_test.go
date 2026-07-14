package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/runtimewire"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestEnterWorktreePreflightObservesCancellationWhileWorkspaceMutationLocked(t *testing.T) {
	env := newServiceTestEnv(t)
	release, err := env.service.acquireWorkspaceMutationLock(env.ctx, env.binding.WorkspaceID)
	if err != nil {
		t.Fatalf("acquireWorkspaceMutationLock: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			release()
		}
	})

	ctx, cancel := context.WithCancel(env.ctx)
	result := make(chan error, 1)
	go func() {
		_, err := env.service.EnterWorktree(ctx, serverapi.WorktreeEnterRequest{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   env.session.Meta().SessionID,
			Selector:    "main",
		})
		result <- err
	}()
	waitForWorkspaceMutationReferences(t, env.service, env.binding.WorkspaceID, 2)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("EnterWorktree error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		release()
		released = true
		select {
		case <-result:
		case <-time.After(3 * time.Second):
			t.Fatal("EnterWorktree remained blocked after releasing workspace mutation")
		}
		t.Fatal("EnterWorktree ignored cancellation while waiting for workspace mutation")
	}
	release()
	released = true
	nextRelease, _, err := env.service.beginWorkspaceMutation(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("beginWorkspaceMutation after canceled waiter: %v", err)
	}
	nextRelease()
}

func waitForWorkspaceMutationReferences(t *testing.T, service *Service, workspaceID string, want int) {
	t.Helper()
	workspaceID = strings.TrimSpace(workspaceID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		service.workspaceMu.Lock()
		lock := service.workspaceLocks[workspaceID]
		refs := 0
		if lock != nil {
			refs = lock.refs
		}
		service.workspaceMu.Unlock()
		if refs == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("workspace mutation references = %d, want %d", refs, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEnterWorktreeRejectsInvalidSelectorsBeforeScheduling(t *testing.T) {
	env := newServiceTestEnv(t)
	validRoot := createExternalWorktree(t, env, "feature/valid-after-invalid")
	ambiguousRoot := filepath.Join(t.TempDir(), filepath.Base(validRoot))
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "feature/ambiguous-enter", ambiguousRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", ambiguousRoot) })
	gate := make(chan struct{})
	env.runtime.transitionGate = gate
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})

	for _, testCase := range []struct {
		selector string
		kind     serverapi.WorktreeSelectorErrorKind
	}{
		{selector: "missing-worktree", kind: serverapi.WorktreeSelectorErrorKindNotFound},
		{selector: filepath.Base(validRoot), kind: serverapi.WorktreeSelectorErrorKindAmbiguous},
	} {
		_, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   env.session.Meta().SessionID,
			Selector:    testCase.selector,
		})
		var selectorErr *serverapi.WorktreeSelectorError
		if !errors.As(err, &selectorErr) || selectorErr.Kind != testCase.kind {
			close(gate)
			t.Fatalf("selector %q error = %v, want synchronous %s selector error", testCase.selector, err, testCase.kind)
		}
	}

	operationID := serverapi.NewWorktreeOperationID()
	ack, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
		OperationID: operationID,
		SessionID:   env.session.Meta().SessionID,
		Selector:    "feature/valid-after-invalid",
	})
	if err != nil || ack.OperationID != operationID {
		close(gate)
		t.Fatalf("valid transition after rejected selector = %+v, %v", ack, err)
	}
	close(gate)
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("outcome = %+v, want valid transition completion", outcome)
	}
}

func TestEnterWorktreeAppliesBeforeAcknowledgementAndAdoptsExternal(t *testing.T) {
	env := newServiceTestEnv(t)
	externalRoot := createExternalWorktree(t, env, "feature/external-enter")
	binding, _, background, err := runtimewire.NewLocalToolRegistryBinding(runtimewire.LocalToolRegistryOptions{
		WorkspaceRoot:       env.workspaceRoot,
		Enabled:             []toolspec.ID{toolspec.ToolExecCommand},
		MinimumExecToBgTime: time.Second,
		ShellOutputMaxChars: 16_000,
		SupportsVision:      true,
	})
	if err != nil {
		t.Fatalf("NewLocalToolRegistryBinding: %v", err)
	}
	t.Cleanup(func() {
		if err := background.Close(); err != nil {
			t.Fatalf("close background manager: %v", err)
		}
	})
	env.runtime.mu.Lock()
	env.runtime.activeSessions[env.session.Meta().SessionID] = true
	env.runtime.rebindHook = func(_ context.Context, _ string, _ string, root string) {
		if err := binding.Rebind(root); err != nil {
			t.Errorf("Rebind(%q): %v", root, err)
		}
	}
	env.runtime.mu.Unlock()
	operationID := serverapi.NewWorktreeOperationID()
	request := serverapi.WorktreeEnterRequest{
		OperationID: operationID,
		SessionID:   env.session.Meta().SessionID,
		Selector:    "feature/external-enter",
	}

	ack, err := env.service.EnterWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("EnterWorktree: %v", err)
	}
	if ack.OperationID != operationID {
		t.Fatalf("ack = %+v", ack)
	}
	target := mustResolveServiceTestTarget(t, env)
	canonicalExternalRoot, err := config.CanonicalWorkspaceRoot(externalRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if target.Worktree == nil || target.Worktree.Root != canonicalExternalRoot {
		t.Fatalf("target after enter = %+v, want %q", target, canonicalExternalRoot)
	}
	if got := worktreeTestExecOutput(t, binding.Registry(), "pwd"); got != canonicalExternalRoot {
		t.Fatalf("following tool pwd = %q, want %q", got, canonicalExternalRoot)
	}
	if got := worktreeTestExecOutput(t, binding.Registry(), "git branch --show-current"); got != "feature/external-enter" {
		t.Fatalf("following tool branch = %q, want %q", got, "feature/external-enter")
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, target.Worktree.ID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if record.Managed || record.CreatedBranch {
		t.Fatalf("external adoption changed provenance: %+v", record)
	}
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func worktreeTestExecOutput(t *testing.T, registry *tools.Registry, command string) string {
	t.Helper()
	handler, ok := registry.Get(toolspec.ToolExecCommand)
	if !ok {
		t.Fatal("exec_command handler is unavailable")
	}
	input, err := json.Marshal(map[string]string{"cmd": command})
	if err != nil {
		t.Fatalf("marshal exec_command input: %v", err)
	}
	result, err := handler.Call(context.Background(), tools.Call{
		ID:    "test-call",
		Name:  toolspec.ToolExecCommand,
		Input: input,
	})
	if err != nil {
		t.Fatalf("exec_command %q: %v", command, err)
	}
	if result.IsError {
		t.Fatalf("exec_command %q failed: %s", command, result.Output)
	}
	var output string
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode exec_command output: %v", err)
	}
	return strings.TrimSpace(output)
}

func TestImmediateEnterRemainsBoundToInitiallyResolvedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	initialRoot := createExternalWorktree(t, env, "feature/enter-bound")
	operationID := serverapi.NewWorktreeOperationID()

	if _, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
		OperationID: operationID,
		SessionID:   env.session.Meta().SessionID,
		Selector:    "feature/enter-bound",
	}); err != nil {
		t.Fatalf("EnterWorktree: %v", err)
	}

	target := mustResolveServiceTestTarget(t, env)
	canonicalInitialRoot, err := config.CanonicalWorkspaceRoot(initialRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if target.Worktree == nil || target.Worktree.Root != canonicalInitialRoot {
		t.Fatalf("target after selector drift = %+v, want initially resolved root %q", target, canonicalInitialRoot)
	}
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestLeaveWorktreeRunsAtTransitionBoundary(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/leave")
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	gate := make(chan struct{})
	env.runtime.transitionGate = gate
	operationID := serverapi.NewWorktreeOperationID()

	if _, err := env.service.LeaveWorktree(env.ctx, serverapi.WorktreeLeaveRequest{
		OperationID: operationID,
		SessionID:   env.session.Meta().SessionID,
	}); err != nil {
		t.Fatalf("LeaveWorktree: %v", err)
	}
	if target := mustResolveServiceTestTarget(t, env); target.Worktree == nil {
		t.Fatalf("target changed before transition boundary: %+v", target)
	}
	close(gate)
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("outcome = %+v", outcome)
	}
	if target := mustResolveServiceTestTarget(t, env); target.Worktree != nil || target.EffectiveWorkdir != env.workspaceRoot {
		t.Fatalf("target after leave = %+v", target)
	}
}

func TestCloseCancelsPendingWorktreeTransitionWithoutPublishingOutcome(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/close-cancel")
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	gate := make(chan struct{})
	env.runtime.transitionGate = gate
	if _, err := env.service.LeaveWorktree(env.ctx, serverapi.WorktreeLeaveRequest{
		OperationID: serverapi.NewWorktreeOperationID(),
		SessionID:   env.session.Meta().SessionID,
	}); err != nil {
		t.Fatalf("LeaveWorktree: %v", err)
	}
	if err := env.service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	if len(env.runtime.transitionOutcomes) != 0 {
		t.Fatalf("shutdown published transition outcomes: %+v", env.runtime.transitionOutcomes)
	}
}

func TestCloseWaitsForTransitionSchedulingCriticalSection(t *testing.T) {
	env := newServiceTestEnv(t)
	env.service.transitionMu.Lock()
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- env.service.Close()
	}()

	select {
	case err := <-closeResult:
		env.service.transitionMu.Unlock()
		t.Fatalf("Close returned before transition scheduling completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	env.service.transitionMu.Unlock()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestImmediateEnterFailureIsReturnedAndPublishesTypedOutcome(t *testing.T) {
	env := newServiceTestEnv(t)
	createExternalWorktree(t, env, "feature/immediate-enter-failure")
	env.runtime.mu.Lock()
	env.runtime.activeSessions[env.session.Meta().SessionID] = true
	env.runtime.rebindErr = errors.New("immediate retarget failed")
	env.runtime.mu.Unlock()
	operationID := serverapi.NewWorktreeOperationID()
	_, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
		OperationID: operationID,
		SessionID:   env.session.Meta().SessionID,
		Selector:    "feature/immediate-enter-failure",
	})
	if err == nil {
		t.Fatal("EnterWorktree succeeded after immediate retarget failure")
	}
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.OperationID != operationID ||
		outcome.State != clientui.WorktreeTransitionFailed ||
		outcome.Failure == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	if len(env.runtime.steeredFailures) != 1 || env.runtime.steeredFailures[0] != outcome {
		t.Fatalf("steered failures = %+v, want %+v", env.runtime.steeredFailures, outcome)
	}
}

func createExternalWorktree(t *testing.T, env *serviceTestEnv, branch string) string {
	t.Helper()
	root := env.baseDir + "-external"
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", branch, root, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", root) })
	return root
}

func waitForWorktreeTransitionOutcome(t *testing.T, runtime *serviceTestRuntime) clientui.WorktreeTransitionOutcome {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		runtime.mu.Lock()
		if len(runtime.transitionOutcomes) > 0 {
			outcome := runtime.transitionOutcomes[len(runtime.transitionOutcomes)-1]
			runtime.mu.Unlock()
			return outcome
		}
		if runtime.transitionOutcomeReady == nil {
			runtime.transitionOutcomeReady = make(chan struct{}, 1)
		}
		ready := runtime.transitionOutcomeReady
		runtime.mu.Unlock()
		select {
		case <-ready:
		case <-timeout.C:
			t.Fatal("timed out waiting for worktree transition outcome")
			return clientui.WorktreeTransitionOutcome{}
		}
	}
}
