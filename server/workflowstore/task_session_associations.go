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
	return latestTaskSessionForNode(ctx, s.queries, currentNode)
}

// ResolveCurrentSessionStartContext reconstructs a legacy runner input from the
// Session's direct current-state ownership. It deliberately does not read
// durable Session metadata or accept a Run ID from Session persistence.
func (s *Store) ResolveCurrentSessionStartContext(ctx context.Context, sessionID runtimeids.SessionID) (RunStartContext, error) {
	if sessionID.IsZero() {
		return RunStartContext{}, errors.New("session id is required")
	}
	taskIDs, err := s.queries.ListSessionWorkflowTaskIDs(ctx, sessionID.String())
	if err != nil {
		return RunStartContext{}, err
	}
	if len(taskIDs) == 0 {
		return RunStartContext{}, sql.ErrNoRows
	}
	if len(taskIDs) != 1 || !taskIDs[0].Valid {
		return RunStartContext{}, fmt.Errorf("session %q has invalid task ownership", sessionID)
	}
	taskID := workflow.TaskID(taskIDs[0].String)
	currentNodes, err := s.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return RunStartContext{}, err
	}
	var currentNode *workflow.CurrentNode
	for i := range currentNodes {
		if currentNodes[i].SessionID == nil || *currentNodes[i].SessionID != sessionID {
			continue
		}
		if currentNode != nil {
			return RunStartContext{}, fmt.Errorf("session %q is bound to multiple current nodes for task %q", sessionID, taskID)
		}
		currentNode = &currentNodes[i]
	}
	if currentNode == nil {
		return RunStartContext{}, sql.ErrNoRows
	}
	association, err := s.LatestTaskSessionForNode(ctx, currentNode.Reference)
	if err != nil {
		return RunStartContext{}, fmt.Errorf("resolve current session node association: %w", err)
	}
	if association.SessionID != sessionID {
		return RunStartContext{}, fmt.Errorf("current node %v is associated with session %q, want %q", currentNode.Reference, association.SessionID, sessionID)
	}
	runs, err := s.ListRuns(ctx, taskID)
	if err != nil {
		return RunStartContext{}, err
	}
	var matchingRun *RunRecord
	for i := range runs {
		run := &runs[i]
		if run.SessionID != sessionID.String() || run.NodeID != currentNode.Reference.NodeID || run.CompletedAt != nil {
			continue
		}
		if matchingRun != nil {
			return RunStartContext{}, fmt.Errorf("session %q has multiple unfinished runs for current node %v", sessionID, currentNode.Reference)
		}
		matchingRun = run
	}
	if matchingRun == nil {
		return RunStartContext{}, sql.ErrNoRows
	}
	return s.GetRunStartContext(ctx, matchingRun.ID)
}

func latestTaskSessionForNode(ctx context.Context, q *sqlitegen.Queries, currentNode workflow.CurrentNodeReference) (TaskSessionAssociation, error) {
	if err := currentNode.Validate(); err != nil {
		return TaskSessionAssociation{}, err
	}
	var sessionIDRaw string
	var associatedAtUnixMs int64
	if branchKey, branchScoped := currentNode.TransitionBranchKey(); branchScoped {
		row, err := q.GetLatestBranchTaskSessionAssociationForNode(ctx, sqlitegen.GetLatestBranchTaskSessionAssociationForNodeParams{
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
		row, err := q.GetLatestSerialTaskSessionAssociationForNode(ctx, sqlitegen.GetLatestSerialTaskSessionAssociationForNodeParams{
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
