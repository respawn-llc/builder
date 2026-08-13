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
	"core/shared/jsoncontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

// CurrentNodeInterruptionPostCommitDiagnostic reports that interruption
// scheduling committed but its Task event could not be delivered.
type CurrentNodeInterruptionPostCommitDiagnostic struct {
	Reference workflow.CurrentNodeReference
}

func (e *CurrentNodeInterruptionPostCommitDiagnostic) Error() string {
	if e == nil {
		return "current node interruption committed with a Task event diagnostic"
	}
	return fmt.Sprintf("current node interruption %v committed with a Task event diagnostic", e.Reference)
}

func currentNodeInterruptionPostCommitDiagnostic(
	reference workflow.CurrentNodeReference,
	cause error,
) error {
	if cause == nil {
		return nil
	}
	return errors.Join(
		&CurrentNodeInterruptionPostCommitDiagnostic{Reference: reference},
		cause,
	)
}

func (s *Store) ListCurrentNodes(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, fmt.Errorf("task id is required")
	}
	return s.listTaskCurrentNodes(ctx, s.queries, taskID)
}

func (s *Store) publishCurrentNodeTaskEvent(ctx context.Context, taskID workflow.TaskID, action serverapi.WorkflowProjectEventAction) error {
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return fmt.Errorf("read task for current node event: %w", err)
	}
	workflowID := task.WorkflowID
	if err := s.PublishWorkflowEvent(ctx, WorkflowEventRecord{
		ProjectID:       &task.ProjectID,
		WorkflowID:      &workflowID,
		Resource:        serverapi.WorkflowProjectEventResourceTask,
		Action:          action,
		PrimaryEntityID: string(taskID),
	}); err != nil {
		return fmt.Errorf("publish current node task event: %w", err)
	}
	return nil
}

// ListCurrentNodesByTask returns the exact durable Current Nodes for each
// requested Task. It preserves the store's canonical Current Node decoding so
// read models never infer workflow state from SQL row shapes.
func (s *Store) ListCurrentNodesByTask(ctx context.Context, taskIDs []workflow.TaskID) (map[workflow.TaskID][]workflow.CurrentNode, error) {
	if s == nil {
		return nil, errors.New("workflow store is required")
	}
	return s.ListCurrentNodesByTaskWithQueries(ctx, s.queries, taskIDs)
}

func interruptCurrentNodeQuery(
	ctx context.Context,
	q *sqlitegen.Queries,
	reference workflow.CurrentNodeReference,
	reason workflow.CurrentNodeInterruptionReason,
	detailJSON string,
	now int64,
) (int64, error) {
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		return q.InterruptBranchCurrentNode(ctx, sqlitegen.InterruptBranchCurrentNodeParams{
			TaskID:                 string(reference.TaskID),
			NodeID:                 string(reference.NodeID),
			TransitionBranchKey:    sql.NullString{String: string(branchKey), Valid: true},
			InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
			InterruptionDetailJson: sql.NullString{String: detailJSON, Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
		})
	}
	return q.InterruptSerialCurrentNode(ctx, sqlitegen.InterruptSerialCurrentNodeParams{
		TaskID:                 string(reference.TaskID),
		NodeID:                 string(reference.NodeID),
		InterruptionReason:     sql.NullString{String: string(reason), Valid: true},
		InterruptionDetailJson: sql.NullString{String: detailJSON, Valid: true},
		InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
	})
}

