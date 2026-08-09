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
	start      currentNodeQueuedStart
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

func (q *currentNodeAutomaticQueue) append(start currentNodeQueuedStart) {
	q.nextOrder++
	if q.nextOrder == 0 {
		panic("automatic queue order overflow")
	}
	entry := &currentNodeAutomaticQueueEntry{
		start:      start,
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
	taskQueue := q.tasks[start.reference.TaskID]
	if taskQueue == nil {
		taskQueue = &currentNodeAutomaticTaskQueue{}
		q.tasks[start.reference.TaskID] = taskQueue
	}
	policyLane := q.lanes.lane(start.policy)
	taskLane := taskQueue.lanes.lane(start.policy)
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

func (q *currentNodeAutomaticQueue) remove(entry *currentNodeAutomaticQueueEntry) currentNodeQueuedStart {
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

	taskQueue := q.tasks[entry.start.reference.TaskID]
	if taskQueue == nil {
		panic("automatic queue task index lost an entry")
	}
	policyLane := q.lanes.lane(entry.start.policy)
	taskLane := taskQueue.lanes.lane(entry.start.policy)
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
		delete(q.tasks, entry.start.reference.TaskID)
	}
	q.size--
	return entry.start
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

func (q *currentNodeAutomaticQueue) selectEntry(lastTask *workflow.TaskID, agentAvailable bool) (*currentNodeAutomaticQueueEntry, bool) {
	if lastTask != nil {
		if taskQueue := q.tasks[*lastTask]; taskQueue != nil {
			script := taskQueue.lanes.lane(currentNodeAdmissionAutomaticScript).first
			candidate := script
			if agentAvailable {
				agent := taskQueue.lanes.lane(currentNodeAdmissionAutomaticAgent).first
				if agent != nil && (candidate == nil || agent.order < candidate.order) {
					candidate = agent
				}
			}
			if candidate != nil {
				return candidate, true
			}
		}
	}
	script := q.lanes.lane(currentNodeAdmissionAutomaticScript).first
	if !agentAvailable {
		return script, script != nil
	}
	agent := q.lanes.lane(currentNodeAdmissionAutomaticAgent).first
	if agent == nil {
		return script, script != nil
	}
	if script == nil || agent.order < script.order {
		return agent, true
	}
	return script, true
}
