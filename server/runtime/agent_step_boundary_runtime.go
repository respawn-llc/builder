package runtime

import "strings"

func (e *Engine) openAgentStepBoundary(stepID string) *agentStepBoundaryFinalizer {
	if e == nil {
		return nil
	}
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return nil
	}
	e.agentBoundaryMu.Lock()
	defer e.agentBoundaryMu.Unlock()
	if e.agentBoundaries == nil {
		e.agentBoundaries = make(map[string]*agentStepBoundaryFinalizer)
	}
	finalizer := e.agentBoundaries[stepID]
	if finalizer == nil {
		finalizer = newAgentStepBoundaryFinalizer(e)
		e.agentBoundaries[stepID] = finalizer
	}
	finalizer.Open()
	return finalizer
}

func (e *Engine) agentStepBoundary(stepID string) *agentStepBoundaryFinalizer {
	if e == nil {
		return nil
	}
	e.agentBoundaryMu.Lock()
	defer e.agentBoundaryMu.Unlock()
	return e.agentBoundaries[strings.TrimSpace(stepID)]
}

func (e *Engine) closeAgentStepBoundary(stepID string) {
	if e == nil {
		return
	}
	e.agentBoundaryMu.Lock()
	delete(e.agentBoundaries, strings.TrimSpace(stepID))
	e.agentBoundaryMu.Unlock()
}
