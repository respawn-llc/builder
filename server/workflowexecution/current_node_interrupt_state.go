package workflowexecution

import (
	"sort"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type currentNodeInterruptFence struct {
	taskID workflow.TaskID
	done   chan struct{}
	closed bool
}

// currentNodeInterruptState indexes one Task-wide lifecycle fence only through
// controller-owned Current Node operation and Current Node identities.
// Callers hold CurrentNodeController.mu.
type currentNodeInterruptState struct {
	byOperation   map[runtimeids.CurrentNodeOperationID]*currentNodeInterruptFence
	byCurrentNode map[workflow.CurrentNodeReferenceKey]*currentNodeInterruptFence
}

func newCurrentNodeInterruptState() currentNodeInterruptState {
	return currentNodeInterruptState{
		byOperation:   make(map[runtimeids.CurrentNodeOperationID]*currentNodeInterruptFence),
		byCurrentNode: make(map[workflow.CurrentNodeReferenceKey]*currentNodeInterruptFence),
	}
}

func (s *currentNodeInterruptState) beginTask(taskID workflow.TaskID) (*currentNodeInterruptFence, error) {
	if s.taskActive(taskID) {
		return nil, ErrTaskExecutionNotQuiescent
	}
	return &currentNodeInterruptFence{taskID: taskID, done: make(chan struct{})}, nil
}

func (s *currentNodeInterruptState) taskActive(taskID workflow.TaskID) bool {
	return s.taskFence(taskID) != nil
}

func (s *currentNodeInterruptState) taskFence(taskID workflow.TaskID) *currentNodeInterruptFence {
	for _, fence := range s.byOperation {
		if fence.taskID == taskID {
			return fence
		}
	}
	for _, fence := range s.byCurrentNode {
		if fence.taskID == taskID {
			return fence
		}
	}
	return nil
}

func (s *currentNodeInterruptState) addOperation(
	fence *currentNodeInterruptFence,
	operationID runtimeids.CurrentNodeOperationID,
) {
	if existing := s.byOperation[operationID]; existing != nil && existing != fence {
		panic("Current Node operation belongs to conflicting Task interruption fences")
	}
	s.byOperation[operationID] = fence
}

func (s *currentNodeInterruptState) addCurrentNode(
	fence *currentNodeInterruptFence,
	key workflow.CurrentNodeReferenceKey,
) {
	if existing := s.byCurrentNode[key]; existing != nil && existing != fence {
		panic("Current Node belongs to conflicting Task interruption fences")
	}
	s.byCurrentNode[key] = fence
}

func (s *currentNodeInterruptState) operationFenced(operationID runtimeids.CurrentNodeOperationID) bool {
	return s.byOperation[operationID] != nil
}

func (s *currentNodeInterruptState) currentNodeFenced(key workflow.CurrentNodeReferenceKey) bool {
	return s.byCurrentNode[key] != nil
}

func (s *currentNodeInterruptState) finishOperation(operationID runtimeids.CurrentNodeOperationID) {
	fence := s.byOperation[operationID]
	delete(s.byOperation, operationID)
	s.finishFence(fence)
}

func (s *currentNodeInterruptState) finishCurrentNode(key workflow.CurrentNodeReferenceKey) {
	fence := s.byCurrentNode[key]
	delete(s.byCurrentNode, key)
	s.finishFence(fence)
}

func (s *currentNodeInterruptState) finishFence(fence *currentNodeInterruptFence) {
	if fence == nil || fence.closed || s.fenceActive(fence) {
		return
	}
	fence.closed = true
	close(fence.done)
}

func (s *currentNodeInterruptState) fenceActive(fence *currentNodeInterruptFence) bool {
	for _, candidate := range s.byOperation {
		if candidate == fence {
			return true
		}
	}
	for _, candidate := range s.byCurrentNode {
		if candidate == fence {
			return true
		}
	}
	return false
}

func (s *currentNodeInterruptState) taskIDs() []workflow.TaskID {
	seen := make(map[workflow.TaskID]struct{}, len(s.byOperation)+len(s.byCurrentNode))
	for _, fence := range s.byOperation {
		seen[fence.taskID] = struct{}{}
	}
	for _, fence := range s.byCurrentNode {
		seen[fence.taskID] = struct{}{}
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
