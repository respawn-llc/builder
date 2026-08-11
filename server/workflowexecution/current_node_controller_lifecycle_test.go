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

func TestPostTurnCompactionReleasesMutationPermitWhileApprovalFenceIsActive(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	controller, store, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
		t,
		workflow.SessionReuseGuaranteedCACReuse,
	)
	source := controller.Snapshot().LiveScopes[0].CurrentNode
	store.mu.Lock()
	store.pendingApproval = workflow.PendingApproval{Source: source}
	store.mu.Unlock()

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
			controller, _, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
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
			if err := controller.FinalizeCurrentNodePostTurn(
				context.Background(),
				scopeID,
				sessionID,
				workflowruntime.PostCompletionRuntime{CompactionMode: "none"},
			); err != nil {
				t.Fatalf("repeat finalized post-turn: %v", err)
			}
		})
	}
}

func TestPostTurnFinalizationSurfacesInvalidThresholdAndCancellation(t *testing.T) {
	t.Run("invalid threshold", func(t *testing.T) {
		controller, _, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
			t,
			workflow.SessionReuseThresholdPossibleReuse,
		)
		err := controller.FinalizeCurrentNodePostTurn(context.Background(), scopeID, sessionID, workflowruntime.PostCompletionRuntime{
			CompactionMode: "local",
		})
		if err == nil {
			t.Fatal("invalid pre-compaction threshold returned nil")
		}
		if retryErr := controller.FinalizeCurrentNodePostTurn(
			context.Background(),
			scopeID,
			sessionID,
			workflowruntime.PostCompletionRuntime{
				PreCompactionTokens: 1,
				CompactionMode:      "local",
			},
		); retryErr != nil {
			t.Fatalf("retry valid post-turn finalization: %v", retryErr)
		}
	})

	t.Run("cancellation is fatal", func(t *testing.T) {
		controller, _, scopeID, sessionID := newPostTurnFinalizationControllerForTest(
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
) (*CurrentNodeController, *currentNodeControllerStore, runtimeids.ExecutionScopeID, runtimeids.SessionID) {
	t.Helper()
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-post-turn-matrix", "node-source")
	sessionID := runtimeids.NewSessionID()
	analysis := postTurnSessionReuseAnalysis(t, source, sessionID, classification)
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			SessionReuse:           &analysis,
			PostCompletionEligible: true,
			PendingApproval:        &workflow.PendingApproval{Source: source},
		},
		sessionReuse: []workflow.SessionReuseAssociation{{
			SessionID:   sessionID,
			CurrentNode: source,
		}},
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
		t.Fatalf("start post-turn source: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	scopeID := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		SessionID:    &sessionID,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete post-turn source: %v", err)
	}
	return controller, store, scopeID, sessionID
}

