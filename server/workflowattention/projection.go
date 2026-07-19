package workflowattention

import "core/server/workflowstore"

func ApprovalProjections(projections []workflowstore.ApprovalTransitionProjection) []ApprovalProjection {
	resolved := make([]ApprovalProjection, 0, len(projections))
	for _, projection := range projections {
		resolved = append(resolved, ApprovalProjection{
			TransitionID:     projection.TransitionID,
			ProjectID:        projection.ProjectID,
			WorkflowID:       projection.WorkflowID,
			TaskID:           projection.TaskID,
			TaskShortID:      projection.TaskShortID,
			TaskTitle:        projection.TaskTitle,
			RunID:            string(projection.SourceRunID),
			SessionID:        projection.SessionID,
			Message:          ApprovalRequiredMessage,
			OccurredAtUnixMs: projection.OccurredAtUnixMs,
		})
	}
	return resolved
}

func InterruptedRunProjections(projections []workflowstore.InterruptedRunAttentionProjection) []InterruptedRunProjection {
	resolved := make([]InterruptedRunProjection, 0, len(projections))
	for _, projection := range projections {
		detailJSON := ""
		if projection.InterruptionDetailJSON != nil {
			detailJSON = *projection.InterruptionDetailJSON
		}
		resolved = append(resolved, InterruptedRunProjection{
			ProjectID:        projection.ProjectID,
			WorkflowID:       projection.WorkflowID,
			TaskID:           projection.TaskID,
			TaskShortID:      projection.TaskShortID,
			TaskTitle:        projection.TaskTitle,
			RunID:            projection.RunID,
			SessionID:        projection.SessionID,
			Message:          InterruptedRunMessage(&projection.InterruptionReason, detailJSON),
			Reason:           projection.InterruptionReason,
			DetailJSON:       detailJSON,
			OccurredAtUnixMs: projection.OccurredAtUnixMs,
		})
	}
	return resolved
}
