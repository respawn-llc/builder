package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestCurrentNodeControllerConcurrentResumeCreatesOneExecution(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-concurrent-resume", "node-agent")
	store := &currentNodeControllerStore{interrupted: []workflow.CurrentNode{{
		Reference:  reference,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
	}}}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	release := make(chan struct{})
	var preparations atomic.Int32
	preparation := func(ctx context.Context) error {
		preparations.Add(1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, resumeErr := controller.ResumeTaskWithPreparation(context.Background(), reference.TaskID, preparation)
			results <- resumeErr
		}()
	}
	close(start)
	for range 2 {
		if resumeErr := <-results; resumeErr != nil {
			t.Fatalf("concurrent ResumeTask: %v", resumeErr)
		}
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return preparations.Load() == 1
	}, "duplicate Resume created more than one launching execution")
	close(release)
	select {
	case started := <-runner.started:
		if !started.Equal(reference) {
			t.Fatalf("started Current Node = %v, want %v", started, reference)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("winning Resume did not start")
	}
	select {
	case duplicate := <-runner.started:
		t.Fatalf("duplicate Resume started a second execution for %v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCurrentNodeControllerStartCommitFailureDiscardsStagedRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-start-commit-failure", "node-agent")
	commitErr := errors.New("commit prepared Start")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
		preparedCommitErr: commitErr,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	_, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error { return nil })
	if !errors.Is(err, commitErr) {
		t.Fatalf("StartTask error = %v, want prepared commit failure", err)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.ExplicitStarts) != 0 ||
		len(snapshot.Gates) != 0 ||
		len(snapshot.LiveScopes) != 0 {
		t.Fatalf("controller state after failed commit = %+v, want no Run ownership", snapshot)
	}
}

func TestCurrentNodeControllerStartCancellationBeforeCommitDiscardsStagedRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-start-commit-cancellation", "node-agent")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
		preparedCommitStarted: make(chan struct{}),
		preparedCommitRelease: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		select {
		case <-store.preparedCommitRelease:
		default:
			close(store.preparedCommitRelease)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(ctx, reference.TaskID, func(context.Context) error { return nil })
		result <- err
	}()
	select {
	case <-store.preparedCommitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("prepared Start did not reach Commit")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartTask cancellation error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StartTask did not return after request cancellation")
	}
	snapshot := controller.Snapshot()
	if len(snapshot.ExplicitStarts) != 0 ||
		len(snapshot.Gates) != 0 ||
		len(snapshot.LiveScopes) != 0 {
		t.Fatalf("controller state after canceled commit = %+v, want no Run ownership", snapshot)
	}
}

func TestCurrentNodeControllerResumeCommitFailureDiscardsEveryStagedRunAndKeepsAttention(t *testing.T) {
	taskID := workflow.TaskID("task-resume-commit-failure")
	first := currentNodeReferenceForControllerTest(t, string(taskID), "node-first")
	second := currentNodeReferenceForControllerTest(t, string(taskID), "node-second")
	commitErr := errors.New("commit prepared Resume")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{
			{Reference: first, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
			{Reference: second, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
		},
		preparedCommitErr: commitErr,
	}
	attention := &currentNodeAttentionRecorder{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerWithAttentionForTest(
		t,
		store,
		&countingCurrentNodeRunner{},
		authority,
		1,
		attention,
	)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	_, err := controller.ResumeTask(context.Background(), taskID)
	if !errors.Is(err, commitErr) {
		t.Fatalf("ResumeTask error = %v, want prepared commit failure", err)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.ExplicitStarts) != 0 ||
		len(snapshot.Gates) != 0 ||
		len(snapshot.LiveScopes) != 0 {
		t.Fatalf("controller state after failed Resume commit = %+v, want no Run ownership", snapshot)
	}
	if resolved := attention.resolvedInterruptions(); len(resolved) != 0 {
		t.Fatalf("attention finalized after failed Resume commit: %+v", resolved)
	}
	store.mu.Lock()
	resumedCount := len(store.resumed)
	store.mu.Unlock()
	if resumedCount != 0 {
		t.Fatalf("durable Resume count after failed commit = %d, want 0", resumedCount)
	}
}

func TestCurrentNodeControllerApprovalCommitFailurePreservesNoRunOwnership(t *testing.T) {
	source := currentNodeReferenceForControllerTest(t, "task-approval-commit-failure", "node-source")
	target := currentNodeReferenceForControllerTest(t, "task-approval-commit-failure", "node-target")
	approval := workflow.PendingApproval{ID: workflow.NewApprovalID(), Source: source}
	commitErr := errors.New("commit prepared Approval")
	store := &currentNodeControllerStore{
		pendingApproval: approval,
		approvalApplied: workflowstore.PendingApprovalApplyResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source},
				Created: []workflow.CurrentNode{{
					Reference:  target,
					Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
				}},
			},
			ResolvedApproval: approval,
		},
		preparedCommitErr: commitErr,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); !errors.Is(err, commitErr) {
		t.Fatalf("ApplyPendingApproval error = %v, want %v", err, commitErr)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.ExplicitStarts) != 0 ||
		len(snapshot.HeldIntents) != 0 ||
		len(snapshot.Gates) != 0 ||
		len(snapshot.LiveScopes) != 0 {
		t.Fatalf("controller state after failed Approval commit = %+v, want no Run ownership", snapshot)
	}
}

