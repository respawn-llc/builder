package workflowexecution

import (
	"sort"

	"core/server/workflow"
)

type currentNodeInterruptFence struct {
	taskID workflow.TaskID
	done   chan struct{}
	closed bool
}

func (c *CurrentNodeController) beginTaskInterruptLocked(taskID workflow.TaskID) (*currentNodeInterruptFence, error) {
	if c.taskInterruptActiveLocked(taskID) {
		return nil, ErrTaskExecutionNotQuiescent
	}
	return &currentNodeInterruptFence{taskID: taskID, done: make(chan struct{})}, nil
}

func (c *CurrentNodeController) taskInterruptFenceLocked(taskID workflow.TaskID) *currentNodeInterruptFence {
	for _, run := range c.runs {
		if run.interruptFence != nil && run.interruptFence.taskID == taskID {
			return run.interruptFence
		}
	}
	return nil
}

func (c *CurrentNodeController) taskInterruptActiveLocked(taskID workflow.TaskID) bool {
	return c.taskInterruptFenceLocked(taskID) != nil
}

func (c *CurrentNodeController) fenceRunLocked(run *currentNodeRun, fence *currentNodeInterruptFence) {
	if run == nil || fence == nil {
		panic("workflow interruption requires a Run and fence")
	}
	if run.interruptFence != nil && run.interruptFence != fence {
		panic("Run generation belongs to conflicting Task interruption fences")
	}
	run.interruptFence = fence
	run.stop = currentNodeRunStopInterrupting
}

func (c *CurrentNodeController) finishInterruptFenceLocked(fence *currentNodeInterruptFence) {
	if fence == nil || fence.closed || c.interruptFenceActiveLocked(fence) {
		return
	}
	fence.closed = true
	close(fence.done)
}

func (c *CurrentNodeController) interruptFenceActiveLocked(fence *currentNodeInterruptFence) bool {
	for _, run := range c.runs {
		if run.interruptFence == fence {
			return true
		}
	}
	return false
}

func (c *CurrentNodeController) interruptingTaskIDsLocked() []workflow.TaskID {
	seen := make(map[workflow.TaskID]struct{})
	for _, run := range c.runs {
		if run.interruptFence != nil {
			seen[run.interruptFence.taskID] = struct{}{}
		}
	}
	taskIDs := make([]workflow.TaskID, 0, len(seen))
	for taskID := range seen {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Slice(taskIDs, func(i int, j int) bool {
		return taskIDs[i] < taskIDs[j]
	})
	return taskIDs
}
