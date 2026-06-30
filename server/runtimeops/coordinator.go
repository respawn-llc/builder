package runtimeops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"core/server/requestmemo"
	"core/shared/clientui"
)

const (
	defaultCoordinatorLimit = 1024
	defaultCoordinatorTTL   = 15 * time.Minute
)

var ErrOperationCanceled = errors.New("runtime operation canceled before execution")

type CancellationResult struct {
	InterruptActive bool
	cancel          context.CancelFunc
}

func (r CancellationResult) CancelOperationAttempt() {
	if r.cancel != nil {
		r.cancel()
	}
}

type CoordinatorOption func(*Coordinator)

type VersionAllocator func(sessionID string) clientui.ReadModelVersion

func WithLimit(limit int) CoordinatorOption {
	return func(c *Coordinator) {
		if limit > 0 {
			c.limit = limit
		}
	}
}

func WithTTL(ttl time.Duration) CoordinatorOption {
	return func(c *Coordinator) {
		if ttl > 0 {
			c.ttl = ttl
		}
	}
}

func WithNow(now func() time.Time) CoordinatorOption {
	return func(c *Coordinator) {
		if now != nil {
			c.now = now
		}
	}
}

type Coordinator struct {
	mu             sync.Mutex
	limit          int
	ttl            time.Duration
	now            func() time.Time
	versionMu      sync.Mutex
	epoch          string
	nextGeneration uint64
	sequences      map[string]uint64
	version        VersionAllocator
	sessions       map[string]*sessionLedger
}

type sessionLedger struct {
	records       map[string]clientui.RuntimeInputReconciliation
	terminal      map[string]time.Time
	terminalOrder []string
	evicted       map[string]clientui.RuntimeOperationRef
	evictedAt     map[string]time.Time
	evictedOrder  []string
	operations    map[string]*operationEntry
	tombstones    map[string]clientui.RuntimeOperationRef
	tombstoneAt   map[string]time.Time
	failedReqs    map[string]any
}

type operationEntry struct {
	req         any
	resp        any
	err         error
	done        chan struct{}
	cancel      context.CancelFunc
	completed   bool
	successful  bool
	active      bool
	completedAt time.Time
	createdAt   time.Time
}

func NewCoordinator(options ...CoordinatorOption) *Coordinator {
	coord := &Coordinator{
		limit:          defaultCoordinatorLimit,
		ttl:            defaultCoordinatorTTL,
		now:            time.Now,
		epoch:          "runtimeops-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36),
		nextGeneration: 1,
		sequences:      make(map[string]uint64),
		sessions:       make(map[string]*sessionLedger),
	}
	for _, option := range options {
		if option != nil {
			option(coord)
		}
	}
	return coord
}

func WithVersionAllocator(allocator VersionAllocator) CoordinatorOption {
	return func(c *Coordinator) {
		c.SetVersionAllocator(allocator)
	}
}

func (c *Coordinator) SetVersionAllocator(allocator VersionAllocator) {
	if c == nil || allocator == nil {
		return
	}
	c.versionMu.Lock()
	c.version = allocator
	c.versionMu.Unlock()
}

func (c *Coordinator) nextVersion(sessionID string) clientui.ReadModelVersion {
	if c == nil {
		version, err := clientui.NewReadModelVersion("runtimeops-unknown", 1, 1)
		if err != nil {
			panic(err)
		}
		return version
	}
	c.versionMu.Lock()
	allocator := c.version
	c.versionMu.Unlock()
	if allocator != nil {
		return allocator(sessionID)
	}
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	key := sessionKey(sessionID)
	c.sequences[key]++
	version, err := clientui.NewReadModelVersion(c.epoch, c.nextGeneration, c.sequences[key])
	if err != nil {
		panic(err)
	}
	return version
}

type Attempt struct {
	ctx context.Context
}

func (a Attempt) Context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

