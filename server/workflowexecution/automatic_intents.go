package workflowexecution

import (
	"sync"

	"core/server/workflow"
)

// AutomaticIntents is Workflow Execution's volatile admission queue for
// automatic workflow starts. It deliberately has no durable representation.
type AutomaticIntents struct {
	mu      sync.Mutex
	queue   []workflow.RunID
	pending map[workflow.RunID]struct{}
	wake    chan struct{}
}

func NewAutomaticIntents() *AutomaticIntents {
	return &AutomaticIntents{
		pending: make(map[workflow.RunID]struct{}),
		wake:    make(chan struct{}, 1),
	}
}

func (i *AutomaticIntents) RequestAutomaticStarts(runIDs []workflow.RunID) {
	if i == nil {
		return
	}
	i.mu.Lock()
	requested := false
	for _, runID := range runIDs {
		if runID == "" {
			continue
		}
		if _, exists := i.pending[runID]; exists {
			continue
		}
		i.pending[runID] = struct{}{}
		i.queue = append(i.queue, runID)
		requested = true
	}
	i.mu.Unlock()
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
		delete(i.pending, runID)
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
		if _, exists := i.pending[runID]; exists {
			continue
		}
		i.pending[runID] = struct{}{}
		retry = append(retry, runID)
	}
	if len(retry) == 0 {
		return
	}
	i.queue = append(retry, i.queue...)
}
