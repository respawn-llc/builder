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
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-post-turn-fence",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: source,
	})
	if err != nil {
		t.Fatalf("new workflow execution lease: %v", err)
	}
	scopeID = lease.ScopeID()
	run := newCurrentNodeRun(source, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	if err := run.transitionDisposition(currentNodeRunDispositionPublishing, nil); err != nil {
		t.Fatalf("publish source Run: %v", err)
	}
	if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err != nil {
		t.Fatalf("run source Run: %v", err)
	}
	run.phase = currentNodeRunRunning
	run.executionLease = &lease
	controller := &CurrentNodeController{
		store: &currentNodeControllerStore{
			pendingApproval: workflow.PendingApproval{Source: source},
		},
		permit:    NewMutationPermit(),
		lifecycle: NewTaskLifecycleCoordinator(),
		authority: authority,
		runs:      newCurrentNodeRunRegistry(),
		exactScopes: map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey{
			scopeID: key,
		},
		completed: map[runtimeids.ExecutionScopeID]struct{}{scopeID: {}},
		postTurnFinalization: map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization{
			scopeID: {
				sessionID:      &sessionID,
				classification: workflow.SessionReuseGuaranteedCACReuse,
				reference:      source,
			},
		},
		heldStarts: make(map[runtimeids.ExecutionScopeID][]workflow.CurrentNodeReferenceKey),
	}
	installCurrentNodeRunLockedForTest(controller, run)
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
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-post-turn-matrix",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: source,
	})
	if err != nil {
		t.Fatalf("new workflow execution lease: %v", err)
	}
	scopeID = lease.ScopeID()
	run := newCurrentNodeRun(source, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	if err := run.transitionDisposition(currentNodeRunDispositionPublishing, nil); err != nil {
		t.Fatalf("publish source Run: %v", err)
	}
	if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err != nil {
		t.Fatalf("run source Run: %v", err)
	}
	run.phase = currentNodeRunRunning
	run.executionLease = &lease
	controller := &CurrentNodeController{
		permit:    NewMutationPermit(),
		lifecycle: NewTaskLifecycleCoordinator(),
		authority: authority,
		runs:      newCurrentNodeRunRegistry(),
		exactScopes: map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey{
			scopeID: key,
		},
		completed: map[runtimeids.ExecutionScopeID]struct{}{scopeID: {}},
		postTurnFinalization: map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization{
			scopeID: {
				sessionID:      &sessionID,
				classification: classification,
				reference:      source,
			},
		},
		heldStarts: make(map[runtimeids.ExecutionScopeID][]workflow.CurrentNodeReferenceKey),
	}
	installCurrentNodeRunLockedForTest(controller, run)
	t.Cleanup(func() { _ = controller.authority.Close(context.Background()) })
	return controller, scopeID, sessionID
}

