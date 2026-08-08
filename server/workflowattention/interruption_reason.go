package workflowattention

import "core/server/workflow"

func ShouldNotifyInterruptedCurrentNode(reason workflow.CurrentNodeInterruptionReason) bool {
	return workflow.IsActionableCurrentNodeInterruptionReason(reason)
}
