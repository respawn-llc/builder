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
	active  map[workflow.RunID]struct{}
	held    map[workflow.RunID][]workflow.RunID
	wake    chan struct{}
}

func NewAutomaticIntents() *AutomaticIntents {
	return &AutomaticIntents{
		pending: make(map[workflow.RunID]struct{}),
		queued:  make(map[workflow.RunID]struct{}),
		active:  make(map[workflow.RunID]struct{}),
		held:    make(map[workflow.RunID][]workflow.RunID),
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
	requested := i.registerLocked(registeredRunIDs)
	i.mu.Unlock()
	i.notify(requested)
	return nil
}

func (i *AutomaticIntents) RegisterAutomaticStartRequest(req AutomaticStartRegistrationRequest) error {
	if req.SourceRunID == nil {
		return i.RegisterAutomaticStarts(req.RunIDs)
	}
	if i == nil {
		return errors.New("automatic workflow intents are required")
	}
	runIDs := append([]workflow.RunID(nil), req.RunIDs...)
	for index, runID := range runIDs {
		if runID == "" {
			return fmt.Errorf("automatic workflow start run id at index %d is blank", index)
		}
	}
	i.mu.Lock()
	if _, active := i.active[*req.SourceRunID]; active {
		i.held[*req.SourceRunID] = append(i.held[*req.SourceRunID], runIDs...)
		i.mu.Unlock()
		return nil
	}
	requested := i.registerLocked(runIDs)
	i.mu.Unlock()
	i.notify(requested)
	return nil
}

func (i *AutomaticIntents) SourceStarted(runID workflow.RunID) {
	if i == nil || runID == "" {
		return
	}
	i.mu.Lock()
	i.active[runID] = struct{}{}
	i.mu.Unlock()
}

func (i *AutomaticIntents) SourceFinished(runID workflow.RunID) {
	if i == nil || runID == "" {
		return
	}
	i.mu.Lock()
	delete(i.active, runID)
	successors := append([]workflow.RunID(nil), i.held[runID]...)
	delete(i.held, runID)
	requested := i.registerLocked(successors)
	i.mu.Unlock()
	i.notify(requested)
}

func (i *AutomaticIntents) registerLocked(runIDs []workflow.RunID) bool {
	requested := false
	for _, runID := range runIDs {
		if _, exists := i.pending[runID]; exists {
			continue
		}
		i.pending[runID] = struct{}{}
		i.queued[runID] = struct{}{}
		i.queue = append(i.queue, runID)
		requested = true
	}
	return requested
}

func (i *AutomaticIntents) notify(requested bool) {
	if requested {
		select {
		case i.wake <- struct{}{}:
		default:
		}
	}
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
