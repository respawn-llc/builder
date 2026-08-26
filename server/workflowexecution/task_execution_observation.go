package workflowexecution

import (
	"errors"
	"fmt"
	"sort"

	"core/server/sessionruntime"
	"core/server/workflow"
)

// WorkflowTaskExecutionObservation is a stale-tolerant read-model snapshot.
// Runtime and Controller facts may come from different completed refreshes.
type WorkflowTaskExecutionObservation struct {
	Executions        map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	ConcurrencyQueued map[workflow.TaskID][]workflow.CurrentNodeReference
	Quiescence        map[workflow.TaskID]bool
}

type workflowTaskControllerReadSnapshot struct {
	concurrencyQueued map[workflow.TaskID][]workflow.CurrentNodeReference
	quiescence        map[workflow.TaskID]bool
	closed            bool
}

// ObserveWorkflowTaskExecutions opportunistically refreshes the Controller
// projection without waiting for lifecycle ownership.
func (c *CurrentNodeController) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (WorkflowTaskExecutionObservation, error) {
	if c == nil {
		return WorkflowTaskExecutionObservation{}, errors.New("current node workflow controller is required")
	}
	selected := make(map[workflow.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID == "" {
			return WorkflowTaskExecutionObservation{}, errors.New("workflow task id is required")
		}
		if _, exists := selected[taskID]; exists {
			return WorkflowTaskExecutionObservation{}, fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
		selected[taskID] = struct{}{}
	}

	observation := WorkflowTaskExecutionObservation{
		Executions:        map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{},
		ConcurrencyQueued: map[workflow.TaskID][]workflow.CurrentNodeReference{},
		Quiescence:        map[workflow.TaskID]bool{},
	}
	executions, err := c.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return WorkflowTaskExecutionObservation{}, err
	}
	observation.Executions = executions
	if c.mu.TryLock() {
		snapshot := &workflowTaskControllerReadSnapshot{
			concurrencyQueued: map[workflow.TaskID][]workflow.CurrentNodeReference{},
			quiescence:        map[workflow.TaskID]bool{},
			closed:            c.closed,
		}
		if c.agentCapacityActive >= c.agentConcurrency {
			for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
				if entry.start.policy != currentNodeAdmissionAutomaticAgent {
					continue
				}
				taskID := entry.start.reference.TaskID
				snapshot.concurrencyQueued[taskID] = append(
					snapshot.concurrencyQueued[taskID],
					entry.start.reference,
				)
			}
		}
		for taskID, references := range snapshot.concurrencyQueued {
			sort.Slice(references, func(i, j int) bool {
				left := references[i]
				right := references[j]
				if left.NodeID != right.NodeID {
					return left.NodeID < right.NodeID
				}
				leftBranch, leftScoped := left.TransitionBranchKey()
				rightBranch, rightScoped := right.TransitionBranchKey()
				if leftScoped != rightScoped {
					return !leftScoped
				}
				return leftBranch < rightBranch
			})
			snapshot.concurrencyQueued[taskID] = references
		}
		for taskID := range selected {
			snapshot.quiescence[taskID] = !c.closed && c.taskExecutionQuiescentLocked(taskID)
		}
		c.mu.Unlock()
		c.taskExecutionReads.Store(snapshot)
	}
	current := c.taskExecutionReads.Load()
	if current == nil {
		for taskID := range selected {
			observation.Quiescence[taskID] = false
		}
		return observation, nil
	}
	if current.closed {
		return WorkflowTaskExecutionObservation{}, errors.New("current node workflow controller is closed")
	}
	observation.ConcurrencyQueued = cloneConcurrencyQueued(current.concurrencyQueued)
	for taskID := range selected {
		quiescent, exists := current.quiescence[taskID]
		if !exists {
			quiescent = false
		}
		observation.Quiescence[taskID] = quiescent && len(observation.Executions[taskID].Executions) == 0
	}
	return observation, nil
}

func cloneConcurrencyQueued(source map[workflow.TaskID][]workflow.CurrentNodeReference) map[workflow.TaskID][]workflow.CurrentNodeReference {
	cloned := make(map[workflow.TaskID][]workflow.CurrentNodeReference, len(source))
	for taskID, references := range source {
		cloned[taskID] = append([]workflow.CurrentNodeReference(nil), references...)
	}
	return cloned
}
