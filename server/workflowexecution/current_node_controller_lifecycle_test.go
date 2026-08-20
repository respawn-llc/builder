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
	delete(fixture.controller.operations, key)
	fixture.controller.mu.Unlock()
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

func TestPostTurnCompactionReleasesTaskMutationLaneWhileApprovalFenceIsActive(t *testing.T) {
	source := currentNodeReferenceForControllerTest(t, "task-post-turn-fence", "node-source")
	controller, operationRef, sessionID := newPostTurnFinalizationControllerForReferenceTest(
		t,
		source,
		workflow.SessionReuseGuaranteedCACReuse,
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	controller.store = &currentNodeControllerStore{
		pendingApproval: workflow.PendingApproval{Source: source},
	}

	finalizationDone := make(chan error, 1)
	go func() {
		_, err := controller.FinalizeCurrentNodePostTurn(context.Background(), operationRef, sessionID, workflowruntime.PostCompletionRuntime{
			CompactionMode: "local",
			Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
				close(entered)
				<-release
				return workflowruntime.PostCompletionCompactionResult{}
			},
		})
		finalizationDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("post-turn compaction did not start")
	}
	_, err := controller.ApplyPendingApproval(context.Background(), "approval")
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
			controller, operationRef, sessionID := newPostTurnFinalizationControllerForTest(
				t,
				workflow.SessionReuseClassification(test.classification),
			)
			compactions := 0
			settlement, err := controller.FinalizeCurrentNodePostTurn(context.Background(), operationRef, sessionID, workflowruntime.PostCompletionRuntime{
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
			if settlement.Kind != workflowruntime.PostTurnSettlementSucceeded {
				t.Fatalf("settlement = %+v, want succeeded", settlement)
			}
			if compactions != test.wantCompactions {
				t.Fatalf("compactions = %d, want %d", compactions, test.wantCompactions)
			}
			if postTurnFinalizationPendingForTest(t, controller, operationRef) {
				t.Fatal("post-turn finalization fence remained after finalization")
			}
		})
	}
}

func TestPostTurnFinalizationSurfacesInvalidThresholdAndCancellation(t *testing.T) {
	t.Run("invalid threshold", func(t *testing.T) {
		controller, operationRef, sessionID := newPostTurnFinalizationControllerForTest(
			t,
			workflow.SessionReuseThresholdPossibleReuse,
		)
		settlement, err := controller.FinalizeCurrentNodePostTurn(context.Background(), operationRef, sessionID, workflowruntime.PostCompletionRuntime{
			CompactionMode: "local",
		})
		if err != nil {
			t.Fatalf("invalid pre-compaction threshold: %v", err)
		}
		if settlement.Kind != workflowruntime.PostTurnSettlementAborted || settlement.Diagnostic == nil {
			t.Fatalf("invalid threshold settlement = %+v, want aborted diagnostic", settlement)
		}
		if postTurnFinalizationPendingForTest(t, controller, operationRef) {
			t.Fatal("invalid threshold left the finalization fence")
		}
	})

	t.Run("cancellation settles as aborted", func(t *testing.T) {
		controller, operationRef, sessionID := newPostTurnFinalizationControllerForTest(
			t,
			workflow.SessionReuseGuaranteedCACReuse,
		)
		entered := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type finalizationResult struct {
			settlement workflowruntime.PostTurnSettlement
			err        error
		}
		done := make(chan finalizationResult, 1)
		go func() {
			settlement, err := controller.FinalizeCurrentNodePostTurn(ctx, operationRef, sessionID, workflowruntime.PostCompletionRuntime{
				CompactionMode: "local",
				Compact: func(compactionCtx context.Context) workflowruntime.PostCompletionCompactionResult {
					close(entered)
					<-compactionCtx.Done()
					return workflowruntime.PostCompletionCompactionResult{Diagnostic: context.Cause(compactionCtx)}
				},
			})
			done <- finalizationResult{settlement: settlement, err: err}
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("post-turn compaction did not start")
		}
		cancel()
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatalf("canceled finalization error = %v", result.err)
			}
			if result.settlement.Kind != workflowruntime.PostTurnSettlementAborted ||
				!errors.Is(result.settlement.Diagnostic, context.Canceled) {
				t.Fatalf("canceled finalization settlement = %+v, want aborted context cancellation", result.settlement)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled post-turn finalization did not return")
		}
	})
}

