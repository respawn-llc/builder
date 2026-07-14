package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/shared/runtimeids"
)

type ActiveStepEffect interface {
	Apply(context.Context) error
	Cancel(error)
}

type activeStepEffects struct {
	mu      sync.Mutex
	pending map[string][]ActiveStepEffect
}

func (e *Engine) QueueActiveStepEffect(originRunID runtimeids.RunID, originStepID runtimeids.StepID, effect ActiveStepEffect) error {
	if e == nil || e.stepLifecycle == nil {
		return errors.New("runtime engine is required")
	}
	if effect == nil {
		return errors.New("active-step effect is required")
	}
	snapshot := e.ActiveRun()
	if snapshot == nil || snapshot.RunID != originRunID.String() || snapshot.StepID != originStepID.String() {
		return errors.New("originating model step is no longer active")
	}
	queued, err := e.stepLifecycle.WithActiveStep(func(stepID string) error {
		if strings.TrimSpace(stepID) != originStepID.String() {
			return errors.New("originating model step is no longer active")
		}
		e.activeStepEffects().enqueue(stepID, effect)
		return nil
	})
	if err != nil {
		return err
	}
	if !queued {
		return errors.New("originating model step is no longer active")
	}
	return nil
}

func (e *Engine) drainActiveStepEffects(ctx context.Context, stepID string) error {
	for {
		effect, ok := e.activeStepEffects().shift(stepID)
		if !ok {
			return nil
		}
		if err := effect.Apply(ctx); err != nil {
			return fmt.Errorf("apply active-step effect: %w", err)
		}
	}
}

func (e *Engine) cancelActiveStepEffects(stepID string, cause error) {
	if cause == nil {
		cause = errors.New("model step ended before active-step effect boundary")
	}
	for _, effect := range e.activeStepEffects().drain(stepID) {
		effect.Cancel(cause)
	}
}

func (e *Engine) activeStepEffects() *activeStepEffects {
	if e == nil {
		return &activeStepEffects{}
	}
	e.activeStepEffectsMu.Lock()
	defer e.activeStepEffectsMu.Unlock()
	if e.activeStepEffectQueue == nil {
		e.activeStepEffectQueue = &activeStepEffects{}
	}
	return e.activeStepEffectQueue
}

func (q *activeStepEffects) enqueue(stepID string, effect ActiveStepEffect) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil {
		q.pending = make(map[string][]ActiveStepEffect)
	}
	q.pending[stepID] = append(q.pending[stepID], effect)
}

func (q *activeStepEffects) shift(stepID string) (ActiveStepEffect, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.pending[stepID]
	if len(pending) == 0 {
		return nil, false
	}
	effect := pending[0]
	if len(pending) == 1 {
		delete(q.pending, stepID)
	} else {
		q.pending[stepID] = pending[1:]
	}
	return effect, true
}

func (q *activeStepEffects) drain(stepID string) []ActiveStepEffect {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.pending[stepID]
	delete(q.pending, stepID)
	return pending
}
