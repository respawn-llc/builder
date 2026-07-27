package workflowstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// ListCurrentNodesByTask returns the exact durable Current Nodes for each
// requested Task. It preserves the store's canonical Current Node decoding so
// read models never infer workflow state from SQL row shapes.
func (s *Store) ListCurrentNodesByTask(ctx context.Context, taskIDs []workflow.TaskID) (map[workflow.TaskID][]workflow.CurrentNode, error) {
	if len(taskIDs) == 0 {
		return map[workflow.TaskID][]workflow.CurrentNode{}, nil
	}
	ids := make([]string, 0, len(taskIDs))
	nodesByTask := make(map[workflow.TaskID][]workflow.CurrentNode, len(taskIDs))
	for _, taskID := range taskIDs {
		if strings.TrimSpace(string(taskID)) == "" {
			return nil, errors.New("task id is required")
		}
		if _, exists := nodesByTask[taskID]; exists {
			return nil, errors.New("task id is duplicated")
		}
		ids = append(ids, string(taskID))
		nodesByTask[taskID] = []workflow.CurrentNode{}
	}
	rows, err := s.queries.ListTaskCurrentNodesByTasks(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		currentNode, err := currentNodeFromRow(sqlitegen.ListTaskCurrentNodesRow(row))
		if err != nil {
			return nil, err
		}
		taskID := currentNode.Reference.TaskID
		if _, requested := nodesByTask[taskID]; !requested {
			return nil, fmt.Errorf("current node query returned unrequested task %q", taskID)
		}
		nodesByTask[taskID] = append(nodesByTask[taskID], currentNode)
	}
	return nodesByTask, nil
}