func postTurnSessionReuseAnalysis(
	t *testing.T,
	source workflow.CurrentNodeReference,
	sessionID runtimeids.SessionID,
	classification workflow.SessionReuseClassification,
) workflow.SessionReuseAnalysisInput {
	t.Helper()
	completed, err := workflow.NewCurrentNode(source, &sessionID, nil)
	if err != nil {
		t.Fatalf("completed Current Node: %v", err)
	}
	if classification == workflow.SessionReuseNone {
		return workflow.SessionReuseAnalysisInput{CompletedCurrentNode: completed}
	}
	review, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  currentNodeControllerTestWorkflowID,
		ID:          "node-review",
		Key:         "review",
		DisplayName: "Review",
	}, workflow.NodeKindAgent, workflow.NodeFields{SubagentRole: "reviewer"})
	if err != nil {
		t.Fatalf("review node: %v", err)
	}
	sourceNode, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  currentNodeControllerTestWorkflowID,
		ID:          source.NodeID,
		Key:         "source",
		DisplayName: "Source",
	}, workflow.NodeKindAgent, workflow.NodeFields{SubagentRole: "coder"})
	if err != nil {
		t.Fatalf("source node: %v", err)
	}
	accepted := workflow.Edge{
		WorkflowID:        currentNodeControllerTestWorkflowID,
		ID:                "edge-source-review",
		Key:               "review",
		TransitionGroupID: "group-source",
		TargetNodeID:      "node-review",
		ContextMode:       workflow.ContextModeNewSession,
	}
	cac := workflow.Edge{
		WorkflowID:        currentNodeControllerTestWorkflowID,
		ID:                "edge-review-source",
		Key:               "source",
		TransitionGroupID: "group-review",
		TargetNodeID:      source.NodeID,
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "source"},
	}
	analysis := workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          currentNodeControllerTestWorkflowID,
			DisplayName: "Post-turn fixture",
			Nodes:       []workflow.Node{sourceNode, review},
			TransitionGroups: []workflow.TransitionGroup{{
				WorkflowID:   currentNodeControllerTestWorkflowID,
				ID:           "group-source",
				SourceNodeID: source.NodeID,
			}, {
				WorkflowID:   currentNodeControllerTestWorkflowID,
				ID:           "group-review",
				SourceNodeID: "node-review",
			}},
			Edges: []workflow.Edge{accepted, cac},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completed,
	}
	if classification == workflow.SessionReuseThresholdPossibleReuse {
		terminal, terminalErr := workflow.NewNode(workflow.NodeIdentity{
			WorkflowID:  currentNodeControllerTestWorkflowID,
			ID:          "node-done",
			Key:         "done",
			DisplayName: "Done",
		}, workflow.NodeKindTerminal, workflow.NodeFields{})
		if terminalErr != nil {
			t.Fatalf("terminal node: %v", terminalErr)
		}
		alternative := workflow.Edge{
			WorkflowID:        currentNodeControllerTestWorkflowID,
			ID:                "edge-source-done",
			Key:               "done",
			TransitionGroupID: "group-review-terminal",
			TargetNodeID:      "node-done",
			ContextMode:       workflow.ContextModeNewSession,
		}
		analysis.Workflow.Nodes = append(analysis.Workflow.Nodes, terminal)
		analysis.Workflow.Edges = append(analysis.Workflow.Edges, alternative)
		analysis.Workflow.TransitionGroups = append(
			analysis.Workflow.TransitionGroups,
			workflow.TransitionGroup{
				WorkflowID:   currentNodeControllerTestWorkflowID,
				ID:           "group-review-terminal",
				SourceNodeID: "node-review",
			},
		)
	}
	return analysis
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
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: ensurer,
		LifecycleReporter: newRecordingLifecycleFatalReporter(),
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
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return len(runner.promptDeliveries()) == 1 && len(ensurer.references()) == 1
	}, "approval target did not reach runner")
	if got := ensurer.references(); !got[0].Equal(target) {
		t.Fatalf("ensured assignments = %+v, want %v", got, target)
	}
	if deliveries := runner.promptDeliveries(); len(deliveries) != 1 ||
		deliveries[0] != workflowruntime.TaskPromptDeliveryAssignment {
		t.Fatalf("runner prompt deliveries = %+v, want Assignment for admitted transition", deliveries)
	}
}

