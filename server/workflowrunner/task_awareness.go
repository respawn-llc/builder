package workflowrunner

import (
	"context"
	"errors"
	"fmt"

	"core/server/workflow"
	"core/server/workflowruntime"
)

type TaskCommentCounter interface {
	CountTaskComments(context.Context, workflow.TaskID) (int64, error)
}

type TaskDependencyCounter interface {
	CountUnsatisfiedBlockers(context.Context, string) (int, error)
}

type taskAwarenessSource struct {
	comments     TaskCommentCounter
	dependencies TaskDependencyCounter
}

func NewTaskAwarenessSource(
	comments TaskCommentCounter,
	dependencies TaskDependencyCounter,
) (workflowruntime.TaskAwarenessSource, error) {
	if comments == nil {
		return nil, errors.New("Task comment counter is required")
	}
	if dependencies == nil {
		return nil, errors.New("Task dependency counter is required")
	}
	return taskAwarenessSource{comments: comments, dependencies: dependencies}, nil
}

func (s taskAwarenessSource) TaskAwareness(
	ctx context.Context,
	taskID workflow.TaskID,
) (workflowruntime.TaskAwareness, error) {
	commentCount, err := s.comments.CountTaskComments(ctx, taskID)
	if err != nil {
		return workflowruntime.TaskAwareness{}, fmt.Errorf("count Task comments for %q: %w", taskID, err)
	}
	if commentCount < 0 {
		return workflowruntime.TaskAwareness{}, fmt.Errorf("count Task comments for %q returned invalid count %d", taskID, commentCount)
	}
	dependencyCount, err := s.dependencies.CountUnsatisfiedBlockers(ctx, string(taskID))
	if err != nil {
		return workflowruntime.TaskAwareness{}, fmt.Errorf("count unsatisfied Task dependencies for %q: %w", taskID, err)
	}
	if dependencyCount < 0 {
		return workflowruntime.TaskAwareness{}, fmt.Errorf("count unsatisfied Task dependencies for %q returned invalid count %d", taskID, dependencyCount)
	}
	return workflowruntime.TaskAwareness{
		CommentCount:               commentCount,
		UnsatisfiedDependencyCount: int64(dependencyCount),
	}, nil
}
