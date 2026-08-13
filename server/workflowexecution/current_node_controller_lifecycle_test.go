package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"sync"
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

func TestCurrentTaskQuiescenceIgnoresLatchedWorkerFailure(t *testing.T) {
	cause := errors.New("automatic assignment failed")
	controller := &CurrentNodeController{workerErr: cause}
	taskID := workflow.TaskID("task-board-read")

	quiescence, err := controller.CurrentTaskQuiescence([]workflow.TaskID{taskID})
	if err != nil {
		t.Fatalf("CurrentTaskQuiescence: %v", err)
	}
	if !quiescence[taskID] {
		t.Fatalf("task quiescence = %+v, want quiescent controller snapshot", quiescence)
	}
	if err := controller.EnsureTaskQuiescent(taskID); !errors.Is(err, cause) {
		t.Fatalf("EnsureTaskQuiescent error = %v, want worker failure %v", err, cause)
	}
}

func TestResumeTaskReturnsConflictBeforeMutationWhenRetainedSessionExecutionIsActive(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-resume-active-session", "node-implementation")
	release := make(chan struct{})
	handle, sessionID := fixture.startQuestionExecution(
		t,
		reference,
		func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
			<-release
			return nil
		},
	)
	t.Cleanup(func() {
		close(release)
		if err := handle.Close(context.Background()); err != nil {
			t.Errorf("close active retained Session execution: %v", err)
		}
	})
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("current node key: %v", err)
	}
	fixture.controller.mu.Lock()
	delete(fixture.controller.live, handle.Scope().ID())
	delete(fixture.controller.liveByNode, key)
	fixture.controller.mu.Unlock()
	fixture.store.interrupted = []workflow.CurrentNode{{
		Reference: reference,
		SessionID: &sessionID,
	}}

	resumed, err := fixture.controller.ResumeTask(context.Background(), reference.TaskID)
	if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("resume error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	if len(resumed) != 0 {
		t.Fatalf("resumed Current Nodes = %+v, want none", resumed)
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	if len(fixture.store.resumed) != 0 {
		t.Fatalf("resume mutations = %+v, want none before conflict", fixture.store.resumed)
	}
}

func TestPostTurnCompactionReleasesMutationPermitWhileApprovalFenceIsActive(t *testing.T) {
	source := currentNodeReferenceForControllerTest(t, "task-post-turn-fence", "node-source")
	sessionID := runtimeids.NewSessionID()
	scopeID := runtimeids.NewExecutionScopeID()
	key, err := source.Key()
	if err != nil {
		t.Fatalf("source key: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	controller := &CurrentNodeController{
		store: &currentNodeControllerStore{
			pendingApproval: workflow.PendingApproval{Source: source},
		},
		mutations: NewTaskMutationCoordinator(),
		authority: sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}),
		liveByNode: map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID{
			key: scopeID,
		},
		live: map[runtimeids.ExecutionScopeID]currentNodeLiveScope{
			scopeID: {reference: source},
		},
		completed: map[runtimeids.ExecutionScopeID]struct{}{scopeID: {}},
		postTurnFinalization: map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization{
			scopeID: {
				sessionID:      &sessionID,
				classification: workflow.SessionReuseGuaranteedCACReuse,
				reference:      source,
			},
		},
		heldStarts: make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart),
	}
	t.Cleanup(func() { _ = controller.authority.Close(context.Background()) })

	finalizationDone := make(chan error, 1)
	go func() {
		finalizationDone <- controller.FinalizeCurrentNodePostTurn(context.Background(), scopeID, sessionID, workflowruntime.PostCompletionRuntime{
			CompactionMode: "local",
			Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
				close(entered)
				<-release
				return workflowruntime.PostCompletionCompactionResult{}
			},
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("post-turn compaction did not start")
	}
	_, err = controller.ApplyPendingApproval(context.Background(), "approval")
	if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("approval apply while post-turn compaction = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	close(release)
	select {
	case err := <-finalizationDone:
		if err != nil {
			t.Fatalf("post-turn finalization: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("post-turn finalization did not finish")
	}
}

func TestPostTurnFinalizationCompactionEligibilityMatrix(t *testing.T) {
	tests := []struct {
		name            string
		classification  workflow.SessionReuseClassification
		usedTokens      int
		preCompaction   int
		compactionMode  string
		wantCompactions int
	}{
		{
			name:            "possible reuse below threshold",
			classification:  workflow.SessionReuseThresholdPossibleReuse,
			usedTokens:      99,
			preCompaction:   100,
			compactionMode:  "local",
			wantCompactions: 0,
		},
		{
			name:            "possible reuse at threshold",
			classification:  workflow.SessionReuseThresholdPossibleReuse,
			usedTokens:      100,
			preCompaction:   100,
			compactionMode:  "local",
			wantCompactions: 1,
		},
		{
			name:            "guaranteed CAC below threshold",
			classification:  workflow.SessionReuseGuaranteedCACReuse,
			usedTokens:      0,
			preCompaction:   100,
			compactionMode:  "local",
			wantCompactions: 1,
		},
		{
			name:            "none skips optimization",
			classification:  workflow.SessionReuseGuaranteedCACReuse,
			usedTokens:      0,
			preCompaction:   0,
			compactionMode:  "none",
			wantCompactions: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
				t,
				workflow.SessionReuseClassification(test.classification),
			)
			compactions := 0
			err := controller.FinalizeCurrentNodePostTurn(context.Background(), scopeID, sessionID, workflowruntime.PostCompletionRuntime{
				UsedTokens:          test.usedTokens,
				PreCompactionTokens: test.preCompaction,
				CompactionMode:      test.compactionMode,
				Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
					compactions++
					return workflowruntime.PostCompletionCompactionResult{
						CommitReceipt: session.CommitReceipt{Committed: true},
					}
				},
			})
			if err != nil {
				t.Fatalf("FinalizeCurrentNodePostTurn: %v", err)
			}
			if compactions != test.wantCompactions {
				t.Fatalf("compactions = %d, want %d", compactions, test.wantCompactions)
			}
			controller.mu.Lock()
			_, finalizing := controller.postTurnFinalization[scopeID]
			controller.mu.Unlock()
			if finalizing {
				t.Fatal("post-turn finalization fence remained after finalization")
			}
		})
	}
}

