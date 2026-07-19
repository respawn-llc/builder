package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type InterruptedRunAttentionProjection struct {
	ProjectID              string
	WorkflowID             string
	TaskID                 workflow.TaskID
	TaskShortID            string
	TaskTitle              string
	RunID                  workflow.RunID
	SessionID              string
	InterruptionReason     string
	InterruptionDetailJSON *string
	OccurredAtUnixMs       int64
}

func (s *Store) PendingApprovalTransitionProjection(ctx context.Context, transitionID workflow.TransitionID) (ApprovalTransitionProjection, bool, error) {
	id := strings.TrimSpace(string(transitionID))
	if id == "" {
		return ApprovalTransitionProjection{}, false, ErrTransitionIDRequired
	}
	row, err := s.queries.GetWorkflowApprovalAttentionCandidateByTransitionID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovalTransitionProjection{}, false, nil
	}
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	taskID, err := requiredAttentionCandidateString(row, "task_id", row.TaskID)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	taskShortID, err := requiredAttentionCandidateString(row, "short_id", row.ShortID)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	taskTitle, err := requiredAttentionCandidateString(row, "title", row.Title)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	sourceRunID, err := requiredAttentionCandidateString(row, "run_id", row.RunID)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	sessionID, err := requiredAttentionCandidateString(row, "session_id", row.SessionID)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	candidateTransitionID, err := requiredAttentionCandidateString(row, "task_transition_id", row.TaskTransitionID)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	if candidateTransitionID != id {
		return ApprovalTransitionProjection{}, false, fmt.Errorf("workflow attention candidate invariant violated: kind=%q id=%q task_transition_id=%q, want %q", row.Kind, row.ID, candidateTransitionID, id)
	}
	return ApprovalTransitionProjection{
		TransitionID:     transitionID,
		ProjectID:        row.ProjectID,
		WorkflowID:       row.WorkflowID,
		TaskID:           workflow.TaskID(taskID),
		TaskShortID:      taskShortID,
		TaskTitle:        taskTitle,
		SourceRunID:      workflow.RunID(sourceRunID),
		SessionID:        sessionID,
		OccurredAtUnixMs: row.OccurredAtUnixMs,
	}, true, nil
}

func (s *Store) PendingInterruptedRunAttentionProjection(ctx context.Context, runID workflow.RunID) (InterruptedRunAttentionProjection, bool, error) {
	id := strings.TrimSpace(string(runID))
	if id == "" {
		return InterruptedRunAttentionProjection{}, false, ErrRunIDRequired
	}
	row, err := s.queries.GetWorkflowInterruptedRunAttentionCandidateByRunID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return InterruptedRunAttentionProjection{}, false, nil
	}
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	taskID, err := requiredAttentionCandidateString(row, "task_id", row.TaskID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	taskShortID, err := requiredAttentionCandidateString(row, "short_id", row.ShortID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	taskTitle, err := requiredAttentionCandidateString(row, "title", row.Title)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	candidateRunID, err := requiredAttentionCandidateString(row, "run_id", row.RunID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	if candidateRunID != id {
		return InterruptedRunAttentionProjection{}, false, fmt.Errorf("workflow attention candidate invariant violated: kind=%q id=%q run_id=%q, want %q", row.Kind, row.ID, candidateRunID, id)
	}
	sessionID, err := requiredAttentionCandidateString(row, "session_id", row.SessionID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	reason, err := requiredAttentionCandidateString(row, "interruption_reason", row.InterruptionReason)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	return InterruptedRunAttentionProjection{
		ProjectID:              row.ProjectID,
		WorkflowID:             row.WorkflowID,
		TaskID:                 workflow.TaskID(taskID),
		TaskShortID:            taskShortID,
		TaskTitle:              taskTitle,
		RunID:                  runID,
		SessionID:              sessionID,
		InterruptionReason:     reason,
		InterruptionDetailJSON: optionalAttentionCandidateString(row.InterruptionDetailJson),
		OccurredAtUnixMs:       row.OccurredAtUnixMs,
	}, true, nil
}

func requiredAttentionCandidateString(row sqlitegen.WorkflowAttentionCandidate, field string, value sql.NullString) (string, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return "", fmt.Errorf("workflow attention candidate invariant violated: kind=%q id=%q field=%q is absent", row.Kind, row.ID, field)
	}
	return value.String, nil
}

func optionalAttentionCandidateString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}