func TestCurrentNodeControllerIdleCompletionCommitFailurePreservesNoRunOwnership(t *testing.T) {
	source := currentNodeReferenceForControllerTest(t, "task-idle-completion-commit-failure", "node-source")
	target := currentNodeReferenceForControllerTest(t, "task-idle-completion-commit-failure", "node-target")
	commitErr := errors.New("commit prepared idle completion")
	store := &currentNodeControllerStore{
		idleResolved: &workflow.CurrentNode{
			Reference:  source,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		},
		completion: workflowstore.CurrentNodeCompletionResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source},
				Created: []workflow.CurrentNode{{
					Reference:  target,
					Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
				}},
			},
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
				CurrentNode: target,
				NodeKind:    workflow.NodeKindAgent,
			}},
		},
		completionCommitErr: commitErr,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	taskID := source.TaskID
	if _, err := controller.CompleteIdleCurrentNode(
		context.Background(),
		workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
		"next",
		nil,
		"forced",
	); !errors.Is(err, commitErr) {
		t.Fatalf("CompleteIdleCurrentNode error = %v, want %v", err, commitErr)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.AutomaticIntents) != 0 ||
		len(snapshot.ExplicitStarts) != 0 ||
		len(snapshot.Gates) != 0 ||
		len(snapshot.LiveScopes) != 0 {
		t.Fatalf("controller state after failed idle completion commit = %+v, want no Run ownership", snapshot)
	}
}

func TestCurrentNodeControllerIdleCompletionWithoutExecutableSuccessorCreatesNoRun(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutation workflow.CurrentNodeMutationResult
	}{
		{
			name: "terminal",
			mutation: workflow.CurrentNodeMutationResult{
				Created: []workflow.CurrentNode{{
					Reference: currentNodeReferenceForControllerTest(t, "task-idle-no-run-terminal", "node-terminal"),
				}},
			},
		},
		{
			name:     "blocked join",
			mutation: workflow.CurrentNodeMutationResult{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := currentNodeReferenceForControllerTest(t, "task-idle-no-run-"+test.name, "node-source")
			test.mutation.Removed = []workflow.CurrentNodeReference{source}
			store := &currentNodeControllerStore{
				idleResolved: &workflow.CurrentNode{
					Reference:  source,
					Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
				},
				completion: workflowstore.CurrentNodeCompletionResult{Mutation: test.mutation},
			}
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
			t.Cleanup(func() {
				_ = controller.Close()
				_ = authority.Close(context.Background())
			})

			taskID := source.TaskID
			if _, err := controller.CompleteIdleCurrentNode(
				context.Background(),
				workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
				"next",
				nil,
				"forced",
			); err != nil {
				t.Fatalf("CompleteIdleCurrentNode: %v", err)
			}
			snapshot := controller.Snapshot()
			if len(snapshot.AutomaticIntents) != 0 ||
				len(snapshot.ExplicitStarts) != 0 ||
				len(snapshot.HeldIntents) != 0 ||
				len(snapshot.Gates) != 0 ||
				len(snapshot.LiveScopes) != 0 {
				t.Fatalf("controller state after %s completion = %+v, want no Run", test.name, snapshot)
			}
		})
	}
}

