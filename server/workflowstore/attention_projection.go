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
	projection, err := approvalTransitionProjectionFromCandidate(row, id)
	if err != nil {
		return ApprovalTransitionProjection{}, false, err
	}
	return projection, true, nil
}

func approvalTransitionProjectionFromCandidate(row sqlitegen.WorkflowAttentionCandidate, transitionID string) (ApprovalTransitionProjection, error) {
	taskID, err := requiredAttentionCandidateString(row, "task_id", row.TaskID)
	if err != nil {
		return ApprovalTransitionProjection{}, err
	}
	taskShortID, err := requiredAttentionCandidateString(row, "short_id", row.ShortID)
	if err != nil {
		return ApprovalTransitionProjection{}, err
	}
	taskTitle, err := requiredAttentionCandidateString(row, "title", row.Title)
	if err != nil {
		return ApprovalTransitionProjection{}, err
	}
	candidateTransitionID, err := requiredAttentionCandidateString(row, "task_transition_id", row.TaskTransitionID)
	if err != nil {
		return ApprovalTransitionProjection{}, err
	}
	if candidateTransitionID != transitionID {
		return ApprovalTransitionProjection{}, fmt.Errorf("workflow attention candidate invariant violated: kind=%q id=%q task_transition_id=%q, want %q", row.Kind, row.ID, candidateTransitionID, transitionID)
	}
	return ApprovalTransitionProjection{
		TransitionID:     workflow.TransitionID(transitionID),
		ProjectID:        row.ProjectID,
		WorkflowID:       row.WorkflowID,
		TaskID:           workflow.TaskID(taskID),
		TaskShortID:      taskShortID,
		TaskTitle:        taskTitle,
		SourceRunID:      workflow.RunID(optionalAttentionCandidateValue(row.RunID)),
		SessionID:        optionalAttentionCandidateValue(row.SessionID),
		OccurredAtUnixMs: row.OccurredAtUnixMs,
	}, nil
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
	projection, err := interruptedRunAttentionProjectionFromCandidate(row, id)
	if err != nil {
		return InterruptedRunAttentionProjection{}, false, err
	}
	return projection, true, nil
}

func interruptedRunAttentionProjectionFromCandidate(row sqlitegen.WorkflowAttentionCandidate, runID string) (InterruptedRunAttentionProjection, error) {
	taskID, err := requiredAttentionCandidateString(row, "task_id", row.TaskID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, err
	}
	taskShortID, err := requiredAttentionCandidateString(row, "short_id", row.ShortID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, err
	}
	taskTitle, err := requiredAttentionCandidateString(row, "title", row.Title)
	if err != nil {
		return InterruptedRunAttentionProjection{}, err
	}
	candidateRunID, err := requiredAttentionCandidateString(row, "run_id", row.RunID)
	if err != nil {
		return InterruptedRunAttentionProjection{}, err
	}
	if candidateRunID != runID {
		return InterruptedRunAttentionProjection{}, fmt.Errorf("workflow attention candidate invariant violated: kind=%q id=%q run_id=%q, want %q", row.Kind, row.ID, candidateRunID, runID)
	}
	reason, err := requiredAttentionCandidateString(row, "interruption_reason", row.InterruptionReason)
	if err != nil {
		return InterruptedRunAttentionProjection{}, err
	}
	return InterruptedRunAttentionProjection{
		ProjectID:              row.ProjectID,
		WorkflowID:             row.WorkflowID,
		TaskID:                 workflow.TaskID(taskID),
		TaskShortID:            taskShortID,
		TaskTitle:              taskTitle,
		RunID:                  workflow.RunID(runID),
		SessionID:              optionalAttentionCandidateValue(row.SessionID),
		InterruptionReason:     reason,
		InterruptionDetailJSON: optionalAttentionCandidateString(row.InterruptionDetailJson),
		OccurredAtUnixMs:       row.OccurredAtUnixMs,
	}, nil
}

func taskAttentionResolution(ctx context.Context, q *sqlitegen.Queries, taskID string) (TaskAttentionResolution, error) {
	rows, err := q.ListWorkflowTaskAttentionCandidates(ctx, taskID)
	if err != nil {
		return TaskAttentionResolution{}, err
	}
	return attentionResolutionFromCandidates(rows)
}

func workflowAttentionResolution(ctx context.Context, q *sqlitegen.Queries, workflowID string) (TaskAttentionResolution, error) {
	rows, err := q.ListWorkflowResolutionAttentionCandidates(ctx, workflowID)
	if err != nil {
		return TaskAttentionResolution{}, err
	}
	return attentionResolutionFromCandidates(rows)
}

func attentionResolutionFromCandidates(rows []sqlitegen.WorkflowAttentionCandidate) (TaskAttentionResolution, error) {
	var resolution TaskAttentionResolution
	for _, row := range rows {
		switch row.Kind {
		case "approval":
			transitionID, err := requiredAttentionCandidateString(row, "task_transition_id", row.TaskTransitionID)
			if err != nil {
				return TaskAttentionResolution{}, err
			}
			projection, err := approvalTransitionProjectionFromCandidate(row, transitionID)
			if err != nil {
				return TaskAttentionResolution{}, err
			}
			resolution.ResolvedApprovalTransitionProjections = append(resolution.ResolvedApprovalTransitionProjections, projection)
		case "interrupted_run":
			runID, err := requiredAttentionCandidateString(row, "run_id", row.RunID)
			if err != nil {
				return TaskAttentionResolution{}, err
			}
			projection, err := interruptedRunAttentionProjectionFromCandidate(row, runID)
			if err != nil {
				return TaskAttentionResolution{}, err
			}
			resolution.ResolvedInterruptedRunProjections = append(resolution.ResolvedInterruptedRunProjections, projection)
		}
	}
	return resolution, nil
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

func optionalAttentionCandidateValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
