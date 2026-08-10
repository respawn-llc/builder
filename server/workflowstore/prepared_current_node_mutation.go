package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/workflow"
)

var ErrPreparedCurrentNodeMutationConsumed = errors.New("prepared Current-Node mutation is already consumed")

type PreparedCurrentNodeMutationResult struct {
	Mutation                      workflow.CurrentNodeMutationResult
	CreatedExecutableCurrentNodes []workflow.CurrentNode
	TaskAttentionResolution       TaskAttentionResolution
	PendingApprovalApply          *PendingApprovalApplyResult
	ManualMove                    *ManualMoveResult
}

// PreparedCurrentNodeMutation owns one uncommitted Current-Node transaction.
// Its concrete representation is intentionally hidden so callers can only
// inspect immutable mutation facts and resolve it exactly once.
type PreparedCurrentNodeMutation interface {
	Result() PreparedCurrentNodeMutationResult
	Commit() error
	Rollback() error
}

type PreparedCurrentNodeCompletion interface {
	Result() CurrentNodeCompletionResult
	Commit() error
	Rollback() error
}

type preparedCurrentNodeMutation struct {
	mu       sync.Mutex
	ctx      context.Context
	tx       *sql.Tx
	result   PreparedCurrentNodeMutationResult
	consumed bool
}

type preparedCurrentNodeCompletion struct {
	mu         sync.Mutex
	ctx        context.Context
	connection *sql.Conn
	lifecycle  *sqlitelifecyclegen.Queries
	result     CurrentNodeCompletionResult
	consumed   bool
}

func (s *Store) prepareCurrentNodeMutation(
	ctx context.Context,
	taskID workflow.TaskID,
	apply func(context.Context, *sqlitegen.Queries) (PreparedCurrentNodeMutationResult, error),
) (PreparedCurrentNodeMutation, error) {
	if apply == nil {
		return nil, errors.New("prepared Current-Node mutation apply function is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireManualMoveTaskWriteLock(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	if locked != 1 {
		return nil, sql.ErrNoRows
	}
	result, err := apply(ctx, q)
	if err != nil {
		return nil, err
	}
	mutation := newPreparedCurrentNodeMutation(ctx, tx, result)
	tx = nil
	return mutation, nil
}

func newPreparedCurrentNodeMutation(
	ctx context.Context,
	tx *sql.Tx,
	result PreparedCurrentNodeMutationResult,
) PreparedCurrentNodeMutation {
	return &preparedCurrentNodeMutation{
		ctx:    ctx,
		tx:     tx,
		result: clonePreparedCurrentNodeMutationResult(result),
	}
}

func newPreparedCurrentNodeCompletion(
	ctx context.Context,
	connection *sql.Conn,
	lifecycle *sqlitelifecyclegen.Queries,
	result CurrentNodeCompletionResult,
) PreparedCurrentNodeCompletion {
	return &preparedCurrentNodeCompletion{
		ctx:        ctx,
		connection: connection,
		lifecycle:  lifecycle,
		result:     cloneCurrentNodeCompletionResult(result),
	}
}

func (m *preparedCurrentNodeMutation) Result() PreparedCurrentNodeMutationResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clonePreparedCurrentNodeMutationResult(m.result)
}

func (m *preparedCurrentNodeMutation) Commit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumed {
		return ErrPreparedCurrentNodeMutationConsumed
	}
	m.consumed = true
	if err := context.Cause(m.ctx); err != nil {
		return errors.Join(err, m.tx.Rollback())
	}
	return m.tx.Commit()
}

func (m *preparedCurrentNodeMutation) Rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumed {
		return ErrPreparedCurrentNodeMutationConsumed
	}
	m.consumed = true
	return m.tx.Rollback()
}

func (m *preparedCurrentNodeCompletion) Result() CurrentNodeCompletionResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneCurrentNodeCompletionResult(m.result)
}

