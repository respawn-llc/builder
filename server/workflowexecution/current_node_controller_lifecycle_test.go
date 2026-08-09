package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
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

func TestResumeTaskPanicsBeforeMutationWhenRetainedSessionExecutionIsActive(t *testing.T) {
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

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_, _ = fixture.controller.ResumeTask(context.Background(), reference.TaskID)
	}()
	invariant, ok := recovered.(resumeActiveSessionInvariant)
	if !ok {
		t.Fatalf("resume panic = %T, want resumeActiveSessionInvariant", recovered)
	}
	if !invariant.currentNode.Equal(reference) ||
		invariant.sessionID != sessionID ||
		invariant.scopeID != handle.Scope().ID() {
		t.Fatalf("resume invariant = %+v, want current node %v Session %s scope %s", invariant, reference, sessionID, handle.Scope().ID())
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	if len(fixture.store.resumed) != 0 {
		t.Fatalf("resume mutations = %+v, want none before panic", fixture.store.resumed)
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
		permit:    NewMutationPermit(),
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
		permit:    NewMutationPermit(),
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
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
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

func TestCurrentNodeControllerDoesNotMakeUnassignedApprovalTargetResumable(t *testing.T) {
	target := currentNodeReferenceForControllerTest(t, "task-approval-steer-failure", "node-target")
	approval := workflow.PendingApproval{
		ID:     workflow.NewApprovalID(),
		Source: currentNodeReferenceForControllerTest(t, "task-approval-steer-failure", "node-source"),
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
	cause := errors.New("assignment append failed")
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{err: cause},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); !errors.Is(err, cause) {
		t.Fatalf("ApplyPendingApproval error = %v, want %v", err, cause)
	}
	time.Sleep(50 * time.Millisecond)
	if starts := runner.starts(); starts != 0 {
		t.Fatalf("runner starts = %d, want none after steering failure", starts)
	}
	if interruption, interrupted := store.interruption(target); interrupted {
		t.Fatalf("unassigned approval target was made resumable: %+v", interruption)
	}
}

func TestCompleteIdleCurrentNodeRecoversSuccessorByAssignmentCommit(t *testing.T) {
	cause := errors.New("assignment persistence failed")
	for _, tc := range []struct {
		name            string
		outcomes        []currentNodeAssignmentSteerOutcome
		wantInterrupted []bool
	}{
		{
			name: "uncommitted assignment",
			outcomes: []currentNodeAssignmentSteerOutcome{{
				waitErr: cause,
			}},
			wantInterrupted: []bool{false},
		},
		{
			name: "committed assignment with observer failure",
			outcomes: []currentNodeAssignmentSteerOutcome{{
				receipt: session.CommitReceipt{Committed: true},
				waitErr: cause,
			}},
			wantInterrupted: []bool{true},
		},
		{
			name: "later fanout assignment preparation failure",
			outcomes: []currentNodeAssignmentSteerOutcome{
				{receipt: session.CommitReceipt{Committed: true}},
				{steerErr: cause},
			},
			wantInterrupted: []bool{true, false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := currentNodeReferenceForControllerTest(t, "task-idle-completion-steer-failure", "node-source")
			targets := []workflowstore.CurrentNodeAutomaticIntent{
				{CurrentNode: currentNodeReferenceForControllerTest(t, "task-idle-completion-steer-failure", "node-target-a"), NodeKind: workflow.NodeKindAgent},
				{CurrentNode: currentNodeReferenceForControllerTest(t, "task-idle-completion-steer-failure", "node-target-b"), NodeKind: workflow.NodeKindAgent},
			}
			targets = targets[:len(tc.wantInterrupted)]
			sourceNode := workflow.CurrentNode{
				Reference:  source,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}
			store := &currentNodeControllerStore{
				idleResolved: &sourceNode,
				completion: workflowstore.CurrentNodeCompletionResult{
					AutomaticIntents: targets,
				},
			}
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			runner := &countingCurrentNodeRunner{}
			controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
				AgentConcurrency: 1,
				AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{
					outcomes: tc.outcomes,
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
			if _, err := controller.CompleteIdleCurrentNode(
				context.Background(),
				workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
				"next",
				nil,
				"forced completion",
			); !errors.Is(err, cause) {
				t.Fatalf("CompleteIdleCurrentNode error = %v, want %v", err, cause)
			}
			if starts := runner.starts(); starts != 0 {
				t.Fatalf("runner starts = %d, want none after assignment failure", starts)
			}
			for index, intent := range targets {
				target := intent.CurrentNode
				interruption, interrupted := store.interruption(target)
				if interrupted != tc.wantInterrupted[index] {
					t.Fatalf(
						"forced-completion successor %v interrupted = %t (%+v), want %t",
						target,
						interrupted,
						interruption,
						tc.wantInterrupted[index],
					)
				}
				if interrupted &&
					(interruption.reason != reasonCurrentNodeRuntimeStartFailed ||
						interruption.detail.Code != string(reasonCurrentNodeRuntimeStartFailed)) {
					t.Fatalf("forced-completion successor %v interruption = %+v, want runtime start failure", target, interruption)
				}
			}
		})
	}
}

func TestCompleteIdleCurrentNodeRetainsLateAssignmentAfterCallerCancellation(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-idle-completion-late-assignment", "node-source")
	target := currentNodeReferenceForControllerTest(t, "task-idle-completion-late-assignment", "node-target")
	sourceNode := workflow.CurrentNode{
		Reference:  source,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
	}
	store := &currentNodeControllerStore{
		idleResolved: &sourceNode,
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: target, NodeKind: workflow.NodeKindAgent}},
		},
	}
	release := make(chan struct{})
	waitStarted := make(chan struct{})
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentSteerer: lateCommitCurrentNodeAssignmentSteerer{
			release: release,
			started: waitStarted,
		},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskID := source.TaskID
	completed := make(chan error, 1)
	go func() {
		_, completeErr := controller.CompleteIdleCurrentNode(
			ctx,
			workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
			"next",
			nil,
			"forced completion",
		)
		completed <- completeErr
	}()
	select {
	case <-waitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("assignment wait did not start")
	}
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CompleteIdleCurrentNode error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CompleteIdleCurrentNode did not return after caller cancellation")
	}
	select {
	case started := <-runner.started:
		t.Fatalf("runner started %v before late assignment commit", started)
	default:
	}

	close(release)
	select {
	case started := <-runner.started:
		if !started.Equal(target) {
			t.Fatalf("runner started %v after late assignment commit, want %v", started, target)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late-assigned successor did not start")
	}
	if interruption, interrupted := store.interruption(target); interrupted {
		t.Fatalf("late-assigned successor was interrupted: %+v", interruption)
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
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshot := controller.Snapshot()
		return hasLiveCurrentNode(snapshot, source) && hasAutomaticCurrentNodeIntent(snapshot, queuedAgent)
	}, "approval source did not hold Agent capacity while queued Agent remained queued")
	sourceScope := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		TransitionID: "review",
	}); err != nil {
		t.Fatalf("complete approval source: %v", err)
	}
	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("ApplyPendingApproval: %v", err)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.HeldIntents) != 1 ||
		!snapshot.HeldIntents[0].CurrentNode.Equal(target) ||
		snapshot.HeldIntents[0].Automatic {
		t.Fatalf("held approval starts = %+v, want explicit target held by source scope", snapshot.HeldIntents)
	}
	if !hasAutomaticCurrentNodeIntent(snapshot, queuedAgent) {
		t.Fatalf("automatic agent queue = %+v, want queued agent while source occupies capacity", snapshot.AutomaticIntents)
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
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		for _, live := range controller.Snapshot().LiveScopes {
			if live.CurrentNode.Equal(target) {
				return !live.Automatic
			}
		}
		return false
	}, "approval target did not enter an explicit live scope")
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasLiveCurrentNode(controller.Snapshot(), queuedAgent)
	}, "queued Agent did not enter a live automatic scope")
}

