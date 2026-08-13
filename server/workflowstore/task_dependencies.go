package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type TaskDependencyMutationOutcome string

const (
	TaskDependencyAdded          TaskDependencyMutationOutcome = "added"
	TaskDependencyAlreadyPresent TaskDependencyMutationOutcome = "already_present"
	TaskDependencyRemoved        TaskDependencyMutationOutcome = "removed"
	TaskDependencyAlreadyAbsent  TaskDependencyMutationOutcome = "already_absent"
)

type TaskDependencyAddRequest struct {
	BlockerTaskID workflow.TaskID
	BlockedTaskID workflow.TaskID
}

type TaskDependencyAddResult struct {
	Outcome        TaskDependencyMutationOutcome
	BlockerTaskID  workflow.TaskID
	BlockerShortID string
	BlockedTaskID  workflow.TaskID
	BlockedShortID string
	ProjectID      string
	WorkflowID     runtimeids.WorkflowID
}

type TaskDependencyRemoveRequest struct {
	BlockerTaskID workflow.TaskID
	BlockedTaskID workflow.TaskID
}

type TaskDependencyRemoveResult struct {
	Outcome        TaskDependencyMutationOutcome
	BlockerTaskID  workflow.TaskID
	BlockerShortID string
	BlockedTaskID  workflow.TaskID
	BlockedShortID string
	ProjectID      string
	WorkflowID     runtimeids.WorkflowID
}

