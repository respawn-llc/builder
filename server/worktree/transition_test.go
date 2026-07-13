package worktree

import (
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestEnterWorktreeSchedulesSingleFlightAdoptsExternalAndPublishesOutcome(t *testing.T) {
	env := newServiceTestEnv(t)
	externalRoot := createExternalWorktree(t, env, "feature/external-enter")
	gate := make(chan struct{})
	env.runtime.transitionGate = gate
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
	replayed, err := env.service.EnterWorktree(env.ctx, request)
	if err != nil || replayed != ack {
		t.Fatalf("identical retry = %+v, %v; want replayed acknowledgement", replayed, err)
	}
	_, err = env.service.LeaveWorktree(env.ctx, serverapi.WorktreeLeaveRequest{
		OperationID: serverapi.NewWorktreeOperationID(),
		SessionID:   env.session.Meta().SessionID,
	})
	var pending *serverapi.WorktreeTransitionPendingError
	if !errors.As(err, &pending) || pending.PendingOperationID != operationID {
		t.Fatalf("different transition error = %v, want pending operation %s", err, operationID.String())
	}
	if target := mustResolveServiceTestTarget(t, env); target.Worktree != nil {
		t.Fatalf("target changed before transition boundary: %+v", target)
	}

	close(gate)
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("outcome = %+v", outcome)
	}
	target := mustResolveServiceTestTarget(t, env)
	canonicalExternalRoot, err := config.CanonicalWorkspaceRoot(externalRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if target.Worktree == nil || target.Worktree.Root != canonicalExternalRoot {
		t.Fatalf("target after enter = %+v, want %q", target, canonicalExternalRoot)
	}
	record, err := env.store.GetWorktreeRecordByID(env.ctx, target.Worktree.ID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if record.Managed || record.CreatedBranch {
		t.Fatalf("external adoption changed provenance: %+v", record)
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
	gate := make(chan struct{})
	env.runtime.transitionGate = gate
	if _, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
		OperationID: serverapi.NewWorktreeOperationID(),
		SessionID:   env.session.Meta().SessionID,
		Selector:    env.workspaceRoot,
	}); err != nil {
		t.Fatalf("EnterWorktree: %v", err)
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

func TestScheduledWorktreeTransitionFailurePublishesAndSteersTypedOutcome(t *testing.T) {
	env := newServiceTestEnv(t)
	operationID := serverapi.NewWorktreeOperationID()
	if _, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
		OperationID: operationID,
		SessionID:   env.session.Meta().SessionID,
		Selector:    "missing-worktree",
	}); err != nil {
		t.Fatalf("EnterWorktree: %v", err)
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		if len(runtime.transitionOutcomes) > 0 {
			outcome := runtime.transitionOutcomes[len(runtime.transitionOutcomes)-1]
			runtime.mu.Unlock()
			return outcome
		}
		runtime.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for worktree transition outcome")
	return clientui.WorktreeTransitionOutcome{}
}
