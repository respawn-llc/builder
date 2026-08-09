package runtime

import (
	"context"
	"errors"
	"fmt"
)

type boundaryLongWork interface {
	longWorkID() boundaryAgendaItemID
	runLongWork(context.Context, *Engine) error
	settleLongWork(error)
}

type runtimeBoundLongWork interface {
	boundaryLongWork
	completeRuntimeBoundLongWork(*Engine, error)
}

type boundaryLongWorkStarter interface {
	beginLongWork(*Engine) error
}

type boundaryLongWorkTransferFailure interface {
	failLongWorkTransfer(*Engine)
}

type boundaryLongAgendaItem interface {
	boundaryAgendaItem
	selectLongWork() boundaryLongWork
}

type boundaryLongWorkResult struct {
	id  boundaryAgendaItemID
	err error
}

type boundaryLongOrchestrator struct {
	selected boundaryLongWork
}

func (e *Engine) reduceIdleBoundary(admission runtimeEventAdmission) (resultErr error) {
	if e == nil || !e.idleBoundaryReductionEligible() {
		return nil
	}
	var grant IdleBoundaryReducerGrant
	if lifecycle, ok := e.cfg.StepLifecycle.(IdleBoundaryReducerLifecycle); ok {
		acquiredGrant, acquired, err := lifecycle.TryAcquireIdleBoundary(admission.Context())
		if err != nil || !acquired {
			return err
		}
		grant = acquiredGrant
		if grant == nil {
			return errors.New("idle Boundary reducer acquisition returned no grant")
		}
		defer func() {
			selectedLongWork := e.longBoundary.selected != nil
			retry, releaseErr := grant.Release()
			resultErr = errors.Join(resultErr, releaseErr)
			if retry && !selectedLongWork && resultErr == nil {
				resultErr = e.reduceIdleBoundary(admission)
			}
		}()
	}
	for {
		next := e.boundaryAgenda.peekNext(idleBoundarySelection())
		switch next.(type) {
		case nil:
			return nil
		case *workflowAssignmentAgendaItem:
			applied, err := e.applyWorkflowAssignmentBoundary(
				admission,
				"",
				idleBoundarySelection(),
			)
			if err != nil || applied == 0 {
				return err
			}
		case *backgroundNoticeAgendaItem:
			return e.startNextBackgroundLongWork(admission)
		case *manualCompactionAgendaItem:
			return e.startNextManualCompactionLongWork(admission)
		case *goalContinuationAgendaItem:
			return e.startNextGoalContinuationLongWork(admission)
		case *humanBoundaryAgendaItem:
			return e.startRuntimeBoundHumanExecution(admission)
		default:
			return fmt.Errorf("unsupported idle Boundary Agenda item %T", next)
		}
	}
}

func (e *Engine) startNextRuntimeBoundLongWork(
	admission runtimeEventAdmission,
) error {
	launcher, hasLauncher := e.cfg.StepLifecycle.(RuntimeBoundExecutionLauncher)
	scopes, hasScopeOwner := e.cfg.StepLifecycle.(AgentStepScopeLifecycle)
	activeExecution := !hasScopeOwner
	if hasScopeOwner {
		_, activeExecution = scopes.CurrentAgentExecutionScope(admission.Context())
	}
	if activeExecution {
		return e.launchNextRuntimeBoundLongWork(admission, nil)
	}
	if !hasLauncher {
		return nil
	}
	return e.launchNextRuntimeBoundLongWork(
		admission,
		launcher,
	)
}