func TestCurrentNodeControllerPrioritizesExplicitResumeOverBlockedAutomaticRun(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	capacitySeed := currentNodeReferenceForControllerTest(t, "task-explicit-priority-capacity", "node-seed")
	occupying := currentNodeReferenceForControllerTest(t, "task-explicit-priority-capacity", "node-agent")
	automaticSeed := currentNodeReferenceForControllerTest(t, "task-explicit-priority-automatic", "node-seed")
	automatic := currentNodeReferenceForControllerTest(t, "task-explicit-priority-automatic", "node-agent")
	explicit := currentNodeReferenceForControllerTest(t, "task-explicit-priority-resume", "node-agent")
	store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
			CurrentNode: occupying,
			NodeKind:    workflow.NodeKindAgent,
		}},
	}}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 5),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, capacitySeed); err != nil {
		t.Fatalf("start capacity seed: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, capacitySeed)
	capacitySeedScope := singleLiveScope(t, controller, capacitySeed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      capacitySeedScope,
		TransitionID: "occupy",
	}); err != nil {
		t.Fatalf("complete capacity seed: %v", err)
	}
	capacitySeedHandle, live := authority.ExecutionByScope(capacitySeedScope)
	if !live {
		t.Fatal("capacity seed retired before stop")
	}
	if err := capacitySeedHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop capacity seed: %v", err)
	}
	if started := <-runner.started; !started.Equal(occupying) {
		t.Fatalf("capacity owner = %v, want %v", started, occupying)
	}
	waitForRunningCurrentNode(t, authority, occupying)

	store.mu.Lock()
	store.started = workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  automaticSeed,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}
	store.completion = workflowstore.CurrentNodeCompletionResult{AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
		CurrentNode: automatic,
		NodeKind:    workflow.NodeKindAgent,
	}}}
	store.mu.Unlock()
	if _, err := controller.StartTask(context.Background(), automaticSeed.TaskID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("start automatic seed: %v", err)
	}
	if started := <-runner.started; !started.Equal(automaticSeed) {
		t.Fatalf("automatic seed = %v, want %v", started, automaticSeed)
	}
	waitForRunningCurrentNode(t, authority, automaticSeed)
	automaticSeedScope := singleLiveScope(t, controller, automaticSeed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      automaticSeedScope,
		TransitionID: "queue",
	}); err != nil {
		t.Fatalf("complete automatic seed: %v", err)
	}
	automaticSeedHandle, live := authority.ExecutionByScope(automaticSeedScope)
	if !live {
		t.Fatal("automatic seed retired before stop")
	}
	if err := automaticSeedHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop automatic seed: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasAutomaticCurrentNodeIntent(controller.Snapshot(), automatic)
	}, "automatic Agent did not remain queued behind occupied capacity")

	store.mu.Lock()
	store.interrupted = []workflow.CurrentNode{{
		Reference:  explicit,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
	}}
	store.mu.Unlock()
	if _, err := controller.ResumeTask(context.Background(), explicit.TaskID); err != nil {
		t.Fatalf("ResumeTask explicit Run: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(explicit) {
			t.Fatalf("start with blocked automatic Run = %v, want explicit Resume %v", started, explicit)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("explicit Resume did not start while automatic Agent waited for capacity")
	}
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), automatic) {
		t.Fatalf("explicit Resume displaced blocked automatic Run: %+v", controller.Snapshot())
	}
}

func TestCurrentNodeControllerSelfLoopSuccessorCoexistsWithExactPredecessor(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-self-loop-generation", "node-agent")
	store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
			CurrentNode: reference,
			NodeKind:    workflow.NodeKindAgent,
		}},
	}}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("start self-loop predecessor: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, reference)
	scopeID := singleLiveScope(t, controller, reference)
	sessionID := runtimeids.NewSessionID()
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		SessionID:    &sessionID,
		TransitionID: "loop",
	}); err != nil {
		t.Fatalf("complete self-loop predecessor: %v", err)
	}
	snapshot := controller.Snapshot()
	if !hasLiveCurrentNode(snapshot, reference) ||
		len(snapshot.HeldIntents) != 1 ||
		!snapshot.HeldIntents[0].CurrentNode.Equal(reference) {
		t.Fatalf("self-loop generations = %+v, want exact predecessor and held successor", snapshot)
	}
	handle, live := authority.ExecutionByScope(scopeID)
	if !live {
		t.Fatal("self-loop predecessor retired before explicit stop")
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop self-loop predecessor: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(reference) {
			t.Fatalf("self-loop successor = %v, want %v", started, reference)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("self-loop successor did not start after predecessor retirement")
	}
	waitForRunningCurrentNode(t, authority, reference)
	if err := controller.FailCurrentNodeScope(
		context.Background(),
		scopeID,
		"workflow_stale_predecessor_callback",
		errors.New("late predecessor diagnostic"),
	); !errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		t.Fatalf("late predecessor callback error = %v, want execution no longer live", err)
	}
	if !hasLiveCurrentNode(controller.Snapshot(), reference) {
		t.Fatal("late predecessor callback retired the self-loop successor")
	}
}

