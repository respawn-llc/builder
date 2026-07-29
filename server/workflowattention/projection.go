package workflowattention

import "core/server/workflowstore"

func ApprovalProjectionFromStore(projection workflowstore.ApprovalAttentionProjection) ApprovalProjection {
	return ApprovalProjection{
		ApprovalID:       projection.ApprovalID,
		Source:           projection.Source,
		ProjectID:        projection.ProjectID,
		WorkflowID:       projection.WorkflowID,
		TaskShortID:      projection.TaskShortID,
		TaskTitle:        projection.TaskTitle,
		SessionID:        projection.SessionID,
		Message:          ApprovalRequiredMessage,
		OccurredAtUnixMs: projection.OccurredAtUnixMs,
	}
}

func ApprovalProjections(projections []workflowstore.ApprovalAttentionProjection) []ApprovalProjection {
	out := make([]ApprovalProjection, 0, len(projections))
	for _, projection := range projections {
		out = append(out, ApprovalProjectionFromStore(projection))
	}
	return out
}

func InterruptedCurrentNodeProjectionFromStore(projection workflowstore.InterruptedCurrentNodeAttentionProjection) InterruptedCurrentNodeProjection {
	return InterruptedCurrentNodeProjection{
		CurrentNode:      projection.CurrentNode,
		ProjectID:        projection.ProjectID,
		WorkflowID:       projection.WorkflowID,
		TaskShortID:      projection.TaskShortID,
		TaskTitle:        projection.TaskTitle,
		SessionID:        projection.SessionID,
		Message:          InterruptedCurrentNodeMessage(projection.InterruptionReason, projection.InterruptionDetailJSON),
		Reason:           projection.InterruptionReason,
		DetailJSON:       projection.InterruptionDetailJSON,
		OccurredAtUnixMs: projection.OccurredAtUnixMs,
	}
}

func InterruptedCurrentNodeProjections(projections []workflowstore.InterruptedCurrentNodeAttentionProjection) []InterruptedCurrentNodeProjection {
	out := make([]InterruptedCurrentNodeProjection, 0, len(projections))
	for _, projection := range projections {
		out = append(out, InterruptedCurrentNodeProjectionFromStore(projection))
	}
	return out
}

func (f *Finalizer) FinalizeTaskResolution(resolution workflowstore.TaskAttentionResolution) {
	f.FinalizeResolution(Resolution{
		Approvals:               ApprovalProjections(resolution.Approvals),
		InterruptedCurrentNodes: InterruptedCurrentNodeProjections(resolution.InterruptedCurrentNodes),
	})
}
