package workflowtest

import (
	"context"

	"core/server/workflow"
	"core/server/workflowstore"
)

func ApplyPendingApproval(
	store *workflowstore.Store,
	ctx context.Context,
	approvalID workflow.ApprovalID,
) (workflowstore.PendingApprovalApplyResult, error) {
	approval, err := store.PendingApproval(ctx, approvalID)
	if err != nil {
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	publication, err := workflowstore.NewLifecyclePublication(store)
	if err != nil {
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	defer func() { _ = publication.Close() }()
	return publication.PublishPendingApproval(
		ctx,
		approvalID,
		func(result workflowstore.PendingApprovalApplyResult) (workflowstore.TaskLifecycleDelta, func(error), error) {
			changes := []workflowstore.LifecycleRunDelta{{
				CurrentNode: approval.Source,
				Expect:      workflowstore.LifecycleFieldAbsent,
				Next:        workflowstore.LifecycleFieldAbsent,
			}}
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
			delta, err := workflowstore.NewTaskLifecycleDelta(approval.Source.TaskID, changes, nil)
			return delta, nil, err
		},
	)
}
