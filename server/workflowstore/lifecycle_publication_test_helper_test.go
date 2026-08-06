package workflowstore

import (
	"context"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

func publishTaskStartForTest(
	ctx context.Context,
	store *Store,
	taskID workflow.TaskID,
) (StartTaskResult, error) {
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return StartTaskResult{}, err
	}
	return publication.PublishTaskStart(
		ctx,
		taskID,
		testsetup.PreparedPublicationStage(NewTaskStartLifecycleDelta),
	)
}

func publishCurrentNodeAdmissionForTest(
	ctx context.Context,
	store *Store,
	reference workflow.CurrentNodeReference,
) error {
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return err
	}
	delta, err := NewTaskLifecycleDelta(reference.TaskID, []LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      LifecycleFieldAbsent,
		Next:        LifecycleFieldPresent,
	}}, nil)
	if err != nil {
		return err
	}
	if err := publication.Publish(ctx, delta); err != nil {
		return err
	}
	return publication.PublishCurrentNodeAdmission(ctx, reference)
}