func TestCurrentNodeControllerInterruptsApprovalTargetWhenAssignmentEnsureFails(t *testing.T) {
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
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: &recordingCurrentNodeAssignmentEnsurer{err: cause},
		LifecycleReporter: newRecordingLifecycleFatalReporter(),
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
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(target)
		return interrupted
	}, "assignment ensure failure did not interrupt Approval target")
	if starts := runner.starts(); starts != 0 {
		t.Fatalf("runner starts = %d, want none after steering failure", starts)
	}
	if interruption, interrupted := store.interruption(target); !interrupted ||
		interruption.reason != reasonCurrentNodeRuntimeStartFailed {
		t.Fatalf("unassigned approval target interruption = %+v, %t, want runtime start failure", interruption, interrupted)
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
	sourceSessionID := runtimeids.NewSessionID()
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		SessionID:    &sourceSessionID,
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
	controller, err = NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		LifecycleReporter: newRecordingLifecycleFatalReporter(),
		AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
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
	case started := <-runner.started:
		if !started.Equal(successor) {
			t.Fatalf("started successor = %v, want %v", started, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successor did not start after source retirement")
	}
}

func TestCurrentNodeControllerAgentPostTurnFailureInterruptsCommittedSuccessorBeforePredecessorRetires(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-agent-post-turn-failure", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-agent-post-turn-failure", "node-successor")
	sessionID := runtimeids.NewSessionID()
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
				CurrentNode: successor,
				NodeKind:    workflow.NodeKindAgent,
			}},
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
		t.Fatalf("start source: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	scopeID := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		SessionID:    &sessionID,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	finalizationErr := controller.FinalizeCurrentNodePostTurn(
		context.Background(),
		scopeID,
		runtimeids.NewSessionID(),
		workflowruntime.PostCompletionRuntime{CompactionMode: "none"},
	)
	if finalizationErr == nil {
		t.Fatal("mismatched post-turn finalization returned nil")
	}
	if err := controller.FailCurrentNodeScope(
		context.Background(),
		scopeID,
		"workflow_runtime_failed",
		finalizationErr,
	); err != nil {
		t.Fatalf("fail completed source scope: %v", err)
	}
	handle, live := authority.ExecutionByScope(scopeID)
	if !live {
		t.Fatal("completed predecessor retired before explicit stop")
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop completed predecessor: %v", err)
	}

	interruption, interrupted := store.interruption(successor)
	if !interrupted || interruption.reason != "workflow_runtime_failed" {
		t.Fatalf("successor interruption = %+v, interrupted = %t", interruption, interrupted)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("failed Agent released committed successor %v", started)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCurrentNodeControllerAgentPostTurnFailureRetainsSuccessorUntilInterruptionCommits(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-agent-post-turn-persistence", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-agent-post-turn-persistence", "node-successor")
	sessionID := runtimeids.NewSessionID()
	persistenceErr := errors.New("interrupt successor persistence failed")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{
				CurrentNode: successor,
				NodeKind:    workflow.NodeKindAgent,
			}},
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
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		store.setInterruptError(nil)
		if err := controller.Close(); err != nil && !errors.Is(err, persistenceErr) {
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
	scopeID := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		SessionID:    &sessionID,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	finalizationErr := controller.FinalizeCurrentNodePostTurn(
		context.Background(),
		scopeID,
		runtimeids.NewSessionID(),
		workflowruntime.PostCompletionRuntime{CompactionMode: "none"},
	)
	if finalizationErr == nil {
		t.Fatal("mismatched post-turn finalization returned nil")
	}
	store.setInterruptError(persistenceErr)
	if err := controller.FailCurrentNodeScope(
		context.Background(),
		scopeID,
		"workflow_runtime_failed",
		finalizationErr,
	); !errors.Is(err, persistenceErr) {
		t.Fatalf("first failure disposition error = %v, want %v", err, persistenceErr)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.HeldIntents) != 1 || !snapshot.HeldIntents[0].CurrentNode.Equal(successor) {
		t.Fatalf("held successors after failed persistence = %+v, want %v", snapshot.HeldIntents, successor)
	}
	if observation, err := controller.ObserveCurrentNodeCompletion(
		context.Background(),
		workflowruntime.CompletionObservationRequest{ScopeID: scopeID},
	); err != nil || !observation.Completed {
		t.Fatalf("completion observation after failed disposition = %+v, %v", observation, err)
	}

	store.setInterruptError(nil)
	if err := controller.FailCurrentNodeScope(
		context.Background(),
		scopeID,
		"workflow_runtime_failed",
		finalizationErr,
	); err == nil {
		t.Fatal("fatal successor interruption persistence failure allowed another lifecycle disposition")
	} else if !errors.Is(err, persistenceErr) {
		t.Fatalf("retry failure disposition error = %v, want fatal cause %v", err, persistenceErr)
	}
	if snapshot := controller.Snapshot(); len(snapshot.HeldIntents) != 1 ||
		!snapshot.HeldIntents[0].CurrentNode.Equal(successor) {
		t.Fatalf("fatal persistence recovery released successor ownership: %+v", snapshot.HeldIntents)
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
		authority:   authority,
		source:      source,
		shellPath:   shellPath,
		started:     make(chan workflow.CurrentNodeReference, 2),
		sourceScope: make(chan runtimeids.ExecutionScopeID, 1),
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
	var sourceScope runtimeids.ExecutionScopeID
	select {
	case sourceScope = <-runner.sourceScope:
	case <-time.After(3 * time.Second):
		t.Fatal("successful Script did not enter its completion callback")
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
	if err := controller.FailCurrentNodeScope(
		context.Background(),
		sourceScope,
		"workflow_script_cleanup_diagnostic",
		errors.New("late script cleanup diagnostic"),
	); !errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		t.Fatalf("late Script cleanup diagnostic error = %v, want execution no longer live", err)
	}
	if interruption, interrupted := store.interruption(successor); interrupted {
		t.Fatalf("late Script cleanup diagnostic interrupted successor: %+v", interruption)
	}
	if !hasLiveCurrentNode(controller.Snapshot(), successor) {
		t.Fatal("late Script cleanup diagnostic removed the successor Run")
	}
}

func TestCurrentNodeControllerRetiringDispositionReadIsInertInsteadOfQueued(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-retiring-read", "node-script")
	store := &currentNodeControllerStore{
		interruptStarted: make(chan struct{}),
		interruptRelease: make(chan struct{}),
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
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	releaseInterruption := func() {
		select {
		case <-store.interruptRelease:
		default:
			close(store.interruptRelease)
		}
	}
	t.Cleanup(func() {
		releaseInterruption()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("start Script Current Node: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, reference)
	scopeID := singleLiveScope(t, controller, reference)
	handle, live := authority.ExecutionByScope(scopeID)
	if !live {
		t.Fatal("Script Exact execution is absent")
	}
	handle.RequestStop()
	select {
	case <-store.interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("outcome-less finalization did not enter durable disposition")
	}

	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions during retiring disposition: %v", err)
	}
	if snapshot := observation.Runs[reference.TaskID]; len(snapshot.Queued) != 0 ||
		len(snapshot.InterruptibleLaunching) != 0 {
		t.Fatalf("retiring Run projected queued: %+v", snapshot)
	}
	if executions := observation.Executions[reference.TaskID].Executions; len(executions) != 0 {
		t.Fatalf("retired Exact remained live in observation: %+v", executions)
	}

	releaseInterruption()
	_, _ = handle.Wait(context.Background())
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return !hasLiveCurrentNode(controller.Snapshot(), reference)
	}, "retiring Run did not finish after durable interruption")
}

func TestCurrentNodeControllerScriptSuccessorInterruptionFailureRetainsOwnershipThroughCleanup(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-script-persistence-failure", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-script-persistence-failure", "node-successor")
	persistenceErr := errors.New("interrupt Script successor persistence failed")
	store := &currentNodeControllerStore{completion: workflowstore.CurrentNodeCompletionResult{
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
		store.setInterruptError(nil)
		if err := controller.Close(); err != nil && !errors.Is(err, persistenceErr) {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start Script predecessor: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	scopeID := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      scopeID,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete Script predecessor: %v", err)
	}
	store.setInterruptError(persistenceErr)
	if err := controller.FailCurrentNodeScope(
		context.Background(),
		scopeID,
		"workflow_script_cleanup_failed",
		errors.New("late Script cleanup failure"),
	); !errors.Is(err, persistenceErr) {
		t.Fatalf("Script successor interruption error = %v, want %v", err, persistenceErr)
	}
	if snapshot := controller.Snapshot(); !hasLiveCurrentNode(snapshot, source) ||
		!hasAutomaticCurrentNodeIntent(snapshot, successor) {
		t.Fatalf("Script persistence failure ownership = %+v, want predecessor and queued successor", snapshot)
	}
	handle, live := authority.ExecutionByScope(scopeID)
	if !live {
		t.Fatal("Script predecessor retired before cleanup")
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop Script predecessor: %v", err)
	}
	if snapshot := controller.Snapshot(); !hasAutomaticCurrentNodeIntent(snapshot, successor) {
		t.Fatalf("Script cleanup released successor after fatal persistence failure: %+v", snapshot)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("Script cleanup admitted successor %v after fatal persistence failure", started)
	case <-time.After(100 * time.Millisecond):
	}
}