// ListCurrentNodesByTaskWithQueries decodes Current Nodes through the supplied
// generated queries. A read-only transaction can therefore own the query
// generation without duplicating the canonical Current Node decoder.
func (s *Store) ListCurrentNodesByTaskWithQueries(ctx context.Context, q *sqlitegen.Queries, taskIDs []workflow.TaskID) (map[workflow.TaskID][]workflow.CurrentNode, error) {
	if s == nil {
		return nil, errors.New("workflow store is required")
	}
	if q == nil {
		return nil, errors.New("workflow queries are required")
	}
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
	rows, err := q.ListTaskCurrentNodesByTasks(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		currentNode, err := s.currentNodeFromRow(sqlitegen.ListTaskCurrentNodesRow(row))
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

func (s *Store) currentNodeForReference(ctx context.Context, q *sqlitegen.Queries, reference workflow.CurrentNodeReference) (workflow.CurrentNode, error) {
	currentNodes, err := s.listTaskCurrentNodes(ctx, q, reference.TaskID)
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

func (s *Store) listTaskCurrentNodes(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	rows, err := q.ListTaskCurrentNodes(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	currentNodes := make([]workflow.CurrentNode, 0, len(rows))
	for _, row := range rows {
		currentNode, err := s.currentNodeFromRow(row)
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
	return newNonExecutableCurrentNodeWithPriorValues(taskID, nodeID, workflow.MaterializedPriorValues{})
}

func newNonExecutableCurrentNodeWithPriorValues(
	taskID workflow.TaskID,
	nodeID workflow.NodeID,
	priorValues workflow.MaterializedPriorValues,
) (workflow.CurrentNode, error) {
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	return workflow.NewCurrentNodeWithMaterializedValues(reference, nil, priorValues, nil, nil)
}

func newReadyCurrentNode(taskID workflow.TaskID, nodeID workflow.NodeID, enteredByEdgeID workflow.EdgeID, selection *workflow.AgentExecutionSelection) (workflow.CurrentNode, error) {
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentNode, err := workflow.NewCurrentNodeWithMaterializedSource(
		reference,
		nil,
		workflow.MaterializedPriorValues{},
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
		&workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		selection,
	)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentNode.EnteredByEdgeID = &enteredByEdgeID
	return currentNode, nil
}

func (s *Store) currentNodeFromRow(row sqlitegen.ListTaskCurrentNodesRow) (workflow.CurrentNode, error) {
	var branchKey *workflow.TransitionBranchKey
	if row.TransitionBranchKey.Valid {
		value := workflow.TransitionBranchKey(row.TransitionBranchKey.String)
		branchKey = &value
	}
	var enteredByEdgeID *workflow.EdgeID
	enteredByEdge, err := nullableGraphIdentity(row.EnteredByEdgeID)
	if err != nil {
		return workflow.CurrentNode{}, fmt.Errorf("decode current node entering edge: %w", err)
	}
	if enteredByEdge != nil {
		value := workflow.EdgeID(*enteredByEdge)
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
	priorValues, err := priorValuesFromJSON(s.priorValuesContract, row.PriorNodeValuesJson)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	continuationSource, err := materializedContinuationSourceFromColumns(
		row.ContinuationSourceKind,
		row.ContinuationSourceSessionID,
		row.LegacyMaterialized,
	)
	if err != nil {
		return workflow.CurrentNode{}, fmt.Errorf("decode current node continuation source: %w", err)
	}
	selection, err := currentNodeExecutionSelectionFromRow(row)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	currentNode, err := workflow.NewCurrentNodeWithMaterializedSource(
		reference,
		currentInputValues,
		priorValues,
		sessionID,
		continuationSource,
		scheduling,
		selection,
	)
	if err != nil {
		return workflow.CurrentNode{}, fmt.Errorf("decode current node: %w", err)
	}
	currentNode.EnteredByEdgeID = enteredByEdgeID
	return currentNode, nil
}

func currentNodeExecutionSelectionFromRow(row sqlitegen.ListTaskCurrentNodesRow) (*workflow.AgentExecutionSelection, error) {
	kind := workflow.NodeKind(strings.TrimSpace(row.NodeKind))
	assigneePresent := row.EffectiveAssignee.Valid
	thinkingPresent := row.EffectiveThinking.Valid
	originPresent := row.AssigneeOrigin.Valid
	switch kind {
	case workflow.NodeKindAgent:
		if !assigneePresent || !originPresent {
			return nil, errors.New("Agent current node requires effective assignee and origin")
		}
	case workflow.NodeKindStart, workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
		if assigneePresent || thinkingPresent || originPresent {
			return nil, fmt.Errorf("%s current node cannot carry Agent execution selection", kind)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("current node node kind %q is invalid", kind)
	}
	var thinking *workflow.ThinkingValue
	if thinkingPresent {
		value, err := workflow.NewThinkingValue(row.EffectiveThinking.String)
		if err != nil {
			return nil, fmt.Errorf("decode current node thinking: %w", err)
		}
		thinking = &value
	}
	selection, err := workflow.NewAgentExecutionSelection(
		row.EffectiveAssignee.String,
		thinking,
		workflow.AssigneeOrigin(row.AssigneeOrigin.String),
	)
	if err != nil {
		return nil, fmt.Errorf("decode current node Agent execution selection: %w", err)
	}
	return &selection, nil
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

func priorValuesFromJSON(contract jsoncontract.Internal, raw string) (workflow.MaterializedPriorValues, error) {
	if err := contract.Validate([]byte(raw)); err != nil {
		return workflow.MaterializedPriorValues{}, fmt.Errorf("decode current node prior values: %w", err)
	}
	values := workflow.MaterializedPriorValues{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return workflow.MaterializedPriorValues{}, fmt.Errorf("decode current node prior values: %w", err)
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

func insertTaskCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNode,
	associatedAt time.Time,
) error {
	node, err := q.GetWorkflowNode(ctx, string(currentNode.Reference.NodeID))
	if err != nil {
		return fmt.Errorf("resolve current node kind: %w", err)
	}
	return insertTaskCurrentNodeWithKind(ctx, q, currentNode, workflow.NodeKind(node.Kind), associatedAt)
}

func insertTaskCurrentNodeWithKind(
	ctx context.Context,
	q *sqlitegen.Queries,
	currentNode workflow.CurrentNode,
	nodeKind workflow.NodeKind,
	associatedAt time.Time,
) error {
	switch nodeKind {
	case workflow.NodeKindAgent:
		if currentNode.AgentExecutionSelection == nil {
			return errors.New("Agent current node requires effective assignee and origin")
		}
	case workflow.NodeKindStart, workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
		if currentNode.AgentExecutionSelection != nil {
			return fmt.Errorf("%s current node cannot carry Agent execution selection", nodeKind)
		}
	default:
		return fmt.Errorf("current node node kind %q is invalid", nodeKind)
	}
	params, err := taskCurrentNodeInsertParams(currentNode)
	if err != nil {
		return err
	}
	if currentNode.SessionID != nil && nodeKind != workflow.NodeKindAgent {
		return fmt.Errorf("%s current node cannot retain a Session", nodeKind)
	}
	if err := q.InsertTaskCurrentNode(ctx, params); err != nil {
		return err
	}
	if currentNode.SessionID == nil {
		return nil
	}
	association, err := normalizeTaskSessionAssociationRequest(TaskSessionAssociationRequest{
		SessionID:    *currentNode.SessionID,
		CurrentNode:  currentNode.Reference,
		AssociatedAt: associatedAt,
	})
	if err != nil {
		return err
	}
	if err := bindSessionToTask(ctx, q, association); err != nil {
		return err
	}
	switch currentNode.ContinuationSource.Kind() {
	case workflow.MaterializedContinuationSourceExact:
		sourceSessionID, exact := currentNode.ContinuationSource.ExactSessionID()
		if !exact {
			return errors.New("exact retained current node omitted source Session")
		}
		return designateCurrentTaskSessionAssociation(ctx, q, association, sourceSessionID)
	case workflow.MaterializedContinuationSourceLegacy:
		return appendLegacyTaskSessionHistory(ctx, q, association)
	default:
		return fmt.Errorf(
			"retained current node has invalid continuation source kind %q",
			currentNode.ContinuationSource.Kind(),
		)
	}
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
	branchKey, branchScoped := reference.TransitionBranchKey()
	var branchValue any
	if branchScoped {
		branchValue = string(branchKey)
	}
	locked, err := q.AcquireCurrentNodeResumeWriteLock(ctx, sqlitegen.AcquireCurrentNodeResumeWriteLockParams{
		TaskID:              string(reference.TaskID),
		NodeID:              string(reference.NodeID),
		TransitionBranchKey: branchValue,
	})
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	if locked != 1 {
		return InterruptedCurrentNodeAttentionProjection{}, false, sql.ErrNoRows
	}
	projection, found, err := s.pendingInterruptedCurrentNodeAttentionProjection(ctx, q, reference)
	if err != nil {
		return InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	var (
		resumed   int64
		resumeErr error
	)
	if branchScoped {
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
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	definition, _, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return nil, err
	}
	return s.interruptedExecutableCurrentNodesWithDefinition(ctx, taskID, definition)
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
	return currentNodeInterruptionPostCommitDiagnostic(
		reference,
		s.publishCurrentNodeTaskEvent(ctx, reference.TaskID, serverapi.WorkflowProjectEventActionInterrupted),
	)
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
	interrupted, err := interruptCurrentNodeQuery(ctx, s.queries, reference, reason, string(detailJSON), now)
	if err != nil {
		return err
	}
	if interrupted != 1 {
		return sql.ErrNoRows
	}
	return currentNodeInterruptionPostCommitDiagnostic(
		reference,
		s.publishCurrentNodeTaskEvent(ctx, reference.TaskID, serverapi.WorkflowProjectEventActionInterrupted),
	)
}

func (s *Store) ReplaceUserInterruptionWithAssignmentFailure(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	detail workflow.CurrentNodeInterruptionDetail,
) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode current node interruption detail: %w", err)
	}
	now := s.now().UTC().UnixMilli()
	branchKey, branchScoped := reference.TransitionBranchKey()
	replaced, err := s.queries.ReplaceUserInterruptionWithAssignmentFailure(
		ctx,
		sqlitegen.ReplaceUserInterruptionWithAssignmentFailureParams{
			InterruptionDetailJson: sql.NullString{String: string(detailJSON), Valid: true},
			InterruptedAtUnixMs:    sql.NullInt64{Int64: now, Valid: true},
			TaskID:                 string(reference.TaskID),
			NodeID:                 string(reference.NodeID),
			TransitionBranchKey:    sql.NullString{String: string(branchKey), Valid: branchScoped},
		},
	)
	if err != nil {
		return err
	}
	if replaced != 1 {
		return sql.ErrNoRows
	}
	return currentNodeInterruptionPostCommitDiagnostic(
		reference,
		s.publishCurrentNodeTaskEvent(ctx, reference.TaskID, serverapi.WorkflowProjectEventActionInterrupted),
	)
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
		reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(row.TaskID), workflow.NodeID(row.Column2), branchKey)
		if err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	seenTasks := make(map[workflow.TaskID]struct{}, len(references))
	for _, reference := range references {
		if _, seen := seenTasks[reference.TaskID]; seen {
			continue
		}
		seenTasks[reference.TaskID] = struct{}{}
		if err := s.publishCurrentNodeTaskEvent(ctx, reference.TaskID, serverapi.WorkflowProjectEventActionInterrupted); err != nil {
			return references, currentNodeInterruptionPostCommitDiagnostic(reference, err)
		}
	}
	return references, nil
}

func taskCurrentNodeInsertParams(currentNode workflow.CurrentNode) (sqlitegen.InsertTaskCurrentNodeParams, error) {
	currentInputValuesJSON, err := json.Marshal(currentNode.CurrentInputValues)
	if err != nil {
		return sqlitegen.InsertTaskCurrentNodeParams{}, fmt.Errorf("encode current node input values: %w", err)
	}
	priorValuesJSON, err := json.Marshal(currentNode.PriorValues)
	if err != nil {
		return sqlitegen.InsertTaskCurrentNodeParams{}, fmt.Errorf("encode current node prior values: %w", err)
	}
	params := sqlitegen.InsertTaskCurrentNodeParams{
		TaskID:                 string(currentNode.Reference.TaskID),
		NodeID:                 string(currentNode.Reference.NodeID),
		TransitionBranchKey:    sql.NullString{},
		EnteredByEdgeID:        sql.NullString{},
		CurrentInputValuesJson: string(currentInputValuesJSON),
		PriorNodeValuesJson:    string(priorValuesJSON),
		SessionID:              sql.NullString{},
		SchedulingState:        sql.NullString{},
		InterruptionReason:     sql.NullString{},
		InterruptionDetailJson: sql.NullString{},
		InterruptedAtUnixMs:    sql.NullInt64{},
		EffectiveAssignee:      sql.NullString{},
		EffectiveThinking:      sql.NullString{},
		AssigneeOrigin:         sql.NullString{},
	}
	sourceKind, sourceSessionID, legacyMaterialized, err := materializedContinuationSourceColumns(currentNode.ContinuationSource)
	if err != nil {
		return sqlitegen.InsertTaskCurrentNodeParams{}, fmt.Errorf("encode current node continuation source: %w", err)
	}
	params.ContinuationSourceKind = sourceKind
	params.ContinuationSourceSessionID = sourceSessionID
	params.LegacyMaterialized = legacyMaterialized
	if branchKey, ok := currentNode.Reference.TransitionBranchKey(); ok {
		params.TransitionBranchKey = sql.NullString{String: string(branchKey), Valid: true}
	}
	if currentNode.EnteredByEdgeID != nil {
		params.EnteredByEdgeID = sql.NullString{String: string(*currentNode.EnteredByEdgeID), Valid: true}
	}
	if currentNode.SessionID != nil {
		params.SessionID = sql.NullString{String: currentNode.SessionID.String(), Valid: true}
	}
	if currentNode.AgentExecutionSelection != nil {
		params.EffectiveAssignee = sql.NullString{
			String: currentNode.AgentExecutionSelection.Assignee,
			Valid:  true,
		}
		params.AssigneeOrigin = sql.NullString{
			String: string(currentNode.AgentExecutionSelection.Origin),
			Valid:  true,
		}
		if currentNode.AgentExecutionSelection.Thinking != nil {
			params.EffectiveThinking = sql.NullString{
				String: string(*currentNode.AgentExecutionSelection.Thinking),
				Valid:  true,
			}
		}
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

func materializedContinuationSourceColumns(
	source workflow.MaterializedContinuationSource,
) (sql.NullString, sql.NullString, int64, error) {
	if err := source.Validate(); err != nil {
		return sql.NullString{}, sql.NullString{}, 0, err
	}
	switch source.Kind() {
	case workflow.MaterializedContinuationSourceExact:
		sessionID, ok := source.ExactSessionID()
		if !ok {
			return sql.NullString{}, sql.NullString{}, 0, errors.New("exact continuation source omitted Session ID")
		}
		return sql.NullString{String: "exact", Valid: true},
			sql.NullString{String: sessionID.String(), Valid: true},
			0,
			nil
	case workflow.MaterializedContinuationSourceDeferredSelf:
		return sql.NullString{String: "deferred_self", Valid: true}, sql.NullString{}, 0, nil
	case workflow.MaterializedContinuationSourceAbsent:
		return sql.NullString{String: "absent", Valid: true}, sql.NullString{}, 0, nil
	case workflow.MaterializedContinuationSourceLegacy:
		return sql.NullString{}, sql.NullString{}, 1, nil
	default:
		return sql.NullString{}, sql.NullString{}, 0, fmt.Errorf("unsupported continuation source kind %q", source.Kind())
	}
}

func materializedContinuationSourceFromColumns(
	kind sql.NullString,
	sessionID sql.NullString,
	legacyMaterialized int64,
) (workflow.MaterializedContinuationSource, error) {
	switch legacyMaterialized {
	case 1:
		if kind.Valid || sessionID.Valid {
			return workflow.MaterializedContinuationSource{}, errors.New("legacy continuation source cannot carry exact source fields")
		}
		return workflow.LegacyMaterializedContinuationSource(), nil
	case 0:
	default:
		return workflow.MaterializedContinuationSource{}, fmt.Errorf("legacy materialized value %d is invalid", legacyMaterialized)
	}
	if !kind.Valid {
		return workflow.MaterializedContinuationSource{}, errors.New("continuation source kind is required")
	}
	switch kind.String {
	case "exact":
		if !sessionID.Valid {
			return workflow.MaterializedContinuationSource{}, errors.New("exact continuation source Session ID is required")
		}
		parsed, err := runtimeids.ParseSessionID(sessionID.String)
		if err != nil {
			return workflow.MaterializedContinuationSource{}, err
		}
		return workflow.NewExactMaterializedContinuationSource(parsed)
	case "deferred_self":
		if sessionID.Valid {
			return workflow.MaterializedContinuationSource{}, errors.New("deferred self continuation source cannot carry a Session ID")
		}
		return workflow.DeferredSelfMaterializedContinuationSource(), nil
	case "absent":
		if sessionID.Valid {
			return workflow.MaterializedContinuationSource{}, errors.New("absent continuation source cannot carry a Session ID")
		}
		return workflow.AbsentMaterializedContinuationSource(), nil
	default:
		return workflow.MaterializedContinuationSource{}, fmt.Errorf("continuation source kind %q is invalid", kind.String)
	}
}
