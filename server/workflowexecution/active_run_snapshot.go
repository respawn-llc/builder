package workflowexecution

import (
	"errors"
	"sort"
	"strings"

	"core/server/workflow"
)

type SchedulerActiveRunPhase uint8

const (
	SchedulerActiveRunPhaseStarting SchedulerActiveRunPhase = iota + 1
	SchedulerActiveRunPhaseRunning
)

type SchedulerActiveRunRevision uint64

type SchedulerActiveRunObservation struct {
	RunID       workflow.RunID
	TaskID      workflow.TaskID
	PlacementID workflow.PlacementID
	NodeID      workflow.NodeID
	Generation  int64
	Phase       SchedulerActiveRunPhase
}

type SchedulerActiveRunSnapshot struct {
	Revision   SchedulerActiveRunRevision
	ActiveRuns []SchedulerActiveRunObservation
}

func (o SchedulerActiveRunObservation) Validate() error {
	switch {
	case strings.TrimSpace(string(o.RunID)) == "":
		return errors.New("scheduler active run id is required")
	case strings.TrimSpace(string(o.TaskID)) == "":
		return errors.New("scheduler active task id is required")
	case strings.TrimSpace(string(o.PlacementID)) == "":
		return errors.New("scheduler active placement id is required")
	case strings.TrimSpace(string(o.NodeID)) == "":
		return errors.New("scheduler active node id is required")
	case o.Generation <= 0:
		return errors.New("scheduler active run generation must be positive")
	case o.Phase != SchedulerActiveRunPhaseStarting && o.Phase != SchedulerActiveRunPhaseRunning:
		return errors.New("scheduler active run phase is invalid")
	default:
		return nil
	}
}

type schedulerActiveRun struct {
	request SchedulerStartRunRequest
	phase   SchedulerActiveRunPhase
}

func (s *SchedulerService) ActiveRunSnapshot() SchedulerActiveRunSnapshot {
	if s == nil {
		return SchedulerActiveRunSnapshot{ActiveRuns: []SchedulerActiveRunObservation{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	activeRuns := make([]SchedulerActiveRunObservation, 0, len(s.active))
	for _, active := range s.active {
		activeRuns = append(activeRuns, SchedulerActiveRunObservation{
			RunID:       active.request.RunID,
			TaskID:      active.request.TaskID,
			PlacementID: active.request.PlacementID,
			NodeID:      active.request.NodeID,
			Generation:  active.request.Generation,
			Phase:       active.phase,
		})
	}
	sort.Slice(activeRuns, func(i, j int) bool {
		if activeRuns[i].RunID != activeRuns[j].RunID {
			return activeRuns[i].RunID < activeRuns[j].RunID
		}
		return activeRuns[i].Generation < activeRuns[j].Generation
	})
	return SchedulerActiveRunSnapshot{
		Revision:   s.activeRevision,
		ActiveRuns: activeRuns,
	}
}

func (s *SchedulerService) recordActiveRunMutationLocked() {
	s.activeRevision++
	if s.activeRevision == 0 {
		panic("workflow scheduler active-run observation revision overflow")
	}
}

func (s *SchedulerService) markActiveRunRunning(request SchedulerStartRunRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.active[request.RunID]
	if !ok || active.request.Generation != request.Generation {
		return
	}
	if active.phase == SchedulerActiveRunPhaseRunning {
		return
	}
	active.phase = SchedulerActiveRunPhaseRunning
	s.active[request.RunID] = active
	s.recordActiveRunMutationLocked()
}