func TestCurrentNodeControllerHoldsSuccessorUntilSourceScopeRetires(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-successor", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-successor", "node-successor")
	sessionID := runtimeids.NewSessionID()
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}},
			SessionReuse: &workflow.SessionReuseAnalysisInput{
				CompletedCurrentNode: workflow.CurrentNode{
					Reference: source,
					SessionID: &sessionID,
				},
			},
			PostCompletionEligible: true,
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
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 4),
	}
	assignmentDeadline := make(chan time.Time, 1)
	controller, err = NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentSteerer: deadlineRecordingCurrentNodeAssignmentSteerer{
			reference: successor,
			deadline:  assignmentDeadline,
		},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start source: %v", err)
	}
	if got := <-runner.started; !got.Equal(source) {
		t.Fatalf("first started current node = %v, want source %v", got, source)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasLiveCurrentNode(controller.Snapshot(), source)
	}, "source did not become live")
	sourceScope := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.HeldIntents) != 1 || !snapshot.HeldIntents[0].CurrentNode.Equal(successor) {
		t.Fatalf("held intents = %+v, want successor held by source retirement", snapshot.HeldIntents)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("successor %v started before source retirement", started)
	case <-time.After(50 * time.Millisecond):
	}
	if err := controller.FinalizeCurrentNodePostTurn(context.Background(), sourceScope, sessionID, workflowruntime.PostCompletionRuntime{
		CompactionMode: "none",
	}); err != nil {
		t.Fatalf("finalize source post-turn: %v", err)
	}
	if snapshot := controller.Snapshot(); len(snapshot.HeldIntents) != 1 ||
		!snapshot.HeldIntents[0].CurrentNode.Equal(successor) {
		t.Fatalf("post-finalization held intents = %+v, want successor held until source retirement", snapshot.HeldIntents)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("successor %v started before source retirement after finalization", started)
	case <-time.After(50 * time.Millisecond):
	}

	sourceHandle, ok := authority.ExecutionByScope(sourceScope)
	if !ok {
		t.Fatal("source scope disappeared before it was stopped")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop source: %v", err)
	}
	select {
	case deadline := <-assignmentDeadline:
		remaining := time.Until(deadline)
		if remaining < interruptCleanupTimeout-time.Second || remaining > interruptCleanupTimeout {
			t.Fatalf("finalization assignment wait deadline remaining = %s, want %s", remaining, interruptCleanupTimeout)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("finalization did not wait for successor assignment with a deadline")
	}
	select {
	case started := <-runner.started:
		if !started.Equal(successor) {
			t.Fatalf("started successor = %v, want %v", started, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successor did not start after source retirement")
	}
}

func TestCurrentNodeControllerCompletionAndTaskInterruptDoNotDeadlockOrReleaseSuccessor(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-complete-interrupt", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-complete-interrupt", "node-successor")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}},
		},
		completionStarted: make(chan struct{}),
		completionRelease: make(chan struct{}),
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

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start source: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	sourceScope := singleLiveScope(t, controller, source)
	completionDone := make(chan error, 1)
	go func() {
		_, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
			ScopeID:      sourceScope,
			TransitionID: "next",
		})
		completionDone <- err
	}()
	select {
	case <-store.completionStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("completion did not enter its exact-scope mutation")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: source.TaskID})
	}()
	select {
	case err := <-interruptDone:
		t.Fatalf("interrupt escaped completion linearization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(store.completionRelease)
	select {
	case err := <-completionDone:
		if err != nil {
			t.Fatalf("complete source: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion and interrupt deadlocked")
	}
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("task interrupt: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt did not finish after completion released")
	}
	select {
	case started := <-runner.started:
		t.Fatalf("successor %v started after Task Interrupt", started)
	case <-time.After(100 * time.Millisecond):
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
