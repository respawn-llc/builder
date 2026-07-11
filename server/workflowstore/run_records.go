package workflowstore

import (
	"database/sql"
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
		StartedAt:               row.StartedAtUnixMs.Int64,
		CompletedAt:             row.CompletedAtUnixMs.Int64,
		InterruptedAt:           row.InterruptedAtUnixMs.Int64,
		InterruptionReason:      row.InterruptionReason.String,
		WaitingAskID:            row.WaitingAskID.String,
		EffectiveCompletionMode: strings.TrimSpace(row.EffectiveCompletionMode.String),
		InvalidCompletions:      row.InvalidCompletionCount,
	}
}

func runRecordFromStartedRecoveryCandidate(row sqlitegen.ListStartedWorkflowRunRecoveryCandidatesRow) RunRecord {
	return RunRecord{
		ID:                      workflow.RunID(row.ID),
		TaskID:                  workflow.TaskID(row.TaskID),
		PlacementID:             workflow.PlacementID(row.PlacementID),
		NodeID:                  workflow.NodeID(row.NodeID.String),
		SessionID:               row.SessionID.String,
		Generation:              row.RunGeneration,
		StartedAt:               row.StartedAtUnixMs.Int64,
		CompletedAt:             row.CompletedAtUnixMs.Int64,
		InterruptedAt:           row.InterruptedAtUnixMs.Int64,
		InterruptionReason:      row.InterruptionReason.String,
		WaitingAskID:            row.WaitingAskID.String,
		EffectiveCompletionMode: strings.TrimSpace(row.EffectiveCompletionMode.String),
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
		StartedAt:               row.StartedAtUnixMs.Int64,
		CompletedAt:             row.CompletedAtUnixMs.Int64,
		InterruptedAt:           row.InterruptedAtUnixMs.Int64,
		InterruptionReason:      row.InterruptionReason.String,
		WaitingAskID:            row.WaitingAskID.String,
		EffectiveCompletionMode: strings.TrimSpace(row.EffectiveCompletionMode.String),
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
		CanceledAt:        optionalInt64(row.CanceledAtUnixMs),
		CancelReason:      row.CancellationReason.String,
		Version:           row.WorkflowRevisionSeen,
	}
}

func optionalInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	valueCopy := value.Int64
	return &valueCopy
}
