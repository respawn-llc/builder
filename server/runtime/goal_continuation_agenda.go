package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

type goalContinuationAgendaItem struct {
	id          boundaryAgendaItemID
	appendNudge bool
	order       uint64
	settled     sync.Once
	settlement  error
	didSettle   atomic.Bool
}

type goalContinuationSelection struct {
	id          boundaryAgendaItemID
	appendNudge bool
	item        *goalContinuationAgendaItem
	detached    atomic.Bool
}

type goalContinuationRuntimeResult struct {
	selection *goalContinuationSelection
	err       error
}

func newGoalContinuationAgendaItem(
	appendNudge bool,
) *goalContinuationAgendaItem {
	return &goalContinuationAgendaItem{
		id:          boundaryAgendaItemID("goal-continuation:" + uuid.NewString()),
		appendNudge: appendNudge,
	}
}

func (i *goalContinuationAgendaItem) agendaID() boundaryAgendaItemID {
	return i.id
}

func (*goalContinuationAgendaItem) agendaBinding() boundaryAgendaBinding {
	return runtimeBoundaryBinding()
}

func (*goalContinuationAgendaItem) agendaEligibility() boundaryEligibility {
	return boundaryEligibilitySafe
}

func (i *goalContinuationAgendaItem) agendaOrder() uint64 {
	return i.order
}

func (i *goalContinuationAgendaItem) setAgendaOrder(order uint64) {
	i.order = order
}

func (i *goalContinuationAgendaItem) settleBoundaryAgenda(err error) {
	i.settled.Do(func() {
		i.settlement = err
		i.didSettle.Store(true)
	})
}

func (i *goalContinuationAgendaItem) selectLongWork() boundaryLongWork {
	return &goalContinuationSelection{
		id:          i.id,
		appendNudge: i.appendNudge,
		item:        i,
	}
}

func (s *goalContinuationSelection) longWorkID() boundaryAgendaItemID {
	return s.id
}

func (s *goalContinuationSelection) beginLongWork(engine *Engine) error {
	if engine == nil || !engine.goalActive() || engine.currentNodeExecutionActive() {
		return errGoalLoopInactive
	}
	state := engine.goalLoopState()
	if !state.Running() && !state.Start() {
		return errors.New("Goal continuation could not enter running state")
	}
	return nil
}

func (s *goalContinuationSelection) runLongWork(
	ctx context.Context,
	engine *Engine,
) error {
	_, err := engine.runGoalTurn(ctx, s.appendNudge)
	return err
}

func (s *goalContinuationSelection) settleLongWork(err error) {
	s.item.settleBoundaryAgenda(err)
}

func (*goalContinuationSelection) failLongWorkTransfer(engine *Engine) {
	engine.goalLoopState().Finish(false)
	engine.finishLiveRunGoalLoop()
}

func (s *goalContinuationSelection) completeRuntimeBoundLongWork(
	engine *Engine,
	err error,
) {
	engine.submitGoalContinuationRuntimeResult(goalContinuationRuntimeResult{
		selection: s,
		err:       err,
	})
}

func (e *Engine) acceptGoalContinuation(
	admission runtimeEventAdmission,
	appendNudge bool,
) error {
	if !e.goalActive() || e.currentNodeExecutionActive() {
		return nil
	}
	if _, selected := e.longBoundary.selected.(*goalContinuationSelection); selected {
		e.goalLoopState().Start()
		return nil
	}
	if e.hasPendingGoalContinuation() {
		return nil
	}
	item := newGoalContinuationAgendaItem(appendNudge)
	if err := e.boundaryAgenda.accept(item); err != nil {
		return err
	}
	if e.stepLifecycle != nil && e.stepLifecycle.Snapshot() != nil {
		return nil
	}
	if scopes, ok := e.cfg.StepLifecycle.(AgentStepScopeLifecycle); ok {
		if _, active := scopes.CurrentAgentExecutionScope(admission.Context()); active {
			return nil
		}
	}
	return e.reduceIdleBoundary(admission)
}

func (e *Engine) hasPendingGoalContinuation() bool {
	for _, item := range e.boundaryAgenda.pending() {
		if _, ok := item.(*goalContinuationAgendaItem); ok {
			return true
		}
	}
	return false
}

func (e *Engine) discardPendingGoalContinuations() {
	for _, item := range e.boundaryAgenda.pending() {
		if _, ok := item.(*goalContinuationAgendaItem); !ok {
			continue
		}
		e.boundaryAgenda.discard(item.agendaID(), nil)
	}
}

func (e *Engine) startNextGoalContinuationLongWork(
	admission runtimeEventAdmission,
) error {
	if !e.idleBoundaryReductionEligible() || e.runtimeEvents == nil {
		return nil
	}
	next, ok := e.boundaryAgenda.peekNext(idleBoundarySelection()).(*goalContinuationAgendaItem)
	if !ok {
		return nil
	}
	return e.startNextRuntimeBoundLongWork(admission, next.id)
}

func (e *Engine) submitGoalContinuationRuntimeResult(
	result goalContinuationRuntimeResult,
) {
	_, resultErr := submitRuntimeEventWithContext(
		e.lifecycleCtx,
		e.lifecycleCtx,
		e,
		result,
		e.applyGoalContinuationRuntimeResult,
	)
	visibleErr := errors.Join(result.err, resultErr)
	if visibleErr != nil {
		e.surfaceRunError(visibleErr)
	}
}

func (e *Engine) applyGoalContinuationRuntimeResult(
	admission runtimeEventAdmission,
	result goalContinuationRuntimeResult,
) (struct{}, error) {
	selected := result.selection
	if selected == nil {
		return struct{}{}, errors.New("Goal continuation result has no selection")
	}
	if !selected.detached.Load() {
		active, ok := e.longBoundary.selected.(*goalContinuationSelection)
		if !ok || active != selected {
			return struct{}{}, fmt.Errorf(
				"Goal continuation result %q has no matching selected work",
				selected.id,
			)
		}
	}
	if selected.id == "" {
		return struct{}{}, fmt.Errorf(
			"Goal continuation result has invalid selected work %q",
			selected.id,
		)
	}
	if result.err != nil && e.agentSteps.current != nil {
		e.invalidateAgentStepScope(
			e.agentSteps.current.scopeID,
			errBoundaryScopeStopped,
		)
	}
	if selected.detached.Load() {
		selected.settleLongWork(result.err)
	} else {
		if _, err := e.longBoundary.settle(boundaryLongWorkResult{
			id:  selected.id,
			err: result.err,
		}); err != nil {
			return struct{}{}, err
		}
	}
	continueGoal := result.err == nil &&
		e.goalActive() &&
		!e.goalLoopState().Suspended()
	appendNudge := true
	if !continueGoal {
		restart := e.goalLoopState().Finish(e.goalActive())
		continueGoal = restart &&
			e.goalActive() &&
			!e.currentNodeExecutionActive()
		appendNudge = !restart
		if !continueGoal {
			e.finishLiveRunGoalLoop()
		}
	}
	if continueGoal {
		if err := e.boundaryAgenda.accept(newGoalContinuationAgendaItem(appendNudge)); err != nil {
			e.goalLoopState().Finish(false)
			e.finishLiveRunGoalLoop()
			return struct{}{}, err
		}
	}
	return struct{}{}, e.reduceIdleBoundary(admission)
}

func (e *Engine) finishGoalContinuationOnRuntimeClose() {
	if e == nil || !e.goalLoopState().Running() {
		return
	}
	e.goalLoopState().Finish(false)
	e.finishLiveRunGoalLoop()
}