func TestPostTurnFinalizationReturnsCompletedDiagnosticSettlement(t *testing.T) {
	controller, operationRef, sessionID := newPostTurnFinalizationControllerForTest(
		t,
		workflow.SessionReuseGuaranteedCACReuse,
	)
	diagnostic := errors.New("compaction observer unavailable")
	settlement, err := controller.FinalizeCurrentNodePostTurn(
		context.Background(),
		operationRef,
		sessionID,
		workflowruntime.PostCompletionRuntime{
			CompactionMode: "local",
			Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
				return workflowruntime.PostCompletionCompactionResult{Diagnostic: diagnostic}
			},
		},
	)
	if err != nil {
		t.Fatalf("FinalizeCurrentNodePostTurn: %v", err)
	}
	if settlement.Kind != workflowruntime.PostTurnSettlementCompletedWithDiagnostic ||
		!errors.Is(settlement.Diagnostic, diagnostic) ||
		settlement.DiagnosticOwner != workflowruntime.DiagnosticOwnerAgentRunner {
		t.Fatalf("diagnostic settlement = %+v", settlement)
	}
	if postTurnFinalizationPendingForTest(t, controller, operationRef) {
		t.Fatal("diagnostic settlement left the finalization fence")
	}
}

func newPostTurnFinalizationControllerForTest(
	t *testing.T,
	classification workflow.SessionReuseClassification,
) (*CurrentNodeController, workflow.CurrentNodeOperationRef, runtimeids.SessionID) {
	t.Helper()
	source := currentNodeReferenceForControllerTest(t, "task-post-turn-matrix", "node-source")
	return newPostTurnFinalizationControllerForReferenceTest(t, source, classification)
}

func newPostTurnFinalizationControllerForReferenceTest(
	t *testing.T,
	source workflow.CurrentNodeReference,
	classification workflow.SessionReuseClassification,
) (*CurrentNodeController, workflow.CurrentNodeOperationRef, runtimeids.SessionID) {
	t.Helper()
	sessionID := runtimeids.NewSessionID()
	key, err := source.Key()
	if err != nil {
		t.Fatalf("source key: %v", err)
	}
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := &CurrentNodeController{
		mutations:  NewTaskMutationCoordinator(),
		authority:  authority,
		operations: make(map[workflow.CurrentNodeReferenceKey]*currentNodeOperation),
	}
	operationID := runtimeids.NewCurrentNodeOperationID()
	workflowRef := sessionruntime.WorkflowExecutionRef{
		ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID,
		OperationID: operationID, CurrentNode: source,
	}
	detached, err := authority.PrepareDetachedScriptExecution(context.Background(), sessionruntime.DetachedScriptExecutionRequest{
		Workflow: workflowRef,
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("prepare post-turn Script execution: %v", err)
	}
	handle, launch, err := detached.Publish(context.Background(), func() error { return nil }, nil)
	if err != nil {
		t.Fatalf("publish post-turn Script execution: %v", err)
	}
	launch()
	completion := workflowstore.CurrentNodeCompletionResult{PostCompletionEligible: true}
	phase := currentNodePostTurnFinalization{
		sessionID: &sessionID, classification: classification, reference: source,
	}
	controller.operations[key] = &currentNodeOperation{
		ref: workflowRef.Operation(), workflow: &workflowRef,
		completion: &completion, postTurnFinalization: &phase,
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = handle.Stop(stopCtx)
		_ = authority.Close(context.Background())
	})
	return controller, workflowRef.Operation(), sessionID
}

