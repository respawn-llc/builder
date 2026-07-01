package workflowstore

import (
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func runRecordFromTaskRun(row sqlitegen.TaskRunRecord) RunRecord {
	return RunRecord{
		ID:                      workflow.RunID(row.ID),
		TaskID:                  workflow.TaskID(row.TaskID),
		PlacementID:             workflow.PlacementID(row.PlacementID),
		NodeID:                  workflow.NodeID(row.NodeID.String),
		SessionID:               row.SessionID.String,
		Generation:              row.RunGeneration,
		AutomationRequestedAt:   row.AutomationRequestedAtUnixMs,
		StartedAt:               row.StartedAtUnixMs,
		CompletedAt:             row.CompletedAtUnixMs,
		InterruptedAt:           row.InterruptedAtUnixMs,
		InterruptionReason:      row.InterruptionReason,
		WaitingAskID:            row.WaitingAskID,
		EffectiveCompletionMode: strings.TrimSpace(row.EffectiveCompletionMode),
		InvalidCompletions:      row.InvalidCompletionCount,
	}
}

func runRecordFromClaimedTaskRun(row sqlitegen.ClaimWorkflowRunRow) RunRecord {
	return RunRecord{
		ID:                      workflow.RunID(row.ID),
		TaskID:                  workflow.TaskID(row.TaskID),
		PlacementID:             workflow.PlacementID(row.PlacementID),
		NodeID:                  workflow.NodeID(row.NodeID.String),
		SessionID:               row.SessionID.String,
		Generation:              row.RunGeneration,
		AutomationRequestedAt:   row.AutomationRequestedAtUnixMs,
		StartedAt:               row.StartedAtUnixMs,
		CompletedAt:             row.CompletedAtUnixMs,
		InterruptedAt:           row.InterruptedAtUnixMs,
		InterruptionReason:      row.InterruptionReason,
		WaitingAskID:            row.WaitingAskID,
		EffectiveCompletionMode: strings.TrimSpace(row.EffectiveCompletionMode),
		InvalidCompletions:      row.InvalidCompletionCount,
	}
}

func taskRecordFromTask(row sqlitegen.TaskRecord) TaskRecord {
	return TaskRecord{
		ID:                workflow.TaskID(row.ID),
		ProjectID:         row.ProjectID,
		WorkflowID:        workflow.WorkflowID(row.WorkflowID),
		LinkID:            row.ProjectWorkflowLinkID,
		ShortID:           row.ShortID,
		Title:             row.Title,
		Body:              row.Body,
		SourceURL:         row.SourceUrl,
		SourceWorkspaceID: strings.TrimSpace(row.SourceWorkspaceID.String),
		ManagedWorktreeID: strings.TrimSpace(row.ManagedWorktreeID.String),
		CanceledAt:        row.CanceledAtUnixMs,
		CancelReason:      row.CancellationReason,
		Version:           row.WorkflowRevisionSeen,
	}
}
