package workflowexecution

import (
	"sort"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type currentNodeInterruptFence struct {
	taskID workflow.TaskID
}

// currentNodeInterruptState indexes one Task-wide lifecycle fence only through
// the canonical Current Node and Exact Execution Scope identities it owns.
// Callers hold CurrentNodeController.mu.
type currentNodeInterruptState struct {
	byScope       map[runtimeids.ExecutionScopeID]*currentNodeInterruptFence
	byCurrentNode map[workflow.CurrentNodeReferenceKey]*currentNodeInterruptFence
}

func newCurrentNodeInterruptState() currentNodeInterruptState {
	return currentNodeInterruptState{
		byScope:       make(map[runtimeids.ExecutionScopeID]*currentNodeInterruptFence),
		byCurrentNode: make(map[workflow.CurrentNodeReferenceKey]*currentNodeInterruptFence),
	}
}

func (s *currentNodeInterruptState) beginTask(taskID workflow.TaskID) (*currentNodeInterruptFence, error) {
	if s.taskActive(taskID) {
		return nil, ErrTaskExecutionNotQuiescent
	}
	return &currentNodeInterruptFence{taskID: taskID}, nil
}

func (s *currentNodeInterruptState) taskActive(taskID workflow.TaskID) bool {
	for _, fence := range s.byScope {
		if fence.taskID == taskID {
			return true
		}
	}
	for _, fence := range s.byCurrentNode {
		if fence.taskID == taskID {
			return true
		}
	}
	return false
}

func (s *currentNodeInterruptState) addScope(fence *currentNodeInterruptFence, scopeID runtimeids.ExecutionScopeID) {
	if existing := s.byScope[scopeID]; existing != nil && existing != fence {
		panic("exact execution scope belongs to conflicting Task interruption fences")
	}
	s.byScope[scopeID] = fence
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

func (s *currentNodeInterruptState) scopeFenced(scopeID runtimeids.ExecutionScopeID) bool {
	return s.byScope[scopeID] != nil
}

func (s *currentNodeInterruptState) currentNodeFenced(key workflow.CurrentNodeReferenceKey) bool {
	return s.byCurrentNode[key] != nil
}

func (s *currentNodeInterruptState) finishScope(scopeID runtimeids.ExecutionScopeID) {
	delete(s.byScope, scopeID)
}

func (s *currentNodeInterruptState) finishCurrentNode(key workflow.CurrentNodeReferenceKey) {
	delete(s.byCurrentNode, key)
}

func (s *currentNodeInterruptState) fenceActive(fence *currentNodeInterruptFence) bool {
	for _, candidate := range s.byScope {
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
	seen := make(map[workflow.TaskID]struct{}, len(s.byScope)+len(s.byCurrentNode))
	for _, fence := range s.byScope {
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
