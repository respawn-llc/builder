package workflowstore

import (
	"errors"
	"testing"

	"core/shared/runtimeids"
)

type lifecycleExactActivation struct {
	err       error
	activated bool
	activate  func() error
}

func (a *lifecycleExactActivation) Activate() error {
	a.activated = true
	if a.activate != nil {
		return a.activate()
	}
	return a.err
}

func TestLifecycleExactRegistrationActivatesBeforeRootSwap(t *testing.T) {
	ctx, publication, reference, _ := lifecyclePublicationCaptureFixture(t, true)
	exact := LifecycleExactExecution{
		ProjectID:   "project-test",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: reference,
		ScopeID:     runtimeids.NewExecutionScopeID(),
		Script:      &LifecycleScriptExecutionTarget{Path: "/test/script"},
		Phase:       LifecycleExactExecutionRunning,
	}
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	activation := &lifecycleExactActivation{
		activate: func() error {
			if _, published := publication.root[reference.TaskID].exact[key]; published {
				return errors.New("Exact root swapped before Authority activation")
			}
			return nil
		},
	}
	if err := publication.PublishExactRegistration(ctx, exact, activation); err != nil {
		t.Fatalf("PublishExactRegistration: %v", err)
	}
	if !activation.activated {
		t.Fatal("PublishExactRegistration did not activate the staged Authority execution")
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	published := capture.ExactExecutions(reference.TaskID)
	if len(published) != 1 || published[0].ScopeID != exact.ScopeID {
		t.Fatalf("published Exact executions = %+v, want scope %s", published, exact.ScopeID)
	}
}

func TestLifecycleExactRegistrationActivationFailureKeepsQueuedRoot(t *testing.T) {
	ctx, publication, reference, _ := lifecyclePublicationCaptureFixture(t, true)
	activationErr := errors.New("staged Authority execution retired")
	activation := &lifecycleExactActivation{err: activationErr}
	err := publication.PublishExactRegistration(ctx, LifecycleExactExecution{
		ProjectID:   "project-test",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: reference,
		ScopeID:     runtimeids.NewExecutionScopeID(),
		Script:      &LifecycleScriptExecutionTarget{Path: "/test/script"},
		Phase:       LifecycleExactExecutionRunning,
	}, activation)
	if !errors.Is(err, activationErr) {
		t.Fatalf("PublishExactRegistration error = %v, want activation failure %v", err, activationErr)
	}
	if !activation.activated {
		t.Fatal("PublishExactRegistration did not activate the staged Authority execution")
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if exact := capture.ExactExecutions(reference.TaskID); len(exact) != 0 {
		t.Fatalf("activation failure published Exact execution: %+v", exact)
	}
	queued := capture.QueuedCurrentNodes(reference.TaskID)
	if len(queued) != 1 || !queued[0].Equal(reference) {
		t.Fatalf("activation failure queued Current Nodes = %+v, want %v", queued, reference)
	}
}
