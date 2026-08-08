package workflowexecution

import (
	"context"
	"errors"
	"fmt"

	"core/server/sessionruntime"
	"core/server/workflow"
)

func (c *CurrentNodeController) currentNodeOwnedLocked(key workflow.CurrentNodeReferenceKey) bool {
	for _, batch := range c.preparationQueue {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect queued task preparation ownership: %v", err))
			}
			if startKey == key {
				return true
			}
		}
	}
	for _, batch := range c.preparationRunning {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect running task preparation ownership: %v", err))
			}
			if startKey == key {
				return true
			}
		}
	}
	if _, queued := c.explicitQueued[key]; queued {
		return true
	}
	if _, reserved := c.explicitReservations[key]; reserved {
		return true
	}
	if _, queued := c.queued[key]; queued {
		return true
	}
	if _, reserved := c.automaticReservations[key]; reserved {
		return true
	}
	if _, gated := c.gates[key]; gated {
		return true
	}
	_, live := c.liveByNode[key]
	return live
}

func (c *CurrentNodeController) wakeAdmissionWorker() {
	select {
	case c.workerWake <- struct{}{}:
	default:
	}
}

func (c *CurrentNodeController) runAdmissions() {
	defer c.workerWG.Done()
	for {
		select {
		case <-c.workerContext.Done():
			return
		case <-c.workerWake:
		}
		for {
			if batch, ok := c.takeTaskPreparationBatch(); ok {
				go c.runTaskPreparationBatch(batch)
				continue
			}
			start, ok := c.takeExplicitStart()
			if !ok {
				start, ok = c.takeAutomaticIntent()
				if !ok {
					break
				}
			}
			go c.runAdmission(start)
		}
	}
}

func (c *CurrentNodeController) runAdmission(start currentNodeQueuedStart) {
	defer c.admissionWG.Done()
	defer close(start.done)
	defer c.finishTaskInterruptAdmission(start.reference)
	defer c.finishAdmissionWorker(start)
	if err := c.admit(c.workerContext, start); err != nil {
		key, keyErr := start.reference.Key()
		if keyErr != nil {
			panic(fmt.Sprintf("inspect failed current node admission: %v", keyErr))
		}
		c.mu.Lock()
		interrupted := c.interrupts.currentNodeFenced(key)
		c.mu.Unlock()
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) ||
			errors.Is(err, ErrTaskExecutionNotQuiescent) ||
			interrupted {
			return
		}
		var failure currentNodeAdmissionError
		if !errors.As(err, &failure) {
			failure = currentNodeAdmissionError{cause: err}
		}
		c.handleAdmissionFailure(start.reference, failure.admitted, failure.cause)
	}
}
