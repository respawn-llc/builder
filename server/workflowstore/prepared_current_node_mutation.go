package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

var ErrPreparedCurrentNodeMutationConsumed = errors.New("prepared Current-Node mutation is already consumed")

type PreparedCurrentNodeMutationResult struct {
	Mutation                      workflow.CurrentNodeMutationResult
	CreatedExecutableCurrentNodes []workflow.CurrentNode
	TaskAttentionResolution       TaskAttentionResolution
}

// PreparedCurrentNodeMutation owns one uncommitted Current-Node transaction.
// Its concrete representation is intentionally hidden so callers can only
// inspect immutable mutation facts and resolve it exactly once.
type PreparedCurrentNodeMutation interface {
	Result() PreparedCurrentNodeMutationResult
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

func clonePreparedCurrentNodeMutationResult(result PreparedCurrentNodeMutationResult) PreparedCurrentNodeMutationResult {
	return PreparedCurrentNodeMutationResult{
		Mutation:                      cloneCurrentNodeMutationResult(result.Mutation),
		CreatedExecutableCurrentNodes: cloneCurrentNodes(result.CreatedExecutableCurrentNodes),
		TaskAttentionResolution: TaskAttentionResolution{
			Approvals: append([]ApprovalAttentionProjection(nil), result.TaskAttentionResolution.Approvals...),
			InterruptedCurrentNodes: append(
				[]InterruptedCurrentNodeAttentionProjection(nil),
				result.TaskAttentionResolution.InterruptedCurrentNodes...,
			),
		},
	}
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
