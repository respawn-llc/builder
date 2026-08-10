package runtimeops

import (
	"context"
	"errors"
	"fmt"

	"core/shared/clientui"
)

type CancellationTarget uint8

const (
	CancellationTargetAbsentOrTerminal CancellationTarget = iota
	CancellationTargetQueuedMessage
	CancellationTargetNonActive
	CancellationTargetActiveInterruptible
)

type CancellationPreparation struct {
	coordinator *Coordinator
	sessionID   string
	ledger      *sessionLedger
	key         string
	ref         clientui.RuntimeOperationRef
	target      CancellationTarget
	cancel      context.CancelFunc
	fence       *operationCancellationFence
	resolved    bool
}

func (p *CancellationPreparation) Target() CancellationTarget {
	if p == nil {
		return CancellationTargetAbsentOrTerminal
	}
	return p.target
}

func (p *CancellationPreparation) Release() error {
	_, err := p.resolve(false)
	return err
}

func (p *CancellationPreparation) Commit() (CancellationResult, error) {
	return p.resolve(true)
}

func (p *CancellationPreparation) resolve(commit bool) (CancellationResult, error) {
	if p == nil || p.coordinator == nil {
		return CancellationResult{}, nil
	}
	c := p.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.resolved {
		return CancellationResult{}, errors.New("runtime operation cancellation preparation was already resolved")
	}
	p.resolved = true
	if p.fence != nil {
		if p.ledger.cancellationFences[p.key] != p.fence {
			panic(fmt.Sprintf(
				"runtime operation cancellation fence ownership changed: session=%q operation=%q",
				sessionKey(p.sessionID),
				p.key,
			))
		}
		delete(p.ledger.cancellationFences, p.key)
		defer close(p.fence.done)
	}
	if !commit {
		return CancellationResult{}, nil
	}
	switch p.target {
	case CancellationTargetAbsentOrTerminal:
		return CancellationResult{}, nil
	case CancellationTargetActiveInterruptible:
		entry := p.ledger.operations[p.key]
		if entry == nil || entry.completed || !entry.active {
			return CancellationResult{}, nil
		}
		var reconciled <-chan struct{}
		record, recorded := p.ledger.records[p.key]
		if !recorded ||
			(record.State != clientui.RuntimeInputReconciliationCommitted &&
				record.State != clientui.RuntimeInputReconciliationSubmitted) {
			reconciled = entry.done
		}
		return CancellationResult{
			InterruptActive: true,
			cancel:          p.cancel,
			reconciled:      reconciled,
		}, nil
	case CancellationTargetQueuedMessage, CancellationTargetNonActive:
		if _, exists := p.ledger.tombstones[p.key]; !exists && len(p.ledger.tombstones) >= c.limit {
			return CancellationResult{}, fmt.Errorf(
				"runtime operation cancellation tombstone capacity exceeded for session %q",
				sessionKey(p.sessionID),
			)
		}
		p.ledger.tombstones[p.key] = p.ref
		p.ledger.tombstoneAt[p.key] = c.now()
		c.recordLocked(
			p.ledger,
			p.ref,
			clientui.RuntimeInputReconciliationCanceledNotCommitted,
			false,
			c.now(),
		)
		return CancellationResult{cancel: p.cancel}, nil
	default:
		panic(fmt.Sprintf("unknown runtime operation cancellation target %d", p.target))
	}
}

func (c *Coordinator) PrepareOperationCancellation(
	ctx context.Context,
	sessionID string,
	ref clientui.RuntimeOperationRef,
) (*CancellationPreparation, error) {
	if c == nil {
		return &CancellationPreparation{}, nil
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		ledger := c.ledgerLocked(sessionID)
		ledger.pruneLocked(c.limit, c.ttl, c.now())
		key := ledger.operationKey(ref)
		if barrier := ledger.commitBarriers[key]; barrier != nil {
			done := barrier.done
			c.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			continue
		}
		if fence := ledger.cancellationFences[key]; fence != nil {
			done := fence.done
			c.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			continue
		}
		if err := context.Cause(ctx); err != nil {
			c.mu.Unlock()
			return nil, err
		}

		target, cancel := classifyCancellationTarget(ledger, key, ref)
		if target == CancellationTargetAbsentOrTerminal {
			c.mu.Unlock()
			return &CancellationPreparation{
				coordinator: c,
				sessionID:   sessionID,
				ledger:      ledger,
				key:         key,
				ref:         ref,
				target:      target,
			}, nil
		}
		if err := context.Cause(ctx); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		fence := &operationCancellationFence{done: make(chan struct{})}
		ledger.cancellationFences[key] = fence
		c.mu.Unlock()
		return &CancellationPreparation{
			coordinator: c,
			sessionID:   sessionID,
			ledger:      ledger,
			key:         key,
			ref:         ref,
			target:      target,
			cancel:      cancel,
			fence:       fence,
		}, nil
	}
}

func classifyCancellationTarget(
	ledger *sessionLedger,
	key string,
	ref clientui.RuntimeOperationRef,
) (CancellationTarget, context.CancelFunc) {
	entry := ledger.operations[key]
	if ref.Kind == clientui.RuntimeOperationKindQueuedMessage {
		if record, ok := ledger.records[key]; ok {
			switch record.State {
			case clientui.RuntimeInputReconciliationAccepted,
				clientui.RuntimeInputReconciliationSubmitted:
				return CancellationTargetQueuedMessage, nil
			default:
				return CancellationTargetAbsentOrTerminal, nil
			}
		}
		if entry != nil && !entry.completed {
			return CancellationTargetQueuedMessage, nil
		}
		return CancellationTargetAbsentOrTerminal, nil
	}
	if record, ok := ledger.records[key]; ok {
		switch record.State {
		case clientui.RuntimeInputReconciliationCommitted, clientui.RuntimeInputReconciliationSubmitted:
			if entry != nil &&
				!entry.completed &&
				entry.active &&
				operationCancellationInterruptsActive(ref) {
				return CancellationTargetActiveInterruptible, entry.cancel
			}
			return CancellationTargetAbsentOrTerminal, nil
		}
	}
	if entry != nil {
		if entry.successful || entry.committed {
			return CancellationTargetAbsentOrTerminal, nil
		}
		if !entry.completed {
			if entry.active && operationCancellationInterruptsActive(ref) {
				return CancellationTargetActiveInterruptible, entry.cancel
			}
			return CancellationTargetNonActive, entry.cancel
		}
	}
	if record, exists := ledger.records[key]; exists &&
		record.State == clientui.RuntimeInputReconciliationAccepted {
		return CancellationTargetNonActive, nil
	}
	return CancellationTargetAbsentOrTerminal, nil
}