func TestPostTurnFinalizationSurfacesInvalidThresholdAndCancellation(t *testing.T) {
	t.Run("invalid threshold", func(t *testing.T) {
		controller, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
			t,
			workflow.SessionReuseThresholdPossibleReuse,
		)
		err := controller.FinalizeCurrentNodePostTurn(context.Background(), scopeID, sessionID, workflowruntime.PostCompletionRuntime{
			CompactionMode: "local",
		})
		if err == nil {
			t.Fatal("invalid pre-compaction threshold returned nil")
		}
		controller.mu.Lock()
		_, finalizing := controller.postTurnFinalization[scopeID]
		controller.mu.Unlock()
		if !finalizing {
			t.Fatal("invalid threshold cleared the finalization fence")
		}
	})

	t.Run("cancellation is fatal", func(t *testing.T) {
		controller, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
			t,
			workflow.SessionReuseGuaranteedCACReuse,
		)
		entered := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- controller.FinalizeCurrentNodePostTurn(ctx, scopeID, sessionID, workflowruntime.PostCompletionRuntime{
				CompactionMode: "local",
				Compact: func(compactionCtx context.Context) workflowruntime.PostCompletionCompactionResult {
					close(entered)
					<-compactionCtx.Done()
					return workflowruntime.PostCompletionCompactionResult{Diagnostic: context.Cause(compactionCtx)}
				},
			})
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("post-turn compaction did not start")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled finalization error = %v, want context canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled post-turn finalization did not return")
		}
	})
}

func newPostTurnFinalizationControllerForTest(
	t *testing.T,
	classification workflow.SessionReuseClassification,
) (*CurrentNodeController, runtimeids.ExecutionScopeID, runtimeids.SessionID) {
	t.Helper()
	source := currentNodeReferenceForControllerTest(t, "task-post-turn-matrix", "node-source")
	sessionID := runtimeids.NewSessionID()
	scopeID := runtimeids.NewExecutionScopeID()
	key, err := source.Key()
	if err != nil {
		t.Fatalf("source key: %v", err)
	}
	controller := &CurrentNodeController{
		mutations: NewTaskMutationCoordinator(),
		authority: sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}),
		liveByNode: map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID{
			key: scopeID,
		},
		live: map[runtimeids.ExecutionScopeID]currentNodeLiveScope{
			scopeID: {reference: source},
		},
		completed: map[runtimeids.ExecutionScopeID]struct{}{scopeID: {}},
		postTurnFinalization: map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization{
			scopeID: {
				sessionID:      &sessionID,
				classification: classification,
				reference:      source,
			},
		},
		heldStarts: make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart),
	}
	t.Cleanup(func() { _ = controller.authority.Close(context.Background()) })
	return controller, scopeID, sessionID
}

