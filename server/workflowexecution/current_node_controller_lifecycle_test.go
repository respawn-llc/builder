package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestEnsureTaskQuiescentRejectsPublishedExactExecution(t *testing.T) {
	t.Run("Agent", func(t *testing.T) {
		fixture := newCurrentNodeQuestionFixture(t)
		reference := currentNodeReferenceForControllerTest(t, "task-live-agent", "node-agent")
		handle, _, _ := fixture.startAgentExecution(
			t,
			reference,
			func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
				<-ctx.Done()
				return context.Cause(ctx)
			},
		)
		t.Cleanup(func() {
			handle.RequestStop()
			if err := handle.Close(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("close live Agent execution: %v", err)
			}
		})
		waitForRunningCurrentNode(t, fixture.authority, reference)

		if err := fixture.controller.EnsureTaskQuiescent(reference.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("EnsureTaskQuiescent error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	})

	t.Run("Script", func(t *testing.T) {
		shellPath, err := exec.LookPath("sh")
		if err != nil {
			t.Skipf("sh executable unavailable: %v", err)
		}
		fixture := newCurrentNodeQuestionFixture(t)
		reference := currentNodeReferenceForControllerTest(t, "task-live-script", "node-script")
		handle := startLiveTestWorkflowScript(t, fixture.controller, fixture.authority, reference, sessionruntime.ScriptExecutionRequest{
			Command: sessionruntime.ScriptCommand{
				Path: shellPath,
				Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			},
		})
		t.Cleanup(func() {
			handle.RequestStop()
			if err := handle.Close(context.Background()); err != nil {
				t.Errorf("close live Script execution: %v", err)
			}
		})
		waitForRunningCurrentNode(t, fixture.authority, reference)

		if err := fixture.controller.EnsureTaskQuiescent(reference.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("EnsureTaskQuiescent error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	})

	t.Run("finalizing Script", func(t *testing.T) {
		shellPath, err := exec.LookPath("sh")
		if err != nil {
			t.Skipf("sh executable unavailable: %v", err)
		}
		fixture := newCurrentNodeQuestionFixture(t)
		reference := currentNodeReferenceForControllerTest(t, "task-finalizing-script", "node-script")
		finalizeEntered := make(chan struct{})
		releaseFinalize := make(chan struct{})
		handle := startLiveTestWorkflowScript(t, fixture.controller, fixture.authority, reference, sessionruntime.ScriptExecutionRequest{
			Command: sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", "exit 0"}},
			Finalize: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.ScriptResult, error) error {
				close(finalizeEntered)
				<-releaseFinalize
				return nil
			},
		})
		t.Cleanup(func() {
			close(releaseFinalize)
			if err := handle.Close(context.Background()); err != nil {
				t.Errorf("close finalizing Script execution: %v", err)
			}
		})
		select {
		case <-finalizeEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("Script execution did not enter finalization")
		}

		if err := fixture.controller.EnsureTaskQuiescent(reference.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("EnsureTaskQuiescent error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	})
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
	taskID := workflow.TaskID("task-reactivate-startup-failure")
	selectedBranch := workflow.TransitionBranchKey("selected")
	reference, err := workflow.NewCurrentNodeReference(
		taskID,
		workflow.NodeID("node-reactivate-startup-failure"),
		&selectedBranch,
	)
	if err != nil {
		t.Fatalf("new selected Current Node reference: %v", err)
	}
	siblingBranch := workflow.TransitionBranchKey("sibling")
	sibling, err := workflow.NewCurrentNodeReference(
		taskID,
		workflow.NodeID("node-reactivate-startup-failure"),
		&siblingBranch,
	)
	if err != nil {
		t.Fatalf("new sibling Current Node reference: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{
			{
				Reference:  reference,
				SessionID:  &sessionID,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
			},
			{
				Reference:  sibling,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
			},
		},
		resumeClassifications: []workflowstore.CurrentNodeResumeClassification{
			{CurrentNode: workflow.CurrentNode{
				Reference:  reference,
				SessionID:  &sessionID,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
			}},
			{
				CurrentNode: workflow.CurrentNode{
					Reference:  sibling,
					Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
				},
				Diagnostics: []workflowstore.CurrentNodeResumeValidationDiagnostic{{
					Code:           workflowstore.CurrentNodeResumeParameterNotMaterializedCode,
					CurrentNode:    sibling,
					EnteringEdgeID: workflow.EdgeID("edge-sibling"),
					ParameterKey:   "input",
				}},
			},
		},
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

	_, err = controller.ReactivateWorkflowSession(context.Background(), sessionID)
	if !errors.Is(err, cause) {
		t.Fatalf("ReactivateWorkflowSession error = %v, want startup failure %v", err, cause)
	}
	if calls := store.interruptionCount(reference); calls != 1 {
		t.Fatalf("startup-failure interruption writes = %d, want 1", calls)
	}
	if calls := store.interruptionCount(sibling); calls != 0 {
		t.Fatalf("sibling startup-failure interruption writes = %d, want 0", calls)
	}
	if len(store.resumed) != 1 || !store.resumed[0].Equal(reference) {
		t.Fatalf("ResumeCurrentNode mutations = %+v, want only selected %v", store.resumed, reference)
	}
}

func TestReactivateWorkflowSessionReturnsOwnQueuedStartHandle(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	taskID := workflow.TaskID("task-reactivate-own-start")
	selectedBranch := workflow.TransitionBranchKey("selected")
	selected, err := workflow.NewCurrentNodeReference(
		taskID,
		workflow.NodeID("node-reactivate-own-start"),
		&selectedBranch,
	)
	if err != nil {
		t.Fatalf("new selected Current Node reference: %v", err)
	}
	siblingBranch := workflow.TransitionBranchKey("sibling")
	sibling, err := workflow.NewCurrentNodeReference(
		taskID,
		workflow.NodeID("node-reactivate-own-start"),
		&siblingBranch,
	)
	if err != nil {
		t.Fatalf("new sibling Current Node reference: %v", err)
	}
	initial, sessionID := fixture.startQuestionExecution(
		t,
		selected,
		func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	)
	attachment, err := fixture.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "retained-reactivation-test",
	})
	if err != nil {
		t.Fatalf("retain Session Runtime: %v", err)
	}
	initial.RequestStop()
	if err := initial.Close(context.Background()); err != nil {
		t.Fatalf("close initial Workflow execution: %v", err)
	}

	runner := retainedSessionReactivationPublicationRunner{
		authority: fixture.authority,
		sessionID: sessionID,
	}
	controller, err := NewCurrentNodeController(
		fixture.store,
		runner,
		fixture.authority,
		NewTaskMutationCoordinator(),
		CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("new reactivation controller: %v", err)
	}
	fixture.store.mu.Lock()
	fixture.store.interrupted = []workflow.CurrentNode{
		{
			Reference:  selected,
			SessionID:  &sessionID,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		},
		{
			Reference:  sibling,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		},
	}
	fixture.store.sessionTaskID = &taskID
	fixture.store.sessionAssociation = &workflowstore.TaskSessionAssociation{
		SessionID:   sessionID,
		CurrentNode: selected,
	}
	fixture.store.mu.Unlock()

	handle, err := controller.ReactivateWorkflowSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ReactivateWorkflowSession: %v", err)
	}
	t.Cleanup(func() {
		handle.RequestStop()
		if err := handle.Close(context.Background()); err != nil {
			t.Errorf("close reactivated Workflow execution: %v", err)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("close reactivation controller: %v", err)
		}
		if _, err := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); err != nil {
			t.Errorf("release retained Session Runtime: %v", err)
		}
	})
	resource, hasResource := handle.Scope().Resource()
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !hasResource ||
		resource.SessionID() != sessionID ||
		!workflowScoped ||
		!workflowRef.CurrentNode.Equal(selected) {
		t.Fatalf("reactivated execution scope = %+v, want Session %s Current Node %v", handle.Scope(), sessionID, selected)
	}
	fixture.store.mu.Lock()
	resumed := append([]workflow.CurrentNodeReference(nil), fixture.store.resumed...)
	admitted := append([]workflow.CurrentNodeReference(nil), fixture.store.admitted...)
	fixture.store.mu.Unlock()
	if len(resumed) != 1 || !resumed[0].Equal(selected) {
		t.Fatalf("ResumeCurrentNode mutations = %+v, want only selected %v", resumed, selected)
	}
	if len(admitted) != 1 || !admitted[0].Equal(selected) {
		t.Fatalf("AdmitCurrentNode mutations = %+v, want only selected %v", admitted, selected)
	}
	if _, live := fixture.authority.ExecutionByCurrentNode(
		"project-test",
		currentNodeControllerTestWorkflowID,
		sibling,
	); live {
		t.Fatalf("sibling Current Node %v has a live execution after exact reactivation", sibling)
	}
}

type retainedSessionReactivationPublicationRunner struct {
	authority *sessionruntime.Authority
	sessionID runtimeids.SessionID
}

func (retainedSessionReactivationPublicationRunner) PrepareScriptPublication(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.Controller,
) (CurrentNodeScriptPublication, error) {
	return nil, nil
}

func (r retainedSessionReactivationPublicationRunner) PrepareAgentPublication(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	onRetire func(),
	controller workflowruntime.Controller,
) (CurrentNodeAgentPublication, error) {
	descriptor, err := session.NewOpenSessionDescriptor(r.sessionID)
	if err != nil {
		return nil, err
	}
	return r.authority.PrepareDetachedAgentExecution(ctx, sessionruntime.DetachedAgentExecutionRequest{
		Descriptor: descriptor,
		Workflow: sessionruntime.WorkflowExecutionRef{
			ProjectID:   "project-test",
			WorkflowID:  currentNodeControllerTestWorkflowID,
			CurrentNode: reference,
		},
		Resource: sessionruntime.CurrentAgentResource{},
		Config: &workflowruntime.CurrentNodeExecutionConfig{
			Contract:       workflowruntime.CompletionContract{Transitions: []workflowruntime.CompletionTransition{{ID: "next"}}},
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     controller,
			Instructions: workflowruntime.TaskInstructions{
				WorkflowID:  currentNodeControllerTestWorkflowID,
				CurrentNode: reference,
			},
		},
		Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
		OnRetire: onRetire,
	})
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
		_, err := controller.ReactivateWorkflowSession(context.Background(), sessionID)
		reactivated <- err
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

func TestReactivateWorkflowSessionReturnsAlreadyPublishedWorkflowExecution(t *testing.T) {
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

	reactivated, err := fixture.controller.ReactivateWorkflowSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ReactivateWorkflowSession: %v", err)
	}
	if reactivated != handle {
		t.Fatalf("reactivated execution = %v, want already-published handle %v", reactivated, handle)
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