func TestObserveWorkflowTaskExecutionsIgnoresLatchedWorkerFailure(t *testing.T) {
	cause := errors.New("automatic assignment failed")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	store := &currentNodeControllerStore{}
	controller := &CurrentNodeController{
		authority: authority,
		publication: &currentNodeControllerLifecyclePublication{
			store: store,
			root:  map[workflow.TaskID][]workflow.CurrentNodeReference{},
			exact: map[workflow.TaskID][]workflowstore.LifecycleExactExecution{},
		},
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

func TestObserveWorkflowTaskExecutionsIncludesPinnedQueuedLifecycleRoot(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-observe-queued-root", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		close(runner.release)
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	if _, err := controller.ResumeTask(context.Background(), reference.TaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("queued Run did not enter slow admission")
	}

	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	lifecycle, exists := observation.Lifecycle[reference.TaskID]
	if !exists {
		t.Fatalf("lifecycle observation = %+v, want queued Task", observation.Lifecycle)
	}
	if len(lifecycle.CurrentNodes) != 1 || !lifecycle.CurrentNodes[0].Reference.Equal(reference) {
		t.Fatalf("lifecycle Current Nodes = %+v, want %v", lifecycle.CurrentNodes, reference)
	}
	if len(lifecycle.QueuedCurrentNodes) != 1 || !lifecycle.QueuedCurrentNodes[0].Equal(reference) {
		t.Fatalf("lifecycle queued Runs = %+v, want %v", lifecycle.QueuedCurrentNodes, reference)
	}
	if len(lifecycle.ExactExecutions) != 0 {
		t.Fatalf("lifecycle Exact executions = %+v, want none before registration", lifecycle.ExactExecutions)
	}
}

func TestObserveWorkflowTaskExecutionsKeepsPinnedExactExecutionAfterAuthorityRetirement(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-observe-retired-exact-root", "node-agent")
	sessionID := runtimeids.NewSessionID()
	scopeID := runtimeids.NewExecutionScopeID()
	exact := workflowstore.LifecycleExactExecution{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: reference,
		ScopeID:     scopeID,
		Agent:       &workflowstore.LifecycleAgentExecutionTarget{SessionID: sessionID},
		Phase:       workflowstore.LifecycleExactExecutionRunning,
	}
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingAdmitted,
			},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := &CurrentNodeController{
		authority: authority,
		publication: &currentNodeControllerLifecyclePublication{
			store: store,
			root: map[workflow.TaskID][]workflow.CurrentNodeReference{
				reference.TaskID: {reference},
			},
			exact: map[workflow.TaskID][]workflowstore.LifecycleExactExecution{
				reference.TaskID: {exact},
			},
		},
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions after Authority retirement: %v", err)
	}
	executions := observation.Executions[reference.TaskID].Executions
	if len(executions) != 1 {
		t.Fatalf("pinned executions = %+v, want one prior Exact execution", executions)
	}
	if executions[0].ScopeID != scopeID ||
		executions[0].Agent == nil ||
		executions[0].Agent.SessionID != sessionID {
		t.Fatalf("pinned execution = %+v, want scope %s session %s", executions[0], scopeID, sessionID)
	}
}

func TestObserveWorkflowTaskExecutionsKeepsPinnedStoppedQuiescenceWhileRunIsStaged(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-observe-staged-run", "node-agent")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := &CurrentNodeController{
		authority: authority,
		publication: &currentNodeControllerLifecyclePublication{
			store: &currentNodeControllerStore{},
			root:  map[workflow.TaskID][]workflow.CurrentNodeReference{},
			exact: map[workflow.TaskID][]workflowstore.LifecycleExactExecution{},
		},
		runs: newCurrentNodeRunRegistry(),
	}
	controller.mu.Lock()
	installCurrentNodeRunLockedForTest(
		controller,
		newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionExplicitOverride),
	)
	controller.mu.Unlock()
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions with staged Run: %v", err)
	}
	if !observation.Quiescence[reference.TaskID] {
		t.Fatalf("pinned quiescence = %+v, want prior stopped publication", observation.Quiescence)
	}
}

func TestCurrentNodeControllerExecutionFinalizedWaitsForTaskLifecycleWriterBeforeRetirement(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-finalization-writer", "node-script")
	finalizedScope := make(chan sessionruntime.ExecutionScope, 1)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			finalizedScope <- scope
		}),
	})
	controller := newCurrentNodeControllerForTest(t, &currentNodeControllerStore{}, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: reference,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	controller.mu.Lock()
	installLiveCurrentNodeRunLockedForTest(
		controller,
		reference,
		workflow.NodeKindScript,
		currentNodeAdmissionExplicitOverride,
		lease,
	)
	controller.mu.Unlock()
	if _, err := authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "exit 0"},
		},
	}); err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	lease.Release()
	var scope sessionruntime.ExecutionScope
	select {
	case scope = <-finalizedScope:
	case <-time.After(3 * time.Second):
		t.Fatal("script execution did not finalize")
	}

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered
	finalizationDone := make(chan struct{})
	go func() {
		controller.ExecutionFinalized(scope)
		close(finalizationDone)
	}()

	time.Sleep(50 * time.Millisecond)
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("reference key: %v", err)
	}
	controller.mu.Lock()
	run, owned := controller.runs.get(key)
	indexedKey, exact := controller.exactScopes[scope.ID()]
	controller.mu.Unlock()
	var disposition currentNodeRunDisposition
	if owned {
		disposition = run.disposition
	}
	if !owned || disposition != currentNodeRunDispositionRunning || !exact || indexedKey != key {
		t.Fatalf("finalization retired source before Task writer: owned=%t disposition=%v exact=%t", owned, disposition, exact)
	}
	select {
	case <-finalizationDone:
		t.Fatal("ExecutionFinalized returned while the Task writer was held")
	default:
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	select {
	case <-finalizationDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ExecutionFinalized did not continue after Task writer released")
	}
}

