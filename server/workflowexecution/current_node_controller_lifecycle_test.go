package workflowexecution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
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
	fixture.store.interrupted = []workflow.CurrentNode{{
		Reference: reference,
		SessionID: &sessionID,
	}}

	resumed, err := fixture.controller.ResumeTask(context.Background(), reference.TaskID)
	if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("resume error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	if len(resumed.CurrentNodes) != 0 {
		t.Fatalf("resumed Current Nodes = %+v, want none", resumed)
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	if len(fixture.store.resumed) != 0 {
		t.Fatalf("resume mutations = %+v, want none before conflict", fixture.store.resumed)
	}
}

func TestReactivateWorkflowSessionReturnsAdmissionFailure(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(
		t,
		"task-reactivate-startup-failure",
		"node-reactivate-startup-failure",
	)
	sessionID := runtimeids.NewSessionID()
	taskID := reference.TaskID
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference:  reference,
			SessionID:  &sessionID,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		}},
		sessionTaskID: &taskID,
		sessionAssociation: &workflowstore.TaskSessionAssociation{
			SessionID:   sessionID,
			CurrentNode: reference,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	cause := errors.New("prepare resumed Workflow runtime")
	controller := newCurrentNodeControllerForTest(
		t,
		store,
		failingCurrentNodeRunner{cause: cause},
		authority,
		1,
	)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	err := controller.ReactivateWorkflowSession(context.Background(), sessionID)
	if !errors.Is(err, cause) {
		t.Fatalf("ReactivateWorkflowSession error = %v, want startup failure %v", err, cause)
	}
	if calls := store.interruptionCount(reference); calls != 1 {
		t.Fatalf("startup-failure interruption writes = %d, want 1", calls)
	}
}