func TestCurrentNodeControllerCompletionCommitFailurePreservesExactPredecessor(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-completion-commit-failure", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-completion-commit-failure", "node-successor")
	commitErr := errors.New("injected completion commit failure")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source},
				Created: []workflow.CurrentNode{{
					Reference:  successor,
					Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
				}},
			},
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
				CurrentNode: successor,
				NodeKind:    workflow.NodeKindAgent,
			}},
		},
		completionCommitErr: commitErr,
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start predecessor: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	scopeID := singleLiveScope(t, controller, source)
	_, err = controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		TransitionID: "next",
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("completion error = %v, want %v", err, commitErr)
	}
	snapshot := controller.Snapshot()
	if !hasLiveCurrentNode(snapshot, source) || len(snapshot.HeldIntents) != 0 {
		t.Fatalf("completion commit failure ownership = %+v, want exact predecessor and no successor", snapshot)
	}
}

func TestCurrentNodeControllerScriptSelfLoopQueuesFreshGenerationBeforePredecessorCleanup(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	tests := []struct {
		name      string
		branchKey *workflow.TransitionBranchKey
	}{
		{name: "serial"},
		{name: "branch", branchKey: func() *workflow.TransitionBranchKey {
			value := workflow.TransitionBranchKey("branch-a")
			return &value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference, err := workflow.NewCurrentNodeReference(
				workflow.TaskID("task-script-self-loop-"+test.name),
				workflow.NodeID("node-script"),
				test.branchKey,
			)
			if err != nil {
				t.Fatalf("NewCurrentNodeReference: %v", err)
			}
			store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
				Mutation: workflow.CurrentNodeMutationResult{
					Removed: []workflow.CurrentNodeReference{reference},
					Created: []workflow.CurrentNode{{
						Reference:  reference,
						Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
					}},
				},
				AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
					CurrentNode: reference,
					NodeKind:    workflow.NodeKindScript,
				}},
			}}
			var controller *CurrentNodeController
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
				ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
					controller.ExecutionFinalized(scope)
				}),
			})
			runner := &recordingScriptRunner{
				authority: authority,
				command: sessionruntime.ScriptCommand{
					Path: shellPath,
					Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
				},
				started: make(chan workflow.CurrentNodeReference, 2),
			}
			controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
			t.Cleanup(func() {
				if err := controller.Close(); err != nil {
					t.Errorf("close controller: %v", err)
				}
				if err := authority.Close(context.Background()); err != nil {
					t.Errorf("close authority: %v", err)
				}
			})

			if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
				t.Fatalf("start predecessor: %v", err)
			}
			<-runner.started
			waitForRunningCurrentNode(t, authority, reference)
			scopeID := singleLiveScope(t, controller, reference)
			if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
				ScopeID:      scopeID,
				TransitionID: "loop",
			}); err != nil {
				t.Fatalf("complete predecessor: %v", err)
			}
			snapshot := controller.Snapshot()
			if !hasLiveCurrentNode(snapshot, reference) ||
				!hasAutomaticCurrentNodeIntent(snapshot, reference) {
				t.Fatalf("Script self-loop ownership = %+v, want exact predecessor and queued successor", snapshot)
			}
			select {
			case started := <-runner.started:
				t.Fatalf("Script self-loop successor %v started before predecessor cleanup", started)
			case <-time.After(50 * time.Millisecond):
			}
			handle, live := authority.ExecutionByScope(scopeID)
			if !live {
				t.Fatal("Script self-loop predecessor retired before explicit cleanup")
			}
			if err := handle.Stop(context.Background()); err != nil {
				t.Fatalf("stop predecessor: %v", err)
			}
			select {
			case started := <-runner.started:
				if !started.Equal(reference) {
					t.Fatalf("Script self-loop successor = %v, want %v", started, reference)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Script self-loop successor did not start after predecessor cleanup")
			}
		})
	}
}

