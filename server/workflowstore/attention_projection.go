package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type ApprovalAttentionProjection struct {
	ApprovalID       workflow.ApprovalID
	Source           workflow.CurrentNodeReference
	ProjectID        string
	WorkflowID       runtimeids.WorkflowID
	TaskShortID      string
	TaskTitle        string
	SessionID        string
	OccurredAtUnixMs int64
}

type InterruptedCurrentNodeAttentionProjection struct {
	CurrentNode            workflow.CurrentNodeReference
	ProjectID              string
	WorkflowID             runtimeids.WorkflowID
	TaskShortID            string
	TaskTitle              string
	SessionID              string
	InterruptionReason     string
	InterruptionDetailJSON string
	OccurredAtUnixMs       int64
}

type TaskAttentionResolution struct {
	Approvals               []ApprovalAttentionProjection
	InterruptedCurrentNodes []InterruptedCurrentNodeAttentionProjection
}

func (s *Store) PendingApprovalAttentionProjection(ctx context.Context, approvalID workflow.ApprovalID) (ApprovalAttentionProjection, bool, error) {
	if err := approvalID.Validate(); err != nil {
		return ApprovalAttentionProjection{}, false, err
	}
	return pendingApprovalAttentionProjection(ctx, s.queries, approvalID)
}

func pendingApprovalAttentionProjection(ctx context.Context, q *sqlitegen.Queries, approvalID workflow.ApprovalID) (ApprovalAttentionProjection, bool, error) {
	row, err := q.GetTaskPendingApproval(ctx, approvalID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovalAttentionProjection{}, false, nil
	}
	if err != nil {
		return ApprovalAttentionProjection{}, false, err
	}
	approval, err := pendingApprovalFromRow(ctx, q, row)
	if err != nil {
		return ApprovalAttentionProjection{}, false, err
	}
	task, err := q.GetTask(ctx, string(approval.Source.TaskID))
	if err != nil {
		return ApprovalAttentionProjection{}, false, err
	}
	sessionID := ""
	if approval.SourceSessionID != nil {
		sessionID = approval.SourceSessionID.String()
	}
	return ApprovalAttentionProjection{
		ApprovalID:       approval.ID,
		Source:           approval.Source,
		ProjectID:        task.ProjectID,
		WorkflowID:       task.WorkflowID,
		TaskShortID:      task.ShortID,
		TaskTitle:        task.Title,
		SessionID:        sessionID,
		OccurredAtUnixMs: approval.CreatedAt.UnixMilli(),
	}, true, nil
}

func (s *Store) PendingInterruptedCurrentNodeAttentionProjection(ctx context.Context, reference workflow.CurrentNodeReference) (InterruptedCurrentNodeAttentionProjection, bool, error) {
	if err := reference.Validate(); err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	return s.pendingInterruptedCurrentNodeAttentionProjection(ctx, s.queries, reference)
}

func (s *Store) pendingInterruptedCurrentNodeAttentionProjection(ctx context.Context, q *sqlitegen.Queries, reference workflow.CurrentNodeReference) (InterruptedCurrentNodeAttentionProjection, bool, error) {
	currentNode, err := s.currentNodeForReference(ctx, q, reference)
	if errors.Is(err, sql.ErrNoRows) {
		return InterruptedCurrentNodeAttentionProjection{}, false, nil
	}
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	if currentNode.Scheduling == nil || currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted || currentNode.Scheduling.Interruption == nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, nil
	}
	reason := string(currentNode.Scheduling.Interruption.Reason)
	if !attentionInterruptionRequiresNotification(reason) {
		return InterruptedCurrentNodeAttentionProjection{}, false, nil
	}
	task, err := q.GetTask(ctx, string(reference.TaskID))
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	detail, err := workflow.MarshalString(currentNode.Scheduling.Interruption.Detail)
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, fmt.Errorf("encode interrupted current node attention detail: %w", err)
	}
	sessionID := ""
	if currentNode.SessionID != nil {
		sessionID = currentNode.SessionID.String()
	}
	return InterruptedCurrentNodeAttentionProjection{
		CurrentNode:            reference,
		ProjectID:              task.ProjectID,
		WorkflowID:             task.WorkflowID,
		TaskShortID:            task.ShortID,
		TaskTitle:              task.Title,
		SessionID:              sessionID,
		InterruptionReason:     reason,
		InterruptionDetailJSON: detail,
		OccurredAtUnixMs:       currentNode.Scheduling.Interruption.OccurredAt.UnixMilli(),
	}, true, nil
}

func (s *Store) taskAttentionResolution(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) (TaskAttentionResolution, error) {
	resolution, err := taskApprovalAttentionResolution(ctx, q, taskID)
	if err != nil {
		return TaskAttentionResolution{}, err
	}
	currentNodes, err := s.listTaskCurrentNodes(ctx, q, taskID)
	if err != nil {
		return TaskAttentionResolution{}, err
	}
	for _, currentNode := range currentNodes {
		projection, found, err := s.pendingInterruptedCurrentNodeAttentionProjection(ctx, q, currentNode.Reference)
		if err != nil {
			return TaskAttentionResolution{}, err
		}
		if found {
			resolution.InterruptedCurrentNodes = append(resolution.InterruptedCurrentNodes, projection)
		}
	}
	return resolution, nil
}

func taskApprovalAttentionResolution(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) (TaskAttentionResolution, error) {
	approvals, err := q.ListTaskPendingApprovals(ctx, string(taskID))
	if err != nil {
		return TaskAttentionResolution{}, err
	}
	resolution := TaskAttentionResolution{}
	for _, row := range approvals {
		approvalID, err := workflow.ParseApprovalID(row.ID)
		if err != nil {
			return TaskAttentionResolution{}, err
		}
		projection, found, err := pendingApprovalAttentionProjection(ctx, q, approvalID)
		if err != nil {
			return TaskAttentionResolution{}, err
		}
		if !found {
			return TaskAttentionResolution{}, fmt.Errorf("pending approval %q disappeared during attention resolution", row.ID)
		}
		resolution.Approvals = append(resolution.Approvals, projection)
	}
	return resolution, nil
}

func (s *Store) workflowAttentionResolution(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID) (TaskAttentionResolution, error) {
	taskIDs, err := q.ListWorkflowTaskIDs(ctx, workflowID)
	if err != nil {
		return TaskAttentionResolution{}, err
	}
	var resolution TaskAttentionResolution
	for _, taskID := range taskIDs {
		taskResolution, err := s.taskAttentionResolution(ctx, q, workflow.TaskID(taskID))
		if err != nil {
			return TaskAttentionResolution{}, err
		}
		resolution.Approvals = append(resolution.Approvals, taskResolution.Approvals...)
		resolution.InterruptedCurrentNodes = append(resolution.InterruptedCurrentNodes, taskResolution.InterruptedCurrentNodes...)
	}
	return resolution, nil
}

func attentionInterruptionRequiresNotification(reason string) bool {
	return workflow.IsActionableCurrentNodeInterruptionReason(
		workflow.CurrentNodeInterruptionReason(strings.TrimSpace(reason)),
	)
}
