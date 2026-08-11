package workflowexecution

import (
	"fmt"

	"core/server/workflow"
)

type currentNodeAutomaticQueueLane uint8

const (
	currentNodeAutomaticAgentLane currentNodeAutomaticQueueLane = iota
	currentNodeAutomaticScriptLane
)

type currentNodeAutomaticQueueLaneState struct {
	first *currentNodeAutomaticQueueEntry
	last  *currentNodeAutomaticQueueEntry
}

type currentNodeAutomaticQueueLanes [2]currentNodeAutomaticQueueLaneState

type currentNodeAutomaticTaskQueue struct {
	lanes currentNodeAutomaticQueueLanes
}

type currentNodeAutomaticQueue struct {
	first     *currentNodeAutomaticQueueEntry
	last      *currentNodeAutomaticQueueEntry
	lanes     currentNodeAutomaticQueueLanes
	tasks     map[workflow.TaskID]*currentNodeAutomaticTaskQueue
	nextOrder uint64
	size      int
}

type currentNodeAutomaticQueueEntry struct {
	runID      currentNodeRunID
	taskID     workflow.TaskID
	policy     currentNodeAdmissionPolicy
	order      uint64
	globalPrev *currentNodeAutomaticQueueEntry
	globalNext *currentNodeAutomaticQueueEntry
	policyPrev *currentNodeAutomaticQueueEntry
	policyNext *currentNodeAutomaticQueueEntry
	taskPrev   *currentNodeAutomaticQueueEntry
	taskNext   *currentNodeAutomaticQueueEntry
}

func currentNodeAutomaticQueueLaneForPolicy(policy currentNodeAdmissionPolicy) currentNodeAutomaticQueueLane {
	switch policy {
	case currentNodeAdmissionAutomaticAgent:
		return currentNodeAutomaticAgentLane
	case currentNodeAdmissionAutomaticScript:
		return currentNodeAutomaticScriptLane
	default:
		panic(fmt.Sprintf("automatic queue received non-automatic policy %d", policy))
	}
}

func (lanes *currentNodeAutomaticQueueLanes) lane(policy currentNodeAdmissionPolicy) *currentNodeAutomaticQueueLaneState {
	return &lanes[currentNodeAutomaticQueueLaneForPolicy(policy)]
}

func (q *currentNodeAutomaticQueue) append(run *currentNodeRun) {
	if run == nil {
		panic("automatic queue requires a Run generation")
	}
	q.nextOrder++
	if q.nextOrder == 0 {
		panic("automatic queue order overflow")
	}
	entry := &currentNodeAutomaticQueueEntry{
		runID:      run.id,
		taskID:     run.reference.TaskID,
		policy:     run.policy,
		order:      q.nextOrder,
		globalPrev: q.last,
	}
	if q.last == nil {
		q.first = entry
	} else {
		q.last.globalNext = entry
	}
	q.last = entry

	if q.tasks == nil {
		q.tasks = make(map[workflow.TaskID]*currentNodeAutomaticTaskQueue)
	}
	taskQueue := q.tasks[run.reference.TaskID]
	if taskQueue == nil {
		taskQueue = &currentNodeAutomaticTaskQueue{}
		q.tasks[run.reference.TaskID] = taskQueue
	}
	policyLane := q.lanes.lane(run.policy)
	taskLane := taskQueue.lanes.lane(run.policy)
	entry.policyPrev = policyLane.last
	entry.taskPrev = taskLane.last
	if policyLane.last == nil {
		policyLane.first = entry
	} else {
		policyLane.last.policyNext = entry
	}
	policyLane.last = entry
	if taskLane.last == nil {
		taskLane.first = entry
	} else {
		taskLane.last.taskNext = entry
	}
	taskLane.last = entry
	q.size++
}

