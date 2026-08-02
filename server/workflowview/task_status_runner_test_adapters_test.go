package workflowview

import (
	"context"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
)

func (r *controllerBackedTaskStatusRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ workflowexecution.LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer workflowexecution.CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return r.StartCurrentNode(ctx, reference, delivery, steer, lease, controller)
}

func (taskStatusProjectionTestRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ workflowexecution.LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer workflowexecution.CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return taskStatusProjectionTestRunner{}.StartCurrentNode(ctx, reference, delivery, steer, lease, controller)
}