func TestCurrentNodeControllerSelfLoopCommitFailurePreservesPredecessorGeneration(t *testing.T) {
	tests := []struct {
		name      string
		branchKey *workflow.TransitionBranchKey
	}{
		{name: "serial"},
		{name: "branch", branchKey: func() *workflow.TransitionBranchKey {
			value := workflow.TransitionBranchKey("branch-a")
			return &value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference, err := workflow.NewCurrentNodeReference(
				workflow.TaskID("task-self-loop-rollback-"+test.name),
				workflow.NodeID("node-agent"),
				test.branchKey,
			)
			if err != nil {
				t.Fatalf("NewCurrentNodeReference: %v", err)
			}
			commitErr := errors.New("injected self-loop commit failure")
			store := &currentNodeControllerStore{
				completion: workflowstore.CurrentNodeCompletionResult{
					Mutation: workflow.CurrentNodeMutationResult{
						Removed: []workflow.CurrentNodeReference{reference},
						Created: []workflow.CurrentNode{{
							Reference:  reference,
							Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
						}},
					},
					AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
						CurrentNode: reference,
						NodeKind:    workflow.NodeKindAgent,
					}},
					PostCompletionEligible: true,
				},
				completionCommitErr: commitErr,
			}
			shellPath, err := exec.LookPath("sh")
			if err != nil {
				t.Skipf("sh executable unavailable: %v", err)
			}
			var controller *CurrentNodeController
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
				ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
					controller.ExecutionFinalized(scope)
				}),
			})
			runner := &recordingScriptRunner{
				authority: authority,
				command: sessionruntime.ScriptCommand{
					Path: shellPath,
					Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
				},
				started: make(chan workflow.CurrentNodeReference, 2),
			}
			controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
			t.Cleanup(func() {
				if err := controller.Close(); err != nil {
					t.Errorf("close controller: %v", err)
				}
				if err := authority.Close(context.Background()); err != nil {
					t.Errorf("close authority: %v", err)
				}
			})
			if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
				t.Fatalf("start predecessor: %v", err)
			}
			<-runner.started
			waitForRunningCurrentNode(t, authority, reference)
			scopeID := singleLiveScope(t, controller, reference)
			_, err = controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
				ScopeID:      scopeID,
				SessionID:    func() *runtimeids.SessionID { value := runtimeids.NewSessionID(); return &value }(),
				TransitionID: "loop",
			})
			if !errors.Is(err, commitErr) {
				t.Fatalf("completion error = %v, want %v", err, commitErr)
			}
			snapshot := controller.Snapshot()
			if !hasLiveCurrentNode(snapshot, reference) ||
				len(snapshot.HeldIntents) != 0 ||
				hasAutomaticCurrentNodeIntent(snapshot, reference) {
				t.Fatalf("self-loop commit failure ownership = %+v, want predecessor only", snapshot)
			}
		})
	}
}

func TestCurrentNodeControllerFailedSuccessorStagingPreservesExistingGenerations(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	seed := currentNodeReferenceForControllerTest(t, "task-staging-rollback", "node-seed")
	source := currentNodeReferenceForControllerTest(t, "task-staging-rollback", "node-source")
	firstTarget := currentNodeReferenceForControllerTest(t, "task-staging-rollback", "node-first-target")
	existing := currentNodeReferenceForControllerTest(t, "task-staging-rollback", "node-existing")
	store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
			{CurrentNode: source, NodeKind: workflow.NodeKindAgent},
			{CurrentNode: existing, NodeKind: workflow.NodeKindAgent},
		},
	}}
	conflictingCompletion := workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
			{CurrentNode: firstTarget, NodeKind: workflow.NodeKindScript},
			{CurrentNode: existing, NodeKind: workflow.NodeKindAgent},
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, seed); err != nil {
		t.Fatalf("start staging seed: %v", err)
	}
	seedStarted := <-runner.started
	if !seedStarted.Equal(seed) {
		t.Fatalf("staging seed start = %v, want %v", seedStarted, seed)
	}
	waitForRunningCurrentNode(t, authority, seed)
	seedScope := singleLiveScope(t, controller, seed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      seedScope,
		TransitionID: "fanout",
	}); err != nil {
		t.Fatalf("complete staging seed: %v", err)
	}
	seedHandle, live := authority.ExecutionByScope(seedScope)
	if !live {
		t.Fatal("staging seed retired before stop")
	}
	if err := seedHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop staging seed: %v", err)
	}
	started := <-runner.started
	if !started.Equal(source) {
		t.Fatalf("first fan-out start = %v, want source %v", started, source)
	}
	waitForRunningCurrentNode(t, authority, source)
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasAutomaticCurrentNodeIntent(controller.Snapshot(), existing)
	}, "existing generation did not remain queued")

	store.mu.Lock()
	store.completion = conflictingCompletion
	store.mu.Unlock()
	_, err = controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      singleLiveScope(t, controller, source),
		TransitionID: "conflict",
	})
	if err == nil {
		t.Fatal("successor staging conflict unexpectedly succeeded")
	}
	snapshot := controller.Snapshot()
	if !hasLiveCurrentNode(snapshot, source) || !hasAutomaticCurrentNodeIntent(snapshot, existing) {
		t.Fatalf("staging rollback replaced predecessor or existing generation: %+v", snapshot)
	}
	for _, held := range snapshot.HeldIntents {
		if held.CurrentNode.Equal(firstTarget) {
			t.Fatalf("staging rollback retained partially staged successor: %+v", snapshot.HeldIntents)
		}
	}
}

