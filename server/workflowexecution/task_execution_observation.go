package workflowexecution

import (
	"errors"
	"fmt"
	"sort"
	"sync"

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

type currentNodeControllerMutex struct {
	sync.Mutex
	owner *CurrentNodeController
}

func (m *currentNodeControllerMutex) Unlock() {
	if m.owner != nil {
		m.owner.publishTaskExecutionReadSnapshotLocked()
	}
	m.Mutex.Unlock()
}

// ObserveWorkflowTaskExecutions never waits for lifecycle ownership. It loads
// each owner's latest completed immutable projection.
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
	executions, err := c.authority.CurrentWorkflowTaskExecutionReadSnapshot()
	if err != nil {
		return WorkflowTaskExecutionObservation{}, err
	}
	observation.Executions = executions
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
	if taskIDs == nil {
		observation.Quiescence = cloneTaskQuiescence(current.quiescence)
		return observation, nil
	}
	for taskID := range selected {
		quiescent, exists := current.quiescence[taskID]
		if !exists {
			quiescent = true
		}
		observation.Quiescence[taskID] = quiescent
	}
	return observation, nil
}

func cloneTaskQuiescence(source map[workflow.TaskID]bool) map[workflow.TaskID]bool {
	cloned := make(map[workflow.TaskID]bool, len(source))
	for taskID, quiescent := range source {
		cloned[taskID] = quiescent
	}
	return cloned
}

func (c *CurrentNodeController) publishTaskExecutionReadSnapshotLocked() {
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
	for taskID := range c.nonQuiescentTaskIDsLocked() {
		snapshot.quiescence[taskID] = false
	}
	c.taskExecutionReads.Store(snapshot)
}

func (c *CurrentNodeController) nonQuiescentTaskIDsLocked() map[workflow.TaskID]struct{} {
	taskIDs := make(map[workflow.TaskID]struct{})
	add := func(taskID workflow.TaskID) {
		if taskID != "" {
			taskIDs[taskID] = struct{}{}
		}
	}
	for _, taskID := range c.interrupts.taskIDs() {
		add(taskID)
	}
	for _, batch := range c.preparationQueue {
		add(batch.taskID)
	}
	for _, batch := range c.preparationRunning {
		add(batch.taskID)
	}
	for _, gate := range c.gates {
		add(gate.reference.TaskID)
	}
	for _, live := range c.live {
		add(live.reference.TaskID)
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		add(entry.start.reference.TaskID)
	}
	for _, start := range c.automaticReservations {
		add(start.reference.TaskID)
	}
	for _, start := range c.explicitQueue {
		add(start.reference.TaskID)
	}
	for _, start := range c.explicitReservations {
		add(start.reference.TaskID)
	}
	for _, start := range c.admissionWorkers {
		add(start.reference.TaskID)
	}
	for _, starts := range c.heldStarts {
		for _, start := range starts {
			add(start.reference.TaskID)
		}
	}
	return taskIDs
}

func cloneConcurrencyQueued(source map[workflow.TaskID][]workflow.CurrentNodeReference) map[workflow.TaskID][]workflow.CurrentNodeReference {
	cloned := make(map[workflow.TaskID][]workflow.CurrentNodeReference, len(source))
	for taskID, references := range source {
		cloned[taskID] = append([]workflow.CurrentNodeReference(nil), references...)
	}
	return cloned
}
