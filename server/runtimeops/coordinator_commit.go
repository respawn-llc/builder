package runtimeops

import (
	"fmt"

	"core/shared/clientui"
)

func (c *Coordinator) CancelOperation(sessionID string, ref clientui.RuntimeOperationRef) error {
	result, err := c.CancelOperationTarget(sessionID, ref)
	result.CancelOperationAttempt()
	return err
}

func (c *Coordinator) TryCommitOperation(sessionID string, ref clientui.RuntimeOperationRef) bool {
	if c == nil {
		return true
	}
	if err := ref.Validate(); err != nil {
		return false
	}
	for {
		c.mu.Lock()
		ledger := c.ledgerLocked(sessionID)
		key := ledger.operationKey(ref)
		ledger.pruneLocked(c.limit, c.ttl, c.now())
		if barrier := ledger.commitBarriers[key]; barrier != nil {
			done := barrier.done
			c.mu.Unlock()
			<-done
			continue
		}
		if _, canceled := ledger.tombstones[key]; canceled {
			c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted, false, c.now())
			c.mu.Unlock()
			return false
		}
		c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCommitted, !ledger.nonEvictableLocked(key), c.now())
		c.mu.Unlock()
		return true
	}
}

func (c *Coordinator) TryCommitOperationMutation(sessionID string, ref clientui.RuntimeOperationRef, mutate func() error) (bool, error) {
	if c == nil {
		if mutate == nil {
			return true, nil
		}
		return true, mutate()
	}
	if err := ref.Validate(); err != nil {
		return false, err
	}
	var (
		ledger  *sessionLedger
		key     string
		barrier *operationCommitBarrier
	)
	for {
		c.mu.Lock()
		ledger = c.ledgerLocked(sessionID)
		key = ledger.operationKey(ref)
		ledger.pruneLocked(c.limit, c.ttl, c.now())
		if existing := ledger.commitBarriers[key]; existing != nil {
			done := existing.done
			c.mu.Unlock()
			<-done
			continue
		}
		if _, canceled := ledger.tombstones[key]; canceled {
			c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted, false, c.now())
			c.mu.Unlock()
			return false, nil
		}
		barrier = &operationCommitBarrier{done: make(chan struct{})}
		ledger.commitBarriers[key] = barrier
		c.mu.Unlock()
		break
	}

	mutationErr, panicValue := invokeOperationMutation(mutate)

	c.mu.Lock()
	if ledger.commitBarriers[key] != barrier {
		c.mu.Unlock()
		panic(fmt.Sprintf(
			"runtime operation commit barrier ownership changed during mutation: session=%q operation=%q",
			sessionKey(sessionID),
			key,
		))
	}
	delete(ledger.commitBarriers, key)
	if mutationErr == nil {
		c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCommitted, !ledger.nonEvictableLocked(key), c.now())
	}
	close(barrier.done)
	c.mu.Unlock()
	if panicValue != nil {
		panic(panicValue)
	}
	if mutationErr != nil {
		return false, mutationErr
	}
	return true, nil
}

func invokeOperationMutation(mutate func() error) (err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	if mutate != nil {
		err = mutate()
	}
	return
}

func (c *Coordinator) MarkOperationActive(sessionID string, ref clientui.RuntimeOperationRef) {
	if c == nil || ref.Validate() != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ledger := c.sessions[sessionKey(sessionID)]; ledger != nil {
		if entry := ledger.operations[ledger.operationKey(ref)]; entry != nil && !entry.completed {
			entry.active = true
		}
	}
}

func (c *Coordinator) MarkOperationAttemptOnly(sessionID string, ref clientui.RuntimeOperationRef) {
	if c == nil || ref.Validate() != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ledger := c.sessions[sessionKey(sessionID)]; ledger != nil {
		if entry := ledger.operations[ledger.operationKey(ref)]; entry != nil && !entry.completed {
			entry.active = true
			entry.interruptActive = false
		}
	}
}
