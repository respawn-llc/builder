package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
)

func deleteTaskThroughLifecyclePublication(
	t testing.TB,
	store *Store,
	ctx context.Context,
	taskID workflow.TaskID,
) DeleteTaskResult {
	t.Helper()
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication for Task deletion: %v", err)
	}
	deleted, err := publication.PublishTaskDeletion(ctx, taskID)
	if err != nil {
		t.Fatalf("PublishTaskDeletion: %v", err)
	}
	return deleted
}

func deleteWorkflowThroughLifecyclePublication(
	store *Store,
	ctx context.Context,
	req WorkflowDeleteRequest,
) (WorkflowDeleteResult, error) {
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return WorkflowDeleteResult{}, err
	}
	return publication.PublishWorkflowDeletion(ctx, req)
}

func deleteProjectThroughLifecyclePublication(
	store *Store,
	ctx context.Context,
	req ProjectDeleteRequest,
) ([]serverapi.ProjectDeleteBlocker, error) {
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return nil, err
	}
	return publication.PublishProjectDeletion(ctx, req)
}

func completeCurrentNodeForStoreTest(
	store *Store,
	ctx context.Context,
	req CurrentNodeCompletionRequest,
) (CurrentNodeCompletionResult, error) {
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return CurrentNodeCompletionResult{}, err
	}
	defer func() { _ = publication.Close() }()
	result, _, err := publication.PublishCurrentNodeCompletion(
		ctx,
		req,
		func(result CurrentNodeCompletionResult) (TaskLifecycleDelta, func(error), error) {
			changes := []LifecycleRunDelta{{
				CurrentNode: req.Source,
				Expect:      LifecycleFieldAbsent,
				Next:        LifecycleFieldAbsent,
			}}
			for _, intent := range result.AutomaticIntents {
				changes = append(changes, LifecycleRunDelta{
					CurrentNode: intent.CurrentNode,
					Expect:      LifecycleFieldAbsent,
					Next:        LifecycleFieldPresent,
				})
			}
			delta, err := NewTaskLifecycleDelta(req.Source.TaskID, changes, nil)
			return delta, nil, err
		},
	)
	return result, err
}

func applyPendingApprovalForStoreTest(
	store *Store,
	ctx context.Context,
	approvalID workflow.ApprovalID,
) (PendingApprovalApplyResult, error) {
	approval, err := store.PendingApproval(ctx, approvalID)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	defer func() { _ = publication.Close() }()
	return publication.PublishPendingApproval(
		ctx,
		approvalID,
		func(result PendingApprovalApplyResult) (TaskLifecycleDelta, func(error), error) {
			changes := []LifecycleRunDelta{{
				CurrentNode: approval.Source,
				Expect:      LifecycleFieldAbsent,
				Next:        LifecycleFieldAbsent,
			}}
			for _, currentNode := range result.Mutation.Created {
				if currentNode.Scheduling == nil {
					continue
				}
				changes = append(changes, LifecycleRunDelta{
					CurrentNode: currentNode.Reference,
					Expect:      LifecycleFieldAbsent,
					Next:        LifecycleFieldPresent,
				})
			}
			delta, err := NewTaskLifecycleDelta(approval.Source.TaskID, changes, nil)
			return delta, nil, err
		},
	)
}

func applyManualMoveForStoreTest(
	store *Store,
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
) (ManualMoveResult, error) {
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer func() { _ = publication.Close() }()
	return publication.PublishManualMove(
		ctx,
		prepared,
		executionTarget,
		func(result ManualMoveResult) (TaskLifecycleDelta, func(error), error) {
			changes := make([]LifecycleRunDelta, 0, len(result.Mutation.Removed)+len(result.Mutation.Created))
			for _, reference := range result.Mutation.Removed {
				changes = append(changes, LifecycleRunDelta{
					CurrentNode: reference,
					Expect:      LifecycleFieldAbsent,
					Next:        LifecycleFieldAbsent,
				})
			}
			for _, currentNode := range result.Mutation.Created {
				if currentNode.Scheduling == nil {
					continue
				}
				changes = append(changes, LifecycleRunDelta{
					CurrentNode: currentNode.Reference,
					Expect:      LifecycleFieldAbsent,
					Next:        LifecycleFieldPresent,
				})
			}
			delta, err := NewTaskLifecycleDelta(prepared.TaskID(), changes, nil)
			return delta, nil, err
		},
	)
}

func manualMoveTaskForStoreTest(
	store *Store,
	ctx context.Context,
	req ManualMoveRequest,
) (ManualMoveResult, error) {
	prepared, err := store.PrepareManualMove(ctx, req)
	if err != nil {
		return ManualMoveResult{}, err
	}
	return applyManualMoveForStoreTest(store, ctx, prepared, nil)
}
