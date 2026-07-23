package workflowexecution

import (
	"errors"
	"fmt"
	"sync"

	"core/server/workflow"
)

// AutomaticIntents is Workflow Execution's volatile admission queue for
// automatic workflow starts. It deliberately has no durable representation.
type AutomaticIntents struct {
	mu      sync.Mutex
	queue   []workflow.RunID
	pending map[workflow.RunID]struct{}
	queued  map[workflow.RunID]struct{}
	wake    chan struct{}
}

func NewAutomaticIntents() *AutomaticIntents {
	return &AutomaticIntents{
		pending: make(map[workflow.RunID]struct{}),
		queued:  make(map[workflow.RunID]struct{}),
		wake:    make(chan struct{}, 1),
	}
}

func (i *AutomaticIntents) RegisterAutomaticStarts(runIDs []workflow.RunID) error {
	if len(runIDs) == 0 {
		return nil
	}
	if i == nil {
		return errors.New("automatic workflow intents are required")
	}
	registeredRunIDs := append([]workflow.RunID(nil), runIDs...)
	for index, runID := range registeredRunIDs {
		if runID == "" {
			return fmt.Errorf("automatic workflow start run id at index %d is blank", index)
		}
	}
	i.mu.Lock()
	requested := false
	for _, runID := range registeredRunIDs {
		if _, exists := i.pending[runID]; exists {
			continue
		}
		i.pending[runID] = struct{}{}
		i.queued[runID] = struct{}{}
		i.queue = append(i.queue, runID)
		requested = true
	}
	for _, runID := range registeredRunIDs {
		if _, exists := i.pending[runID]; !exists {
			i.mu.Unlock()
			return fmt.Errorf("automatic workflow start run %q was not registered", runID)
		}
	}
	i.mu.Unlock()
	if requested {
		select {
		case i.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (i *AutomaticIntents) Notifications() <-chan struct{} {
	if i == nil {
		return nil
	}
	return i.wake
}

func (i *AutomaticIntents) PendingRunIDs() []workflow.RunID {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	runIDs := make([]workflow.RunID, 0, len(i.pending))
	for runID := range i.pending {
		runIDs = append(runIDs, runID)
	}
	return runIDs
}

func (i *AutomaticIntents) Take(limit int) []workflow.RunID {
	if i == nil || limit <= 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.queue) == 0 {
		return nil
	}
	if limit > len(i.queue) {
		limit = len(i.queue)
	}
	runIDs := append([]workflow.RunID(nil), i.queue[:limit]...)
	i.queue = append([]workflow.RunID(nil), i.queue[limit:]...)
	for _, runID := range runIDs {
		delete(i.queued, runID)
	}
	return runIDs
}

func (i *AutomaticIntents) ReturnFront(runIDs []workflow.RunID) {
	if i == nil || len(runIDs) == 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	retry := make([]workflow.RunID, 0, len(runIDs))
	for _, runID := range runIDs {
		if runID == "" {
			continue
		}
		if _, exists := i.queued[runID]; exists {
			continue
		}
		if _, exists := i.pending[runID]; !exists {
			i.pending[runID] = struct{}{}
		}
		i.queued[runID] = struct{}{}
		retry = append(retry, runID)
	}
	if len(retry) == 0 {
		return
	}
	i.queue = append(retry, i.queue...)
}

func (i *AutomaticIntents) Resolve(runIDs ...workflow.RunID) {
	if i == nil || len(runIDs) == 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	remove := make(map[workflow.RunID]struct{}, len(runIDs))
	for _, runID := range runIDs {
		if runID == "" {
			continue
		}
		delete(i.pending, runID)
		delete(i.queued, runID)
		remove[runID] = struct{}{}
	}
	if len(remove) == 0 || len(i.queue) == 0 {
		return
	}
	retained := i.queue[:0]
	for _, runID := range i.queue {
		if _, removed := remove[runID]; !removed {
			retained = append(retained, runID)
		}
	}
	i.queue = retained
}
