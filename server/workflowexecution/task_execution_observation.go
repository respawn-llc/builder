package workflowexecution

import (
	"errors"
	"fmt"
	"sort"

	"core/server/sessionruntime"
	"core/server/workflow"
)

type WorkflowTaskRunSnapshot struct {
	Queued                 []workflow.CurrentNodeReference
	InterruptibleLaunching []workflow.CurrentNodeReference
}

// WorkflowTaskExecutionObservation is one linearizable live Workflow
// observation. Exact executions are captured under the Authority live-state
// lock and selected Task Quiescence is captured under the Controller lock.
type WorkflowTaskExecutionObservation struct {
	Executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	Runs       map[workflow.TaskID]WorkflowTaskRunSnapshot
	Quiescence map[workflow.TaskID]bool
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
		Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{},
		Runs:       map[workflow.TaskID]WorkflowTaskRunSnapshot{},
		Quiescence: map[workflow.TaskID]bool{},
	}
	err := c.authority.WithWorkflowTaskExecutionSnapshots(func(executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return errors.New("current node workflow controller is closed")
		}
		if err := c.lifecycleAvailability.Available(); err != nil {
			return err
		}
		observation.Executions = executions
		for _, run := range c.runs {
			if run.stopping() || run.callbackErr != nil {
				continue
			}
			snapshot := observation.Runs[run.reference.TaskID]
			switch run.phase {
			case currentNodeRunQueued:
				snapshot.Queued = append(snapshot.Queued, run.reference)
			case currentNodeRunLaunching:
				snapshot.InterruptibleLaunching = append(snapshot.InterruptibleLaunching, run.reference)
			}
			observation.Runs[run.reference.TaskID] = snapshot
		}
		for taskID, snapshot := range observation.Runs {
			sortCurrentNodeReferences(snapshot.Queued)
			sortCurrentNodeReferences(snapshot.InterruptibleLaunching)
			observation.Runs[taskID] = snapshot
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

func sortCurrentNodeReferences(references []workflow.CurrentNodeReference) {
	sort.Slice(references, func(i, j int) bool {
		if references[i].TaskID != references[j].TaskID {
			return references[i].TaskID < references[j].TaskID
		}
		if references[i].NodeID != references[j].NodeID {
			return references[i].NodeID < references[j].NodeID
		}
		left, leftPresent := references[i].TransitionBranchKey()
		right, rightPresent := references[j].TransitionBranchKey()
		if leftPresent != rightPresent {
			return !leftPresent
		}
		return left < right
	})
}
