package workflowview

import (
	"context"
	"errors"

	"core/server/metadata"
)

// TaskDependencyCounter is the startup-time dependency counter used by the
// workflow runner and persisted inspection before the recovered Controller
// exists. Its satisfaction implementation is shared with TaskDependencies.
type TaskDependencyCounter struct {
	satisfaction taskDependencySatisfaction
}

func NewTaskDependencyCounter(metadataStore *metadata.Store) (*TaskDependencyCounter, error) {
	if metadataStore == nil || metadataStore.Queries() == nil {
		return nil, errors.New("metadata store is required")
	}
	return &TaskDependencyCounter{
		satisfaction: taskDependencySatisfaction{
			queries:   metadataStore.Queries(),
			projector: NewTaskProjector(),
		},
	}, nil
}

func (c *TaskDependencyCounter) CountUnsatisfiedBlockers(ctx context.Context, taskID string) (int, error) {
	if c == nil {
		return 0, errors.New("task dependency counter is required")
	}
	return c.satisfaction.CountUnsatisfiedBlockers(ctx, taskID)
}
