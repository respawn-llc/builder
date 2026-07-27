package workflowexecution

import (
	"context"

	"core/server/workflow"
	"core/server/workflowstore"
)

func (c *CurrentNodeController) publishPendingInterruptedCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	reason workflow.CurrentNodeInterruptionReason,
) {
	if c == nil || c.attention == nil || !workflow.IsActionableCurrentNodeInterruptionReason(reason) {
		return
	}
	c.attention.PublishPendingInterruptedCurrentNode(ctx, reference)
}

func (c *CurrentNodeController) finalizeTaskAttentionResolution(resolution workflowstore.TaskAttentionResolution) {
	if c == nil || c.attention == nil ||
		(len(resolution.Approvals) == 0 && len(resolution.InterruptedCurrentNodes) == 0) {
		return
	}
	c.attention.FinalizeTaskResolution(resolution)
}
