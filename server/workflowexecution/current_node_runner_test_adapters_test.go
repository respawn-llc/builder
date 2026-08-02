package workflowexecution

import (
	"context"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
)

func startLegacyCurrentNode(
	start func(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.TaskPromptDelivery,
		CurrentNodeAssignmentSteer,
		sessionruntime.WorkflowExecutionLease,
		workflowruntime.Controller,
	) error,
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	delivery workflowruntime.TaskPromptDelivery,
	steer CurrentNodeAssignmentSteer,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) error {
	return start(ctx, reference, delivery, steer, lease, controller)
}

func (r *controlledScriptRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r failingCurrentNodeRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *blockingCurrentNodeRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *countingCurrentNodeRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *boundedExplicitAdmissionRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *runningAndFinalizingScriptRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *runningAndQueuedGateRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *parallelExplicitRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *firstAdmissionBlockingScriptRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *completingScriptRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *recordingScriptRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *selectiveScriptFailureRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}

func (r *finalizingBeforeLiveRunner) StartCurrentNodeWithPreparation(ctx context.Context, reference workflow.CurrentNodeReference, _ LaunchPreparation, delivery workflowruntime.TaskPromptDelivery, steer CurrentNodeAssignmentSteer, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	return startLegacyCurrentNode(r.StartCurrentNode, ctx, reference, delivery, steer, lease, controller)
}