func TestObserveWorkflowTaskExecutionsIgnoresLatchedWorkerFailure(t *testing.T) {
	cause := errors.New("automatic assignment failed")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := &CurrentNodeController{
		authority: authority,
		workerErr: cause,
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	taskID := workflow.TaskID("task-status-read")

	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	if !observation.Quiescence[taskID] {
		t.Fatalf("task quiescence = %+v, want quiescent observation", observation.Quiescence)
	}
}

func TestObserveWorkflowTaskExecutionsDoesNotWaitForControllerLifecycleLock(t *testing.T) {
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := &CurrentNodeController{
		authority: authority,
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	taskID := workflow.TaskID("task-status-stale-read")

	controller.mu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(controller.mu.Unlock) }
	defer unlock()
	readDone := make(chan error, 1)
	go func() {
		observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID})
		if err == nil && observation.Quiescence[taskID] {
			err = errors.New("unobserved Task unexpectedly reported quiescent")
		}
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("ObserveWorkflowTaskExecutions while lifecycle lock held: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Task status observation waited for Controller lifecycle lock")
	}
	unlock()
}

func TestCurrentNodeControllerCompletesRetainedSessionAfterScopeRetires(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	source := currentNodeReferenceForControllerTest(t, "task-retained-session-completion", "node-source")
	sourceNode := workflow.CurrentNode{
		Reference: source,
		SessionID: &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
		},
	}
	store := &currentNodeControllerStore{
		idleResolved: &sourceNode,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	scopeID := runtimeids.NewExecutionScopeID()
	if _, err := controller.RecordProtocolViolation(context.Background(), workflowruntime.ViolationRequest{
		ScopeID:   scopeID,
		SessionID: &sessionID,
		Kind:      workflowruntime.ViolationKindInvalidCompletion,
		MaxCount:  2,
	}); err != nil {
		t.Fatalf("record retained Session protocol violation before completion: %v", err)
	}
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		SessionID:    &sessionID,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete retained Session Current Node: %v", err)
	}
	if calls := store.completionCount(); calls != 1 {
		t.Fatalf("completion calls = %d, want 1", calls)
	}
	controller.mu.Lock()
	_, retainedViolation := controller.violations[scopeID]
	controller.mu.Unlock()
	if retainedViolation {
		t.Fatal("successful retained Session completion kept its retired-scope violation counter")
	}
}

func TestCurrentNodeControllerRecordsProtocolViolationsForRetainedSessionAfterScopeRetires(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	source := currentNodeReferenceForControllerTest(t, "task-retained-session-violation", "node-source")
	sourceNode := workflow.CurrentNode{
		Reference: source,
		SessionID: &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
		},
	}
	store := &currentNodeControllerStore{
		idleResolved: &sourceNode,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	request := workflowruntime.ViolationRequest{
		ScopeID:   runtimeids.NewExecutionScopeID(),
		SessionID: &sessionID,
		Kind:      workflowruntime.ViolationKindInvalidCompletion,
		MaxCount:  2,
	}

	first, err := controller.RecordProtocolViolation(context.Background(), request)
	if err != nil {
		t.Fatalf("record first retained Session protocol violation: %v", err)
	}
	if first.Count != 1 || first.Interrupted {
		t.Fatalf("first retained Session protocol violation = %+v", first)
	}
	if err := controller.ResetProtocolViolationBudget(context.Background(), workflowruntime.ViolationResetRequest{
		ScopeID:   request.ScopeID,
		SessionID: &sessionID,
	}); err != nil {
		t.Fatalf("reset retained Session protocol violation budget: %v", err)
	}
	afterReset, err := controller.RecordProtocolViolation(context.Background(), request)
	if err != nil {
		t.Fatalf("record retained Session protocol violation after reset: %v", err)
	}
	if afterReset.Count != 1 || afterReset.Interrupted {
		t.Fatalf("retained Session protocol violation after reset = %+v", afterReset)
	}
	atCap, err := controller.RecordProtocolViolation(context.Background(), request)
	if err != nil {
		t.Fatalf("record retained Session protocol violation at cap: %v", err)
	}
	if atCap.Count != 2 || !atCap.Interrupted {
		t.Fatalf("retained Session protocol violation at cap = %+v", atCap)
	}
	controller.mu.Lock()
	_, retainedViolation := controller.violations[request.ScopeID]
	controller.mu.Unlock()
	if retainedViolation {
		t.Fatal("retained Session kept retired-scope violation counter after reaching cap")
	}
}

