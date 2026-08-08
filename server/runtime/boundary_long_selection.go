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

func (e *Engine) reduceIdleBoundary(admission runtimeEventAdmission) error {
	if e == nil || !e.idleBoundaryReductionEligible() {
		return nil
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
		case *humanBoundaryAgendaItem:
			return e.startRuntimeBoundHumanExecution(admission)
		default:
			return fmt.Errorf("unsupported idle Boundary Agenda item %T", next)
		}
	}
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