func TestReactivateWorkflowSessionJoinsConcurrentExplicitResumeAdmission(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(
		t,
		"task-reactivate-concurrent-resume",
		"node-reactivate-concurrent-resume",
	)
	sessionID := runtimeids.NewSessionID()
	taskID := reference.TaskID
	interrupted := workflow.CurrentNode{
		Reference:  reference,
		SessionID:  &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
	}
	store := &currentNodeControllerStore{
		interrupted:   []workflow.CurrentNode{interrupted},
		sessionTaskID: &taskID,
		sessionAssociation: &workflowstore.TaskSessionAssociation{
			SessionID:   sessionID,
			CurrentNode: reference,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	admissionFailure := errors.New("blocked current node setup released")
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		cause:   admissionFailure,
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.ResumeTask(context.Background(), taskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit Resume admission did not begin")
	}
	store.mu.Lock()
	store.interrupted = nil
	store.currentNodes = []workflow.CurrentNode{{
		Reference:  reference,
		SessionID:  &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
	}}
	store.mu.Unlock()

	reactivated := make(chan error, 1)
	go func() {
		reactivated <- controller.ReactivateWorkflowSession(context.Background(), sessionID)
	}()
	select {
	case err := <-reactivated:
		t.Fatalf("reactivation returned before concurrent Resume admission completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(runner.release)
	err := <-reactivated
	if !errors.Is(err, admissionFailure) {
		t.Fatalf("ReactivateWorkflowSession error = %v, want joined admission failure", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.resumed) != 1 {
		t.Fatalf("ResumeCurrentNode mutations = %d, want one shared Resume path", len(store.resumed))
	}
}

func TestReactivateWorkflowSessionJoinsAlreadyPublishedWorkflowExecution(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(
		t,
		"task-reactivate-published-resume",
		"node-reactivate-published-resume",
	)
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
			t.Errorf("close published Workflow execution: %v", err)
		}
	})
	taskID := reference.TaskID
	fixture.store.mu.Lock()
	fixture.store.sessionTaskID = &taskID
	fixture.store.sessionAssociation = &workflowstore.TaskSessionAssociation{
		SessionID:   sessionID,
		CurrentNode: reference,
	}
	fixture.store.currentNodes = []workflow.CurrentNode{{
		Reference:  reference,
		SessionID:  &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingAdmitted},
	}}
	fixture.store.mu.Unlock()

	if err := fixture.controller.ReactivateWorkflowSession(context.Background(), sessionID); err != nil {
		t.Fatalf("ReactivateWorkflowSession: %v", err)
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped || !workflowRef.CurrentNode.Equal(reference) {
		t.Fatalf("published execution scope = %+v, want Workflow Current Node %v", handle.Scope(), reference)
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	if len(fixture.store.resumed) != 0 {
		t.Fatalf("ResumeCurrentNode mutations = %d, want published Resume no-op", len(fixture.store.resumed))
	}
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

func TestCurrentNodeControllerAgentCompletionRequiresMatchingActiveProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(workflowruntime.AgentCompletionProvenance) workflowruntime.AgentCompletionProvenance
		wantOK bool
	}{
		{name: "matching", mutate: func(value workflowruntime.AgentCompletionProvenance) workflowruntime.AgentCompletionProvenance {
			return value
		}, wantOK: true},
		{name: "stale scope", mutate: func(value workflowruntime.AgentCompletionProvenance) workflowruntime.AgentCompletionProvenance {
			value.ScopeID = runtimeids.NewExecutionScopeID()
			return value
		}},
		{name: "stale run", mutate: func(value workflowruntime.AgentCompletionProvenance) workflowruntime.AgentCompletionProvenance {
			value.RunID = mustAgentCompletionRunID(t, "11111111-1111-4111-8111-111111111111")
			return value
		}},
		{name: "stale step", mutate: func(value workflowruntime.AgentCompletionProvenance) workflowruntime.AgentCompletionProvenance {
			value.StepID = mustAgentCompletionStepID(t, "22222222-2222-4222-8222-222222222222")
			return value
		}},
		{name: "duplicate", mutate: func(value workflowruntime.AgentCompletionProvenance) workflowruntime.AgentCompletionProvenance {
			return value
		}, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCurrentNodeQuestionFixture(t)
			source := currentNodeReferenceForControllerTest(t, "task-agent-completion-"+test.name, "node-source")
			release := make(chan struct{})
			start := make(chan struct{})
			completionResult := make(chan error, 1)
			var releaseOnce sync.Once
			var generateOnce sync.Once
			var (
				scopeID   runtimeids.ExecutionScopeID
				sessionID runtimeids.SessionID
				bridge    sessionruntime.AgentRuntimeBridge
			)
			client := callbackCurrentNodeLLMClient{generate: func(context.Context, llm.Request) (llm.Response, error) {
				firstGenerate := false
				generateOnce.Do(func() { firstGenerate = true })
				if !firstGenerate {
					return llm.Response{}, context.Canceled
				}
				var active *runtime.RunSnapshot
				if err := bridge.WithEngine(context.Background(), func(_ context.Context, engine *runtime.Engine) error {
					active = engine.ActiveRun()
					return nil
				}); err != nil {
					releaseOnce.Do(func() { close(release) })
					return llm.Response{}, err
				}
				if active == nil {
					releaseOnce.Do(func() { close(release) })
					return llm.Response{}, errors.New("Agent completion test has no active Run")
				}
				provenance := test.mutate(workflowruntime.AgentCompletionProvenance{
					ScopeID: scopeID,
					RunID:   mustAgentCompletionRunID(t, active.RunID),
					StepID:  mustAgentCompletionStepID(t, active.StepID),
				})
				_, err := fixture.controller.CompleteAgentCurrentNode(context.Background(), workflowruntime.AgentCompletionRequest{
					Provenance:   provenance,
					SessionID:    sessionID,
					TransitionID: "next",
				})
				if test.name == "duplicate" && err == nil {
					_, err = fixture.controller.CompleteAgentCurrentNode(context.Background(), workflowruntime.AgentCompletionRequest{
						Provenance:   provenance,
						SessionID:    sessionID,
						TransitionID: "next",
					})
					if err == nil {
						err = errors.New("duplicate Agent completion unexpectedly succeeded")
					}
				}
				select {
				case completionResult <- err:
				default:
				}
				releaseOnce.Do(func() { close(release) })
				if err != nil && test.name != "duplicate" {
					return llm.Response{}, context.Canceled
				}
				return llm.Response{}, nil
			}}
			handle, assignedSessionID, _ := fixture.startAgentExecutionWithClient(
				t,
				source,
				client,
				func(ctx context.Context, scope sessionruntime.ExecutionScope, runtimeBridge sessionruntime.AgentRuntimeBridge) error {
					<-start
					scopeID = scope.ID()
					bridge = runtimeBridge
					err := bridge.WithEngine(ctx, func(ctx context.Context, engine *runtime.Engine) error {
						_, submitErr := engine.SubmitWorkflowTurn(ctx)
						return submitErr
					})
					<-release
					return err
				},
			)
			sessionID = assignedSessionID
			close(start)
			err := func() error {
				_, waitErr := handle.Wait(context.Background())
				return waitErr
			}()
			completionErr := <-completionResult
			if test.wantOK {
				if err != nil {
					t.Fatalf("matching Agent completion: %v", err)
				}
				if test.name == "duplicate" && completionErr == nil {
					t.Fatal("duplicate Agent completion unexpectedly succeeded")
				}
				if calls := fixture.store.completionCount(); calls != 1 {
					t.Fatalf("completion calls = %d, want 1", calls)
				}
				return
			}
			if completionErr == nil {
				t.Fatal("stale Agent completion unexpectedly succeeded")
			}
			if calls := fixture.store.completionCount(); calls != 0 {
				t.Fatalf("completion calls = %d, want 0", calls)
			}
		})
	}
}

func mustAgentCompletionRunID(t *testing.T, raw string) runtimeids.RunID {
	t.Helper()
	value, err := runtimeids.ParseRunID(raw)
	if err != nil {
		t.Fatalf("parse Run ID: %v", err)
	}
	return value
}

func mustAgentCompletionStepID(t *testing.T, raw string) runtimeids.StepID {
	t.Helper()
	value, err := runtimeids.ParseStepID(raw)
	if err != nil {
		t.Fatalf("parse Step ID: %v", err)
	}
	return value
}

func TestCurrentNodeControllerRejectsProtocolViolationsAfterScopeRetires(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	store := &currentNodeControllerStore{}
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

	if _, err := controller.RecordProtocolViolation(context.Background(), request); !errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		t.Fatalf("record retired-scope protocol violation error = %v, want %v", err, sessionruntime.ErrExecutionNoLongerLive)
	}
	if err := controller.ResetProtocolViolationBudget(context.Background(), workflowruntime.ViolationResetRequest{
		ScopeID:   request.ScopeID,
		SessionID: &sessionID,
	}); !errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		t.Fatalf("reset retired-scope protocol violation error = %v, want %v", err, sessionruntime.ErrExecutionNoLongerLive)
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
	controller := newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: steerer,
	})
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
	controller := newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{err: cause},
	})
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
