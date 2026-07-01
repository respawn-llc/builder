package runtimeops

import "core/shared/clientui"

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
	version := c.nextVersion(sessionID)
	key := ref.Key()
	c.mu.Lock()
	defer c.mu.Unlock()
	ledger := c.ledgerLocked(sessionID)
	ledger.pruneLocked(c.limit, c.ttl, c.now())
	if _, canceled := ledger.tombstones[key]; canceled {
		c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted, version, false, c.now())
		return false
	}
	c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCommitted, version, !ledger.nonEvictableLocked(key), c.now())
	return true
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
	version := c.nextVersion(sessionID)
	key := ref.Key()
	c.mu.Lock()
	defer c.mu.Unlock()
	ledger := c.ledgerLocked(sessionID)
	ledger.pruneLocked(c.limit, c.ttl, c.now())
	if _, canceled := ledger.tombstones[key]; canceled {
		c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted, version, false, c.now())
		return false, nil
	}
	if mutate != nil {
		if err := mutate(); err != nil {
			return false, err
		}
	}
	c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCommitted, version, !ledger.nonEvictableLocked(key), c.now())
	return true, nil
}

func (c *Coordinator) MarkOperationActive(sessionID string, ref clientui.RuntimeOperationRef) {
	if c == nil || ref.Validate() != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ledger := c.sessions[sessionKey(sessionID)]; ledger != nil {
		if entry := ledger.operations[ref.Key()]; entry != nil && !entry.completed {
			entry.active = true
		}
	}
}