func (m *preparedCurrentNodeCompletion) Commit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumed {
		return ErrPreparedCurrentNodeMutationConsumed
	}
	m.consumed = true
	if err := context.Cause(m.ctx); err != nil {
		return errors.Join(err, m.rollbackAndClose())
	}
	if err := m.lifecycle.Commit(m.ctx); err != nil {
		return errors.Join(err, m.rollbackAndClose())
	}
	m.restoreAndClose()
	return nil
}

func (m *preparedCurrentNodeCompletion) Rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.consumed {
		return ErrPreparedCurrentNodeMutationConsumed
	}
	m.consumed = true
	return m.rollbackAndClose()
}

func (m *preparedCurrentNodeCompletion) rollbackAndClose() error {
	err := m.lifecycle.Rollback(context.Background())
	m.restoreAndClose()
	return err
}

func (m *preparedCurrentNodeCompletion) restoreAndClose() {
	if err := m.lifecycle.SetBusyTimeout5Seconds(context.Background()); err != nil {
		slog.Error("restore workflow completion SQLite busy timeout", "error", err)
	}
	if err := m.connection.Close(); err != nil {
		slog.Error("close workflow completion SQLite connection", "error", err)
	}
}

func clonePreparedCurrentNodeMutationResult(result PreparedCurrentNodeMutationResult) PreparedCurrentNodeMutationResult {
	cloned := PreparedCurrentNodeMutationResult{
		Mutation:                      cloneCurrentNodeMutationResult(result.Mutation),
		CreatedExecutableCurrentNodes: cloneCurrentNodes(result.CreatedExecutableCurrentNodes),
		TaskAttentionResolution:       cloneTaskAttentionResolution(result.TaskAttentionResolution),
	}
	if result.PendingApprovalApply != nil {
		approval := clonePendingApprovalApplyResult(*result.PendingApprovalApply)
		cloned.PendingApprovalApply = &approval
	}
	if result.ManualMove != nil {
		move := cloneManualMoveResult(*result.ManualMove)
		cloned.ManualMove = &move
	}
	return cloned
}

func clonePendingApprovalApplyResult(result PendingApprovalApplyResult) PendingApprovalApplyResult {
	cloned := result
	cloned.Mutation = cloneCurrentNodeMutationResult(result.Mutation)
	cloned.ResolvedApproval = clonePendingApproval(result.ResolvedApproval)
	cloned.AutomaticIntents = append([]workflow.CurrentNodeReference(nil), result.AutomaticIntents...)
	cloned.TaskAttentionResolution = cloneTaskAttentionResolution(result.TaskAttentionResolution)
	return cloned
}

func cloneManualMoveResult(result ManualMoveResult) ManualMoveResult {
	cloned := result
	cloned.CurrentNodes = cloneCurrentNodes(result.CurrentNodes)
	cloned.Mutation = cloneCurrentNodeMutationResult(result.Mutation)
	cloned.TaskAttentionResolution = cloneTaskAttentionResolution(result.TaskAttentionResolution)
	return cloned
}

func cloneTaskAttentionResolution(result TaskAttentionResolution) TaskAttentionResolution {
	return TaskAttentionResolution{
		Approvals: append([]ApprovalAttentionProjection(nil), result.Approvals...),
		InterruptedCurrentNodes: append(
			[]InterruptedCurrentNodeAttentionProjection(nil),
			result.InterruptedCurrentNodes...,
		),
	}
}

func clonePendingApproval(approval workflow.PendingApproval) workflow.PendingApproval {
	cloned := approval
	cloned.SourceSessionID = clonePendingApprovalSessionID(approval.SourceSessionID)
	cloned.OutputValues = cloneCurrentNodeOutputValues(approval.OutputValues)
	cloned.Branches = make([]workflow.PendingApprovalBranch, 0, len(approval.Branches))
	for _, branch := range approval.Branches {
		clonedBranch := branch
		clonedBranch.Target.CurrentNode = cloneCurrentNodes([]workflow.CurrentNode{branch.Target.CurrentNode})[0]
		clonedBranch.EffectiveEdge = branch.EffectiveEdge.Canonical()
		clonedBranch.ContextSourceResolution.SessionID = clonePendingApprovalSessionID(
			branch.ContextSourceResolution.SessionID,
		)
		cloned.Branches = append(cloned.Branches, clonedBranch)
	}
	return cloned
}