func TestCurrentNodeControllerKeepsAgentCapacityUntilPredecessorRetiresWhileScriptsProgress(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	seed := currentNodeReferenceForControllerTest(t, "task-retiring-capacity", "node-seed")
	source := currentNodeReferenceForControllerTest(t, "task-retiring-capacity", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-retiring-capacity", "node-successor")
	secondarySeed := currentNodeReferenceForControllerTest(t, "task-retiring-secondary", "node-seed")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-retiring-secondary", "node-agent")
	script := currentNodeReferenceForControllerTest(t, "task-retiring-secondary", "node-script")
	store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
			CurrentNode: source,
			NodeKind:    workflow.NodeKindAgent,
		}},
	}}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 5),
	}
	controller, err = NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:      1,
		AssignmentSteerer:     noOpCurrentNodeAssignmentSteerer{},
		LifecycleAvailability: NewLifecycleFatalAvailability(),
	})
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, seed); err != nil {
		t.Fatalf("start capacity seed: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, seed)
	seedScope := singleLiveScope(t, controller, seed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      seedScope,
		TransitionID: "source",
	}); err != nil {
		t.Fatalf("complete capacity seed: %v", err)
	}
	seedHandle, live := authority.ExecutionByScope(seedScope)
	if !live {
		t.Fatal("capacity seed retired before stop")
	}
	if err := seedHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop capacity seed: %v", err)
	}
	started := <-runner.started
	if !started.Equal(source) {
		t.Fatalf("automatic capacity owner = %v, want %v", started, source)
	}
	waitForRunningCurrentNode(t, authority, source)

	store.mu.Lock()
	store.started = workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  secondarySeed,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}
	store.mu.Unlock()
	if _, err := controller.StartTask(context.Background(), secondarySeed.TaskID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("start secondary seed: %v", err)
	}
	secondaryStarted := <-runner.started
	if !secondaryStarted.Equal(secondarySeed) {
		t.Fatalf("secondary seed start = %v, want %v", secondaryStarted, secondarySeed)
	}
	waitForRunningCurrentNode(t, authority, secondarySeed)

	store.mu.Lock()
	store.completion = workflowstore.CurrentNodeCompletionResult{AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
		CurrentNode: successor,
		NodeKind:    workflow.NodeKindAgent,
	}}}
	store.mu.Unlock()
	scopeID := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	handle, live := authority.ExecutionByScope(scopeID)
	if !live {
		t.Fatal("source retired before stop")
	}

	store.mu.Lock()
	store.completion = workflowstore.CurrentNodeCompletionResult{AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: script, NodeKind: workflow.NodeKindScript},
	}}
	store.mu.Unlock()
	secondaryScope := singleLiveScope(t, controller, secondarySeed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      secondaryScope,
		TransitionID: "fanout",
	}); err != nil {
		t.Fatalf("complete secondary seed: %v", err)
	}
	secondaryHandle, live := authority.ExecutionByScope(secondaryScope)
	if !live {
		t.Fatal("secondary seed retired before stop")
	}

	if err := secondaryHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop secondary seed: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(script) {
			t.Fatalf("start during retiring = %v, want Script %v", started, script)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Script did not bypass Agent capacity during retirement")
	}
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), queuedAgent) {
		t.Fatalf("Agent started before predecessor retirement released capacity: %+v", controller.Snapshot())
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop source predecessor: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(queuedAgent) && !started.Equal(successor) {
			t.Fatalf("first Agent after predecessor retirement = %v, want queued Agent %v or successor %v", started, queuedAgent, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an eligible Agent did not start after predecessor retirement released capacity")
	}
}

type selectiveLateCommitSteerer struct {
	target  workflow.CurrentNodeReference
	started chan struct{}
	release <-chan struct{}
}

func (s selectiveLateCommitSteerer) SteerCurrentNodeAssignment(
	_ context.Context,
	reference workflow.CurrentNodeReference,
) (CurrentNodeAssignmentSteer, error) {
	if reference.Equal(s.target) {
		return &lateCommitCurrentNodeAssignmentSteer{
			started: s.started,
			release: s.release,
		}, nil
	}
	return completedCurrentNodeAssignmentSteer{
		receipt: session.CommitReceipt{Committed: true},
	}, nil
}

func TestCurrentNodeControllerPrefersSameTaskAutomaticContinuation(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	seed := currentNodeReferenceForControllerTest(t, "task-locality", "node-seed")
	first := currentNodeReferenceForControllerTest(t, "task-locality", "node-first")
	otherSeed := currentNodeReferenceForControllerTest(t, "task-other", "node-seed")
	other := currentNodeReferenceForControllerTest(t, "task-other", "node-other")
	continuation := currentNodeReferenceForControllerTest(t, "task-locality", "node-continuation")
	store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
			CurrentNode: first,
			NodeKind:    workflow.NodeKindAgent,
		}},
	}}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 3),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, seed); err != nil {
		t.Fatalf("start locality seed: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, seed)
	seedScope := singleLiveScope(t, controller, seed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      seedScope,
		TransitionID: "first",
	}); err != nil {
		t.Fatalf("complete locality seed: %v", err)
	}
	seedHandle, live := authority.ExecutionByScope(seedScope)
	if !live {
		t.Fatal("locality seed retired before stop")
	}
	if err := seedHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop locality seed: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(first) {
			t.Fatalf("first automatic start = %v, want %v", started, first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first automatic Run did not start")
	}
	waitForRunningCurrentNode(t, authority, first)

	store.mu.Lock()
	store.started = workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  otherSeed,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}
	store.completion = workflowstore.CurrentNodeCompletionResult{AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
		CurrentNode: other,
		NodeKind:    workflow.NodeKindAgent,
	}}}
	store.mu.Unlock()
	if _, err := controller.StartTask(context.Background(), otherSeed.TaskID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("start other seed: %v", err)
	}
	startedOtherSeed := <-runner.started
	if !startedOtherSeed.Equal(otherSeed) {
		t.Fatalf("other seed start = %v, want %v", startedOtherSeed, otherSeed)
	}
	waitForRunningCurrentNode(t, authority, otherSeed)
	otherSeedScope := singleLiveScope(t, controller, otherSeed)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      otherSeedScope,
		TransitionID: "other",
	}); err != nil {
		t.Fatalf("complete other seed: %v", err)
	}
	otherSeedHandle, live := authority.ExecutionByScope(otherSeedScope)
	if !live {
		t.Fatal("other seed retired before stop")
	}
	if err := otherSeedHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop other seed: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasAutomaticCurrentNodeIntent(controller.Snapshot(), other)
	}, "older other-Task Agent did not remain queued")

	store.mu.Lock()
	store.completion = workflowstore.CurrentNodeCompletionResult{AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
		CurrentNode: continuation,
		NodeKind:    workflow.NodeKindAgent,
	}}}
	store.mu.Unlock()
	firstScope := singleLiveScope(t, controller, first)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      firstScope,
		TransitionID: "continue",
	}); err != nil {
		t.Fatalf("complete first Run: %v", err)
	}
	handle, live := authority.ExecutionByScope(firstScope)
	if !live {
		t.Fatal("first automatic Run is not exact")
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop first automatic Run: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(continuation) {
			t.Fatalf("automatic start after %v = %v, want same-Task continuation %v", first, started, continuation)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("same-Task continuation did not start")
	}
}
