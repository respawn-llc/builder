package workflowrunner

import (
	"sync"

	"core/server/workflow"
)

// AutomaticIntents is the volatile admission queue for automatic workflow
// starts. It deliberately has no durable representation: a server restart
// loses every pending request and recovery turns any already-started run into
// an interruption instead.
type AutomaticIntents struct {
	mu      sync.Mutex
	queue   []workflow.RunID
	pending map[workflow.RunID]struct{}
}

func NewAutomaticIntents() *AutomaticIntents {
	return &AutomaticIntents{pending: make(map[workflow.RunID]struct{})}
}

func (i *AutomaticIntents) RequestAutomaticStarts(runIDs []workflow.RunID) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, runID := range runIDs {
		if runID == "" {
			continue
		}
		if _, exists := i.pending[runID]; exists {
			continue
		}
		i.pending[runID] = struct{}{}
		i.queue = append(i.queue, runID)
	}
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
