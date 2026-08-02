package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestCurrentNodeControllerScriptPolicyMatrixDoesNotUseAgentCapacity(t *testing.T) {
	occupyingAgent := currentNodeReferenceForControllerTest(t, "task-policy-agent", "node-occupying-agent")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-policy-queued", "node-queued-agent")
	script := currentNodeReferenceForControllerTest(t, "task-policy-script", "node-script")
	tests := []struct {
		name  string
		apply func(*CurrentNodeController, workflow.CurrentNodeReferenceKey)
		clean func(*CurrentNodeController, workflow.CurrentNodeReferenceKey)
	}{
		{
			name: "predecessor held",
			apply: func(controller *CurrentNodeController, _ workflow.CurrentNodeReferenceKey) {
				controller.heldStarts[runtimeids.NewExecutionScopeID()] = []currentNodeQueuedStart{{
					reference:         script,
					launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
					policy:            currentNodeAdmissionAutomaticScript,
				}}
			},
			clean: func(controller *CurrentNodeController, _ workflow.CurrentNodeReferenceKey) {
				controller.heldStarts = make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart)
			},
		},
		{
			name: "reserved",
			apply: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				controller.automaticReservations[key] = currentNodeQueuedStart{
					reference:         script,
					launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
					policy:            currentNodeAdmissionAutomaticScript,
				}
			},
			clean: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				delete(controller.automaticReservations, key)
			},
		},
		{
			name: "gated",
			apply: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				controller.gates[key] = currentNodeAdmissionGate{
					reference: script,
					policy:    currentNodeAdmissionAutomaticScript,
				}
			},
			clean: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				delete(controller.gates, key)
			},
		},
		{
			name: "live",
			apply: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				scopeID := runtimeids.NewExecutionScopeID()
				controller.live[scopeID] = currentNodeLiveScope{
					reference: script,
					policy:    currentNodeAdmissionAutomaticScript,
				}
				controller.liveByNode[key] = scopeID
			},
			clean: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				scopeID := controller.liveByNode[key]
				delete(controller.liveByNode, key)
				delete(controller.live, scopeID)
			},
		},
		{
			name: "finalizing",
			apply: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				scopeID := runtimeids.NewExecutionScopeID()
				controller.live[scopeID] = currentNodeLiveScope{
					reference: script,
					policy:    currentNodeAdmissionAutomaticScript,
				}
				controller.liveByNode[key] = scopeID
				controller.stopping[scopeID] = struct{}{}
			},
			clean: func(controller *CurrentNodeController, key workflow.CurrentNodeReferenceKey) {
				scopeID := controller.liveByNode[key]
				delete(controller.liveByNode, key)
				delete(controller.live, scopeID)
				delete(controller.stopping, scopeID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			controller := newCurrentNodeControllerForTest(t, &currentNodeControllerStore{}, &countingCurrentNodeRunner{}, authority, 1)
			t.Cleanup(func() {
				if err := controller.Close(); err != nil {
					t.Errorf("close controller: %v", err)
				}
				if err := authority.Close(context.Background()); err != nil {
					t.Errorf("close authority: %v", err)
				}
			})
			queuedKey, err := queuedAgent.Key()
			if err != nil {
				t.Fatalf("queued Agent key: %v", err)
			}
			scriptKey, err := script.Key()
			if err != nil {
				t.Fatalf("Script key: %v", err)
			}
			controller.mu.Lock()
			controller.live[runtimeids.NewExecutionScopeID()] = currentNodeLiveScope{
				reference: occupyingAgent,
				policy:    currentNodeAdmissionAutomaticAgent,
			}
			controller.agentCapacityActive = 1
			controller.automaticQueue.append(currentNodeQueuedStart{
				reference:         queuedAgent,
				launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
				policy:            currentNodeAdmissionAutomaticAgent,
			})
			controller.queued[queuedKey] = struct{}{}
			test.apply(controller, scriptKey)
			if got := controller.agentCapacityActive; got != 1 {
				controller.mu.Unlock()
				t.Fatalf("Agent capacity with %s Script = %d, want 1", test.name, got)
			}
			if _, ok := controller.automaticQueue.selectEntry(nil, false); ok {
				controller.mu.Unlock()
				t.Fatalf("queued Agent became admissible while %s Script owned controller state", test.name)
			}
			test.clean(controller, scriptKey)
			if got := controller.agentCapacityActive; got != 1 {
				controller.mu.Unlock()
				t.Fatalf("Agent capacity after %s Script cleanup = %d, want 1", test.name, got)
			}
			controller.mu.Unlock()
		})
	}
}