func TestCurrentNodeControllerSteersApprovalTargetBeforeStartingIt(t *testing.T) {
	target := currentNodeReferenceForControllerTest(t, "task-approval-steer", "node-target")
	approval := workflow.PendingApproval{
		ID:     workflow.NewApprovalID(),
		Source: currentNodeReferenceForControllerTest(t, "task-approval-steer", "node-source"),
	}
	store := &currentNodeControllerStore{
		pendingApproval: approval,
		approvalApplied: workflowstore.PendingApprovalApplyResult{
			Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  target,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}},
			ResolvedApproval: approval,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	steerer := &recordingCurrentNodeAssignmentSteerer{}
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: steerer,
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("ApplyPendingApproval: %v", err)
	}
	if got := steerer.references(); len(got) != 1 || !got[0].Equal(target) {
		t.Fatalf("steered assignments = %+v, want %v", got, target)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return len(runner.promptDeliveries()) == 1
	}, "approval target did not reach runner")
	if deliveries := runner.promptDeliveries(); len(deliveries) != 1 ||
		deliveries[0] != workflowruntime.TaskPromptDeliveryResume {
		t.Fatalf("runner prompt deliveries = %+v, want Resume after transition steer", deliveries)
	}
}

func TestCompleteIdleCurrentNodeInterruptsOnlyFailedAgentAndStartsHealthyScriptSibling(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	cause := errors.New("assignment persistence failed")
	source := currentNodeReferenceForControllerTest(t, "task-idle-completion-independent-failure", "node-source")
	agent := currentNodeReferenceForControllerTest(t, "task-idle-completion-independent-failure", "node-agent")
	script := currentNodeReferenceForControllerTest(t, "task-idle-completion-independent-failure", "node-script")
	agentKey, err := agent.Key()
	if err != nil {
		t.Fatalf("Agent Current Node key: %v", err)
	}
	sourceNode := workflow.CurrentNode{
		Reference:  source,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
	}
	completion := workflowstore.CurrentNodeCompletionResult{
		AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
			{CurrentNode: agent, NodeKind: workflow.NodeKindAgent},
			{CurrentNode: script, NodeKind: workflow.NodeKindScript},
		},
	}
	interruptStarted, interruptRelease := make(chan struct{}), make(chan struct{})
	store := &currentNodeControllerStore{
		idleResolved:     &sourceNode,
		completion:       completion,
		interruptStarted: interruptStarted,
		interruptRelease: interruptRelease,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{
			byReference: map[workflow.CurrentNodeReferenceKey]currentNodeAssignmentSteerOutcome{
				agentKey: {steerErr: cause},
			},
		},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	taskID := source.TaskID
	completionDone := make(chan error, 1)
	go func() {
		_, err := controller.CompleteIdleCurrentNode(
			context.Background(),
			workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
			"next",
			nil,
			"forced completion",
		)
		completionDone <- err
	}()
	<-interruptStarted
	quiescence, quiescenceErr := controller.CurrentTaskQuiescence([]workflow.TaskID{taskID})
	if quiescenceErr != nil || quiescence[taskID] {
		t.Fatalf("healthy Script sibling queued = %t, error = %v, want true", !quiescence[taskID], quiescenceErr)
	}
	close(interruptRelease)
	if completeErr := <-completionDone; !errors.Is(completeErr, cause) {
		t.Fatalf("CompleteIdleCurrentNode error = %v, want %v", completeErr, cause)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(script) {
			t.Fatalf("started successor = %v, want healthy Script %v", started, script)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("healthy Script sibling did not start")
	}
	if interruption, interrupted := store.interruption(agent); !interrupted {
		t.Fatalf("failed Agent was not interrupted: %+v", interruption)
	}
	if interruption, interrupted := store.interruption(script); interrupted {
		t.Fatalf("healthy Script was interrupted: %+v", interruption)
	}
}

func TestCompleteIdleCurrentNodePreparesFanOutAssignmentsIndependently(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-idle-preparation", "node-source")
	first := currentNodeReferenceForControllerTest(t, "task-idle-preparation", "node-first")
	sibling := currentNodeReferenceForControllerTest(t, "task-idle-preparation", "node-sibling")
	sourceNode := workflow.CurrentNode{
		Reference:  source,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
	}
	store := &currentNodeControllerStore{
		idleResolved: &sourceNode,
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
				{CurrentNode: first, NodeKind: workflow.NodeKindScript},
				{CurrentNode: sibling, NodeKind: workflow.NodeKindScript},
			},
		},
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	started := make(chan struct{})
	siblingPrepared := make(chan struct{})
	preparationErr := errors.New("assignment preparation stopped")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentSteerer: &blockingCurrentNodeAssignmentSteerer{
			blocked:         first,
			release:         release,
			started:         started,
			siblingPrepared: siblingPrepared,
			err:             preparationErr,
		},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		releaseFirst()
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	taskID := source.TaskID
	completed := make(chan error, 1)
	go func() {
		_, completeErr := controller.CompleteIdleCurrentNode(
			context.Background(),
			workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
			"next",
			nil,
			"forced completion",
		)
		completed <- completeErr
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first assignment preparation did not start")
	}
	select {
	case <-siblingPrepared:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked first assignment preparation prevented preparing its sibling")
	}
	quiescence, quiescenceErr := controller.CurrentTaskQuiescence([]workflow.TaskID{taskID})
	if quiescenceErr != nil || quiescence[taskID] {
		t.Fatalf("prepared sibling queued = %t, error = %v, want true", !quiescence[taskID], quiescenceErr)
	}
	releaseFirst()
	select {
	case completeErr := <-completed:
		if !errors.Is(completeErr, preparationErr) {
			t.Fatalf("CompleteIdleCurrentNode error = %v, want %v", completeErr, preparationErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CompleteIdleCurrentNode did not return after assignment preparations settled")
	}
	select {
	case startedSibling := <-runner.started:
		if !startedSibling.Equal(sibling) {
			t.Fatalf("started successor = %v, want prepared sibling %v", startedSibling, sibling)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prepared sibling did not start after completion returned")
	}
}

func TestCurrentNodeControllerHoldsApprovalTargetUntilCompletedSourceScopeRetires(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-approval-retirement", "node-source")
	target := currentNodeReferenceForControllerTest(t, "task-approval-retirement", "node-target")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-approval-retirement", "node-queued-agent")
	approval := workflow.PendingApproval{ID: workflow.NewApprovalID(), Source: source}
	targetNode := workflow.CurrentNode{
		Reference:  target,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
	}
	store := &currentNodeControllerStore{
		pendingApproval: approval,
		approvalApplied: workflowstore.PendingApprovalApplyResult{
			Mutation:         workflow.CurrentNodeMutationResult{Removed: []workflow.CurrentNodeReference{source}, Created: []workflow.CurrentNode{targetNode}},
			ResolvedApproval: approval,
		},
		completion: workflowstore.CurrentNodeCompletionResult{PendingApproval: &approval},
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
			Args: []string{"-c", "while :; do sleep 1; done"},
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

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: source,
		NodeKind:    workflow.NodeKindAgent,
	}})
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: queuedAgent,
		NodeKind:    workflow.NodeKindAgent,
	}})
	waitForRunningCurrentNode(t, authority, source)
	sourceScope := singleLiveScope(t, authority, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		TransitionID: "review",
	}); err != nil {
		t.Fatalf("complete approval source: %v", err)
	}
	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("ApplyPendingApproval: %v", err)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("approval target %v started before source retirement", started)
	case <-time.After(50 * time.Millisecond):
	}
	sourceHandle, live := authority.ExecutionByScope(sourceScope)
	if !live {
		t.Fatal("completed approval source scope retired before stop")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop approval source: %v", err)
	}
	targetStarted := false
	for !targetStarted {
		select {
		case started := <-runner.started:
			switch {
			case started.Equal(target):
				targetStarted = true
			case started.Equal(queuedAgent):
				continue
			default:
				t.Fatalf("started approval successor = %v, want target or queued Agent", started)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("approval target did not start after source retirement")
		}
	}
	waitForRunningCurrentNode(t, authority, target)
	waitForRunningCurrentNode(t, authority, queuedAgent)
}

