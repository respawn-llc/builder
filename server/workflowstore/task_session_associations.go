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

// CurrentNodeSessionBindingRequest compare-and-swaps the exact Current Node's
// retained Session while recording the resulting Task Session association.
type CurrentNodeSessionBindingRequest struct {
	Association              TaskSessionAssociationRequest
	ExpectedCurrentSessionID *runtimeids.SessionID
}

type TaskSessionAssociation struct {
	SessionID    runtimeids.SessionID
	CurrentNode  workflow.CurrentNodeReference
	AssociatedAt time.Time
}

// ErrSessionNotCurrentWorkflowNode means that a retained Task-owned Session is
// not the Session of a currently executable workflow node. It is an ordinary
// retained-session state, not a data-integrity failure.
var ErrSessionNotCurrentWorkflowNode = errors.New("session is not bound to a current workflow node")

// BindSessionToCurrentNode atomically establishes the live agent-session
// binding for an exact Current Node and records its retained provenance.
// AssociateTaskSession intentionally does not make a Current Node live.
func (s *Store) BindSessionToCurrentNode(ctx context.Context, req CurrentNodeSessionBindingRequest) (TaskSessionAssociation, error) {
	normalized, err := normalizeTaskSessionAssociationRequest(req.Association)
	if err != nil {
		return TaskSessionAssociation{}, err
	}
	if req.ExpectedCurrentSessionID != nil && req.ExpectedCurrentSessionID.IsZero() {
		return TaskSessionAssociation{}, errors.New("expected current Session id is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskSessionAssociation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := bindSessionToTask(ctx, q, normalized); err != nil {
		return TaskSessionAssociation{}, err
	}
	if err := bindSessionToExactCurrentNode(ctx, q, normalized, req.ExpectedCurrentSessionID); err != nil {
		return TaskSessionAssociation{}, err
	}
	if err := upsertTaskSessionAssociation(ctx, q, normalized); err != nil {
		return TaskSessionAssociation{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskSessionAssociation{}, err
	}
	return taskSessionAssociationFromRequest(normalized), nil
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
	if err := bindSessionToTask(ctx, q, normalized); err != nil {
		return TaskSessionAssociation{}, err
	}
	if err := upsertTaskSessionAssociation(ctx, q, normalized); err != nil {
		return TaskSessionAssociation{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskSessionAssociation{}, err
	}
	return taskSessionAssociationFromRequest(normalized), nil
}

func bindSessionToTask(ctx context.Context, q *sqlitegen.Queries, req TaskSessionAssociationRequest) error {
	bound, err := q.BindSessionToTask(ctx, sqlitegen.BindSessionToTaskParams{
		TaskID:    sql.NullString{String: string(req.CurrentNode.TaskID), Valid: true},
		SessionID: req.SessionID.String(),
	})
	if err != nil {
		return err
	}
	if bound != 1 {
		return errors.New("session cannot be bound to task")
	}
	return nil
}

func bindSessionToExactCurrentNode(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskSessionAssociationRequest,
	expectedCurrentSessionIDValue *runtimeids.SessionID,
) error {
	var (
		expectedCurrentSessionID sql.NullString
		bound                    int64
		err                      error
	)
	if expectedCurrentSessionIDValue != nil {
		expectedCurrentSessionID = sql.NullString{
			String: expectedCurrentSessionIDValue.String(),
			Valid:  true,
		}
	}
	if branchKey, branchScoped := req.CurrentNode.TransitionBranchKey(); branchScoped {
		bound, err = q.BindSessionToBranchCurrentNode(ctx, sqlitegen.BindSessionToBranchCurrentNodeParams{
			SessionID:                sql.NullString{String: req.SessionID.String(), Valid: true},
			TaskID:                   string(req.CurrentNode.TaskID),
			NodeID:                   string(req.CurrentNode.NodeID),
			TransitionBranchKey:      sql.NullString{String: string(branchKey), Valid: true},
			ExpectedCurrentSessionID: expectedCurrentSessionID,
		})
	} else {
		bound, err = q.BindSessionToSerialCurrentNode(ctx, sqlitegen.BindSessionToSerialCurrentNodeParams{
			SessionID:                sql.NullString{String: req.SessionID.String(), Valid: true},
			TaskID:                   string(req.CurrentNode.TaskID),
			NodeID:                   string(req.CurrentNode.NodeID),
			ExpectedCurrentSessionID: expectedCurrentSessionID,
		})
	}
	if err != nil {
		return err
	}
	if bound != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func upsertTaskSessionAssociation(ctx context.Context, q *sqlitegen.Queries, req TaskSessionAssociationRequest) error {
	if branchKey, branchScoped := req.CurrentNode.TransitionBranchKey(); branchScoped {
		return q.UpsertBranchSessionWorkflowNodeAssociation(ctx, sqlitegen.UpsertBranchSessionWorkflowNodeAssociationParams{
			SessionID:           req.SessionID.String(),
			NodeID:              string(req.CurrentNode.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
			AssociatedAtUnixMs:  req.AssociatedAt.UnixMilli(),
		})
	}
	return q.UpsertSerialSessionWorkflowNodeAssociation(ctx, sqlitegen.UpsertSerialSessionWorkflowNodeAssociationParams{
		SessionID:          req.SessionID.String(),
		NodeID:             string(req.CurrentNode.NodeID),
		AssociatedAtUnixMs: req.AssociatedAt.UnixMilli(),
	})
}

func taskSessionAssociationFromRequest(req TaskSessionAssociationRequest) TaskSessionAssociation {
	return TaskSessionAssociation{
		SessionID:    req.SessionID,
		CurrentNode:  req.CurrentNode,
		AssociatedAt: req.AssociatedAt,
	}
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

// ResolveCurrentSessionStartContext resolves prompt state only from direct
// Session ownership, the matching Current Node, and the latest definition.
// Discarded execution history is not an input.
func (s *Store) ResolveCurrentSessionStartContext(ctx context.Context, sessionID runtimeids.SessionID) (CurrentNodeStartContext, error) {
	if sessionID.IsZero() {
		return CurrentNodeStartContext{}, errors.New("session id is required")
	}
	taskIDs, err := s.queries.ListSessionWorkflowTaskIDs(ctx, sessionID.String())
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	if len(taskIDs) == 0 {
		return CurrentNodeStartContext{}, ErrSessionNotCurrentWorkflowNode
	}
	if len(taskIDs) != 1 || !taskIDs[0].Valid {
		return CurrentNodeStartContext{}, fmt.Errorf("session %q has invalid task ownership", sessionID)
	}
	taskID := workflow.TaskID(taskIDs[0].String)
	currentNodes, err := s.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	var currentNode *workflow.CurrentNode
	for i := range currentNodes {
		if currentNodes[i].SessionID == nil || *currentNodes[i].SessionID != sessionID {
			continue
		}
		if currentNode != nil {
			return CurrentNodeStartContext{}, fmt.Errorf("session %q is bound to multiple current nodes for task %q", sessionID, taskID)
		}
		currentNode = &currentNodes[i]
	}
	if currentNode == nil {
		return CurrentNodeStartContext{}, ErrSessionNotCurrentWorkflowNode
	}
	association, err := s.LatestTaskSessionForNode(ctx, currentNode.Reference)
	if err != nil {
		return CurrentNodeStartContext{}, fmt.Errorf("resolve current session node association: %w", err)
	}
	if association.SessionID != sessionID {
		return CurrentNodeStartContext{}, fmt.Errorf("current node %v is associated with session %q, want %q", currentNode.Reference, association.SessionID, sessionID)
	}
	return s.resolveCurrentNodeStartContext(ctx, *currentNode)
}

// ValidateCurrentNodeSessionBinding proves that a volatile exact workflow
// scope still belongs to the Task-owned Session and its exact Current Node.
// It intentionally reads no Run history or execution snapshot.
func (s *Store) ValidateCurrentNodeSessionBinding(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	reference workflow.CurrentNodeReference,
) error {
	if sessionID.IsZero() {
		return errors.New("session id is required")
	}
	if err := reference.Validate(); err != nil {
		return err
	}
	taskIDs, err := s.queries.ListSessionWorkflowTaskIDs(ctx, sessionID.String())
	if err != nil {
		return err
	}
	if len(taskIDs) != 1 || !taskIDs[0].Valid || workflow.TaskID(taskIDs[0].String) != reference.TaskID {
		return ErrSessionNotCurrentWorkflowNode
	}
	currentNode, err := currentNodeForReference(ctx, s.queries, reference)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionNotCurrentWorkflowNode
	}
	if err != nil {
		return err
	}
	if currentNode.SessionID == nil || *currentNode.SessionID != sessionID {
		return ErrSessionNotCurrentWorkflowNode
	}
	association, err := s.LatestTaskSessionForNode(ctx, reference)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionNotCurrentWorkflowNode
	}
	if err != nil {
		return err
	}
	if association.SessionID != sessionID || !association.CurrentNode.Equal(reference) {
		return ErrSessionNotCurrentWorkflowNode
	}
	return nil
}

// ResolveCurrentNodeStartContext prepares an admitted executable Current Node
// from its own materialized values and the latest definition. It never
// reconstructs discarded execution history.
func (s *Store) ResolveCurrentNodeStartContext(ctx context.Context, reference workflow.CurrentNodeReference) (CurrentNodeStartContext, error) {
	if err := reference.Validate(); err != nil {
		return CurrentNodeStartContext{}, err
	}
	currentNode, err := currentNodeForReference(ctx, s.queries, reference)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	return s.resolveCurrentNodeStartContext(ctx, currentNode)
}

func (s *Store) resolveCurrentNodeStartContext(ctx context.Context, currentNode workflow.CurrentNode) (CurrentNodeStartContext, error) {
	if currentNode.EnteredByEdgeID == nil {
		return CurrentNodeStartContext{}, fmt.Errorf("current workflow node %v has no entering edge", currentNode.Reference)
	}
	taskRow, err := s.queries.GetTask(ctx, string(currentNode.Reference.TaskID))
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	task, err := taskRecordFromTask(taskRow)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	definition, workflowRecord, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	node, err := currentNodeDefinitionNode(definition, currentNode.Reference.NodeID)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	if !executableNodeKind(node.Kind()) {
		return CurrentNodeStartContext{}, fmt.Errorf("current node %v is not executable", currentNode.Reference)
	}
	var enteringEdge *workflow.Edge
	for i := range definition.Edges {
		if definition.Edges[i].ID != *currentNode.EnteredByEdgeID {
			continue
		}
		enteringEdge = &definition.Edges[i]
		break
	}
	if enteringEdge == nil {
		return CurrentNodeStartContext{}, fmt.Errorf("current workflow node %v entering edge %q is absent from workflow %q", currentNode.Reference, *currentNode.EnteredByEdgeID, definition.ID)
	}
	if enteringEdge.TargetNodeID != currentNode.Reference.NodeID {
		return CurrentNodeStartContext{}, fmt.Errorf("current workflow node %v entering edge %q targets node %q", currentNode.Reference, enteringEdge.ID, enteringEdge.TargetNodeID)
	}
	transitionIDs, transitionOptions, err := currentNodeTransitionOptions(definition, node)
	if err != nil {
		return CurrentNodeStartContext{}, err
	}
	var executionRoot *ExecutionRoot
	if task.ExecutionTarget != nil {
		root, err := executionRootForTask(ctx, s.queries, taskRow)
		if err != nil {
			return CurrentNodeStartContext{}, err
		}
		executionRoot = &root
	}
	values := cloneCurrentNodeOutputValues(currentNode.CurrentInputValues)
	if _, ok := values[workflow.RuntimePromptParameterCommentary]; !ok {
		values[workflow.RuntimePromptParameterCommentary] = ""
	}
	effectiveContextMode := enteringEdge.ContextMode
	var sourceSessionID *runtimeids.SessionID
	if enteringEdge.ContextMode != workflow.ContextModeNewSession {
		if currentNode.SessionID == nil {
			if workflow.CanonicalContextSource(enteringEdge.ContextSource).Kind == workflow.ContextSourcePreviousTargetOrNew {
				effectiveContextMode = workflow.ContextModeNewSession
			} else {
				return CurrentNodeStartContext{}, fmt.Errorf("continuation current node %v has no retained session", currentNode.Reference)
			}
		} else {
			value := *currentNode.SessionID
			sourceSessionID = &value
		}
	}
	sourceNode, err := currentNodeDefinitionNode(definition, transitionGroupSourceNodeID(definition, enteringEdge.TransitionGroupID))
	if err != nil {
		return CurrentNodeStartContext{}, fmt.Errorf("resolve entering source node for current node %v: %w", currentNode.Reference, err)
	}
	return CurrentNodeStartContext{
		Task:            task,
		Workflow:        workflowRecord,
		Node:            nodeRecordFromCurrentDefinition(node),
		CurrentNode:     currentNode,
		EnteringEdge:    *enteringEdge,
		ContextMode:     effectiveContextMode,
		SourceSessionID: sourceSessionID,
		IsFanoutBranch:  currentNode.Reference.IsBranchScoped(),
		AcceptedTransitionPath: AcceptedTransitionPath{
			SourceNodeDisplayName: workflow.NodeDisplayName(sourceNode),
			TargetNodeDisplayName: workflow.NodeDisplayName(node),
		},
		TransitionIDs:                  transitionIDs,
		TransitionOptions:              transitionOptions,
		HasContinueSessionOutgoingEdge: currentNodeHasContinueSessionOutgoingEdge(definition, workflow.NodeIDOf(node)),
		PromptTemplate:                 strings.TrimSpace(enteringEdge.PromptTemplate),
		ParameterValues:                values,
		PriorParameterValues:           clonePriorParameterValues(currentNode.PriorNodeValues),
		ExecutionRoot:                  executionRoot,
	}, nil
}

func currentNodeHasContinueSessionOutgoingEdge(definition workflow.Definition, nodeID workflow.NodeID) bool {
	for _, edge := range definition.Edges {
		if edge.ContextMode != workflow.ContextModeContinueSession && edge.ContextMode != workflow.ContextModeCompactAndContinueSession {
			continue
		}
		if transitionGroupSourceNodeID(definition, edge.TransitionGroupID) == nodeID {
			return true
		}
	}
	return false
}

func transitionGroupSourceNodeID(definition workflow.Definition, groupID workflow.TransitionGroupID) workflow.NodeID {
	for _, group := range definition.TransitionGroups {
		if group.ID == groupID {
			return group.SourceNodeID
		}
	}
	return ""
}

func currentNodeTransitionOptions(definition workflow.Definition, source workflow.Node) ([]string, []TransitionOption, error) {
	transitionIDs := make([]string, 0)
	options := make([]TransitionOption, 0)
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID != workflow.NodeIDOf(source) {
			continue
		}
		transitionID := strings.TrimSpace(string(group.TransitionID))
		if transitionID == "" {
			return nil, nil, fmt.Errorf("workflow %q has a blank transition id for current node %q", definition.ID, workflow.NodeIDOf(source))
		}
		transitionIDs = append(transitionIDs, transitionID)
		options = append(options, TransitionOption{
			ID:          transitionID,
			DisplayName: strings.TrimSpace(group.DisplayName),
			Description: strings.TrimSpace(group.Description),
			Parameters:  currentNodeTransitionParameters(definition, group),
		})
	}
	return transitionIDs, options, nil
}

func currentNodeTransitionParameters(definition workflow.Definition, group workflow.TransitionGroup) []workflow.Parameter {
	wiring := workflow.DeriveWiring(definition)
	if source, err := currentNodeDefinitionNode(definition, group.SourceNodeID); err == nil && source.Kind() == workflow.NodeKindJoin {
		return parametersFromOutputFields(wiring.JoinOutputFieldsForNode(group.SourceNodeID))
	}
	for _, edge := range definition.Edges {
		if edge.TransitionGroupID == group.ID {
			return append([]workflow.Parameter(nil), edge.Parameters...)
		}
	}
	return nil
}

func clonePriorParameterValues(values map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(values))
	for nodeKey, outputs := range values {
		out[nodeKey] = cloneCurrentNodeOutputValues(outputs)
	}
	return out
}

func parametersFromOutputFields(fields []workflow.OutputField) []workflow.Parameter {
	out := make([]workflow.Parameter, 0, len(fields))
	for _, field := range fields {
		out = append(out, workflow.Parameter{Key: field.Name, Description: field.Description})
	}
	return out
}

func nodeRecordFromCurrentDefinition(node workflow.Node) NodeRecord {
	scriptPath := ""
	if path := workflow.NodeScriptPath(node); path.IsPresent() {
		scriptPath = path.String()
	}
	return NodeRecord{
		ID:                 workflow.NodeIDOf(node),
		WorkflowID:         workflow.NodeWorkflowID(node),
		Key:                workflow.NodeKey(node),
		Kind:               node.Kind(),
		DisplayName:        workflow.NodeDisplayName(node),
		GroupID:            workflow.NodeGroupID(node),
		SubagentRole:       workflow.NodeSubagentRole(node),
		PromptTemplate:     workflow.NodePromptTemplate(node),
		CompletionMode:     workflow.NodeCompletionMode(node),
		ScriptPath:         scriptPath,
		InputFields:        workflow.NodeInputFields(node),
		JoinInputProviders: workflow.NodeJoinInputProviders(node),
		OutputFields:       workflow.NodeOutputFields(node),
	}
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