func postTurnFinalizationPendingForTest(
	t *testing.T,
	controller *CurrentNodeController,
	operationRef workflow.CurrentNodeOperationRef,
) bool {
	t.Helper()
	key, err := operationRef.CurrentNode.Key()
	if err != nil {
		t.Fatalf("post-turn Current Node key: %v", err)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	operation := controller.operations[key]
	return operation != nil &&
		operation.ref.OperationID == operationRef.OperationID &&
		operation.postTurnFinalization != nil
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
		WorkflowExecutionRetired: sessionruntime.WorkflowExecutionRetiredFunc(func(outcome sessionruntime.WorkflowRetirementOutcome) {
			controller.WorkflowExecutionRetired(outcome)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
		agents: map[workflow.CurrentNodeReference]struct{}{
			source:      {},
			queuedAgent: {},
			target:      {},
		},
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
	if _, err := completeCurrentNodeLifecycleForTest(
		context.Background(), controller, sourceScope, "review",
	); err != nil {
		t.Fatalf("complete approval source: %v", err)
	}
	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("ApplyPendingApproval before source retirement = %v, want %v", err, ErrTaskExecutionNotQuiescent)
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
	sourceOperation := workflowOperationForScopeForTest(t, authority, sourceScope)
	controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
		Operation: sourceOperation, Kind: sessionruntime.ExecutionScopeAgent,
		Disposition: sessionruntime.WorkflowRetirementCompleted,
	})
	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("ApplyPendingApproval after source retirement: %v", err)
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
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop approval source: %v", err)
	}
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
		completionDiagnostic: errors.New("completion event publication failed"),
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents:           []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}},
			SourceSessionID:            &sessionID,
			SessionReuseClassification: workflow.SessionReuseGuaranteedCACReuse,
			PostCompletionEligible:     true,
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		WorkflowExecutionRetired: sessionruntime.WorkflowExecutionRetiredFunc(func(outcome sessionruntime.WorkflowRetirementOutcome) {
			controller.WorkflowExecutionRetired(outcome)
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
	controller = newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentSteerer: deadlineRecordingCurrentNodeAssignmentSteerer{
			reference: successor,
			deadline:  assignmentDeadline,
		},
	})
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
		return hasLiveCurrentNode(authority, source)
	}, "source did not become live")
	sourceScope := singleLiveScope(t, authority, source)
	if _, err := completeCurrentNodeLifecycleForTest(
		context.Background(), controller, sourceScope, "next",
	); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	sourceKey, err := source.Key()
	if err != nil {
		t.Fatalf("source key: %v", err)
	}
	controller.mu.Lock()
	phase := controller.operations[sourceKey].postTurnFinalization
	controller.mu.Unlock()
	if phase == nil ||
		phase.sessionID == nil ||
		*phase.sessionID != sessionID ||
		phase.classification != workflow.SessionReuseGuaranteedCACReuse {
		t.Fatalf("prepared post-turn facts = %+v, want source Session and guaranteed CAC", phase)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("successor %v started before source retirement", started)
	case <-time.After(50 * time.Millisecond):
	}
	sourceOperation := workflowOperationForScopeForTest(t, authority, sourceScope)
	if _, err := controller.FinalizeCurrentNodePostTurn(context.Background(), sourceOperation, sessionID, workflowruntime.PostCompletionRuntime{
		CompactionMode: "none",
	}); err != nil {
		t.Fatalf("finalize source post-turn: %v", err)
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
	controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
		Operation:   sourceOperation,
		Kind:        sessionruntime.ExecutionScopeAgent,
		Disposition: sessionruntime.WorkflowRetirementCompleted,
	})
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
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop source: %v", err)
	}
	if interruption, interrupted := store.interruption(source); interrupted {
		t.Fatalf("committed diagnostic interrupted source: %+v", interruption)
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
		WorkflowExecutionRetired: sessionruntime.WorkflowExecutionRetiredFunc(func(outcome sessionruntime.WorkflowRetirementOutcome) {
			controller.WorkflowExecutionRetired(outcome)
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
	sourceScope := singleLiveScope(t, authority, source)
	completionDone := make(chan error, 1)
	go func() {
		_, err := completeCurrentNodeLifecycleForTest(
			context.Background(), controller, sourceScope, "next",
		)
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
		WorkflowExecutionRetired: sessionruntime.WorkflowExecutionRetiredFunc(func(outcome sessionruntime.WorkflowRetirementOutcome) {
			controller.WorkflowExecutionRetired(outcome)
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

func TestCurrentNodeControllerSettlesNoOptimizationCompletionAfterEarlierRetirement(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-script-retirement-first", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-script-retirement-first", "node-successor")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents:       []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindScript}},
			PostCompletionEligible: false,
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		WorkflowExecutionRetired: sessionruntime.WorkflowExecutionRetiredFunc(func(outcome sessionruntime.WorkflowRetirementOutcome) {
			controller.WorkflowExecutionRetired(outcome)
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
		t.Fatalf("start Script current node: %v", err)
	}
	if started := <-runner.started; !started.Equal(source) {
		t.Fatalf("started source = %v, want %v", started, source)
	}
	waitForRunningCurrentNode(t, authority, source)
	sourceScope := singleLiveScope(t, authority, source)
	sourceOperation := workflowOperationForScopeForTest(t, authority, sourceScope)
	controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
		Operation:   sourceOperation,
		Kind:        sessionruntime.ExecutionScopeScript,
		Disposition: sessionruntime.WorkflowRetirementCompleted,
	})
	if _, err := completeCurrentNodeLifecycleForTest(
		context.Background(),
		controller,
		sourceScope,
		"next",
	); err != nil {
		t.Fatalf("complete retired Script source: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(successor) {
			t.Fatalf("started successor = %v, want %v", started, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retirement-first no-optimization completion did not release its successor")
	}
	sourceKey, err := source.Key()
	if err != nil {
		t.Fatalf("source key: %v", err)
	}
	controller.mu.Lock()
	retained := controller.operations[sourceKey]
	controller.mu.Unlock()
	if retained != nil {
		t.Fatalf("retirement-first no-optimization operation retained: %+v", retained)
	}
}

func TestCurrentNodeControllerSettlesNoOptimizationPendingApprovalInEitherOrder(t *testing.T) {
	for _, retirementFirst := range []bool{true, false} {
		order := "completion_before_retirement"
		if retirementFirst {
			order = "retirement_before_completion"
		}
		t.Run(order, func(t *testing.T) {
			shellPath, err := exec.LookPath("sh")
			if err != nil {
				t.Skipf("sh executable unavailable: %v", err)
			}
			source := currentNodeReferenceForControllerTest(t, "task-script-approval-"+order, "node-source")
			approval := workflow.PendingApproval{ID: workflow.NewApprovalID(), Source: source}
			store := &currentNodeControllerStore{
				completion: workflowstore.CurrentNodeCompletionResult{
					PendingApproval:        &approval,
					PostCompletionEligible: false,
				},
			}
			var controller *CurrentNodeController
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
				WorkflowExecutionRetired: sessionruntime.WorkflowExecutionRetiredFunc(func(outcome sessionruntime.WorkflowRetirementOutcome) {
					controller.WorkflowExecutionRetired(outcome)
				}),
			})
			runner := &recordingScriptRunner{
				authority: authority,
				command: sessionruntime.ScriptCommand{
					Path: shellPath,
					Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
				},
				started: make(chan workflow.CurrentNodeReference, 1),
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
				t.Fatalf("start Script current node: %v", err)
			}
			<-runner.started
			waitForRunningCurrentNode(t, authority, source)
			sourceScope := singleLiveScope(t, authority, source)
			sourceOperation := workflowOperationForScopeForTest(t, authority, sourceScope)
			retire := func() {
				controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
					Operation:   sourceOperation,
					Kind:        sessionruntime.ExecutionScopeScript,
					Disposition: sessionruntime.WorkflowRetirementCompleted,
				})
			}
			complete := func() {
				if _, err := completeCurrentNodeLifecycleForTest(
					context.Background(),
					controller,
					sourceScope,
					"review",
				); err != nil {
					t.Fatalf("complete Script source: %v", err)
				}
			}
			if retirementFirst {
				retire()
				complete()
			} else {
				complete()
				retire()
			}
			sourceKey, err := source.Key()
			if err != nil {
				t.Fatalf("source key: %v", err)
			}
			controller.mu.Lock()
			retained := controller.operations[sourceKey]
			controller.mu.Unlock()
			if retained != nil {
				t.Fatalf("settled Pending Approval operation retained: %+v", retained)
			}
		})
	}
}