func TestCurrentNodeControllerCloseWaitsForInFlightTaskMutation(t *testing.T) {
	taskID := workflow.TaskID("task-close-in-flight")
	reference := currentNodeReferenceForControllerTest(t, string(taskID), "node-start")
	started := make(chan struct{})
	release := make(chan struct{})
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Created: []workflow.CurrentNode{{
					Reference:  reference,
					Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
				}},
			},
		},
		startTaskStarted: started,
		startTaskRelease: release,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	startDone := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(
			context.Background(),
			taskID,
			TaskStartPreparation{
				Prepare: func(context.Context) error { return nil },
				Commit:  func(context.Context) error { return nil },
			},
			func(TaskPreparationFinalization) {},
		)
		startDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("StartTask did not enter its durable mutation")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- controller.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before in-flight Task mutation finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-startDone; err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after in-flight Task mutation settled")
	}
}

func TestCurrentNodeControllerCloseCancelsAdmissionBeforeWaitingForLifecycleBarrier(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-close-admission-wait", "node-agent")
	release := make(chan struct{})
	started := make(chan struct{})
	start := currentNodeQueuedStart{
		reference: reference,
		policy:    currentNodeAdmissionExplicitOverride,
		assignmentWait: &lateCommitCurrentNodeAssignmentSteer{
			release: release,
			started: started,
		},
		done: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, &currentNodeControllerStore{}, &countingCurrentNodeRunner{}, authority, 1)
	key := start.referenceKey()
	controller.mu.Lock()
	controller.explicitReservations[key] = start
	controller.admissionWorkers[key] = start
	controller.mu.Unlock()
	controller.admissionWG.Add(1)
	go controller.runAdmission(start)
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("admission did not begin waiting for assignment")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- controller.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel admission before waiting for lifecycle ownership")
	}
}

