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
	item   runtimeinput.PendingWorkItem
	accept CommandAcceptance
	run    func(context.Context, *exclusiveStepReservation, runtimeinput.PendingWorkItem) error
}

func (e *Engine) scheduleOperationalPendingWork(ctx context.Context, request operationalPendingWorkRequest) error {
	if request.run == nil {
		return errors.New("operational Pending Work executor is required")
	}
	if request.item.Kind != runtimeinput.PendingWorkItemKindManualCompaction &&
		request.item.Kind != runtimeinput.PendingWorkItemKindWorktreeTransition {
		return errors.New("operational Pending Work item kind is invalid")
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
	if err := request.item.Validate(); err != nil {
		return err
	}
	if request.item.Kind == runtimeinput.PendingWorkItemKindManualCompaction {
		if err := e.manualCompactionAdmissionError(); err != nil {
			return err
		}
	}
	reservationKind := exclusiveStepReservationWorktreeTransition
	if request.item.Kind == runtimeinput.PendingWorkItemKindManualCompaction {
		reservationKind = exclusiveStepReservationManualCompaction
	}

	e.ensureOrchestrationCollaborators()
	reservation := &exclusiveStepReservation{
		Kind:      reservationKind,
		queueable: true,
	}
	pendingCtx, cancelPending := context.WithCancelCause(context.Background())
	committed, acceptErr := runCommandAcceptance(request.accept, func() (bool, error) {
		reservation.pendingWork = &pendingOperationalWork{
			item:   request.item,
			cancel: cancelPending,
		}
		if err := e.stepLifecycle.AcquireReservation(reservation); err != nil {
			reservation.pendingWork = nil
			return false, err
		}
		e.publishPendingWorkChanged()
		return true, nil
	})
	if err := commandAcceptanceResult(committed, acceptErr); err != nil {
		if committed {
			e.stepLifecycle.ReleaseReservation(reservation)
		}
		cancelPending(err)
		return err
	}

	run := func(lifecycleCtx context.Context) error {
		stopLifecycleCancellation := context.AfterFunc(lifecycleCtx, func() {
			cancelPending(context.Cause(lifecycleCtx))
		})
		defer stopLifecycleCancellation()
		defer cancelPending(context.Canceled)
		defer e.stepLifecycle.ReleaseReservation(reservation)
		return request.run(pendingCtx, reservation, request.item)
	}
	var launched bool
	if request.item.Kind == runtimeinput.PendingWorkItemKindWorktreeTransition {
		launched = e.launchIndeterminateWorktreeLifecycleTask(run)
	} else {
		launched = e.launchLifecycleTask(func(lifecycleCtx context.Context) *resultGroupFatal {
			fatal, _ := resultGroupFatalFromError(run(lifecycleCtx))
			return fatal
		})
	}
	if !launched {
		e.stepLifecycle.ReleaseReservation(reservation)
		cancelPending(ErrEngineClosed)
		return ErrEngineClosed
	}
	return nil
}

func manualCompactionPendingWorkItem(id runtimeids.CompactionRequestID, payload runtimeinput.ManualCompactionAdmission) (runtimeinput.PendingWorkItem, error) {
	itemID, err := serverapi.PendingWorkItemIDFromCompactionRequest(id)
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	if payload.Guidance != nil {
		guidance := *payload.Guidance
		payload.Guidance = &guidance
	}
	canonical, err := payload.CanonicalInput()
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	item := runtimeinput.PendingWorkItem{
		ID: itemID, Lane: runtimeinput.PendingWorkLaneSteer, Kind: runtimeinput.PendingWorkItemKindManualCompaction,
		State: runtimeinput.PendingWorkItemStatePending, CanonicalInput: canonical, ManualCompaction: &payload,
	}
	return item, nil
}

func worktreePendingWorkItem(id clientui.WorktreeTransitionID, payload runtimeinput.PendingWorkWorktreeTransition) (runtimeinput.PendingWorkItem, error) {
	itemID, err := serverapi.PendingWorkItemIDFromWorktreeOperation(id)
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	if payload.Selector != nil {
		selector := *payload.Selector
		payload.Selector = &selector
	}
	canonical, err := payload.CanonicalInput()
	if err != nil {
		return runtimeinput.PendingWorkItem{}, err
	}
	item := runtimeinput.PendingWorkItem{
		ID: itemID, Lane: runtimeinput.PendingWorkLaneSteer, Kind: runtimeinput.PendingWorkItemKindWorktreeTransition,
		State: runtimeinput.PendingWorkItemStatePending, CanonicalInput: canonical, WorktreeTransition: &payload,
	}
	return item, nil
}
