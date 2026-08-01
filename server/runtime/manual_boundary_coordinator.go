package runtime

import (
	"context"
	"errors"
	"sync"

	"core/server/session"
)

var errManualBoundaryNoGeneration = errors.New("agent step generation ended before provider dispatch")

type manualBoundaryGenerationPhase uint8

const (
	manualBoundaryGenerationOpen manualBoundaryGenerationPhase = iota + 1
	manualBoundaryGenerationDraining
)

type manualBoundaryGeneration struct {
	id      uint64
	phase   manualBoundaryGenerationPhase
	pending []*pendingManualCompaction
}

type manualBoundaryCoordinator struct {
	mu         sync.Mutex
	current    *manualBoundaryGeneration
	nextID     uint64
	armed      bool
	armedErr   error
	turnActive bool
	changed    chan struct{}
}

type pendingManualCompactionState uint8

const (
	pendingManualCompactionQueued pendingManualCompactionState = iota
	pendingManualCompactionExecuting
	pendingManualCompactionCanceled
	pendingManualCompactionCompleted
)

type pendingManualCompaction struct {
	mu              sync.Mutex
	state           pendingManualCompactionState
	generationID    uint64
	acceptanceOrder *uint64
	ctx             context.Context
	instructions    compactionInstructionsInput
	onActive        func()
	done            chan manualCompactionResult
}

type manualCompactionResult struct {
	receipt session.CommitReceipt
	err     error
}

func newManualBoundaryCoordinator() *manualBoundaryCoordinator {
	return &manualBoundaryCoordinator{changed: make(chan struct{})}
}

func (c *manualBoundaryCoordinator) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *manualBoundaryCoordinator) beginGeneration() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	c.armed = false
	c.armedErr = nil
	c.current = &manualBoundaryGeneration{
		id:    c.nextID,
		phase: manualBoundaryGenerationOpen,
	}
	c.turnActive = true
	c.signalLocked()
	return c.nextID
}

func (c *manualBoundaryCoordinator) armNextGeneration() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.armed = true
	c.armedErr = nil
	c.turnActive = true
	c.signalLocked()
	c.mu.Unlock()
}

func (c *manualBoundaryCoordinator) abortArmedGeneration(err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.armed || c.current != nil {
		c.mu.Unlock()
		return
	}
	c.armed = false
	if err == nil {
		err = errManualBoundaryNoGeneration
	}
	c.armedErr = err
	c.turnActive = false
	c.signalLocked()
	c.mu.Unlock()
}

func (c *manualBoundaryCoordinator) enqueueForGeneration(
	ctx context.Context,
	instructions compactionInstructionsInput,
	onActive func(),
) (*pendingManualCompaction, error) {
	return c.enqueueForGenerationOrdered(ctx, instructions, onActive, nil)
}

func (c *manualBoundaryCoordinator) enqueueForGenerationOrdered(
	ctx context.Context,
	instructions compactionInstructionsInput,
	onActive func(),
	acceptanceOrder *uint64,
) (*pendingManualCompaction, error) {
	if c == nil {
		return nil, errors.New("manual boundary coordinator is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entry := &pendingManualCompaction{
		state:           pendingManualCompactionQueued,
		acceptanceOrder: cloneManualCompactionAcceptanceOrder(acceptanceOrder),
		ctx:             ctx,
		instructions:    instructions,
		onActive:        onActive,
		done:            make(chan manualCompactionResult, 1),
	}
	for {
		c.mu.Lock()
		if c.current != nil && c.current.phase == manualBoundaryGenerationOpen {
			entry.generationID = c.current.id
			pending := c.current.pending
			insertAt := len(pending)
			if entry.acceptanceOrder != nil {
				for index, candidate := range pending {
					if candidate.acceptanceOrder == nil || *entry.acceptanceOrder < *candidate.acceptanceOrder {
						insertAt = index
						break
					}
				}
			}
			pending = append(pending, nil)
			copy(pending[insertAt+1:], pending[insertAt:])
			pending[insertAt] = entry
			c.current.pending = pending
			c.mu.Unlock()
			return entry, nil
		}
		if !c.turnActive {
			if c.armedErr != nil {
				err := c.armedErr
				c.mu.Unlock()
				return nil, err
			}
			c.mu.Unlock()
			return nil, errManualBoundaryNoGeneration
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func cloneManualCompactionAcceptanceOrder(order *uint64) *uint64 {
	if order == nil {
		return nil
	}
	copyOrder := *order
	return &copyOrder
}

func (c *manualBoundaryCoordinator) sealAndTake() []*pendingManualCompaction {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.phase != manualBoundaryGenerationOpen {
		return nil
	}
	c.current.phase = manualBoundaryGenerationDraining
	entries := append([]*pendingManualCompaction(nil), c.current.pending...)
	c.current.pending = nil
	c.signalLocked()
	return entries
}

func (c *manualBoundaryCoordinator) finishGeneration() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.current != nil && c.current.phase == manualBoundaryGenerationDraining {
		c.current = nil
		c.signalLocked()
	}
	c.mu.Unlock()
}

func (c *manualBoundaryCoordinator) endTurn() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.turnActive = false
	c.armed = false
	c.armedErr = nil
	c.current = nil
	c.signalLocked()
	c.mu.Unlock()
}

func (c *manualBoundaryCoordinator) beginExecution(entry *pendingManualCompaction) bool {
	if c == nil || entry == nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.state != pendingManualCompactionQueued {
		return false
	}
	entry.state = pendingManualCompactionExecuting
	return true
}

func (c *manualBoundaryCoordinator) cancel(entry *pendingManualCompaction) bool {
	if c == nil || entry == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return false
	}
	for index, candidate := range c.current.pending {
		if candidate != entry {
			continue
		}
		entry.mu.Lock()
		if entry.state != pendingManualCompactionQueued {
			entry.mu.Unlock()
			return false
		}
		entry.state = pendingManualCompactionCanceled
		entry.mu.Unlock()
		c.current.pending = append(c.current.pending[:index], c.current.pending[index+1:]...)
		c.signalLocked()
		entry.complete(manualCompactionResult{err: entry.ctx.Err()})
		return true
	}
	return false
}

func (entry *pendingManualCompaction) complete(result manualCompactionResult) {
	if entry == nil {
		return
	}
	entry.mu.Lock()
	if entry.state == pendingManualCompactionCompleted {
		entry.mu.Unlock()
		return
	}
	entry.state = pendingManualCompactionCompleted
	entry.mu.Unlock()
	entry.done <- result
}

func completeManualBoundaryEntries(entries []*pendingManualCompaction, err error) {
	if err == nil {
		err = errManualBoundaryNoGeneration
	}
	for _, entry := range entries {
		entry.complete(manualCompactionResult{err: err})
	}
}

func (c *manualBoundaryCoordinator) rejectDetached(entries []*pendingManualCompaction, err error) {
	completeManualBoundaryEntries(entries, err)
	c.finishGeneration()
}
