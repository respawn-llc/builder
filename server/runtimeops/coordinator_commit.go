package runtimeops

import (
	"fmt"

	"core/shared/clientui"
)

func (c *Coordinator) TryRecordOperationMutation(
	sessionID string,
	ref clientui.RuntimeOperationRef,
	state clientui.RuntimeInputReconciliationState,
	mutate func() (bool, error),
) (bool, error) {
	if c == nil {
		if mutate == nil {
			return false, nil
		}
		return mutate()
	}
	if err := ref.Validate(); err != nil {
		return false, err
	}
	switch state {
	case clientui.RuntimeInputReconciliationCommitted, clientui.RuntimeInputReconciliationSubmitted:
	default:
		return false, fmt.Errorf("runtime operation mutation cannot record reconciliation state %q", state)
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

	committed, mutationErr, panicValue := invokeOperationMutation(mutate)

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
	if committed {
		c.recordLocked(ledger, ref, state, !ledger.nonEvictableLocked(key), c.now())
	}
	close(barrier.done)
	c.mu.Unlock()
	if panicValue != nil {
		panic(panicValue)
	}
	return committed, mutationErr
}

func invokeOperationMutation(mutate func() (bool, error)) (committed bool, err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	if mutate != nil {
		committed, err = mutate()
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