func cloneCurrentNodeCompletionResult(result CurrentNodeCompletionResult) CurrentNodeCompletionResult {
	cloned := result
	cloned.Mutation = cloneCurrentNodeMutationResult(result.Mutation)
	cloned.AutomaticIntents = append([]CurrentNodeAutomaticIntent(nil), result.AutomaticIntents...)
	if result.PendingApproval != nil {
		approval := clonePendingApproval(*result.PendingApproval)
		cloned.PendingApproval = &approval
	}
	if result.SessionReuse != nil {
		reuse := *result.SessionReuse
		reuse.Workflow = cloneWorkflowDefinition(result.SessionReuse.Workflow)
		reuse.AcceptedBranches = make([]workflow.Edge, 0, len(result.SessionReuse.AcceptedBranches))
		for _, edge := range result.SessionReuse.AcceptedBranches {
			reuse.AcceptedBranches = append(reuse.AcceptedBranches, edge.Canonical())
		}
		reuse.RetainedAssociations = append([]workflow.SessionReuseAssociation(nil), result.SessionReuse.RetainedAssociations...)
		reuse.CompletedCurrentNode = cloneCurrentNodes([]workflow.CurrentNode{result.SessionReuse.CompletedCurrentNode})[0]
		cloned.SessionReuse = &reuse
	}
	return cloned
}

func cloneWorkflowDefinition(definition workflow.Definition) workflow.Definition {
	cloned := definition
	cloned.NodeGroups = make([]workflow.NodeGroup, 0, len(definition.NodeGroups))
	for _, group := range definition.NodeGroups {
		group.MemberNodeIDs = append([]workflow.NodeID(nil), group.MemberNodeIDs...)
		cloned.NodeGroups = append(cloned.NodeGroups, group)
	}
	cloned.Nodes = make([]workflow.Node, 0, len(definition.Nodes))
	for _, node := range definition.Nodes {
		value, err := workflow.NewNode(node.Identity(), node.Kind(), workflow.NodeFields{
			SubagentRole:       workflow.NodeSubagentRole(node),
			CompletionMode:     workflow.NodeCompletionMode(node),
			JoinInputProviders: workflow.NodeJoinInputProviders(node),
			ScriptPath:         workflow.NodeScriptPath(node),
		})
		if err != nil {
			panic(err)
		}
		cloned.Nodes = append(cloned.Nodes, value)
	}
	cloned.TransitionGroups = append([]workflow.TransitionGroup(nil), definition.TransitionGroups...)
	cloned.Edges = make([]workflow.Edge, 0, len(definition.Edges))
	for _, edge := range definition.Edges {
		cloned.Edges = append(cloned.Edges, edge.Canonical())
	}
	return cloned
}

func cloneCurrentNodeMutationResult(result workflow.CurrentNodeMutationResult) workflow.CurrentNodeMutationResult {
	return workflow.CurrentNodeMutationResult{
		Removed: append([]workflow.CurrentNodeReference(nil), result.Removed...),
		Created: cloneCurrentNodes(result.Created),
		Updated: cloneCurrentNodes(result.Updated),
	}
}

func cloneCurrentNodes(nodes []workflow.CurrentNode) []workflow.CurrentNode {
	cloned := make([]workflow.CurrentNode, 0, len(nodes))
	for _, node := range nodes {
		value, err := workflow.NewCurrentNodeWithExecutionSelection(
			node.Reference,
			node.CurrentInputValues,
			node.PriorValues,
			node.SessionID,
			node.Scheduling,
			node.AgentExecutionSelection,
		)
		if err != nil {
			panic(err)
		}
		if node.EnteredByEdgeID != nil {
			edgeID := *node.EnteredByEdgeID
			value.EnteredByEdgeID = &edgeID
		}
		cloned = append(cloned, value)
	}
	return cloned
}
