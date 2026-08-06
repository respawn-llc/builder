package workflowtest

import (
	"context"

	"core/server/workflowstore"
)

func ApplyManualMove(
	store *workflowstore.Store,
	ctx context.Context,
	prepared workflowstore.ManualMovePreparation,
	executionTarget *workflowstore.ExecutionTargetCandidate,
) (workflowstore.ManualMoveResult, error) {
	publication, err := workflowstore.NewLifecyclePublication(store)
	if err != nil {
		return workflowstore.ManualMoveResult{}, err
	}
	defer func() { _ = publication.Close() }()
	return publication.PublishManualMove(
		ctx,
		prepared,
		executionTarget,
		func(result workflowstore.ManualMoveResult) (workflowstore.TaskLifecycleDelta, func(error), error) {
			changes := make([]workflowstore.LifecycleRunDelta, 0, len(result.Mutation.Removed)+len(result.Mutation.Created))
			for _, reference := range result.Mutation.Removed {
				changes = append(changes, workflowstore.LifecycleRunDelta{
					CurrentNode: reference,
					Expect:      workflowstore.LifecycleFieldAbsent,
					Next:        workflowstore.LifecycleFieldAbsent,
				})
			}
			for _, currentNode := range result.Mutation.Created {
				if currentNode.Scheduling == nil {
					continue
				}
				changes = append(changes, workflowstore.LifecycleRunDelta{
					CurrentNode: currentNode.Reference,
					Expect:      workflowstore.LifecycleFieldAbsent,
					Next:        workflowstore.LifecycleFieldPresent,
				})
			}
			delta, err := workflowstore.NewTaskLifecycleDelta(prepared.TaskID(), changes, nil)
			return delta, nil, err
		},
	)
}

func ManualMoveTask(
	store *workflowstore.Store,
	ctx context.Context,
	req workflowstore.ManualMoveRequest,
) (workflowstore.ManualMoveResult, error) {
	prepared, err := store.PrepareManualMove(ctx, req)
	if err != nil {
		return workflowstore.ManualMoveResult{}, err
	}
	return ApplyManualMove(store, ctx, prepared, nil)
}