func (e *Engine) launchNextRuntimeBoundLongWork(
	admission runtimeEventAdmission,
	launcher RuntimeBoundExecutionLauncher,
) error {
	selected, err := e.longBoundary.selectNext(
		e.boundaryAgenda,
		idleBoundarySelection(),
	)
	if err != nil || selected == nil {
		return err
	}
	runtimeBound, ok := selected.(runtimeBoundLongWork)
	if !ok {
		panic(fmt.Sprintf(
			"runtime-bound Boundary Agenda selection has unexpected type %T",
			selected,
		))
	}
	if starter, ok := selected.(boundaryLongWorkStarter); ok {
		if err := starter.beginLongWork(e); err != nil {
			_, settleErr := e.longBoundary.settle(boundaryLongWorkResult{
				id:  selected.longWorkID(),
				err: err,
			})
			return errors.Join(err, settleErr, e.reduceIdleBoundary(admission))
		}
	}
	if launcher != nil {
		launchErr := launcher.LaunchRuntimeBoundExecution(
			admission.command,
			func(workCtx context.Context, _ *Engine) error {
				runErr := selected.runLongWork(workCtx, e)
				runtimeBound.completeRuntimeBoundLongWork(e, runErr)
				return runErr
			},
			func(abortErr error) {
				runtimeBound.completeRuntimeBoundLongWork(e, abortErr)
			},
		)
		if launchErr != nil {
			_, settleErr := e.longBoundary.settle(boundaryLongWorkResult{
				id:  selected.longWorkID(),
				err: launchErr,
			})
			return errors.Join(
				launchErr,
				settleErr,
				e.reduceIdleBoundary(admission),
			)
		}
		return nil
	}
	transferErr := e.transferBoundaryLongWork(
		admission,
		selected,
		func(workCtx context.Context) {
			runErr := selected.runLongWork(workCtx, e)
			runtimeBound.completeRuntimeBoundLongWork(e, runErr)
		},
	)
	if transferErr != nil {
		if failed, ok := selected.(boundaryLongWorkTransferFailure); ok {
			failed.failLongWorkTransfer(e)
		}
	}
	return transferErr
}

func (o *boundaryLongOrchestrator) selectNext(
	agenda *boundaryAgenda,
	selection boundarySelection,
) (boundaryLongWork, error) {
	if o == nil {
		return nil, errors.New("long Boundary Agenda orchestrator is required")
	}
	if o.selected != nil {
		return nil, nil
	}
	item := agenda.selectNextLong(selection)
	if item == nil {
		return nil, nil
	}
	work := item.selectLongWork()
	if work == nil || work.longWorkID() != item.agendaID() {
		panic(fmt.Sprintf(
			"long Boundary Agenda item %q produced invalid selected work",
			item.agendaID(),
		))
	}
	o.selected = work
	return work, nil
}

func (o *boundaryLongOrchestrator) settle(result boundaryLongWorkResult) (boundaryLongWork, error) {
	if o == nil || o.selected == nil {
		return nil, errors.New("long Boundary Agenda result has no selected work")
	}
	if result.id != o.selected.longWorkID() {
		return nil, fmt.Errorf(
			"long Boundary Agenda result %q does not match selected work %q",
			result.id,
			o.selected.longWorkID(),
		)
	}
	selected := o.selected
	o.selected = nil
	selected.settleLongWork(result.err)
	return selected, nil
}

func (o *boundaryLongOrchestrator) detach(
	selected boundaryLongWork,
) error {
	if o == nil || o.selected == nil {
		return errors.New("long Boundary Agenda detach has no selected work")
	}
	if o.selected != selected {
		return fmt.Errorf(
			"long Boundary Agenda detach %q does not match selected work %q",
			selected.longWorkID(),
			o.selected.longWorkID(),
		)
	}
	o.selected = nil
	return nil
}

func (o *boundaryLongOrchestrator) close(err error) {
	if o == nil || o.selected == nil {
		return
	}
	selected := o.selected
	o.selected = nil
	selected.settleLongWork(err)
}

func (e *Engine) transferBoundaryLongWork(
	admission runtimeEventAdmission,
	selected boundaryLongWork,
	work func(context.Context),
) error {
	err := admission.startWork(work)
	if err == nil {
		return nil
	}
	_, settleErr := e.longBoundary.settle(boundaryLongWorkResult{
		id:  selected.longWorkID(),
		err: err,
	})
	return errors.Join(err, settleErr)
}

func (e *Engine) submitBoundaryLongWorkResult(
	id boundaryAgendaItemID,
	workErr error,
) {
	_, resultErr := submitRuntimeEventWithContext(
		e.lifecycleCtx,
		e.lifecycleCtx,
		e,
		boundaryLongWorkResult{id: id, err: workErr},
		func(
			admission runtimeEventAdmission,
			result boundaryLongWorkResult,
		) (struct{}, error) {
			if _, err := e.longBoundary.settle(result); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, e.reduceIdleBoundary(admission)
		},
	)
	var visibleErr error
	if resultErr != nil &&
		!errors.Is(resultErr, context.Canceled) &&
		!errors.Is(resultErr, ErrEngineClosed) {
		visibleErr = resultErr
	}
	if workErr != nil &&
		!errors.Is(workErr, context.Canceled) &&
		!errors.Is(workErr, ErrEngineClosed) {
		visibleErr = errors.Join(visibleErr, workErr)
	}
	if visibleErr != nil {
		e.surfaceRunError(visibleErr)
	}
}
