package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

// TaskExecutionTargetStateKind classifies a task's authoritative execution
// history while execution targets are being introduced.
type TaskExecutionTargetStateKind string

const (
	TaskExecutionTargetStateMaterialized  TaskExecutionTargetStateKind = "materialized"
	TaskExecutionTargetStateLegacyManaged TaskExecutionTargetStateKind = "legacy_managed"
	TaskExecutionTargetStateLegacyMissing TaskExecutionTargetStateKind = "legacy_missing"
	TaskExecutionTargetStateUnstarted     TaskExecutionTargetStateKind = "unstarted"
)

// TaskExecutionTargetState is the one authoritative classification used by
// initiating actions and the legacy worktree path.
type TaskExecutionTargetState struct {
	Kind   TaskExecutionTargetStateKind
	Target *workflow.ExecutionTarget
}

func (s *Store) TaskExecutionTargetState(ctx context.Context, taskID workflow.TaskID) (TaskExecutionTargetState, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return TaskExecutionTargetState{}, errors.New("task id is required")
	}
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return TaskExecutionTargetState{}, err
	}
	return s.taskExecutionTargetState(ctx, task)
}

func (s *Store) taskExecutionTargetState(ctx context.Context, task sqlitegen.TaskRecord) (TaskExecutionTargetState, error) {
	target, err := s.GetTaskExecutionTarget(ctx, workflow.TaskID(task.ID))
	if err != nil {
		return TaskExecutionTargetState{}, err
	}
	if target != nil {
		return TaskExecutionTargetState{Kind: TaskExecutionTargetStateMaterialized, Target: target}, nil
	}

	worktreeID := strings.TrimSpace(task.ManagedWorktreeID.String)
	if task.ManagedWorktreeID.Valid && worktreeID != "" {
		worktree, worktreeErr := s.metadata.GetWorktreeRecordByID(ctx, worktreeID)
		if worktreeErr == nil && worktree.Managed && !worktree.IsMain && strings.TrimSpace(worktree.CanonicalRoot) != "" {
			return TaskExecutionTargetState{Kind: TaskExecutionTargetStateLegacyManaged}, nil
		}
		if worktreeErr != nil && !errors.Is(worktreeErr, sql.ErrNoRows) {
			return TaskExecutionTargetState{}, worktreeErr
		}
	}

	runCount, err := s.queries.CountTaskRunsByTask(ctx, task.ID)
	if err != nil {
		return TaskExecutionTargetState{}, err
	}
	if runCount == 0 {
		return TaskExecutionTargetState{Kind: TaskExecutionTargetStateUnstarted}, nil
	}
	return TaskExecutionTargetState{Kind: TaskExecutionTargetStateLegacyMissing}, nil
}