func TestCompleteIdleCurrentNodeRejectsSessionMovingToAnotherTask(t *testing.T) {
	first := workflow.CurrentNode{
		Reference: currentNodeReferenceForControllerTest(t, "task-idle-first", "node-first"),
	}
	second := workflow.CurrentNode{
		Reference: currentNodeReferenceForControllerTest(t, "task-idle-second", "node-second"),
	}
	store := &currentNodeControllerStore{
		idleResolvedSequence: []workflow.CurrentNode{first, second},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	sessionID := runtimeids.NewSessionID()

	_, err := controller.CompleteIdleCurrentNode(
		context.Background(),
		workflowstore.IdleCurrentNodeSelector{SessionID: &sessionID},
		"next",
		nil,
		"",
	)
	if err == nil {
		t.Fatal("CompleteIdleCurrentNode accepted a Session that moved to another Task")
	}
	if store.completions != 0 {
		t.Fatalf("completion mutations = %d, want none", store.completions)
	}
}

func TestTaskInterruptDispositionsTransferredSuccessorBeforeLateAssignmentDelivery(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-late-assignment-interrupt", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-late-assignment-interrupt", "node-successor")
	occupier := currentNodeReferenceForControllerTest(t, "task-capacity-occupier", "node-occupier")
	interruptible := currentNodeReferenceForControllerTest(t, string(source.TaskID), "node-interruptible")
	unrelated := currentNodeReferenceForControllerTest(t, "task-unrelated-assignment", "node-unrelated")
	releaseAssignment, assignmentStarted, assignmentResumed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	interruptStarted, interruptRelease, releaseSuccessor := make(chan struct{}), make(chan struct{}), make(chan struct{})
	assignmentErr := errors.New("assignment was not committed")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source},
			},
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}, {CurrentNode: occupier, NodeKind: workflow.NodeKindAgent}, {CurrentNode: interruptible, NodeKind: workflow.NodeKindScript}},
		},
		interruptStarted: interruptStarted,
		interruptRelease: interruptRelease,
		resumeClassifications: []workflowstore.CurrentNodeResumeClassification{{
			CurrentNode: workflow.CurrentNode{
				Reference:  unrelated,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
			},
		}},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &runningAndQueuedGateRunner{
		authority:        authority,
		shellPath:        shellPath,
		queued:           successor,
		runningStarted:   make(chan struct{}),
		queuedRegistered: make(chan struct{}),
		returnQueued:     releaseSuccessor,
	}
	controller, err = NewCurrentNodeController(
		store,
		runner,
		authority,
		NewTaskMutationCoordinator(),
		CurrentNodeControllerConfig{
			AgentConcurrency: 1,
			AssignmentSteerer: lateCommitCurrentNodeAssignmentSteerer{
				delayed: &successor,
				release: releaseAssignment,
				started: assignmentStarted,
				resumed: assignmentResumed,
				err:     assignmentErr,
			},
		},
	)
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start source: %v", err)
	}
	<-runner.runningStarted
	waitForRunningCurrentNode(t, authority, source)

	sourceScope := singleLiveScope(t, authority, source)
	completionCtx, cancelCompletion := context.WithCancel(context.Background())
	completionDone := make(chan error, 1)
	go func() {
		_, completeErr := controller.CompleteCurrentNode(completionCtx, workflowruntime.CompletionRequest{
			ScopeID:      sourceScope,
			TransitionID: "next",
		})
		completionDone <- completeErr
	}()
	<-assignmentStarted
	unrelatedDone := make(chan error, 1)
	go func() {
		unrelatedDone <- controller.EnsureTaskResumeEligible(context.Background(), unrelated.TaskID)
	}()
	select {
	case unrelatedErr := <-unrelatedDone:
		if unrelatedErr != nil {
			t.Fatalf("unrelated Task mutation: %v", unrelatedErr)
		}
	case <-time.After(time.Second):
		t.Fatal("successor assignment wait blocked unrelated Task mutation")
	}
	cancelCompletion()
	select {
	case completeErr := <-completionDone:
		if !errors.Is(completeErr, context.Canceled) {
			t.Fatalf("CompleteCurrentNode diagnostic = %v, want caller cancellation", completeErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CompleteCurrentNode did not transfer the canceled assignment wait")
	}
	<-assignmentResumed
	sourceHandle, live := authority.ExecutionByScope(sourceScope)
	if !live {
		t.Fatal("completed source scope retired before stop")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop completed source: %v", err)
	}
	waitForRunningCurrentNode(t, authority, occupier)
	waitForRunningCurrentNode(t, authority, interruptible)

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(
			context.Background(),
			InterruptSelector{TaskID: source.TaskID},
		)
	}()
	select {
	case <-interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Task interrupt did not enter durable successor disposition")
	}
	close(interruptRelease)
	close(releaseAssignment)
	if interruptErr := <-interruptDone; interruptErr != nil {
		t.Fatalf("Interrupt Task with transferred successor: %v", interruptErr)
	}
	if interruption, interrupted := store.interruption(successor); !interrupted {
		t.Fatal("transferred successor was not durably interrupted")
	} else if interruption.reason != reasonCurrentNodeRuntimeStartFailed ||
		interruption.detail.Code != string(reasonCurrentNodeRuntimeStartFailed) {
		t.Fatalf("transferred successor interruption = %+v, want assignment failure", interruption)
	}
	if hasLiveCurrentNode(authority, successor) {
		t.Fatal("transferred successor remained live after Task interrupt")
	}
}

func TestCurrentNodeControllerCompletesSuccessfulScriptBeforeScopeRetirement(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-script-complete", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-script-complete", "node-successor")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}},
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &completingScriptRunner{
		authority: authority,
		source:    source,
		shellPath: shellPath,
		started:   make(chan workflow.CurrentNodeReference, 2),
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
		t.Fatalf("start script current node: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(successor) {
			t.Fatalf("started successor = %v, want %v", started, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successful script completion did not start its automatic successor")
	}
	if calls := store.completionCount(); calls != 1 {
		t.Fatalf("completion calls = %d, want 1", calls)
	}
	if interruption, interrupted := store.interruption(source); interrupted {
		t.Fatalf("successful script source was interrupted: %+v", interruption)
	}
}
