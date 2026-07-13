package workflowstore

import (
	"strings"

	"core/server/metadata"
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
		AutomationRequestedAt:   metadata.OptionalInt64(row.AutomationRequestedAtUnixMs),
		StartedAt:               metadata.OptionalInt64(row.StartedAtUnixMs),
		CompletedAt:             metadata.OptionalInt64(row.CompletedAtUnixMs),
		InterruptedAt:           metadata.OptionalInt64(row.InterruptedAtUnixMs),
		InterruptionReason:      metadata.OptionalString(row.InterruptionReason),
		WaitingAskID:            metadata.OptionalString(row.WaitingAskID),
		EffectiveCompletionMode: metadata.OptionalString(row.EffectiveCompletionMode),
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
		StartedAt:               metadata.OptionalInt64(row.StartedAtUnixMs),
		CompletedAt:             metadata.OptionalInt64(row.CompletedAtUnixMs),
		InterruptedAt:           metadata.OptionalInt64(row.InterruptedAtUnixMs),
		InterruptionReason:      metadata.OptionalString(row.InterruptionReason),
		WaitingAskID:            metadata.OptionalString(row.WaitingAskID),
		EffectiveCompletionMode: metadata.OptionalString(row.EffectiveCompletionMode),
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
		AutomationRequestedAt:   metadata.OptionalInt64(row.AutomationRequestedAtUnixMs),
		StartedAt:               metadata.OptionalInt64(row.StartedAtUnixMs),
		CompletedAt:             metadata.OptionalInt64(row.CompletedAtUnixMs),
		InterruptedAt:           metadata.OptionalInt64(row.InterruptedAtUnixMs),
		InterruptionReason:      metadata.OptionalString(row.InterruptionReason),
		WaitingAskID:            metadata.OptionalString(row.WaitingAskID),
		EffectiveCompletionMode: metadata.OptionalString(row.EffectiveCompletionMode),
		InvalidCompletions:      row.InvalidCompletionCount,
	}
}

func taskRecordFromTask(row sqlitegen.TaskRecord) (TaskRecord, error) {
	executionTarget, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		return TaskRecord{}, err
	}
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
		ExecutionTarget:   executionTarget,
		CanceledAt:        metadata.OptionalInt64(row.CanceledAtUnixMs),
		CancelReason:      metadata.OptionalString(row.CancellationReason),
		Version:           row.WorkflowRevisionSeen,
	}, nil
}
