package workflowtest

import (
	"context"

	"core/server/workflowstore"
)

func CompleteCurrentNode(
	store *workflowstore.Store,
	ctx context.Context,
	req workflowstore.CurrentNodeCompletionRequest,
) (workflowstore.CurrentNodeCompletionResult, error) {
	publication, err := workflowstore.NewLifecyclePublication(store)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	defer func() { _ = publication.Close() }()
	return publication.PublishCurrentNodeCompletion(
		ctx,
		req,
		func(result workflowstore.CurrentNodeCompletionResult) (workflowstore.TaskLifecycleDelta, func(error), error) {
			changes := []workflowstore.LifecycleRunDelta{{
				CurrentNode: req.Source,
				Expect:      workflowstore.LifecycleFieldAbsent,
				Next:        workflowstore.LifecycleFieldAbsent,
			}}
			for _, intent := range result.AutomaticIntents {
				changes = append(changes, workflowstore.LifecycleRunDelta{
					CurrentNode: intent.CurrentNode,
					Expect:      workflowstore.LifecycleFieldAbsent,
					Next:        workflowstore.LifecycleFieldPresent,
				})
			}
			delta, err := workflowstore.NewTaskLifecycleDelta(req.Source.TaskID, changes, nil)
			return delta, nil, err
		},
	)
}