func TestCurrentNodeControllerFailedScriptDoesNotReleaseAgentCapacity(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	occupyingAgent := currentNodeReferenceForControllerTest(t, "task-failed-script-agent", "node-occupying-agent")
	failedScript := currentNodeReferenceForControllerTest(t, "task-failed-script", "node-failed-script")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-failed-script-queued-agent", "node-queued-agent")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &selectiveScriptFailureRunner{
		authority: authority,
		failed:    failedScript,
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

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: occupyingAgent,
		NodeKind:    workflow.NodeKindAgent,
	}})
	select {
	case started := <-runner.started:
		if !started.Equal(occupyingAgent) {
			t.Fatalf("occupying Agent start = %v, want %v", started, occupyingAgent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("occupying Agent did not start")
	}
	waitForRunningCurrentNode(t, authority, occupyingAgent)
	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{
		{CurrentNode: failedScript, NodeKind: workflow.NodeKindScript},
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
	})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(failedScript)
		return interrupted
	}, "failed Script was not interrupted")
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), queuedAgent) {
		t.Fatalf("queued Agent = %+v, want queued after failed Script cleanup", controller.Snapshot().AutomaticIntents)
	}
	occupyingHandle, live := authority.ExecutionByScope(singleLiveScope(t, controller, occupyingAgent))
	if !live {
		t.Fatal("occupying Agent is not live")
	}
	if err := occupyingHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop occupying Agent: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(queuedAgent) {
			t.Fatalf("queued Agent start = %v, want %v", started, queuedAgent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued Agent did not start after occupying Agent stopped")
	}
}

func TestCurrentNodeControllerFailedReservationReleasesAgentCapacity(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	failed := currentNodeReferenceForControllerTest(t, "task-failed-reservation", "node-failed")
	next := currentNodeReferenceForControllerTest(t, "task-next-after-failed-reservation", "node-next")
	store := &currentNodeControllerStore{}
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

	controller.enqueueStarts([]currentNodeQueuedStart{{
		reference:         failed,
		launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
		policy:            currentNodeAdmissionAutomaticAgent,
		assignmentSteer: completedCurrentNodeAssignmentSteer{
			err: errors.New("assignment preparation failed"),
		},
	}})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(failed)
		return interrupted
	}, "failed reservation was not interrupted")

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: next,
		NodeKind:    workflow.NodeKindAgent,
	}})
	select {
	case started := <-runner.started:
		if !started.Equal(next) {
			t.Fatalf("next Agent start = %v, want %v", started, next)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("next Agent did not start after failed reservation released capacity")
	}
}

type selectiveScriptFailureRunner struct {
	authority *sessionruntime.Authority
	command   sessionruntime.ScriptCommand
	failed    workflow.CurrentNodeReference
	started   chan workflow.CurrentNodeReference
}

func (r *selectiveScriptFailureRunner) StartCurrentNodeWithPreparation(_ context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	if reference.Equal(r.failed) {
		return errors.New("script start failed")
	}
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  r.command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}

type finalizingBeforeLiveRunner struct {
	authority *sessionruntime.Authority
	fast      workflow.CurrentNodeReference
	shellPath string
	finalized <-chan struct{}
	started   chan workflow.CurrentNodeReference
}

