package workflowexecution

import (
	"errors"
	"fmt"
	"sort"

	"core/server/sessionruntime"
	"core/server/workflow"
)

// WorkflowTaskExecutionObservation is one linearizable live Workflow
// observation. Exact executions are captured under the Authority live-state
// lock and selected Task Quiescence is captured under the Controller lock.
type WorkflowTaskExecutionObservation struct {
	Executions        map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	ConcurrencyQueued map[workflow.TaskID][]workflow.CurrentNodeReference
	Quiescence        map[workflow.TaskID]bool
}

// ObserveWorkflowTaskExecutions captures the global exact-execution map and,
// when taskIDs are supplied, their Controller-owned Quiescence under one live
// observation. The Authority lock is acquired before the Controller lock.
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
	err := c.authority.WithWorkflowTaskExecutionSnapshots(func(executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return errors.New("current node workflow controller is closed")
		}
		observation.Executions = executions
		if c.agentCapacityActive >= c.agentConcurrency {
			for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
				if entry.start.policy != currentNodeAdmissionAutomaticAgent {
					continue
				}
				taskID := entry.start.reference.TaskID
				observation.ConcurrencyQueued[taskID] = append(
					observation.ConcurrencyQueued[taskID],
					entry.start.reference,
				)
			}
		}
		for taskID, references := range observation.ConcurrencyQueued {
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
			observation.ConcurrencyQueued[taskID] = references
		}
		for taskID := range selected {
			quiescent, err := c.taskQuiescentLocked(taskID)
			if err != nil {
				return err
			}
			observation.Quiescence[taskID] = quiescent
		}
		return nil
	})
	if err != nil {
		return WorkflowTaskExecutionObservation{}, err
	}
	return observation, nil
}
