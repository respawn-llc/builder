package app

import (
	"context"
	"errors"
	"sync"
)

var errRuntimeReactivationUnavailable = errors.New("runtime reactivation is unavailable")

const runtimeReactivationTimeout = uiRuntimeControlTimeout

type runtimeReactivateFunc func(context.Context) error

type runtimeReactivator struct {
	mu           sync.Mutex
	reactivateFn runtimeReactivateFunc
	inflight     *runtimeReactivation
}

type runtimeReactivation struct {
	done chan struct{}
	err  error
}

func newRuntimeReactivator() *runtimeReactivator {
	return &runtimeReactivator{}
}

func (m *runtimeReactivator) SetReactivateFunc(fn runtimeReactivateFunc) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.reactivateFn = fn
	m.mu.Unlock()
}

func (m *runtimeReactivator) Reactivate(ctx context.Context) error {
	if m == nil {
		return errRuntimeReactivationUnavailable
	}
	m.mu.Lock()
	if reactivation := m.inflight; reactivation != nil {
		m.mu.Unlock()
		return waitForRuntimeReactivation(ctx, reactivation)
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if m.reactivateFn == nil {
		m.mu.Unlock()
		return errRuntimeReactivationUnavailable
	}
	reactivation := &runtimeReactivation{done: make(chan struct{})}
	m.inflight = reactivation
	reactivateFn := m.reactivateFn
	m.mu.Unlock()
	go m.runReactivation(reactivation, reactivateFn)
	return waitForRuntimeReactivation(ctx, reactivation)
}

func (m *runtimeReactivator) runReactivation(reactivation *runtimeReactivation, reactivateFn runtimeReactivateFunc) {
	reactivateCtx, cancel := context.WithTimeout(context.Background(), runtimeReactivationTimeout)
	defer cancel()
	err := reactivateFn(reactivateCtx)

	m.mu.Lock()
	reactivation.err = err
	close(reactivation.done)
	m.inflight = nil
	m.mu.Unlock()
}

func waitForRuntimeReactivation(ctx context.Context, reactivation *runtimeReactivation) error {
	if reactivation == nil {
		return errRuntimeReactivationUnavailable
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-reactivation.done:
		return reactivation.err
	}
}