func (s *Store) AddTaskDependency(ctx context.Context, req TaskDependencyAddRequest) (TaskDependencyAddResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskDependencyAddResult{}, fmt.Errorf("begin task dependency transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)

	if _, err := q.AcquireTaskDependencyWriteLock(ctx, string(req.BlockerTaskID)); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return TaskDependencyAddResult{}, fmt.Errorf("lock task dependency project: %w", err)
		}
		_, policyErr := (workflow.TaskDependencyPolicy{}).EvaluateAttach(workflow.TaskDependencyAttachFacts{
			TaskDependencyPairFacts: workflow.TaskDependencyPairFacts{
				BlockerTaskID: req.BlockerTaskID,
				BlockedTaskID: req.BlockedTaskID,
			},
		})
		return TaskDependencyAddResult{}, policyErr
	}

	decision, err := attachTaskDependencyWithQueries(ctx, q, req)
	if err != nil {
		return TaskDependencyAddResult{}, err
	}
	result := TaskDependencyAddResult{
		Outcome:       taskDependencyMutationOutcome(decision),
		BlockerTaskID: req.BlockerTaskID,
		BlockedTaskID: req.BlockedTaskID,
	}
	if err := populateTaskDependencyIdentity(ctx, q, &result.BlockerShortID, &result.BlockedShortID, &result.ProjectID, &result.WorkflowID, req.BlockerTaskID, req.BlockedTaskID); err != nil {
		return TaskDependencyAddResult{}, err
	}
	if decision == workflow.TaskDependencyAttachAlreadyPresent {
		if err := tx.Commit(); err != nil {
			return TaskDependencyAddResult{}, fmt.Errorf("commit idempotent task dependency add: %w", err)
		}
		return result, nil
	}
	nowUnixMs := s.now().UnixMilli()
	for _, taskID := range []workflow.TaskID{req.BlockerTaskID, req.BlockedTaskID} {
		if err := advanceTaskUpdatedAt(ctx, q, string(taskID), nowUnixMs); err != nil {
			return TaskDependencyAddResult{}, fmt.Errorf("touch task %q after dependency add: %w", taskID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return TaskDependencyAddResult{}, fmt.Errorf("commit task dependency add: %w", err)
	}
	return result, nil
}

func attachTaskDependencyWithQueries(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskDependencyAddRequest,
) (workflow.TaskDependencyAttachDecision, error) {
	facts, err := loadTaskDependencyAttachFacts(ctx, q, req)
	if err != nil {
		return workflow.TaskDependencyAttachRejected, err
	}
	if err := (workflow.TaskDependencyPolicy{}).ValidatePair(facts.TaskDependencyPairFacts); err != nil {
		return workflow.TaskDependencyAttachRejected, err
	}
	inserted, err := q.InsertTaskDependency(ctx, sqlitegen.InsertTaskDependencyParams{
		BlockerTaskID: string(req.BlockerTaskID),
		BlockedTaskID: string(req.BlockedTaskID),
	})
	if err != nil {
		return workflow.TaskDependencyAttachRejected, fmt.Errorf("insert task dependency %q -> %q: %w", req.BlockerTaskID, req.BlockedTaskID, err)
	}
	if inserted == 0 {
		return workflow.TaskDependencyAttachAlreadyPresent, nil
	}
	decision, err := (workflow.TaskDependencyPolicy{}).EvaluateAttach(facts)
	if err != nil {
		return workflow.TaskDependencyAttachRejected, err
	}
	return decision, nil
}

func (s *Store) RemoveTaskDependency(ctx context.Context, req TaskDependencyRemoveRequest) (TaskDependencyRemoveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskDependencyRemoveResult{}, fmt.Errorf("begin task dependency removal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.AcquireTaskDependencyWriteLock(ctx, string(req.BlockerTaskID)); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return TaskDependencyRemoveResult{}, fmt.Errorf("lock task dependency project: %w", err)
		}
		return TaskDependencyRemoveResult{}, (workflow.TaskDependencyPolicy{}).ValidatePair(workflow.TaskDependencyPairFacts{
			BlockerTaskID: req.BlockerTaskID,
			BlockedTaskID: req.BlockedTaskID,
		})
	}
	facts, err := loadTaskDependencyPairFacts(ctx, q, req)
	if err != nil {
		return TaskDependencyRemoveResult{}, err
	}
	if err := (workflow.TaskDependencyPolicy{}).ValidatePair(facts); err != nil {
		return TaskDependencyRemoveResult{}, err
	}
	removed, err := q.DeleteTaskDependency(ctx, sqlitegen.DeleteTaskDependencyParams{
		BlockerTaskID: string(req.BlockerTaskID),
		BlockedTaskID: string(req.BlockedTaskID),
	})
	if err != nil {
		return TaskDependencyRemoveResult{}, fmt.Errorf("delete task dependency %q -> %q: %w", req.BlockerTaskID, req.BlockedTaskID, err)
	}
	result := TaskDependencyRemoveResult{
		Outcome:       TaskDependencyAlreadyAbsent,
		BlockerTaskID: req.BlockerTaskID,
		BlockedTaskID: req.BlockedTaskID,
	}
	if err := populateTaskDependencyIdentity(ctx, q, &result.BlockerShortID, &result.BlockedShortID, &result.ProjectID, &result.WorkflowID, req.BlockerTaskID, req.BlockedTaskID); err != nil {
		return TaskDependencyRemoveResult{}, err
	}
	if removed == 1 {
		result.Outcome = TaskDependencyRemoved
		nowUnixMs := s.now().UnixMilli()
		for _, taskID := range []workflow.TaskID{req.BlockerTaskID, req.BlockedTaskID} {
			if err := advanceTaskUpdatedAt(ctx, q, string(taskID), nowUnixMs); err != nil {
				return TaskDependencyRemoveResult{}, fmt.Errorf("touch task %q after dependency removal: %w", taskID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return TaskDependencyRemoveResult{}, fmt.Errorf("commit task dependency removal: %w", err)
	}
	return result, nil
}

func loadTaskDependencyPairFacts(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskDependencyRemoveRequest,
) (workflow.TaskDependencyPairFacts, error) {
	facts := workflow.TaskDependencyPairFacts{
		BlockerTaskID: req.BlockerTaskID,
		BlockedTaskID: req.BlockedTaskID,
	}
	rows, err := q.GetTaskDependencyPairSnapshot(ctx, sqlitegen.GetTaskDependencyPairSnapshotParams{
		BlockerTaskID: string(req.BlockerTaskID),
		BlockedTaskID: string(req.BlockedTaskID),
	})
	if err != nil {
		return workflow.TaskDependencyPairFacts{}, fmt.Errorf("load task dependency pair %q -> %q: %w", req.BlockerTaskID, req.BlockedTaskID, err)
	}
	for _, row := range rows {
		task := &workflow.TaskDependencyTaskFacts{ID: workflow.TaskID(row.ID), ProjectID: row.ProjectID}
		switch row.TaskRole {
		case "blocker":
			facts.Blocker = task
		case "blocked":
			facts.Blocked = task
		default:
			panic(fmt.Sprintf("task dependency pair snapshot returned invalid role %q", row.TaskRole))
		}
	}
	return facts, nil
}

func populateTaskDependencyIdentity(ctx context.Context, q *sqlitegen.Queries, blockerShortID, blockedShortID, projectID *string, workflowID *runtimeids.WorkflowID, blockerTaskID, blockedTaskID workflow.TaskID) error {
	blocker, err := q.GetTask(ctx, string(blockerTaskID))
	if err != nil {
		return fmt.Errorf("load blocker task %q for dependency result: %w", blockerTaskID, err)
	}
	blocked, err := q.GetTask(ctx, string(blockedTaskID))
	if err != nil {
		return fmt.Errorf("load blocked task %q for dependency result: %w", blockedTaskID, err)
	}
	*blockerShortID = blocker.ShortID
	*blockedShortID = blocked.ShortID
	*projectID = blocker.ProjectID
	*workflowID = blocker.WorkflowID
	return nil
}

func taskDependencyMutationOutcome(decision workflow.TaskDependencyAttachDecision) TaskDependencyMutationOutcome {
	switch decision {
	case workflow.TaskDependencyAttachAlreadyPresent:
		return TaskDependencyAlreadyPresent
	default:
		return TaskDependencyAdded
	}
}

func loadTaskDependencyAttachFacts(
	ctx context.Context,
	q *sqlitegen.Queries,
	req TaskDependencyAddRequest,
) (workflow.TaskDependencyAttachFacts, error) {
	facts := workflow.TaskDependencyAttachFacts{
		TaskDependencyPairFacts: workflow.TaskDependencyPairFacts{
			BlockerTaskID: req.BlockerTaskID,
			BlockedTaskID: req.BlockedTaskID,
		},
	}
	blocker, err := q.GetTask(ctx, string(req.BlockerTaskID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return workflow.TaskDependencyAttachFacts{}, fmt.Errorf("load blocker task %q: %w", req.BlockerTaskID, err)
	}
	if err == nil {
		facts.Blocker = &workflow.TaskDependencyTaskFacts{
			ID:        workflow.TaskID(blocker.ID),
			ProjectID: blocker.ProjectID,
		}
	}
	blocked, err := q.GetTask(ctx, string(req.BlockedTaskID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return workflow.TaskDependencyAttachFacts{}, fmt.Errorf("load blocked task %q: %w", req.BlockedTaskID, err)
	}
	if err == nil {
		facts.Blocked = &workflow.TaskDependencyTaskFacts{
			ID:        workflow.TaskID(blocked.ID),
			ProjectID: blocked.ProjectID,
		}
	}
	reverse, err := q.GetTaskDependency(ctx, sqlitegen.GetTaskDependencyParams{
		BlockerTaskID: string(req.BlockedTaskID),
		BlockedTaskID: string(req.BlockerTaskID),
	})
	switch {
	case err == nil:
		facts.ReversePairPresent = reverse.BlockerTaskID == string(req.BlockedTaskID) && reverse.BlockedTaskID == string(req.BlockerTaskID)
	case errors.Is(err, sql.ErrNoRows):
	default:
		return workflow.TaskDependencyAttachFacts{}, fmt.Errorf("load reciprocal task dependency %q -> %q: %w", req.BlockedTaskID, req.BlockerTaskID, err)
	}
	facts.BlockerOutgoingCount, err = q.CountTaskDependenciesByBlocker(ctx, string(req.BlockerTaskID))
	if err != nil {
		return workflow.TaskDependencyAttachFacts{}, fmt.Errorf("count dependencies blocked by %q: %w", req.BlockerTaskID, err)
	}
	facts.BlockedIncomingCount, err = q.CountTaskDependenciesByBlocked(ctx, string(req.BlockedTaskID))
	if err != nil {
		return workflow.TaskDependencyAttachFacts{}, fmt.Errorf("count dependencies blocking %q: %w", req.BlockedTaskID, err)
	}
	return facts, nil
}