func TestCurrentNodeControllerCloseWaitsForTaskLifecycleWriterBeforeStoppingRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-close-writer", "node-agent")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, &currentNodeControllerStore{}, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered
	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: reference,
		NodeKind:    workflow.NodeKindAgent,
	}})
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("reference key: %v", err)
	}
	controller.mu.Lock()
	run, owned := controller.runs.get(key)
	controller.mu.Unlock()
	if !owned {
		t.Fatal("controller did not accept queued Run")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- controller.Close()
	}()

	time.Sleep(50 * time.Millisecond)
	controller.mu.Lock()
	disposition := run.disposition
	controller.mu.Unlock()
	if disposition != currentNodeRunDispositionQueued {
		t.Fatalf("controller close stopped Run before Task writer: disposition=%v", disposition)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("controller Close returned while Task writer was held: %v", err)
	default:
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("controller Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("controller Close did not continue after Task writer released")
	}
	if run.disposition != currentNodeRunDispositionStopped ||
		run.stop == nil ||
		run.stop.reason != currentNodeRunStopControllerClosed {
		t.Fatalf("controller close Run disposition = %+v, want one typed controller-closed stop", run)
	}
}

func TestCurrentNodeControllerCloseRejectsRunAcceptanceAfterItsOwnershipSnapshot(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-close-acceptance", "node-agent")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, &currentNodeControllerStore{}, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})
	type queueResult struct {
		run *currentNodeRun
		err error
	}
	writerEntered := make(chan struct{})
	attemptQueue := make(chan struct{})
	queued := make(chan queueResult, 1)
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-attemptQueue
			controller.mu.Lock()
			err := controller.queueExplicitStartLocked(*newCurrentNodeRun(
				reference,
				workflow.NodeKindAgent,
				currentNodeAdmissionExplicitOverride,
			))
			key, keyErr := reference.Key()
			var run *currentNodeRun
			if keyErr == nil {
				run, _ = controller.runs.get(key)
			}
			controller.mu.Unlock()
			queued <- queueResult{run: run, err: errors.Join(err, keyErr)}
			return nil
		})
	}()
	<-writerEntered
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- controller.Close()
	}()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return controller.closed
	}, "controller Close did not establish its closed boundary")
	close(attemptQueue)
	result := <-queued
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("controller Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("controller Close did not finish")
	}
	if result.err == nil || result.run != nil {
		t.Fatalf(
			"Run acceptance crossed controller Close ownership snapshot: error=%v accepted=%t disposition=%v",
			result.err,
			result.run != nil,
			func() currentNodeRunDisposition {
				if result.run == nil {
					return 0
				}
				return result.run.disposition
			}(),
		)
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

func TestCurrentNodeControllerEnsuresApprovalTargetBeforeStartingIt(t *testing.T) {
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
	ensurer := &recordingCurrentNodeAssignmentEnsurer{}
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: ensurer,
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
	if got := ensurer.references(); len(got) != 1 || !got[0].Equal(target) {
		t.Fatalf("ensured assignments = %+v, want %v", got, target)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return len(runner.promptDeliveries()) == 1
	}, "approval target did not reach runner")
	if deliveries := runner.promptDeliveries(); len(deliveries) != 1 ||
		deliveries[0] != workflowruntime.TaskPromptDeliveryAssignment {
		t.Fatalf("runner prompt deliveries = %+v, want Assignment after approved transition", deliveries)
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
		AssignmentEnsurer: &recordingCurrentNodeAssignmentEnsurer{err: cause},
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
		t.Fatalf("runner starts = %d, want none after assignment ensure failure", starts)
	}
	if interruption, interrupted := store.interruption(target); !interrupted ||
		interruption.reason != reasonCurrentNodeRuntimeStartFailed {
		t.Fatalf("unassigned approval target interruption = %+v, present=%t, want runtime-start interruption", interruption, interrupted)
	}
}

