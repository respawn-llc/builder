package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type TaskSessionAssociationRequest struct {
	SessionID    runtimeids.SessionID
	CurrentNode  workflow.CurrentNodeReference
	AssociatedAt time.Time
}

type TaskSessionAssociation struct {
	SessionID    runtimeids.SessionID
	CurrentNode  workflow.CurrentNodeReference
	AssociatedAt time.Time
}

func (s *Store) AssociateTaskSession(ctx context.Context, req TaskSessionAssociationRequest) (TaskSessionAssociation, error) {
	normalized, err := normalizeTaskSessionAssociationRequest(req)
	if err != nil {
		return TaskSessionAssociation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskSessionAssociation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	bound, err := q.BindSessionToTask(ctx, sqlitegen.BindSessionToTaskParams{
		TaskID:    sql.NullString{String: string(normalized.CurrentNode.TaskID), Valid: true},
		SessionID: normalized.SessionID.String(),
	})
	if err != nil {
		return TaskSessionAssociation{}, err
	}
	if bound != 1 {
		return TaskSessionAssociation{}, errors.New("session cannot be bound to task")
	}
	if branchKey, branchScoped := normalized.CurrentNode.TransitionBranchKey(); branchScoped {
		err = q.UpsertBranchSessionWorkflowNodeAssociation(ctx, sqlitegen.UpsertBranchSessionWorkflowNodeAssociationParams{
			SessionID:           normalized.SessionID.String(),
			NodeID:              string(normalized.CurrentNode.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
			AssociatedAtUnixMs:  normalized.AssociatedAt.UnixMilli(),
		})
	} else {
		err = q.UpsertSerialSessionWorkflowNodeAssociation(ctx, sqlitegen.UpsertSerialSessionWorkflowNodeAssociationParams{
			SessionID:          normalized.SessionID.String(),
			NodeID:             string(normalized.CurrentNode.NodeID),
			AssociatedAtUnixMs: normalized.AssociatedAt.UnixMilli(),
		})
	}
	if err != nil {
		return TaskSessionAssociation{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskSessionAssociation{}, err
	}
	return TaskSessionAssociation{
		SessionID:    normalized.SessionID,
		CurrentNode:  normalized.CurrentNode,
		AssociatedAt: normalized.AssociatedAt,
	}, nil
}

func (s *Store) CountTaskSessions(ctx context.Context, taskID workflow.TaskID) (int64, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return 0, errors.New("task id is required")
	}
	return s.queries.CountTaskSessions(ctx, sql.NullString{String: string(taskID), Valid: true})
}

func (s *Store) LatestTaskSessionForNode(ctx context.Context, currentNode workflow.CurrentNodeReference) (TaskSessionAssociation, error) {
	if err := currentNode.Validate(); err != nil {
		return TaskSessionAssociation{}, err
	}
	var sessionIDRaw string
	var associatedAtUnixMs int64
	if branchKey, branchScoped := currentNode.TransitionBranchKey(); branchScoped {
		row, err := s.queries.GetLatestBranchTaskSessionAssociationForNode(ctx, sqlitegen.GetLatestBranchTaskSessionAssociationForNodeParams{
			TaskID:              sql.NullString{String: string(currentNode.TaskID), Valid: true},
			NodeID:              string(currentNode.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
		})
		if err != nil {
			return TaskSessionAssociation{}, err
		}
		sessionIDRaw = row.SessionID
		associatedAtUnixMs = row.AssociatedAtUnixMs
	} else {
		row, err := s.queries.GetLatestSerialTaskSessionAssociationForNode(ctx, sqlitegen.GetLatestSerialTaskSessionAssociationForNodeParams{
			TaskID: sql.NullString{String: string(currentNode.TaskID), Valid: true},
			NodeID: string(currentNode.NodeID),
		})
		if err != nil {
			return TaskSessionAssociation{}, err
		}
		sessionIDRaw = row.SessionID
		associatedAtUnixMs = row.AssociatedAtUnixMs
	}
	sessionID, err := runtimeids.ParseSessionID(sessionIDRaw)
	if err != nil {
		return TaskSessionAssociation{}, fmt.Errorf("decode associated session id: %w", err)
	}
	return TaskSessionAssociation{
		SessionID:    sessionID,
		CurrentNode:  currentNode,
		AssociatedAt: time.UnixMilli(associatedAtUnixMs).UTC(),
	}, nil
}

func normalizeTaskSessionAssociationRequest(req TaskSessionAssociationRequest) (TaskSessionAssociationRequest, error) {
	if req.SessionID.IsZero() {
		return TaskSessionAssociationRequest{}, errors.New("session id is required")
	}
	if err := req.CurrentNode.Validate(); err != nil {
		return TaskSessionAssociationRequest{}, err
	}
	if req.AssociatedAt.IsZero() || req.AssociatedAt.UnixMilli() <= 0 {
		return TaskSessionAssociationRequest{}, errors.New("association time is required")
	}
	req.AssociatedAt = req.AssociatedAt.UTC().Truncate(time.Millisecond)
	return req, nil
}
