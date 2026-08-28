package runtime

import (
	"context"
	"errors"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type operationalPendingWorkRequest struct {
	compactionRequestID *runtimeids.CompactionRequestID
	worktreeOperationID *clientui.WorktreeTransitionID
	reservationKind     exclusiveStepReservationKind
	manualCompaction    *runtimeinput.ManualCompactionAdmission
	worktreeTransition  *runtimeinput.PendingWorkWorktreeTransition
	accept              CommandAcceptance
	run                 func(context.Context, *exclusiveStepReservation, runtimeinput.PendingWorkItem) error
}

func (e *Engine) scheduleOperationalPendingWork(ctx context.Context, request operationalPendingWorkRequest) error {
	if request.run == nil {
		return errors.New("operational Pending Work executor is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := e.requirePendingWorkCapacity(); err != nil {
		return err
	}
	item, err := request.pendingWorkItem()
	if err != nil {
		return err
	}

	e.ensureOrchestrationCollaborators()
	reservation := &exclusiveStepReservation{
		Kind:      request.reservationKind,
		queueable: true,
	}
	pendingCtx, cancelPending := context.WithCancelCause(context.Background())
	admitted := false
	committed, acceptErr := runCommandAcceptance(request.accept, func() (bool, error) {
		reservation.pendingWork = &pendingOperationalWork{
			order:  *e.nextPendingWorkSteerAdmission(),
			item:   item,
			cancel: cancelPending,
		}
		if err := e.stepLifecycle.AcquireReservation(reservation); err != nil {
			reservation.pendingWork = nil
			return false, err
		}
		admitted = true
		e.publishPendingWorkChanged()
		return true, nil
	})
	if err := commandAcceptanceResult(committed, acceptErr); err != nil {
		if admitted {
			e.stepLifecycle.ReleaseReservation(reservation)
		}
		cancelPending(err)
		return err
	}
	if !admitted {
		cancelPending(context.Canceled)
		return context.Canceled
	}

	launched := e.launchLifecycleTask(func(lifecycleCtx context.Context) *resultGroupFatal {
		stopLifecycleCancellation := context.AfterFunc(lifecycleCtx, func() {
			cancelPending(context.Cause(lifecycleCtx))
		})
		defer stopLifecycleCancellation()
		defer cancelPending(context.Canceled)
		defer e.stepLifecycle.ReleaseReservation(reservation)
		fatal, abort := resultGroupFatalFromError(request.run(pendingCtx, reservation, item))
		if abort {
			return fatal
		}
		return nil
	})
	if !launched {
		e.stepLifecycle.ReleaseReservation(reservation)
		cancelPending(ErrEngineClosed)
		return ErrEngineClosed
	}
	return nil
}

func (r operationalPendingWorkRequest) pendingWorkItem() (runtimeinput.PendingWorkItem, error) {
	item := runtimeinput.PendingWorkItem{
		Lane:  runtimeinput.PendingWorkLaneSteer,
		State: runtimeinput.PendingWorkItemStatePending,
	}
	var err error
	switch r.reservationKind {
	case exclusiveStepReservationManualCompaction:
		if r.compactionRequestID == nil ||
			r.worktreeOperationID != nil ||
			r.manualCompaction == nil ||
			r.worktreeTransition != nil {
			return runtimeinput.PendingWorkItem{}, errors.New("manual-compaction Pending Work payload is required")
		}
		item.ID, err = serverapi.PendingWorkItemIDFromCompactionRequest(*r.compactionRequestID)
		if err != nil {
			return runtimeinput.PendingWorkItem{}, err
		}
		payload := *r.manualCompaction
		if payload.Guidance != nil {
			guidance := *payload.Guidance
			payload.Guidance = &guidance
		}
		item.Kind = runtimeinput.PendingWorkItemKindManualCompaction
		item.ManualCompaction = &payload
		item.CanonicalInput, err = payload.CanonicalInput()
	case exclusiveStepReservationWorktreeTransition:
		if r.worktreeOperationID == nil ||
			r.compactionRequestID != nil ||
			r.worktreeTransition == nil ||
			r.manualCompaction != nil {
			return runtimeinput.PendingWorkItem{}, errors.New("Worktree-transition Pending Work payload is required")
		}
		item.ID, err = serverapi.PendingWorkItemIDFromWorktreeOperation(*r.worktreeOperationID)
		if err != nil {
			return runtimeinput.PendingWorkItem{}, err
		}
		payload := *r.worktreeTransition
		if payload.Selector != nil {
			selector := *payload.Selector
			payload.Selector = &selector
		}
		item.Kind = runtimeinput.PendingWorkItemKindWorktreeTransition
		item.WorktreeTransition = &payload
		item.CanonicalInput, err = payload.CanonicalInput()
	default:
		return runtimeinput.PendingWorkItem{}, errors.New("operational Pending Work reservation kind is invalid")
	}
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	if err := item.Validate(); err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	return item, nil
}
