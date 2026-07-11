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
		AutomationRequestedAt:   optionalInt64(row.AutomationRequestedAtUnixMs),
		StartedAt:               optionalInt64(row.StartedAtUnixMs),
		CompletedAt:             optionalInt64(row.CompletedAtUnixMs),
		InterruptedAt:           optionalInt64(row.InterruptedAtUnixMs),
		InterruptionReason:      optionalString(row.InterruptionReason),
		WaitingAskID:            optionalString(row.WaitingAskID),
		EffectiveCompletionMode: optionalString(row.EffectiveCompletionMode),
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
		StartedAt:               optionalInt64(row.StartedAtUnixMs),
		CompletedAt:             optionalInt64(row.CompletedAtUnixMs),
		InterruptedAt:           optionalInt64(row.InterruptedAtUnixMs),
		InterruptionReason:      optionalString(row.InterruptionReason),
		WaitingAskID:            optionalString(row.WaitingAskID),
		EffectiveCompletionMode: optionalString(row.EffectiveCompletionMode),
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
		AutomationRequestedAt:   optionalInt64(row.AutomationRequestedAtUnixMs),
		StartedAt:               optionalInt64(row.StartedAtUnixMs),
		CompletedAt:             optionalInt64(row.CompletedAtUnixMs),
		InterruptedAt:           optionalInt64(row.InterruptedAtUnixMs),
		InterruptionReason:      optionalString(row.InterruptionReason),
		WaitingAskID:            optionalString(row.WaitingAskID),
		EffectiveCompletionMode: optionalString(row.EffectiveCompletionMode),
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
		CancelReason:      optionalString(row.CancellationReason),
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

func optionalString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	valueCopy := value.String
	return &valueCopy
}