func currentNodeForReference(ctx context.Context, q *sqlitegen.Queries, reference workflow.CurrentNodeReference) (workflow.CurrentNode, error) {
	currentNodes, err := listTaskCurrentNodes(ctx, q, reference.TaskID)
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

func newReadyCurrentNode(taskID workflow.TaskID, nodeID workflow.NodeID, enteredByEdgeID workflow.EdgeID) (workflow.CurrentNode, error) {
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return workflow.NewCurrentNodeWithEntry(reference, &enteredByEdgeID, nil, nil, nil, &workflow.CurrentNodeScheduling{
		State: workflow.CurrentNodeSchedulingReady,
	})
}

func currentNodeFromRow(row sqlitegen.ListTaskCurrentNodesRow) (workflow.CurrentNode, error) {
	var branchKey *workflow.TransitionBranchKey
	if row.TransitionBranchKey.Valid {
		value := workflow.TransitionBranchKey(row.TransitionBranchKey.String)
		branchKey = &value
	}
	var enteredByEdgeID *workflow.EdgeID
	if row.EnteredByEdgeID.Valid {
		value := workflow.EdgeID(row.EnteredByEdgeID.String)
		enteredByEdgeID = &value
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
	currentNode, err := workflow.NewCurrentNodeWithEntry(reference, enteredByEdgeID, currentInputValues, priorNodeValues, sessionID, scheduling)
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

func currentNodeSchedulingFromRow(row sqlitegen.ListTaskCurrentNodesRow) (*workflow.CurrentNodeScheduling, error) {
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

func deleteTaskCurrentNode(ctx context.Context, q *sqlitegen.Queries, reference workflow.CurrentNodeReference) (int64, error) {
	if err := reference.Validate(); err != nil {
		return 0, err
	}
	branchKey := sql.NullString{}
	if value, present := reference.TransitionBranchKey(); present {
		branchKey = sql.NullString{String: string(value), Valid: true}
	}
	return q.DeleteTaskCurrentNode(ctx, sqlitegen.DeleteTaskCurrentNodeParams{
		TaskID:              string(reference.TaskID),
		NodeID:              string(reference.NodeID),
		TransitionBranchKey: branchKey,
	})
}

// AdmitCurrentNode atomically moves a ready executable Current Node into the
// durable restart-marker state before Workflow Execution starts its
// process-local Exact Execution Scope.
func (s *Store) AdmitCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	var (
		admitted int64
		err      error
	)
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		admitted, err = s.queries.AdmitBranchCurrentNode(ctx, sqlitegen.AdmitBranchCurrentNodeParams{
			TaskID:              string(reference.TaskID),
			NodeID:              string(reference.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
		})
	} else {
		admitted, err = s.queries.AdmitSerialCurrentNode(ctx, sqlitegen.AdmitSerialCurrentNodeParams{
			TaskID: string(reference.TaskID),
			NodeID: string(reference.NodeID),
		})
	}
	if err != nil {
		return err
	}
	if admitted != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// ResumeCurrentNode clears an interrupted restart marker. Workflow Execution
// immediately follows it with AdmitCurrentNode under the same mutation permit;
// it is deliberately not an automatic recovery path.
func (s *Store) ResumeCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference) (InterruptedCurrentNodeAttentionProjection, bool, error) {
	if err := reference.Validate(); err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	projection, found, err := pendingInterruptedCurrentNodeAttentionProjection(ctx, q, reference)
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	var (
		resumed   int64
		resumeErr error
	)
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		resumed, resumeErr = q.ResumeBranchCurrentNode(ctx, sqlitegen.ResumeBranchCurrentNodeParams{
			TaskID:              string(reference.TaskID),
			NodeID:              string(reference.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
		})
	} else {
		resumed, resumeErr = q.ResumeSerialCurrentNode(ctx, sqlitegen.ResumeSerialCurrentNodeParams{
			TaskID: string(reference.TaskID),
			NodeID: string(reference.NodeID),
		})
	}
	if resumeErr != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, resumeErr
	}
	if resumed != 1 {
		return InterruptedCurrentNodeAttentionProjection{}, false, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	return projection, found, nil
}

// InterruptedExecutableCurrentNodes returns the exact interrupted nodes a
// caller may explicitly resume. Pending Approval sources are excluded here
// and again atomically by ResumeCurrentNode/AdmitCurrentNode.
func (s *Store) InterruptedExecutableCurrentNodes(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	definition, _, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return nil, err
	}
	currentNodes, err := s.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	interrupted := make([]workflow.CurrentNode, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil || currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			continue
		}
		node, err := currentNodeDefinitionNode(definition, currentNode.Reference.NodeID)
		if err != nil {
			return nil, err
		}
		if !executableNodeKind(node.Kind()) {
			continue
		}
		eligible, err := s.IsCurrentNodeExecutionEligible(ctx, currentNode.Reference)
		if err != nil {
			return nil, err
		}
		if eligible {
			interrupted = append(interrupted, currentNode)
		}
	}
	return interrupted, nil
}

// InterruptAdmittedCurrentNode records that a scope preparation or live
// execution failed after admission. The caller owns exact-scope validation;
// persistence only accepts the durable admitted marker so a stale predecessor
// cannot overwrite a successor or a manually changed Current Node.
func (s *Store) InterruptAdmittedCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(reason)) == "" {
		return errors.New("current node interruption reason is required")
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode current node interruption detail: %w", err)
	}
	now := s.now().UTC().UnixMilli()
	var interrupted int64
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		interrupted, err = s.queries.InterruptBranchAdmittedCurrentNode(ctx, sqlitegen.InterruptBranchAdmittedCurrentNodeParams{
			TaskID:                 string(reference.TaskID),
			NodeID:                 string(reference.NodeID),
			TransitionBranchKey:    sql.NullString{String: string(branchKey), Valid: true},
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: string(detailJSON), Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	} else {
		interrupted, err = s.queries.InterruptSerialAdmittedCurrentNode(ctx, sqlitegen.InterruptSerialAdmittedCurrentNodeParams{
			TaskID:                 string(reference.TaskID),
			NodeID:                 string(reference.NodeID),
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: string(detailJSON), Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	}
	if err != nil {
		return err
	}
	if interrupted != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// InterruptCurrentNode records an interruption for ready or admitted
// executable work. It is used by Workflow Execution while draining its own
// in-memory intents and gates; it never authorizes an interrupt from durable
// state alone.
func (s *Store) InterruptCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(reason)) == "" {
		return errors.New("current node interruption reason is required")
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode current node interruption detail: %w", err)
	}
	now := s.now().UTC().UnixMilli()
	var interrupted int64
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		interrupted, err = s.queries.InterruptBranchCurrentNode(ctx, sqlitegen.InterruptBranchCurrentNodeParams{
			TaskID:                 string(reference.TaskID),
			NodeID:                 string(reference.NodeID),
			TransitionBranchKey:    sql.NullString{String: string(branchKey), Valid: true},
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: string(detailJSON), Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	} else {
		interrupted, err = s.queries.InterruptSerialCurrentNode(ctx, sqlitegen.InterruptSerialCurrentNodeParams{
			TaskID:                 string(reference.TaskID),
			NodeID:                 string(reference.NodeID),
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: string(detailJSON), Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	}
	if err != nil {
		return err
	}
	if interrupted != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// RecoverExecutableCurrentNodes turns ready or admitted executable work left
// by a previous process into resumable interruption state. Pending Approval
// sources remain frozen and no Automatic Intent is reconstructed.
func (s *Store) RecoverExecutableCurrentNodes(
	ctx context.Context,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
) ([]workflow.CurrentNodeReference, error) {
	if strings.TrimSpace(string(reason)) == "" {
		return nil, errors.New("current node interruption reason is required")
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("encode current node interruption detail: %w", err)
	}
	rows, err := s.queries.RecoverExecutableCurrentNodes(ctx, sqlitegen.RecoverExecutableCurrentNodesParams{
		InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
		InterruptionDetailJson: sql.NullString{String: string(detailJSON), Valid: true},
		InterruptedAtUnixMs:    sql.NullInt64{Int64: s.now().UTC().UnixMilli(), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	references := make([]workflow.CurrentNodeReference, 0, len(rows))
	for _, row := range rows {
		var branchKey *workflow.TransitionBranchKey
		if row.TransitionBranchKey.Valid {
			value := workflow.TransitionBranchKey(row.TransitionBranchKey.String)
			branchKey = &value
		}
		reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(row.TaskID), workflow.NodeID(row.NodeID), branchKey)
		if err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, nil
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
		EnteredByEdgeID:        sql.NullString{},
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
	if currentNode.EnteredByEdgeID != nil {
		params.EnteredByEdgeID = sql.NullString{String: string(*currentNode.EnteredByEdgeID), Valid: true}
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
