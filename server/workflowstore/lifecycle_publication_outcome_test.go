package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestLifecycleCompletionReportsCommittedWhenEventDeliveryFails(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	eventErr := errors.New("workflow event delivery failed")
	store.SetWorkflowEventPublisher(&recordingCurrentNodeEventPublisher{err: eventErr})
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		testsetup.PreparedPublicationStage(NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	source := started.Mutation.Created[0].Reference
	scopeID := runtimeids.NewExecutionScopeID()
	if err := publication.PublishExactRegistration(ctx, LifecycleExactExecution{
		ProjectID:   binding.ProjectID,
		WorkflowID:  workflowID,
		CurrentNode: source,
		ScopeID:     scopeID,
		Script:      &LifecycleScriptExecutionTarget{Path: "/test/script"},
		Phase:       LifecycleExactExecutionFinalizing,
	}, &lifecycleExactActivation{}); err != nil {
		t.Fatalf("PublishExactRegistration: %v", err)
	}

	_, outcome, err := publication.PublishCurrentNodeCompletion(
		ctx,
		CurrentNodeCompletionRequest{
			Source:       source,
			TransitionID: "review",
			OutputValues: map[string]string{"summary": "completed"},
		},
		func(CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
			delta, err := NewTaskLifecycleDelta(
				task.ID,
				[]LifecycleRunDelta{{
					CurrentNode: source,
					Expect:      LifecycleFieldPresent,
					Next:        LifecycleFieldAbsent,
				}},
				[]LifecycleExactDelta{{
					CurrentNode: source,
					ExpectScope: &scopeID,
				}},
			)
			return delta, nil, err
		},
	)
	if !outcome.Committed() {
		t.Fatalf("completion outcome = %+v, want committed disposition", outcome)
	}
	if !errors.Is(err, eventErr) {
		t.Fatalf("completion error = %v, want event failure %v", err, eventErr)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if exact := capture.ExactExecutions(task.ID); len(exact) != 0 {
		t.Fatalf("committed completion retained Exact execution: %+v", exact)
	}
}

func TestLifecycleInterruptionReportsCommittedWhenEventContextIsCanceled(t *testing.T) {
	ctx, publication, reference, _ := lifecyclePublicationCaptureFixture(t, true)
	eventCtx, cancelEvent := context.WithCancel(ctx)
	eventErr := errors.New("event context canceled after commit")
	publication.store.SetWorkflowEventPublisher(workflowEventPublisherFunc(
		func(context.Context, WorkflowEventRecord) error {
			cancelEvent()
			return errors.Join(eventErr, context.Cause(eventCtx))
		},
	))

	outcome, err := publication.PublishCurrentNodeInterruption(
		eventCtx,
		[]workflow.CurrentNodeReference{reference},
		CurrentNodeInterruptionFromReadyOrAdmitted,
		LifecycleFieldPresent,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.NewCurrentNodeInterruptionDetail("canceled event", nil),
		nil,
	)
	if !outcome.Committed() {
		t.Fatalf("interruption outcome = %+v, want committed disposition", outcome)
	}
	if !errors.Is(err, eventErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption error = %v, want event and canceled-context failures", err)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(reference.TaskID); len(queued) != 0 {
		t.Fatalf("committed interruption retained Run: %+v", queued)
	}
}

type workflowEventPublisherFunc func(context.Context, WorkflowEventRecord) error

func (f workflowEventPublisherFunc) PublishWorkflowEvent(
	ctx context.Context,
	event WorkflowEventRecord,
) error {
	return f(ctx, event)
}
