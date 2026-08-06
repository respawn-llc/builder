package sessiontest

import (
	"context"
	"errors"
	"sync"

	"core/server/session"
)

// PersistenceGate delegates ordinary persistence while allowing tests to block
// or fail one typed persistence observation.
type PersistenceGate struct {
	delegate   session.PersistenceObserver
	reconciler session.EventLogReconciliationObserver

	mu   sync.Mutex
	next *persistenceGateStep
}

type persistenceGateStep struct {
	entered chan struct{}
	release chan struct{}
	err     error
	match   func(session.PersistedStoreSnapshot) bool
}

func NewPersistenceGate(delegate session.PersistenceObserver) *PersistenceGate {
	reconciler, _ := delegate.(session.EventLogReconciliationObserver)
	return &PersistenceGate{delegate: delegate, reconciler: reconciler}
}

func (g *PersistenceGate) FailNext(err error) {
	if err == nil {
		panic("test persistence gate failure requires an error")
	}
	g.arm(&persistenceGateStep{entered: make(chan struct{}), err: err})
}

func (g *PersistenceGate) FailWhen(match func(session.PersistedStoreSnapshot) bool, err error) {
	if match == nil {
		panic("test persistence gate match requires a predicate")
	}
	if err == nil {
		panic("test persistence gate failure requires an error")
	}
	g.arm(&persistenceGateStep{entered: make(chan struct{}), err: err, match: match})
}

func (g *PersistenceGate) BlockNext() (<-chan struct{}, func()) {
	step := &persistenceGateStep{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	g.arm(step)
	var once sync.Once
	return step.entered, func() {
		once.Do(func() { close(step.release) })
	}
}

func (g *PersistenceGate) BlockWhen(match func(session.PersistedStoreSnapshot) bool) (<-chan struct{}, func()) {
	if match == nil {
		panic("test persistence gate match requires a predicate")
	}
	step := &persistenceGateStep{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		match:   match,
	}
	g.arm(step)
	var once sync.Once
	return step.entered, func() {
		once.Do(func() {
			close(step.release)
		})
	}
}

func (g *PersistenceGate) arm(step *persistenceGateStep) {
	if g == nil || step == nil {
		panic("test persistence gate cannot arm a nil step")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next != nil {
		panic("test persistence gate already has an armed step")
	}
	g.next = step
}

func (g *PersistenceGate) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	g.mu.Lock()
	step := g.next
	if step != nil && (step.match == nil || step.match(snapshot)) {
		g.next = nil
	} else {
		step = nil
	}
	g.mu.Unlock()
	if step != nil {
		close(step.entered)
		if step.release != nil {
			select {
			case <-step.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if step.err != nil {
			return step.err
		}
	}
	return g.delegate.ObservePersistedStore(ctx, snapshot)
}

func (g *PersistenceGate) ObserveEventLogReconciliation(ctx context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	if g == nil || g.reconciler == nil {
		return errors.New("test persistence gate delegate does not reconcile event logs")
	}
	return g.reconciler.ObserveEventLogReconciliation(ctx, reconciliation)
}
