package workflowexecution

import (
	"context"
	"fmt"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

// currentNodeRunID is an opaque, process-local execution generation. Values
// are allocated monotonically and are never reused during a controller's
// lifetime.
type currentNodeRunID struct {
	sequence uint64
}

func (id currentNodeRunID) valid() bool {
	return id.sequence != 0
}

type currentNodeRunPhase uint8

const (
	currentNodeRunStaged currentNodeRunPhase = iota
	currentNodeRunHeld
	currentNodeRunQueued
	currentNodeRunLaunching
	currentNodeRunExact
	currentNodeRunRetiring
)

type currentNodeRun struct {
	id                 currentNodeRunID
	reference          workflow.CurrentNodeReference
	key                workflow.CurrentNodeReferenceKey
	policy             currentNodeAdmissionPolicy
	phase              currentNodeRunPhase
	preparation        TaskStartPreparation
	taskPromptDelivery workflowruntime.TaskPromptDelivery
	assignmentSteer    CurrentNodeAssignmentSteer
	predecessor        *currentNodeRunID
	phaseChanged       chan struct{}
	launchContext      context.Context
	launchCancel       context.CancelFunc
	admissionDone      chan struct{}
	lease              *sessionruntime.WorkflowExecutionLease
	exactScopeID       *runtimeids.ExecutionScopeID
	agentCapacity      bool
}

func (run *currentNodeRun) transition(phase currentNodeRunPhase) {
	if run == nil {
		panic("transition nil current node Run")
	}
	if run.phase == phase {
		return
	}
	run.phase = phase
	close(run.phaseChanged)
	run.phaseChanged = make(chan struct{})
}

func (run *currentNodeRun) launching() bool {
	return run != nil && run.phase == currentNodeRunLaunching
}

func (run *currentNodeRun) exact() bool {
	return run != nil && run.phase == currentNodeRunExact && run.exactScopeID != nil
}

func (c *CurrentNodeController) allocateRunLocked(start currentNodeQueuedStart) (*currentNodeRun, bool, error) {
	key, err := start.reference.Key()
	if err != nil {
		return nil, false, err
	}
	if currentID, exists := c.currentRuns[key]; exists {
		if current := c.runs[currentID]; current != nil {
			return current, false, nil
		}
		panic(fmt.Sprintf("current node index %v points to absent Run generation", key))
	}
	c.nextRunSequence++
	if c.nextRunSequence == 0 {
		panic("current node Run identity overflow")
	}
	run := &currentNodeRun{
		id:                 currentNodeRunID{sequence: c.nextRunSequence},
		reference:          start.reference,
		key:                key,
		policy:             start.policy,
		phase:              currentNodeRunStaged,
		preparation:        start.preparation,
		taskPromptDelivery: start.taskPromptDelivery,
		assignmentSteer:    start.assignmentSteer,
		phaseChanged:       make(chan struct{}),
	}
	c.runs[run.id] = run
	c.currentRuns[key] = run.id
	return run, true, nil
}

func (c *CurrentNodeController) stageSuccessorRunLocked(
	start currentNodeQueuedStart,
	predecessorID currentNodeRunID,
) (*currentNodeRun, error) {
	key, err := start.reference.Key()
	if err != nil {
		return nil, err
	}
	if currentID, exists := c.currentRuns[key]; exists && currentID != predecessorID {
		return nil, fmt.Errorf("current node %v already belongs to another Run generation", start.reference)
	}
	predecessor := c.runs[predecessorID]
	if predecessor == nil {
		return nil, fmt.Errorf("predecessor Run generation is absent for current node %v", start.reference)
	}
	c.nextRunSequence++
	if c.nextRunSequence == 0 {
		panic("current node Run identity overflow")
	}
	run := &currentNodeRun{
		id:                 currentNodeRunID{sequence: c.nextRunSequence},
		reference:          start.reference,
		key:                key,
		policy:             start.policy,
		phase:              currentNodeRunStaged,
		preparation:        start.preparation,
		taskPromptDelivery: start.taskPromptDelivery,
		assignmentSteer:    start.assignmentSteer,
		predecessor:        &predecessorID,
		phaseChanged:       make(chan struct{}),
	}
	c.runs[run.id] = run
	return run, nil
}

func (c *CurrentNodeController) activateRunLocked(run *currentNodeRun, phase currentNodeRunPhase) error {
	if run == nil || c.runs[run.id] != run {
		return errorsNewCurrentNodeRunAbsent()
	}
	if run.phase != currentNodeRunStaged {
		return fmt.Errorf("current node Run %d has phase %d, want staged", run.id.sequence, run.phase)
	}
	if run.predecessor != nil {
		if current, exists := c.currentRuns[run.key]; exists && current != *run.predecessor {
			return fmt.Errorf("current node predecessor generation changed before successor activation")
		}
		c.currentRuns[run.key] = run.id
	} else if current := c.currentRuns[run.key]; current != run.id {
		return fmt.Errorf("current node generation changed before Run activation")
	}
	run.transition(phase)
	return nil
}

func (c *CurrentNodeController) stageSuccessorRunsLocked(
	starts []currentNodeQueuedStart,
	predecessorID currentNodeRunID,
	phase currentNodeRunPhase,
) ([]currentNodeRunID, error) {
	ids := make([]currentNodeRunID, 0, len(starts))
	rollback := func() {
		predecessor := c.runs[predecessorID]
		for _, id := range ids {
			run := c.runs[id]
			sameReference := predecessor != nil && run != nil && run.key == predecessor.key
			c.removeRunLocked(id)
			if sameReference {
				c.currentRuns[predecessor.key] = predecessorID
			}
		}
	}
	for _, start := range starts {
		run, err := c.stageSuccessorRunLocked(start, predecessorID)
		if err != nil {
			rollback()
			return nil, err
		}
		ids = append(ids, run.id)
		if err := c.activateRunLocked(run, phase); err != nil {
			rollback()
			return nil, err
		}
	}
	if len(ids) != 0 {
		predecessor := c.runs[predecessorID]
		if predecessor != nil {
			if current, exists := c.currentRuns[predecessor.key]; exists && current == predecessorID {
				delete(c.currentRuns, predecessor.key)
			}
		}
	}
	return ids, nil
}

func (c *CurrentNodeController) startsForRunsLocked(ids []currentNodeRunID) []currentNodeQueuedStart {
	starts := make([]currentNodeQueuedStart, 0, len(ids))
	for _, id := range ids {
		run := c.runs[id]
		if run == nil {
			continue
		}
		starts = append(starts, currentNodeQueuedStart{
			reference:          run.reference,
			preparation:        run.preparation,
			taskPromptDelivery: run.taskPromptDelivery,
			assignmentSteer:    run.assignmentSteer,
			policy:             run.policy,
		})
	}
	return starts
}

func errorsNewCurrentNodeRunAbsent() error {
	return fmt.Errorf("current node Run generation is absent")
}

func (c *CurrentNodeController) removeRunLocked(id currentNodeRunID) {
	run := c.runs[id]
	if run == nil {
		return
	}
	if run.launchCancel != nil {
		run.launchCancel()
		run.launchCancel = nil
		run.launchContext = nil
	}
	if run.exactScopeID != nil {
		if current, exists := c.exactRuns[*run.exactScopeID]; exists && current == id {
			delete(c.exactRuns, *run.exactScopeID)
		}
	}
	if current, exists := c.currentRuns[run.key]; exists && current == id {
		delete(c.currentRuns, run.key)
	}
	if run.agentCapacity {
		if c.agentCapacityActive <= 0 {
			panic("automatic Agent capacity released without an owning Run")
		}
		run.agentCapacity = false
		c.agentCapacityActive--
	}
	close(run.phaseChanged)
	delete(c.runs, id)
}

func (c *CurrentNodeController) runByScopeLocked(scopeID runtimeids.ExecutionScopeID) (*currentNodeRun, bool) {
	id, exists := c.exactRuns[scopeID]
	if !exists {
		return nil, false
	}
	run := c.runs[id]
	if run == nil {
		panic(fmt.Sprintf("exact scope %s points to absent Run generation", scopeID))
	}
	return run, true
}

func (c *CurrentNodeController) launchingRunByScopeLocked(scopeID runtimeids.ExecutionScopeID) (*currentNodeRun, bool) {
	for _, run := range c.runs {
		if run.launching() && run.lease != nil && run.lease.ScopeID() == scopeID {
			return run, true
		}
	}
	return nil, false
}

func (c *CurrentNodeController) currentRunLocked(key workflow.CurrentNodeReferenceKey) (*currentNodeRun, bool) {
	id, exists := c.currentRuns[key]
	if !exists {
		return nil, false
	}
	run := c.runs[id]
	if run == nil {
		panic(fmt.Sprintf("current node %v points to absent Run generation", key))
	}
	return run, true
}

func (c *CurrentNodeController) runPredecessorActiveLocked(run *currentNodeRun) bool {
	if run == nil || run.predecessor == nil {
		return false
	}
	_, active := c.runs[*run.predecessor]
	return active
}