func (r *finalizingBeforeLiveRunner) StartCurrentNodeWithPreparation(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ LaunchPreparation,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	lease sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	command := sessionruntime.ScriptCommand{
		Path: r.shellPath,
		Args: []string{"-c", "while :; do sleep 1; done"},
	}
	if reference.Equal(r.fast) {
		command.Args = []string{"-c", "exit 0"}
		if _, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
			Workflow: &lease,
			Command:  command,
		}); err != nil {
			return err
		}
		lease.Release()
		select {
		case <-r.finalized:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}

func TestCurrentNodeControllerFinalizedGateReleasesAgentCapacity(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	fast := currentNodeReferenceForControllerTest(t, "task-finalized-gate", "node-fast")
	next := currentNodeReferenceForControllerTest(t, "task-after-finalized-gate", "node-next")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	finalized := make(chan struct{})
	var finalizedOnce sync.Once
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
			finalizedOnce.Do(func() { close(finalized) })
		}),
	})
	runner := &finalizingBeforeLiveRunner{
		authority: authority,
		fast:      fast,
		shellPath: shellPath,
		finalized: finalized,
		started:   make(chan workflow.CurrentNodeReference, 1),
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
		CurrentNode: fast,
		NodeKind:    workflow.NodeKindAgent,
	}})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(fast)
		return interrupted
	}, "fast-finalized gated Agent was not cleaned up")

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: next,
		NodeKind:    workflow.NodeKindAgent,
	}})
	select {
	case started := <-runner.started:
		if !started.Equal(next) {
			t.Fatalf("queued Agent start = %v, want %v", started, next)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued Agent did not start after gated finalization released capacity")
	}
}

func TestExecutionFinalizationDoesNotMakeUnassignedHeldSuccessorResumable(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-successor-steer-failure", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-successor-steer-failure", "node-successor")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-unrelated-agent", "node-agent")
	queuedScript := currentNodeReferenceForControllerTest(t, "task-unrelated-script", "node-script")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}},
		},
	}
	steerer := &recordingCurrentNodeAssignmentSteerer{}
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
	controller, err = NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: steerer,
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

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{CurrentNode: source, NodeKind: workflow.NodeKindAgent}})
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: queuedScript, NodeKind: workflow.NodeKindScript},
	})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasAutomaticCurrentNodeIntent(controller.Snapshot(), queuedAgent)
	}, "unrelated Agent did not remain queued while source occupied capacity")
	sourceScope := singleLiveScope(t, controller, source)
	cause := errors.New("assignment persistence failed")
	steerer.setWaitError(cause)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	sourceHandle, live := authority.ExecutionByScope(sourceScope)
	if !live {
		t.Fatal("completed source scope retired before stop")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop source: %v", err)
	}

	if interruption, interrupted := store.interruption(successor); interrupted {
		t.Fatalf("unassigned held successor was made resumable: %+v", interruption)
	}
	if err := controller.EnsureTaskQuiescent(source.TaskID); err != nil {
		t.Fatalf("uncommitted assignment failure latched controller failure: %v", err)
	}
	started := make(map[workflow.CurrentNodeReferenceKey]struct{}, 2)
	deadline := time.After(3 * time.Second)
	for len(started) < 2 {
		select {
		case currentNode := <-runner.started:
			if currentNode.Equal(successor) {
				t.Fatalf("unassigned held successor started after assignment failure")
			}
			if !currentNode.Equal(queuedAgent) && !currentNode.Equal(queuedScript) {
				t.Fatalf("unexpected start after assignment failure: %v", currentNode)
			}
			currentNodeKey, err := currentNode.Key()
			if err != nil {
				t.Fatalf("started current node key: %v", err)
			}
			started[currentNodeKey] = struct{}{}
		case <-deadline:
			t.Fatalf("queued work did not start after source capacity was released: %+v", started)
		}
	}
}
