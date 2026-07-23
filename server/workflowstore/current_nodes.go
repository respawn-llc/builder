package workflowstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func (s *Store) ListCurrentNodes(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, fmt.Errorf("task id is required")
	}
	return listTaskCurrentNodes(ctx, s.queries, taskID)
}

func currentNodeForReference(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID, nodeID workflow.NodeID) (workflow.CurrentNode, error) {
	currentNodes, err := listTaskCurrentNodes(ctx, q, taskID)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	for _, currentNode := range currentNodes {
		if currentNode.Reference.Equal(reference) {
			return currentNode, nil
		}
	}
	return workflow.CurrentNode{}, sql.ErrNoRows
}

func listTaskCurrentNodes(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	rows, err := q.ListTaskCurrentNodes(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	currentNodes := make([]workflow.CurrentNode, 0, len(rows))
	for _, row := range rows {
		currentNode, err := currentNodeFromRow(row)
		if err != nil {
			return nil, err
		}
		currentNodes = append(currentNodes, currentNode)
	}
	return currentNodes, nil
}

func newBacklogCurrentNode(taskID workflow.TaskID, nodeID workflow.NodeID) (workflow.CurrentNode, error) {
	return newNonExecutableCurrentNode(taskID, nodeID)
}

func newNonExecutableCurrentNode(taskID workflow.TaskID, nodeID workflow.NodeID) (workflow.CurrentNode, error) {
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return workflow.NewCurrentNode(reference, nil, nil)
}

func newReadyCurrentNode(taskID workflow.TaskID, nodeID workflow.NodeID) (workflow.CurrentNode, error) {
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return workflow.NewCurrentNode(reference, nil, &workflow.CurrentNodeScheduling{
		State: workflow.CurrentNodeSchedulingReady,
	})
}

func currentNodeFromRow(row sqlitegen.TaskCurrentNode) (workflow.CurrentNode, error) {
	var branchKey *workflow.TransitionBranchKey
	if row.TransitionBranchKey.Valid {
		value := workflow.TransitionBranchKey(row.TransitionBranchKey.String)
		branchKey = &value
	}
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(row.TaskID), workflow.NodeID(row.NodeID), branchKey)
	if err != nil {
		return workflow.CurrentNode{}, fmt.Errorf("decode current node reference: %w", err)
	}
	var sessionID *runtimeids.SessionID
	if row.SessionID.Valid {
		parsed, err := runtimeids.ParseSessionID(row.SessionID.String)
		if err != nil {
			return workflow.CurrentNode{}, fmt.Errorf("decode current node session: %w", err)
		}
		sessionID = &parsed
	}
	scheduling, err := currentNodeSchedulingFromRow(row)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentInputValues, err := currentNodeInputValuesFromJSON(row.CurrentInputValuesJson)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	priorNodeValues, err := priorNodeValuesFromJSON(row.PriorNodeValuesJson)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentNode, err := workflow.NewCurrentNodeWithMaterializedValues(reference, currentInputValues, priorNodeValues, sessionID, scheduling)
	if err != nil {
		return workflow.CurrentNode{}, fmt.Errorf("decode current node: %w", err)
	}
	return currentNode, nil
}

func currentNodeInputValuesFromJSON(raw string) (map[string]string, error) {
	values := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode current node input values: %w", err)
	}
	if values == nil {
		return nil, fmt.Errorf("decode current node input values: expected object")
	}
	return values, nil
}

func priorNodeValuesFromJSON(raw string) (map[string]map[string]string, error) {
	values := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode current node prior node values: %w", err)
	}
	if values == nil {
		return nil, fmt.Errorf("decode current node prior node values: expected object")
	}
	return values, nil
}

func currentNodeSchedulingFromRow(row sqlitegen.TaskCurrentNode) (*workflow.CurrentNodeScheduling, error) {
	if !row.SchedulingState.Valid {
		return nil, nil
	}
	scheduling := &workflow.CurrentNodeScheduling{
		State: workflow.CurrentNodeSchedulingState(row.SchedulingState.String),
	}
	if scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		return scheduling, nil
	}
	var detail workflow.CurrentNodeInterruptionDetail
	if err := json.Unmarshal([]byte(row.InterruptionDetailJson.String), &detail); err != nil {
		return nil, fmt.Errorf("decode current node interruption detail: %w", err)
	}
	scheduling.Interruption = &workflow.CurrentNodeInterruption{
		Reason:     workflow.CurrentNodeInterruptionReason(row.InterruptionReason.String),
		Detail:     detail,
		OccurredAt: time.UnixMilli(row.InterruptedAtUnixMs.Int64).UTC(),
	}
	return scheduling, nil
}

func insertTaskCurrentNode(ctx context.Context, q *sqlitegen.Queries, currentNode workflow.CurrentNode) error {
	params, err := taskCurrentNodeInsertParams(currentNode)
	if err != nil {
		return err
	}
	return q.InsertTaskCurrentNode(ctx, params)
}

func taskCurrentNodeInsertParams(currentNode workflow.CurrentNode) (sqlitegen.InsertTaskCurrentNodeParams, error) {
	currentInputValuesJSON, err := json.Marshal(currentNode.CurrentInputValues)
	if err != nil {
		return sqlitegen.InsertTaskCurrentNodeParams{}, fmt.Errorf("encode current node input values: %w", err)
	}
	priorNodeValuesJSON, err := json.Marshal(currentNode.PriorNodeValues)
	if err != nil {
		return sqlitegen.InsertTaskCurrentNodeParams{}, fmt.Errorf("encode current node prior node values: %w", err)
	}
	params := sqlitegen.InsertTaskCurrentNodeParams{
		TaskID:                 string(currentNode.Reference.TaskID),
		NodeID:                 string(currentNode.Reference.NodeID),
		TransitionBranchKey:    sql.NullString{},
		CurrentInputValuesJson: string(currentInputValuesJSON),
		PriorNodeValuesJson:    string(priorNodeValuesJSON),
		SessionID:              sql.NullString{},
		SchedulingState:        sql.NullString{},
		InterruptionReason:     sql.NullString{},
		InterruptionDetailJson: sql.NullString{},
		InterruptedAtUnixMs:    sql.NullInt64{},
	}
	if branchKey, ok := currentNode.Reference.TransitionBranchKey(); ok {
		params.TransitionBranchKey = sql.NullString{String: string(branchKey), Valid: true}
	}
	if currentNode.SessionID != nil {
		params.SessionID = sql.NullString{String: currentNode.SessionID.String(), Valid: true}
	}
	if currentNode.Scheduling == nil {
		return params, nil
	}
	params.SchedulingState = sql.NullString{String: string(currentNode.Scheduling.State), Valid: true}
	if currentNode.Scheduling.Interruption == nil {
		return params, nil
	}
	detailJSON, err := json.Marshal(currentNode.Scheduling.Interruption.Detail)
	if err != nil {
		return sqlitegen.InsertTaskCurrentNodeParams{}, fmt.Errorf("encode current node interruption detail: %w", err)
	}
	params.InterruptionReason = sql.NullString{
		String: string(currentNode.Scheduling.Interruption.Reason),
		Valid:  true,
	}
	params.InterruptionDetailJson = sql.NullString{String: string(detailJSON), Valid: true}
	params.InterruptedAtUnixMs = sql.NullInt64{
		Int64: currentNode.Scheduling.Interruption.OccurredAt.UnixMilli(),
		Valid: true,
	}
	return params, nil
}
