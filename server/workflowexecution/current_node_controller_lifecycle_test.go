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

func TestCurrentNodeControllerInterruptsUnassignedApprovalTarget(t *testing.T) {
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
	if interruption, interrupted := store.interruption(target); !interrupted {
		t.Fatal("unassigned approval target was not interrupted")
	} else if interruption.reason != reasonCurrentNodeRuntimeStartFailed {
		t.Fatalf("approval target interruption = %+v, want runtime start failure", interruption)
	}
}

func TestCompleteIdleCurrentNodeInterruptsOnlyFailedAgentAndStartsHealthyScriptSibling(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	cause := errors.New("assignment persistence failed")
	for _, test := range []struct {
		name    string
		outcome currentNodeAssignmentSteerOutcome
	}{
		{
			name:    "uncommitted assignment",
			outcome: currentNodeAssignmentSteerOutcome{waitErr: cause},
		},
		{
			name:    "assignment preparation failure",
			outcome: currentNodeAssignmentSteerOutcome{steerErr: cause},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := currentNodeReferenceForControllerTest(t, "task-idle-completion-independent-failure", "node-source")
			agent := currentNodeReferenceForControllerTest(t, "task-idle-completion-independent-failure", "node-agent")
			script := currentNodeReferenceForControllerTest(t, "task-idle-completion-independent-failure", "node-script")
			sourceNode := workflow.CurrentNode{
				Reference:  source,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}
			expected := workflowstore.CurrentNodeCompletionResult{
				AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
					{CurrentNode: agent, NodeKind: workflow.NodeKindAgent},
					{CurrentNode: script, NodeKind: workflow.NodeKindScript},
				},
			}
			store := &currentNodeControllerStore{
				idleResolved: &sourceNode,
				completion:   expected,
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
			controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
				AgentConcurrency: 1,
				AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{
					outcomes: []currentNodeAssignmentSteerOutcome{
						test.outcome,
						{receipt: session.CommitReceipt{Committed: true}},
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
			completed, completeErr := controller.CompleteIdleCurrentNode(
				context.Background(),
				workflowstore.IdleCurrentNodeSelector{TaskID: &taskID},
				"next",
				nil,
				"forced completion",
			)
			if !errors.Is(completeErr, cause) {
				t.Fatalf("CompleteIdleCurrentNode error = %v, want %v", completeErr, cause)
			}
			if len(completed.AutomaticIntents) != len(expected.AutomaticIntents) {
				t.Fatalf("committed Automatic Intents = %+v, want %+v", completed.AutomaticIntents, expected.AutomaticIntents)
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
		})
	}
}

func TestCompleteIdleCurrentNodeClassifiesTransferredAssignmentsIndependently(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	diagnostic := errors.New("late assignment diagnostic")
	for _, test := range []struct {
		name             string
		receipt          session.CommitReceipt
		eventualErr      error
		wantAgentStarted bool
	}{
		{
			name:             "committed with diagnostic",
			receipt:          session.CommitReceipt{Committed: true},
			eventualErr:      diagnostic,
			wantAgentStarted: true,
		},
		{
			name:        "uncommitted with error",
			eventualErr: diagnostic,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := currentNodeReferenceForControllerTest(t, "task-idle-completion-transferred", "node-source")
			agent := currentNodeReferenceForControllerTest(t, "task-idle-completion-transferred", "node-agent")
			healthyAgent := currentNodeReferenceForControllerTest(t, "task-idle-completion-transferred", "node-healthy-agent")
			sourceNode := workflow.CurrentNode{
				Reference:  source,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}
			store := &currentNodeControllerStore{
				idleResolved: &sourceNode,
				completion: workflowstore.CurrentNodeCompletionResult{
					AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
						{CurrentNode: agent, NodeKind: workflow.NodeKindAgent},
						{CurrentNode: healthyAgent, NodeKind: workflow.NodeKindAgent},
					},
				},
			}
			release := make(chan struct{})
			waitStarted := make(chan struct{})
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
			controller, err = NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
				AgentConcurrency: 1,
				AssignmentSteerer: &lateFirstCurrentNodeAssignmentSteerer{
					release: release,
					started: waitStarted,
					receipt: test.receipt,
					err:     test.eventualErr,
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
			case completeErr := <-completed:
				if !errors.Is(completeErr, context.Canceled) {
					t.Fatalf("CompleteIdleCurrentNode error = %v, want context canceled", completeErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("CompleteIdleCurrentNode did not return after caller cancellation")
			}
			select {
			case started := <-runner.started:
				if !started.Equal(healthyAgent) {
					t.Fatalf("first successor started = %v, want healthy Agent %v", started, healthyAgent)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("healthy Agent sibling was stranded behind unresolved assignment")
			}
			waitForRunningCurrentNode(t, authority, healthyAgent)
			if interruption, interrupted := store.interruption(healthyAgent); interrupted {
				t.Fatalf("healthy Agent was interrupted before capacity handoff: %+v", interruption)
			}
			close(release)
			if test.wantAgentStarted {
				select {
				case started := <-runner.started:
					t.Fatalf("transferred Agent started while healthy sibling owned capacity: %v", started)
				case <-time.After(100 * time.Millisecond):
				}
				healthyScope := singleLiveScope(t, authority, healthyAgent)
				healthyHandle, live := authority.ExecutionByScope(healthyScope)
				if !live {
					t.Fatal("healthy Agent scope retired before stop")
				}
				if err := healthyHandle.Stop(context.Background()); err != nil {
					t.Fatalf("stop healthy Agent: %v", err)
				}
				select {
				case started := <-runner.started:
					if !started.Equal(agent) {
						t.Fatalf("successor after capacity release = %v, want transferred Agent %v", started, agent)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("transferred Agent did not start after capacity became available")
				}
			} else {
				testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
					_, interrupted := store.interruption(agent)
					return interrupted
				}, "uncommitted transferred Agent was not interrupted")
			}
			_, agentInterrupted := store.interruption(agent)
			if agentInterrupted == test.wantAgentStarted {
				t.Fatalf("Agent interrupted = %t, want %t", agentInterrupted, !test.wantAgentStarted)
			}
			if interruption, interrupted := store.interruption(healthyAgent); interrupted && !test.wantAgentStarted {
				t.Fatalf("healthy Agent was interrupted: %+v", interruption)
			}
		})
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

func TestTaskInterruptDispositionsTransferredSuccessorBeforeLateAssignmentDelivery(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-late-assignment-interrupt", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-late-assignment-interrupt", "node-successor")
	releaseAssignment := make(chan struct{})
	assignmentStarted := make(chan struct{})
	assignmentResumed := make(chan struct{})
	interruptStarted := make(chan struct{})
	interruptRelease := make(chan struct{})
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source},
			},
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
				CurrentNode: successor,
				NodeKind:    workflow.NodeKindAgent,
			}},
		},
		interruptStarted: interruptStarted,
		interruptRelease: interruptRelease,
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
	controller, err = NewCurrentNodeController(
		store,
		runner,
		authority,
		NewMutationPermit(),
		CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseAssignment:
		default:
			close(releaseAssignment)
		}
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start source: %v", err)
	}
	if started := <-runner.started; !started.Equal(source) {
		t.Fatalf("started Current Node = %v, want source %v", started, source)
	}
	waitForRunningCurrentNode(t, authority, source)
	controller.steerer = lateCommitCurrentNodeAssignmentSteerer{
		release: releaseAssignment,
		started: assignmentStarted,
		resumed: assignmentResumed,
	}

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
	select {
	case <-assignmentStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("successor assignment wait did not start")
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
	select {
	case <-assignmentResumed:
	case <-time.After(3 * time.Second):
		t.Fatal("transferred successor did not resume its assignment wait")
	}

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
	preflightCtx, cancelPreflight := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelPreflight()
	preflightErr := controller.EnsureTaskResumeEligible(
		preflightCtx,
		workflow.TaskID("task-unrelated-resume-preflight"),
	)
	if !errors.Is(preflightErr, context.DeadlineExceeded) {
		t.Fatalf("unrelated lifecycle mutation crossed the active interruption write: %v", preflightErr)
	}
	close(interruptRelease)
	select {
	case interruptErr := <-interruptDone:
		t.Fatalf("Task interrupt returned before transferred assignment resolved: %v", interruptErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseAssignment)
	if interruptErr := <-interruptDone; interruptErr != nil {
		t.Fatalf("Interrupt Task with transferred successor: %v", interruptErr)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(successor)
		return interrupted
	}, "transferred successor was not interrupted with its drained Task owner")
	select {
	case started := <-runner.started:
		t.Fatalf("transferred successor started after Task interrupt: %v", started)
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
