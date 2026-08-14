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

func (s *Store) AddTaskDependency(ctx context.Context, req TaskDependencyAddRequest) (_ TaskDependencyAddResult, metadataOperationErr error) {
	tx, err := s.metadata.BeginTransaction(ctx, "AddTaskDependency", nil)
	if err != nil {
		return TaskDependencyAddResult{}, fmt.Errorf("begin task dependency transaction: %w", err)
	}
	defer tx.Settle(ctx, &metadataOperationErr)
	q := tx.Queries()

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
		Outcome:        taskDependencyMutationOutcome(decision.Decision),
		BlockerTaskID:  req.BlockerTaskID,
		BlockerShortID: decision.BlockerShortID,
		BlockedTaskID:  req.BlockedTaskID,
		BlockedShortID: decision.BlockedShortID,
		ProjectID:      decision.ProjectID,
		WorkflowID:     decision.WorkflowID,
	}
	if decision.Decision == workflow.TaskDependencyAttachAlreadyPresent {
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
) (taskDependencyAttachResult, error) {
	facts, err := loadTaskDependencyAttachFacts(ctx, q, req)
	if err != nil {
		return taskDependencyAttachResult{}, err
	}
	if err := (workflow.TaskDependencyPolicy{}).ValidatePair(facts.TaskDependencyPairFacts); err != nil {
		return taskDependencyAttachResult{}, err
	}
	inserted, err := q.InsertTaskDependency(ctx, sqlitegen.InsertTaskDependencyParams{
		BlockerTaskID: string(req.BlockerTaskID),
		BlockedTaskID: string(req.BlockedTaskID),
	})
	if err != nil {
		return taskDependencyAttachResult{}, fmt.Errorf("insert task dependency %q -> %q: %w", req.BlockerTaskID, req.BlockedTaskID, err)
	}
	if inserted == 0 {
		facts.Decision = workflow.TaskDependencyAttachAlreadyPresent
		return facts, nil
	}
	decision, err := (workflow.TaskDependencyPolicy{}).EvaluateAttach(facts.TaskDependencyAttachFacts)
	if err != nil {
		return taskDependencyAttachResult{}, err
	}
	facts.Decision = decision
	return facts, nil
}

func (s *Store) RemoveTaskDependency(ctx context.Context, req TaskDependencyRemoveRequest) (_ TaskDependencyRemoveResult, metadataOperationErr error) {
	tx, err := s.metadata.BeginTransaction(ctx, "RemoveTaskDependency", nil)
	if err != nil {
		return TaskDependencyRemoveResult{}, fmt.Errorf("begin task dependency removal transaction: %w", err)
	}
	defer tx.Settle(ctx, &metadataOperationErr)
	q := tx.Queries()
	if _, err := q.AcquireTaskDependencyWriteLock(ctx, string(req.BlockerTaskID)); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return TaskDependencyRemoveResult{}, fmt.Errorf("lock task dependency project: %w", err)
		}
		return TaskDependencyRemoveResult{}, (workflow.TaskDependencyPolicy{}).ValidatePair(workflow.TaskDependencyPairFacts{
			BlockerTaskID: req.BlockerTaskID,
			BlockedTaskID: req.BlockedTaskID,
		})
	}
	identity, facts, err := loadTaskDependencyPairFacts(ctx, q, req)
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
		Outcome:        TaskDependencyAlreadyAbsent,
		BlockerTaskID:  req.BlockerTaskID,
		BlockerShortID: identity.BlockerShortID,
		BlockedTaskID:  req.BlockedTaskID,
		BlockedShortID: identity.BlockedShortID,
		ProjectID:      identity.ProjectID,
		WorkflowID:     identity.WorkflowID,
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
) (taskDependencyIdentity, workflow.TaskDependencyPairFacts, error) {
	facts := workflow.TaskDependencyPairFacts{
		BlockerTaskID: req.BlockerTaskID,
		BlockedTaskID: req.BlockedTaskID,
	}
	rows, err := q.GetTaskDependencyPairSnapshot(ctx, sqlitegen.GetTaskDependencyPairSnapshotParams{
		BlockerTaskID: string(req.BlockerTaskID),
		BlockedTaskID: string(req.BlockedTaskID),
	})
	if err != nil {
		return taskDependencyIdentity{}, workflow.TaskDependencyPairFacts{}, fmt.Errorf("load task dependency pair %q -> %q: %w", req.BlockerTaskID, req.BlockedTaskID, err)
	}
	identity := taskDependencyIdentity{}
	for _, row := range rows {
		task := &workflow.TaskDependencyTaskFacts{ID: workflow.TaskID(row.ID), ProjectID: row.ProjectID}
		switch row.TaskRole {
		case "blocker":
			facts.Blocker = task
			identity.BlockerShortID = row.ShortID
			identity.ProjectID = row.ProjectID
			identity.WorkflowID = row.WorkflowID
		case "blocked":
			facts.Blocked = task
			identity.BlockedShortID = row.ShortID
		default:
			panic(fmt.Sprintf("task dependency pair snapshot returned invalid role %q", row.TaskRole))
		}
	}
	return identity, facts, nil
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
) (taskDependencyAttachResult, error) {
	result := taskDependencyAttachResult{TaskDependencyAttachFacts: workflow.TaskDependencyAttachFacts{
		TaskDependencyPairFacts: workflow.TaskDependencyPairFacts{
			BlockerTaskID: req.BlockerTaskID,
			BlockedTaskID: req.BlockedTaskID,
		},
	}}
	rows, err := q.GetTaskDependencyAttachSnapshot(ctx, sqlitegen.GetTaskDependencyAttachSnapshotParams{
		BlockerTaskID: string(req.BlockerTaskID),
		BlockedTaskID: string(req.BlockedTaskID),
	})
	if err != nil {
		return taskDependencyAttachResult{}, fmt.Errorf("load task dependency attach snapshot %q -> %q: %w", req.BlockerTaskID, req.BlockedTaskID, err)
	}
	for _, row := range rows {
		result.ReversePairPresent = row.ReversePairPresent != 0
		result.BlockerOutgoingCount = row.BlockerOutgoingCount
		result.BlockedIncomingCount = row.BlockedIncomingCount
		task := &workflow.TaskDependencyTaskFacts{ID: workflow.TaskID(row.ID), ProjectID: row.ProjectID}
		switch row.TaskRole {
		case "blocker":
			result.Blocker = task
			result.BlockerShortID = row.ShortID
			result.ProjectID = row.ProjectID
			result.WorkflowID = row.WorkflowID
		case "blocked":
			result.Blocked = task
			result.BlockedShortID = row.ShortID
		default:
			panic(fmt.Sprintf("task dependency attach snapshot returned invalid role %q", row.TaskRole))
		}
	}
	return result, nil
}

type taskDependencyIdentity struct {
	BlockerShortID string
	BlockedShortID string
	ProjectID      string
	WorkflowID     runtimeids.WorkflowID
}

type taskDependencyAttachResult struct {
	workflow.TaskDependencyAttachFacts
	taskDependencyIdentity
	Decision workflow.TaskDependencyAttachDecision
}