func (q *currentNodeAutomaticQueue) remove(entry *currentNodeAutomaticQueueEntry) currentNodeRunID {
	if entry.globalPrev == nil {
		q.first = entry.globalNext
	} else {
		entry.globalPrev.globalNext = entry.globalNext
	}
	if entry.globalNext == nil {
		q.last = entry.globalPrev
	} else {
		entry.globalNext.globalPrev = entry.globalPrev
	}

	taskQueue := q.tasks[entry.taskID]
	if taskQueue == nil {
		panic("automatic queue task index lost an entry")
	}
	policyLane := q.lanes.lane(entry.policy)
	taskLane := taskQueue.lanes.lane(entry.policy)
	if entry.policyPrev == nil {
		policyLane.first = entry.policyNext
	} else {
		entry.policyPrev.policyNext = entry.policyNext
	}
	if entry.policyNext == nil {
		policyLane.last = entry.policyPrev
	} else {
		entry.policyNext.policyPrev = entry.policyPrev
	}
	if entry.taskPrev == nil {
		taskLane.first = entry.taskNext
	} else {
		entry.taskPrev.taskNext = entry.taskNext
	}
	if entry.taskNext == nil {
		taskLane.last = entry.taskPrev
	} else {
		entry.taskNext.taskPrev = entry.taskPrev
	}
	if taskQueue.lanes[currentNodeAutomaticAgentLane].first == nil &&
		taskQueue.lanes[currentNodeAutomaticScriptLane].first == nil {
		delete(q.tasks, entry.taskID)
	}
	q.size--
	return entry.runID
}

func (q *currentNodeAutomaticQueue) clear() {
	q.first = nil
	q.last = nil
	q.lanes = currentNodeAutomaticQueueLanes{}
	q.tasks = nil
	q.nextOrder = 0
	q.size = 0
}

func (q *currentNodeAutomaticQueue) len() int {
	return q.size
}

func (q *currentNodeAutomaticQueue) selectEntry(
	lastTask *workflow.TaskID,
	agentAvailable bool,
	eligible func(currentNodeRunID) bool,
) (*currentNodeAutomaticQueueEntry, bool) {
	firstEligible := func(
		entry *currentNodeAutomaticQueueEntry,
		next func(*currentNodeAutomaticQueueEntry) *currentNodeAutomaticQueueEntry,
	) *currentNodeAutomaticQueueEntry {
		for entry != nil && eligible != nil && !eligible(entry.runID) {
			entry = next(entry)
		}
		return entry
	}
	if lastTask != nil {
		if taskQueue := q.tasks[*lastTask]; taskQueue != nil {
			script := firstEligible(
				taskQueue.lanes.lane(currentNodeAdmissionAutomaticScript).first,
				func(entry *currentNodeAutomaticQueueEntry) *currentNodeAutomaticQueueEntry {
					return entry.taskNext
				},
			)
			candidate := script
			if agentAvailable {
				agent := firstEligible(
					taskQueue.lanes.lane(currentNodeAdmissionAutomaticAgent).first,
					func(entry *currentNodeAutomaticQueueEntry) *currentNodeAutomaticQueueEntry {
						return entry.taskNext
					},
				)
				if agent != nil && (candidate == nil || agent.order < candidate.order) {
					candidate = agent
				}
			}
			if candidate != nil {
				return candidate, true
			}
		}
	}
	script := firstEligible(
		q.lanes.lane(currentNodeAdmissionAutomaticScript).first,
		func(entry *currentNodeAutomaticQueueEntry) *currentNodeAutomaticQueueEntry {
			return entry.policyNext
		},
	)
	if !agentAvailable {
		return script, script != nil
	}
	agent := firstEligible(
		q.lanes.lane(currentNodeAdmissionAutomaticAgent).first,
		func(entry *currentNodeAutomaticQueueEntry) *currentNodeAutomaticQueueEntry {
			return entry.policyNext
		},
	)
	if agent == nil {
		return script, script != nil
	}
	if script == nil || agent.order < script.order {
		return agent, true
	}
	return script, true
}