func TestCompleteIdleCurrentNodeRecoversSuccessorByAssignmentCommit(t *testing.T) {
	cause := errors.New("assignment persistence failed")
	for _, tc := range []struct {
		name            string
		outcomes        []currentNodeAssignmentEnsureOutcome
		wantInterrupted []bool
		wantStarts      int
	}{
		{
			name: "uncommitted assignment",
			outcomes: []currentNodeAssignmentEnsureOutcome{{
				waitErr: cause,
			}},
			wantInterrupted: []bool{true},
		},
		{
			name: "committed assignment with observer failure",
			outcomes: []currentNodeAssignmentEnsureOutcome{{
				receipt: session.CommitReceipt{Committed: true},
				waitErr: cause,
			}},
			wantInterrupted: []bool{true},
		},
		{
			name: "later fanout assignment preparation failure",
			outcomes: []currentNodeAssignmentEnsureOutcome{
				{receipt: session.CommitReceipt{Committed: true}},
				{ensureErr: cause},
			},
			wantInterrupted: []bool{false, true},
			wantStarts:      1,
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
			runnerRelease := make(chan struct{})
			runner := &blockingCurrentNodeRunner{
				entered: make(chan struct{}),
				release: runnerRelease,
			}
			controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
				AgentConcurrency: 1,
				AssignmentEnsurer: &recordingCurrentNodeAssignmentEnsurer{
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
			t.Cleanup(func() {
				close(runnerRelease)
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
			if tc.wantStarts == 1 {
				select {
				case <-runner.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("healthy successor did not reach its runner")
				}
			} else {
				select {
				case <-runner.entered:
					t.Fatal("failed successor reached its runner")
				case <-time.After(50 * time.Millisecond):
				}
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

func TestCompleteIdleCurrentNodeKeepsMixedFanoutScriptHealthyWhenAgentAssignmentFails(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-idle-mixed-fanout", "node-source")
	agentTarget := currentNodeReferenceForControllerTest(t, string(source.TaskID), "node-agent")
	scriptTarget := currentNodeReferenceForControllerTest(t, string(source.TaskID), "node-script")
	sourceNode := workflow.CurrentNode{
		Reference:  source,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
	}
	store := &currentNodeControllerStore{
		idleResolved: &sourceNode,
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{
				{CurrentNode: agentTarget, NodeKind: workflow.NodeKindAgent},
				{CurrentNode: scriptTarget, NodeKind: workflow.NodeKindScript},
			},
		},
	}
	cause := errors.New("Agent assignment append failed")
	ensurer := &recordingCurrentNodeAssignmentEnsurer{
		errors: map[workflow.CurrentNodeReferenceKey]error{},
	}
	agentKey, err := agentTarget.Key()
	if err != nil {
		t.Fatalf("Agent target key: %v", err)
	}
	ensurer.errors[agentKey] = cause
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
		AgentConcurrency:  1,
		AssignmentEnsurer: ensurer,
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
		"split",
		nil,
		"forced completion",
	); !errors.Is(err, cause) {
		t.Fatalf("CompleteIdleCurrentNode error = %v, want %v", err, cause)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(scriptTarget) {
			t.Fatalf("healthy mixed-fanout successor = %v, want Script %v", started, scriptTarget)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("healthy Script successor was stranded after Agent assignment failure")
	}
	if interruption, interrupted := store.interruption(agentTarget); !interrupted ||
		interruption.reason != reasonCurrentNodeRuntimeStartFailed {
		t.Fatalf("failed Agent interruption = %+v, present=%t", interruption, interrupted)
	}
	if interruption, interrupted := store.interruption(scriptTarget); interrupted {
		t.Fatalf("healthy Script successor was interrupted: %+v", interruption)
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
		AssignmentEnsurer: lateCommitCurrentNodeAssignmentEnsurer{
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

func TestCurrentNodeControllerPublishesApprovalTargetAfterCompletedSourceRetirement(t *testing.T) {
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
	if len(snapshot.HeldIntents) != 0 {
		t.Fatalf("held approval starts = %+v, want none after source retirement publication", snapshot.HeldIntents)
	}
	foundTarget := false
	for _, start := range snapshot.ExplicitStarts {
		foundTarget = foundTarget || start.CurrentNode.Equal(target)
	}
	if !foundTarget {
		t.Fatalf("explicit approval starts = %+v, want target %v", snapshot.ExplicitStarts, target)
	}
	if !hasAutomaticCurrentNodeIntent(snapshot, queuedAgent) {
		t.Fatalf("automatic agent queue = %+v, want queued agent while source occupies capacity", snapshot.AutomaticIntents)
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
			t.Fatal("approval target did not start after source retirement publication")
		}
	}
	sourceHandle, live := authority.ExecutionByScope(sourceScope)
	if !live {
		t.Fatal("completed approval source scope retired before stop")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop approval source: %v", err)
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
		AssignmentEnsurer: deadlineRecordingCurrentNodeAssignmentEnsurer{
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

func TestCurrentNodeControllerCompletionCommitWinsTaskInterruptWithoutDeadlock(t *testing.T) {
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
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	if err := controller.Interrupt(context.Background(), InterruptSelector{TaskID: source.TaskID}); !errors.Is(err, ErrNoInterruptibleExecution) {
		t.Fatalf("task interrupt error = %v, want completed-state revalidation", err)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("successor %v started after Task Interrupt", started)
	case <-time.After(100 * time.Millisecond):
	}
	sourceHandle, live := authority.ExecutionByScope(sourceScope)
	if !live {
		t.Fatal("completed source scope retired before explicit stop")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop completed source: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(successor) {
			t.Fatalf("started successor = %v, want %v", started, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successor did not start after completed source Authority retirement")
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

func TestCurrentNodeControllerRetiresCommittedCompletionAfterEventFailure(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-completion-event-failure", "node-source")
	eventErr := errors.New("completion event delivery failed")
	store := &currentNodeControllerStore{
		completionEventErr: eventErr,
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	handles := make(chan sessionruntime.ExecutionHandle, 1)
	controller = newCurrentNodeControllerForTest(t, store, &completingScriptRunner{
		authority: authority,
		source:    source,
		shellPath: shellPath,
		started:   make(chan workflow.CurrentNodeReference, 1),
		handles:   handles,
	}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start Current Node: %v", err)
	}
	handle := <-handles
	if _, err := handle.Wait(context.Background()); !errors.Is(err, eventErr) {
		t.Fatalf("Wait error = %v, want surfaced event failure %v", err, eventErr)
	}
	if _, live := authority.ExecutionByScope(handle.Scope().ID()); live {
		t.Fatal("committed completion left Authority Exact Scope live")
	}
	if hasLiveCurrentNode(controller.Snapshot(), source) {
		t.Fatalf("committed completion left controller Run live: %+v", controller.Snapshot())
	}
}

func TestCurrentNodeControllerInterruptsAgentThatReturnsWithoutAcceptedCompletion(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-agent-finalizer-failure", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: reference,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	controller.mu.Lock()
	installLiveCurrentNodeRunLockedForTest(
		controller,
		reference,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
		lease,
	)
	controller.mu.Unlock()

	if err := controller.FinalizeCurrentNodeResult(context.Background(), lease.ScopeID(), nil); err != nil {
		t.Fatalf("FinalizeCurrentNodeResult: %v", err)
	}
	interruption, interrupted := store.interruption(reference)
	if !interrupted ||
		interruption.reason != workflow.CurrentNodeInterruptionReason("workflow_result_finalization_failed") {
		t.Fatalf("Agent finalizer interruption = %+v, present=%t", interruption, interrupted)
	}
}