func Do[Req any, Resp any](
	coord *Coordinator,
	ctx context.Context,
	sessionID string,
	ref clientui.RuntimeOperationRef,
	req Req,
	same func(Req, Req) bool,
	run func(context.Context, Attempt) (Resp, error),
) (Resp, error) {
	var zero Resp
	if run == nil {
		return zero, nil
	}
	if coord == nil {
		return run(ctx, Attempt{ctx: ctx})
	}
	if err := ref.Validate(); err != nil {
		return zero, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := ref.Key()
	for {
		recordVersion := coord.nextVersion(sessionID)
		coord.mu.Lock()
		ledger := coord.ledgerLocked(sessionID)
		ledger.pruneLocked(coord.limit, coord.ttl, coord.now())
		if _, canceled := ledger.tombstones[key]; canceled {
			coord.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted, recordVersion, false, coord.now())
			coord.mu.Unlock()
			return zero, ErrOperationCanceled
		}
		if failedReq, ok := ledger.failedReqs[key]; ok && !sameOperationRequest(failedReq, req, same) {
			coord.mu.Unlock()
			return zero, fmt.Errorf("client_request_id %q: %w", operationRequestID(ref), requestmemo.ErrClientRequestIDReused)
		}
		if existing := ledger.operations[key]; existing != nil {
			if !sameOperationRequest(existing.req, req, same) {
				coord.mu.Unlock()
				return zero, fmt.Errorf("client_request_id %q: %w", operationRequestID(ref), requestmemo.ErrClientRequestIDReused)
			}
			done := existing.done
			coord.mu.Unlock()
			select {
			case <-done:
				if existing.successful {
					resp, ok := existing.resp.(Resp)
					if !ok {
						return zero, fmt.Errorf("runtime operation response type mismatch for %s", key)
					}
					return resp, nil
				}
				continue
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
		attemptCtx, cancel := context.WithCancel(context.Background())
		entry := &operationEntry{
			req:       req,
			done:      make(chan struct{}),
			cancel:    cancel,
			createdAt: coord.now(),
		}
		ledger.operations[key] = entry
		coord.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationAccepted, recordVersion, false, coord.now())
		coord.mu.Unlock()

		resp, err := run(ctx, Attempt{ctx: attemptCtx})

		coord.mu.Lock()
		_, tombstoned := ledger.tombstones[key]
		if tombstoned && err == nil {
			resp = zero
			err = ErrOperationCanceled
		}
		entry.resp = resp
		entry.err = err
		entry.completed = true
		entry.successful = err == nil
		entry.completedAt = coord.now()
		if err != nil {
			delete(ledger.operations, key)
			if tombstoned {
				delete(ledger.failedReqs, key)
			} else {
				ledger.failedReqs[key] = req
			}
		} else {
			delete(ledger.failedReqs, key)
		}
		ledger.markTerminalEvictableLocked(key, coord.now())
		close(entry.done)
		coord.mu.Unlock()
		return resp, err
	}
}

func (c *Coordinator) CancelOperationTarget(sessionID string, ref clientui.RuntimeOperationRef) (CancellationResult, error) {
	if c == nil {
		return CancellationResult{}, nil
	}
	if err := ref.Validate(); err != nil {
		return CancellationResult{}, err
	}
	key := ref.Key()
	version := c.nextVersion(sessionID)
	var cancel context.CancelFunc
	interruptActive := false
	c.mu.Lock()
	ledger := c.ledgerLocked(sessionID)
	ledger.pruneLocked(c.limit, c.ttl, c.now())
	if record, ok := ledger.records[key]; ok {
		switch record.State {
		case clientui.RuntimeInputReconciliationCommitted, clientui.RuntimeInputReconciliationSubmitted:
			if entry := ledger.operations[key]; entry != nil && !entry.completed && entry.active && operationCancellationInterruptsActive(ref) {
				cancel = entry.cancel
				c.mu.Unlock()
				return CancellationResult{InterruptActive: true, cancel: cancel}, nil
			}
			c.mu.Unlock()
			return CancellationResult{}, nil
		}
	}
	if entry := ledger.operations[key]; entry != nil {
		if entry.successful {
			c.mu.Unlock()
			return CancellationResult{}, nil
		}
		if !entry.completed {
			cancel = entry.cancel
			interruptActive = entry.active && operationCancellationInterruptsActive(ref)
		}
	}
	if _, exists := ledger.tombstones[key]; !exists && len(ledger.tombstones) >= c.limit {
		c.mu.Unlock()
		return CancellationResult{}, fmt.Errorf("runtime operation cancellation tombstone capacity exceeded for session %q", sessionKey(sessionID))
	}
	ledger.tombstones[key] = ref
	ledger.tombstoneAt[key] = c.now()
	c.recordLocked(ledger, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted, version, false, c.now())
	c.mu.Unlock()
	if cancel != nil {
		if interruptActive {
			return CancellationResult{InterruptActive: true, cancel: cancel}, nil
		}
		cancel()
	}
	return CancellationResult{}, nil
}

func (c *Coordinator) RecordCommitted(sessionID string, ref clientui.RuntimeOperationRef) {
	c.record(sessionID, ref, clientui.RuntimeInputReconciliationCommitted)
}

func (c *Coordinator) RecordAccepted(sessionID string, ref clientui.RuntimeOperationRef) {
	c.record(sessionID, ref, clientui.RuntimeInputReconciliationAccepted)
}

func (c *Coordinator) RecordUserMessageFlushed(sessionID string, ref clientui.RuntimeOperationRef) {
	c.recordRuntimeAccepted(sessionID, ref, clientui.RuntimeInputReconciliationCommitted)
}

func (c *Coordinator) RecordSubmitted(sessionID string, ref clientui.RuntimeOperationRef) {
	c.record(sessionID, ref, clientui.RuntimeInputReconciliationSubmitted)
}

func (c *Coordinator) RecordQueuedMessageSubmitted(sessionID string, ref clientui.RuntimeOperationRef) {
	c.recordRuntimeAccepted(sessionID, ref, clientui.RuntimeInputReconciliationSubmitted)
}

func (c *Coordinator) RecordQueuedMessageStatus(sessionID string, ref clientui.RuntimeOperationRef, submitted bool) {
	if submitted {
		c.RecordSubmitted(sessionID, ref)
		return
	}
	c.RecordFailedWithRestore(sessionID, ref)
}

func (c *Coordinator) RecordCanceledNotCommitted(sessionID string, ref clientui.RuntimeOperationRef) {
	c.record(sessionID, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func (c *Coordinator) RecordInterruptCancellation(sessionID string, ref clientui.RuntimeOperationRef) {
	c.RecordCanceledNotCommitted(sessionID, ref)
}

func (c *Coordinator) RecordFailedWithRestore(sessionID string, ref clientui.RuntimeOperationRef) {
	c.record(sessionID, ref, clientui.RuntimeInputReconciliationFailedWithRestore)
}

func (c *Coordinator) RecordQueuedMessageFailed(sessionID string, ref clientui.RuntimeOperationRef) {
	c.RecordFailedWithRestore(sessionID, ref)
}

func (c *Coordinator) RecordShellCompletion(sessionID string, ref clientui.RuntimeOperationRef, err error) {
	if err != nil {
		c.RecordFailedWithRestore(sessionID, ref)
		return
	}
	c.RecordCommitted(sessionID, ref)
}

func (c *Coordinator) RecordCompactCompletion(sessionID string, ref clientui.RuntimeOperationRef, err error) {
	if err != nil {
		c.RecordFailedWithRestore(sessionID, ref)
		return
	}
	c.RecordCommitted(sessionID, ref)
}

func (c *Coordinator) RecordRuntimeAccessFailure(sessionID string, ref clientui.RuntimeOperationRef) {
	c.RecordFailedWithRestore(sessionID, ref)
}

func (c *Coordinator) Snapshot(sessionID string, version clientui.ReadModelVersion, refs []clientui.RuntimeOperationRef) clientui.RuntimeInputReconciliationSnapshot {
	if c == nil || len(refs) == 0 {
		return clientui.NewEmptyRuntimeInputReconciliationSnapshot(version)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ledger := c.sessions[sessionKey(sessionID)]
	operations := make([]clientui.RuntimeInputReconciliation, 0, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			continue
		}
		record := clientui.RuntimeInputReconciliation{
			Version:      version,
			OperationRef: ref,
			State:        clientui.RuntimeInputReconciliationUnknown,
		}
		key := ref.Key()
		if ledger != nil {
			if existing, ok := ledger.records[key]; ok {
				record.State = existing.State
			} else if _, ok := ledger.evicted[key]; ok {
				record.State = clientui.RuntimeInputReconciliationEvicted
			}
		}
		operations = append(operations, record)
	}
	return clientui.RuntimeInputReconciliationSnapshot{Version: version, Operations: operations}
}

func (c *Coordinator) record(sessionID string, ref clientui.RuntimeOperationRef, state clientui.RuntimeInputReconciliationState) {
	if c == nil {
		return
	}
	if err := ref.Validate(); err != nil {
		return
	}
	if err := state.Validate(); err != nil {
		return
	}
	version := c.nextVersion(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	ledger := c.ledgerLocked(sessionID)
	key := ref.Key()
	c.recordLocked(ledger, ref, state, version, !ledger.nonEvictableLocked(key), c.now())
	ledger.pruneLocked(c.limit, c.ttl, c.now())
}

func (c *Coordinator) recordRuntimeAccepted(sessionID string, ref clientui.RuntimeOperationRef, state clientui.RuntimeInputReconciliationState) {
	if c == nil || ref.Validate() != nil || state.Validate() != nil {
		return
	}
	version := c.nextVersion(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	ledger := c.ledgerLocked(sessionID)
	key := ref.Key()
	delete(ledger.tombstones, key)
	delete(ledger.tombstoneAt, key)
	c.recordLocked(ledger, ref, state, version, !ledger.nonEvictableLocked(key), c.now())
	ledger.pruneLocked(c.limit, c.ttl, c.now())
}

func (c *Coordinator) recordLocked(ledger *sessionLedger, ref clientui.RuntimeOperationRef, state clientui.RuntimeInputReconciliationState, version clientui.ReadModelVersion, terminalEvictable bool, now time.Time) {
	key := ref.Key()
	if _, tombstoned := ledger.tombstones[key]; tombstoned && state != clientui.RuntimeInputReconciliationCanceledNotCommitted {
		return
	}
	ledger.records[key] = clientui.RuntimeInputReconciliation{
		Version:      version,
		OperationRef: ref,
		State:        state,
	}
	if terminalEvictable && state != clientui.RuntimeInputReconciliationAccepted {
		if _, exists := ledger.terminal[key]; !exists {
			ledger.terminalOrder = append(ledger.terminalOrder, key)
		}
		ledger.terminal[key] = now
	}
}

func (c *Coordinator) ledgerLocked(sessionID string) *sessionLedger {
	key := sessionKey(sessionID)
	if ledger := c.sessions[key]; ledger != nil {
		return ledger
	}
	ledger := &sessionLedger{
		records:     make(map[string]clientui.RuntimeInputReconciliation),
		terminal:    make(map[string]time.Time),
		evicted:     make(map[string]clientui.RuntimeOperationRef),
		evictedAt:   make(map[string]time.Time),
		operations:  make(map[string]*operationEntry),
		tombstones:  make(map[string]clientui.RuntimeOperationRef),
		tombstoneAt: make(map[string]time.Time),
		failedReqs:  make(map[string]any),
	}
	c.sessions[key] = ledger
	return ledger
}

func (l *sessionLedger) pruneLocked(limit int, ttl time.Duration, now time.Time) {
	if limit <= 0 {
		limit = defaultCoordinatorLimit
	}
	if ttl <= 0 {
		ttl = defaultCoordinatorTTL
	}
	retained := l.terminalOrder[:0]
	for _, key := range l.terminalOrder {
		completedAt, ok := l.terminal[key]
		if !ok {
			continue
		}
		if !completedAt.IsZero() && now.Sub(completedAt) >= ttl {
			record := l.records[key]
			delete(l.records, key)
			delete(l.terminal, key)
			if entry := l.operations[key]; entry != nil && entry.successful && entry.completed {
				delete(l.operations, key)
			}
			delete(l.failedReqs, key)
			l.recordEvictedLocked(key, record.OperationRef, now)
			continue
		}
		retained = append(retained, key)
	}
	l.terminalOrder = retained
	for len(l.terminalOrder) > limit {
		key := l.terminalOrder[0]
		l.terminalOrder = l.terminalOrder[1:]
		record := l.records[key]
		delete(l.records, key)
		delete(l.terminal, key)
		if entry := l.operations[key]; entry != nil && entry.successful && entry.completed {
			delete(l.operations, key)
		}
		delete(l.failedReqs, key)
		l.recordEvictedLocked(key, record.OperationRef, now)
	}
	for key, createdAt := range l.tombstoneAt {
		if !createdAt.IsZero() && now.Sub(createdAt) >= ttl {
			delete(l.tombstoneAt, key)
			delete(l.tombstones, key)
			delete(l.operations, key)
			delete(l.failedReqs, key)
			if record, ok := l.records[key]; ok {
				if _, terminal := l.terminal[key]; !terminal {
					delete(l.records, key)
					l.recordEvictedLocked(key, record.OperationRef, now)
				}
			}
		}
	}
	l.pruneEvictedLocked(limit, ttl, now)
}

func (l *sessionLedger) nonEvictableLocked(key string) bool {
	if _, ok := l.tombstones[key]; ok {
		return true
	}
	if entry := l.operations[key]; entry != nil && !entry.completed {
		return true
	}
	return false
}

func (l *sessionLedger) markTerminalEvictableLocked(key string, now time.Time) {
	if _, tombstone := l.tombstones[key]; tombstone {
		return
	}
	record, ok := l.records[key]
	if !ok || record.State == clientui.RuntimeInputReconciliationAccepted {
		return
	}
	if _, exists := l.terminal[key]; !exists {
		l.terminalOrder = append(l.terminalOrder, key)
	}
	l.terminal[key] = now
}

func (l *sessionLedger) recordEvictedLocked(key string, ref clientui.RuntimeOperationRef, now time.Time) {
	if _, exists := l.evicted[key]; !exists {
		l.evictedOrder = append(l.evictedOrder, key)
	}
	l.evicted[key] = ref
	l.evictedAt[key] = now
}

func (l *sessionLedger) pruneEvictedLocked(limit int, ttl time.Duration, now time.Time) {
	retained := l.evictedOrder[:0]
	for _, key := range l.evictedOrder {
		evictedAt, ok := l.evictedAt[key]
		if !ok {
			continue
		}
		if !evictedAt.IsZero() && now.Sub(evictedAt) >= ttl {
			delete(l.evictedAt, key)
			delete(l.evicted, key)
			continue
		}
		retained = append(retained, key)
	}
	l.evictedOrder = retained
	for len(l.evictedOrder) > limit {
		key := l.evictedOrder[0]
		l.evictedOrder = l.evictedOrder[1:]
		delete(l.evictedAt, key)
		delete(l.evicted, key)
	}
}
