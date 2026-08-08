package workflowexecution

import (
	"context"
	"errors"
	"fmt"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func (c *CurrentNodeController) Close() error {
	if c == nil {
		return nil
	}
	var (
		startedShutdown     bool
		queuedPreparations  []*taskPreparationBatch
		runningPreparations []*taskPreparationBatch
		gates               []currentNodeAdmissionGate
		liveScopes          []runtimeids.ExecutionScopeID
	)
	if err := c.permit.Run(context.Background(), func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return nil
		}
		startedShutdown = true
		c.closed = true
		queuedPreparations = append([]*taskPreparationBatch(nil), c.preparationQueue...)
		c.preparationQueue = nil
		runningPreparations = append([]*taskPreparationBatch(nil), c.preparationRunning...)
		c.explicitQueue = nil
		c.explicitQueued = make(map[workflow.CurrentNodeReferenceKey]struct{})
		c.automaticQueue.clear()
		c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
		c.heldStarts = make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart)
		c.postTurnFinalization = make(map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization)
		gates = make([]currentNodeAdmissionGate, 0, len(c.gates))
		for _, gate := range c.gates {
			gates = append(gates, gate)
		}
		liveScopes = make([]runtimeids.ExecutionScopeID, 0, len(c.live))
		for scopeID := range c.live {
			liveScopes = append(liveScopes, scopeID)
		}
		for _, batch := range queuedPreparations {
			closeQueuedTaskPreparationBatch(batch, preparationShutdownCause())
		}
		for _, batch := range runningPreparations {
			batch.cancel(preparationShutdownCause())
		}
		return nil
	}); err != nil {
		return err
	}
	if !startedShutdown {
		return nil
	}

	c.workerCancel()
	for _, gate := range gates {
		gate.lease.Cancel()
	}
	handles := make([]sessionruntime.ExecutionHandle, 0, len(liveScopes))
	for _, scopeID := range liveScopes {
		handle, exists := c.authority.ExecutionByScope(scopeID)
		if exists {
			handles = append(handles, handle)
		}
	}
	for _, handle := range handles {
		handle.RequestStop()
	}
	var stopErrs []error
	for _, handle := range handles {
		scopeID := handle.Scope().ID()
		if err := handle.Stop(context.Background()); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop workflow execution scope %s: %w", scopeID, err))
		}
	}
	c.workerWG.Wait()
	c.preparationWG.Wait()
	c.admissionWG.Wait()
	c.mu.Lock()
	workerErr := c.workerErr
	c.mu.Unlock()
	return errors.Join(errors.Join(stopErrs...), workerErr)
}
