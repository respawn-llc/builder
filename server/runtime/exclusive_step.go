package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"core/server/llm"
	"core/shared/textutil"

	"github.com/google/uuid"
)

var ErrAgentBusy = errors.New("agent is busy")

var ErrEngineClosed = errors.New("runtime engine is closed")

var ErrExclusiveStepReservationPending = errors.New("manual compaction is already pending")

var errPendingModelRecoveryClear = errors.New("clear pending model recovery")

type defaultExclusiveStepLifecycle struct {
	engine     *Engine
	background backgroundNoticeScheduler

	mu                 sync.Mutex
	active             *exclusiveRunState
	nextWaiters        []*exclusiveStepWaiter
	heldReservation    *exclusiveStepReservation
	heldWaiter         *exclusiveStepWaiter
	reservationWaiters map[*exclusiveStepReservation]*exclusiveStepWaiter
	reservations       map[*exclusiveStepReservation]struct{}
	suspended          *exclusiveRunState
	boundaryReady      bool
	boundaryDone       chan struct{}
	runSeq             uint64
	terminalPublishing bool
}

type exclusiveStepWaiter struct {
	ready       chan struct{}
	reservation *exclusiveStepReservation
}

type exclusiveRunState struct {
	sequence    uint64
	mode        RunMode
	activeKind  ActiveKind
	cancel      context.CancelFunc
	runID       string
	stepID      string
	startedAt   time.Time
	closing     bool
	interrupted bool
	reservation *exclusiveStepReservation
}

func (s *defaultExclusiveStepLifecycle) Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) (err error) {
	return s.run(ctx, options, false, fn)
}

func (s *defaultExclusiveStepLifecycle) RunNext(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
	return s.run(ctx, options, true, fn)
}

func (s *defaultExclusiveStepLifecycle) AcquireReservation(reservation *exclusiveStepReservation) error {
	if reservation == nil || reservation.Kind != exclusiveStepReservationManualCompaction {
		return errors.New("exclusive step reservation is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reservationPendingLocked(reservation) {
		return ErrExclusiveStepReservationPending
	}
	waiter := &exclusiveStepWaiter{ready: make(chan struct{}), reservation: reservation}
	if s.reservationWaiters == nil {
		s.reservationWaiters = make(map[*exclusiveStepReservation]*exclusiveStepWaiter)
		s.reservations = make(map[*exclusiveStepReservation]struct{})
	}
	s.reservationWaiters[reservation] = waiter
	s.reservations[reservation] = struct{}{}
	if s.heldReservation == nil {
		s.heldReservation = reservation
		s.heldWaiter = waiter
	}
	insertAt := len(s.nextWaiters)
	if reservation.queueable {
		for index, queued := range s.nextWaiters {
			if queued.reservation == nil {
				select {
				case <-queued.ready:
					continue
				default:
				}
				insertAt = index
				break
			}
		}
	}
	s.nextWaiters = append(s.nextWaiters, nil)
	copy(s.nextWaiters[insertAt+1:], s.nextWaiters[insertAt:])
	s.nextWaiters[insertAt] = waiter
	s.notifyNextWaiterLocked()
	return nil
}

func (s *defaultExclusiveStepLifecycle) ReleaseReservation(reservation *exclusiveStepReservation) {
	s.mu.Lock()
	if reservation == nil || s.reservations == nil {
		s.mu.Unlock()
		panic("exclusive step reservation release does not match the held reservation")
	}
	if _, known := s.reservations[reservation]; !known {
		s.mu.Unlock()
		panic("exclusive step reservation release does not match the held reservation")
	}
	if waiter := s.reservationWaiters[reservation]; waiter != nil {
		index := slices.Index(s.nextWaiters, waiter)
		if index >= 0 {
			s.nextWaiters = append(s.nextWaiters[:index], s.nextWaiters[index+1:]...)
		}
		if s.boundaryDone != nil {
			close(s.boundaryDone)
			s.boundaryDone = nil
			s.boundaryReady = false
		}
		delete(s.reservationWaiters, reservation)
	}
	delete(s.reservations, reservation)
	if s.heldReservation == reservation {
		s.heldReservation = nil
		s.heldWaiter = nil
	}
	s.notifyNextWaiterLocked()
	s.mu.Unlock()
	s.engine.surfaceRunError(s.scheduleIdleWork(true))
}

func (s *defaultExclusiveStepLifecycle) run(ctx context.Context, options exclusiveStepOptions, waitForNext bool, fn func(stepCtx context.Context, stepID string) error) (err error) {
	var stepCtx context.Context
	var stepID string
	if waitForNext {
		stepCtx, stepID, err = s.beginNext(ctx, options)
	} else {
		stepCtx, stepID, err = s.begin(ctx, options)
	}
	if err != nil {
		return err
	}
	if options.EmitRunState {
		if snapshot := s.Snapshot(); snapshot != nil {
			s.engine.beginLiveRunStep(snapshot)
		}
	}
	if options.EmitRunState {
		if snapshot := s.Snapshot(); snapshot != nil {
			mode := runModeFromActiveKind(snapshot.ActiveKind)
			_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventRunStateChanged, StepID: stepID, RunState: &RunState{
				Lifecycle:  RunningRunLifecycle(mode),
				RunID:      snapshot.RunID,
				ActiveKind: snapshot.ActiveKind,
				Status:     snapshot.Status,
				StartedAt:  snapshot.StartedAt,
			}}))

		}
	}
	err = fn(stepCtx, stepID)
	return s.finishStep(stepID, options, err)
}

func (s *defaultExclusiveStepLifecycle) finishStep(stepID string, options exclusiveStepOptions, err error) error {
	s.closeActiveStepQueue(stepID)
	if fatal, ok := resultGroupFatalFromError(err); ok {
		return s.finishRuntimeAbort(stepID, options, fatal)
	}
	if clearReasoningErr := s.engine.steer(stepID, steerClearReasoningStateIntent()); clearReasoningErr != nil {
		err = errors.Join(err, fmt.Errorf("clear reasoning state at agent step termination: %w", clearReasoningErr))
	}
	if assignmentErr := s.engine.flushPendingWorkflowAssignments(stepID); assignmentErr != nil {
		err = errors.Join(err, fmt.Errorf("flush workflow assignments: %w", assignmentErr))
	}
	if drainErr := s.engine.drainActiveStepGoalMutations(stepID); drainErr != nil {
		err = errors.Join(err, fmt.Errorf("drain active-step goal mutations: %w", drainErr))
	}
	finishedAt := time.Now().UTC()
	status := statusFromRunError(err)
	snapshot := s.snapshotWithFinishedAt(finishedAt, status)
	if status != RunStatusCompleted {
		if cleanupErr := s.engine.resetReasoningAndClearStreamingState(stepID); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("reset failed-step streaming state: %w", cleanupErr))
		}
	}
	err, _, publishLiveRunFinished := s.publishTerminalStep(
		stepID,
		options,
		snapshot,
		status,
		err,
		func() error {
			clearErr := s.engine.store.ClearPendingModelRecoveryForStep(stepID)
			if clearErr == nil {
				return nil
			}
			wrapped := fmt.Errorf("%w: %w", errPendingModelRecoveryClear, clearErr)
			_ = s.engine.steer(stepID, steerEventIntent(Event{
				Kind:   EventInFlightClearFailed,
				StepID: stepID,
				Error:  wrapped.Error(),
			}))
			return wrapped
		},
	)
	if !errors.Is(err, errPendingModelRecoveryClear) {
		if status == RunStatusCompleted && snapshot != nil && snapshot.ActiveKind == ActiveKindUserTurn {
			s.engine.resumeSuspendedGoalAfterSuccessfulUserTurn()
		}
		if startErr := s.scheduleIdleWork(status != RunStatusFailed); startErr != nil {
			err = errors.Join(err, startErr)
		}
	}
	if publishLiveRunFinished != nil {
		publishLiveRunFinished()
	}
	return err
}

func (s *defaultExclusiveStepLifecycle) finishRuntimeAbort(
	stepID string,
	options exclusiveStepOptions,
	fatal *resultGroupFatal,
) error {
	liveErr := error(fatal)
	var ancillaryErr error
	if resetErr := s.engine.resetReasoningAndClearStreamingState(stepID); resetErr != nil {
		ancillaryErr = fmt.Errorf("reset failed-step streaming state: %w", resetErr)
		liveErr = errors.Join(liveErr, ancillaryErr)
	}
	finishedAt := time.Now().UTC()
	status := RunStatusFailed
	snapshot := s.snapshotWithFinishedAt(finishedAt, status)
	_, publicationErr, publishLiveRunFinished := s.publishTerminalStep(
		stepID,
		options,
		snapshot,
		status,
		liveErr,
		nil,
	)
	if publishLiveRunFinished != nil {
		publishLiveRunFinished()
	}
	s.engine.surfaceRunError(errors.Join(ancillaryErr, publicationErr))
	s.engine.SetStreamingError(runtimeAbortFeedbackMessage(fatal))
	s.engine.closeAdmissionAfterRuntimeAbort()
	return &resultGroupRuntimeAbort{
		fatal:     fatal,
		ancillary: errors.Join(ancillaryErr, publicationErr),
	}
}

func (s *defaultExclusiveStepLifecycle) publishTerminalStep(
	stepID string,
	options exclusiveStepOptions,
	snapshot *RunSnapshot,
	status RunStatus,
	err error,
	beforeLiveRun func() error,
) (error, error, func()) {
	s.beginTerminalPublication()
	if options.EmitRunState {
		state := &RunState{Lifecycle: IdleRunLifecycle()}
		if snapshot != nil {
			mode := runModeFromActiveKind(snapshot.ActiveKind)
			state.Lifecycle = FinishedRunLifecycle(mode)
			state.RunID = snapshot.RunID
			state.ActiveKind = snapshot.ActiveKind
			state.Status = snapshot.Status
			state.StartedAt = snapshot.StartedAt
			state.FinishedAt = snapshot.FinishedAt
		}
		_ = s.engine.steer(stepID, steerEventIntent(Event{
			Kind:     EventRunStateChanged,
			StepID:   stepID,
			RunState: state,
		}))
	}
	var publicationErr error
	if snapshot != nil && s.engine.cfg.StepLifecycle != nil {
		if publishErr := s.engine.cfg.StepLifecycle.StepEnded(
			context.Background(),
			stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionEnded, *snapshot),
		); publishErr != nil {
			publicationErr = fmt.Errorf("publish step ended: %w", publishErr)
			err = errors.Join(err, publicationErr)
		}
	}
	if beforeLiveRun != nil {
		err = errors.Join(err, beforeLiveRun())
	}
	var publishLiveRunFinished func()
	if options.EmitRunState {
		publishLiveRunFinished = s.engine.finishLiveRunStep(snapshot, status, err)
	}
	s.finishTerminalPublication()
	return err, publicationErr, publishLiveRunFinished
}

func (s *defaultExclusiveStepLifecycle) Interrupt() error {
	_, err := s.InterruptCurrent(nil)
	return err
}

func (s *defaultExclusiveStepLifecycle) InterruptCurrent(beforeCancel func(*RunSnapshot)) (*RunSnapshot, error) {
	return s.interruptMatching("", "", beforeCancel)
}

func (s *defaultExclusiveStepLifecycle) InterruptMatching(
	runID string,
	stepID string,
	beforeCancel func(*RunSnapshot),
) (*RunSnapshot, error) {
	if runID == "" || stepID == "" {
		return nil, errors.New("observed run and step identities are required")
	}
	return s.interruptMatching(runID, stepID, beforeCancel)
}

func (s *defaultExclusiveStepLifecycle) interruptMatching(
	runID string,
	stepID string,
	beforeCancel func(*RunSnapshot),
) (*RunSnapshot, error) {
	s.mu.Lock()
	active := s.active
	suspended := s.suspended
	if active == nil && suspended == nil {
		s.mu.Unlock()
		return nil, nil
	}
	if (active != nil && active.interrupted) || (suspended != nil && suspended.interrupted) {
		s.mu.Unlock()
		return nil, nil
	}
	snapshot := cloneRunSnapshot(s.snapshotLocked())
	if runID != "" && (snapshot == nil || snapshot.RunID != runID || snapshot.StepID != stepID) {
		s.mu.Unlock()
		return nil, nil
	}
	if beforeCancel != nil {
		beforeCancel(cloneRunSnapshot(snapshot))
	}
	if active != nil {
		active.interrupted = true
	}
	if suspended != nil {
		suspended.interrupted = true
	}
	s.mu.Unlock()
	if active != nil && active.cancel != nil {
		active.cancel()
	}
	if suspended != nil && suspended.cancel != nil {
		suspended.cancel()
	}
	s.mu.Lock()
	if active != nil && (s.active == nil || s.active.sequence != active.sequence) {
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	if err := s.engine.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeInterruption), Content: textutil.Value(interruptMessage)}})); err != nil {
		s.mu.Lock()
		if active != nil && s.active != nil && s.active.sequence == active.sequence {
			s.active.interrupted = false
		}
		s.mu.Unlock()
		return nil, err
	}
	return snapshot, nil
}

func (s *defaultExclusiveStepLifecycle) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil || s.heldReservation != nil || s.terminalPublishing || len(s.nextWaiters) > 0
}

func (s *defaultExclusiveStepLifecycle) Snapshot() *RunSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRunSnapshot(s.snapshotLocked())
}

func (s *defaultExclusiveStepLifecycle) WithActiveStep(fn func(stepID string) error) (bool, error) {
	if s == nil || fn == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.stepID == "" {
		return false, nil
	}
	if s.active.closing {
		return true, ErrAgentBusy
	}
	return true, fn(s.active.stepID)
}

func (s *defaultExclusiveStepLifecycle) ApplyForActiveStep(stepID string, apply func() error) error {
	if s == nil || apply == nil {
		return errors.New("active step authority action is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.stepID != stepID || s.active.closing || s.active.interrupted {
		return ErrActiveStepInactive
	}
	return apply()
}

func (s *defaultExclusiveStepLifecycle) closeActiveStepQueue(stepID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.stepID != stepID {
		return
	}
	s.active.closing = true
}

func (s *defaultExclusiveStepLifecycle) begin(ctx context.Context, options exclusiveStepOptions) (context.Context, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateExclusiveStepStart(ctx, options); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	if s.active != nil || s.heldReservation != nil || s.terminalPublishing || len(s.nextWaiters) > 0 {
		s.mu.Unlock()
		return nil, "", ErrAgentBusy
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, "", err
	}
	stepCtx, stepID := s.activateLocked(ctx, options)
	s.mu.Unlock()
	return s.publishStepBegan(options, stepCtx, stepID)
}

func (s *defaultExclusiveStepLifecycle) beginNext(ctx context.Context, options exclusiveStepOptions) (context.Context, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateExclusiveStepStart(ctx, options); err != nil {
		return nil, "", err
	}

	s.mu.Lock()
	if s.reservationPendingLocked(options.Reservation) {
		s.mu.Unlock()
		return nil, "", ErrExclusiveStepReservationPending
	}
	if options.Reservation == nil && s.active == nil && s.heldReservation == nil && !s.terminalPublishing && len(s.nextWaiters) == 0 {
		stepCtx, stepID := s.activateLocked(ctx, options)
		s.mu.Unlock()
		return s.publishStepBegan(options, stepCtx, stepID)
	}
	var waiter *exclusiveStepWaiter
	if options.Reservation != nil {
		waiter = s.reservationWaiters[options.Reservation]
	}
	if waiter == nil {
		if options.Reservation != nil {
			s.mu.Unlock()
			return nil, "", errors.New("exclusive step reservation has no queued boundary token")
		}
		waiter = &exclusiveStepWaiter{ready: make(chan struct{})}
		s.nextWaiters = append(s.nextWaiters, waiter)
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
	case <-waiter.ready:
	}
	if err := ctx.Err(); err != nil {
		idle := s.cancelNextWaiter(waiter)
		if idle {
			return nil, "", errors.Join(err, s.scheduleIdleWork(true))
		}
		return nil, "", err
	}

	s.mu.Lock()
	if len(s.nextWaiters) == 0 || s.nextWaiters[0] != waiter ||
		(s.active != nil && !s.boundaryReady) ||
		s.terminalPublishing {
		s.mu.Unlock()
		return nil, "", errors.New("exclusive step next-boundary reservation invariant violated")
	}
	s.nextWaiters = s.nextWaiters[1:]
	if s.active != nil && s.boundaryReady {
		s.suspended = s.active
		s.active = nil
		s.boundaryReady = false
	}
	if waiter == s.heldWaiter {
		s.heldWaiter = nil
	}
	if options.Reservation != nil {
		delete(s.reservationWaiters, options.Reservation)
	}
	stepCtx, stepID := s.activateLocked(ctx, options)
	s.mu.Unlock()
	return s.publishStepBegan(options, stepCtx, stepID)
}

func validateExclusiveStepStart(ctx context.Context, options exclusiveStepOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !options.ActiveKind.Valid() {
		return errors.New("exclusive step active kind is required")
	}
	if options.Reservation != nil && options.Reservation.Kind != exclusiveStepReservationManualCompaction {
		return errors.New("exclusive step reservation kind is invalid")
	}
	return nil
}

func (s *defaultExclusiveStepLifecycle) activateLocked(ctx context.Context, options exclusiveStepOptions) (context.Context, string) {
	stepCtx, cancel := context.WithCancel(ctx)
	s.runSeq++
	runID := uuid.NewString()
	stepID := uuid.NewString()
	startedAt := time.Now().UTC()
	s.active = &exclusiveRunState{
		sequence:    s.runSeq,
		mode:        runModeFromActiveKind(options.ActiveKind),
		activeKind:  options.ActiveKind,
		cancel:      cancel,
		runID:       runID,
		stepID:      stepID,
		startedAt:   startedAt,
		reservation: options.Reservation,
	}
	return stepCtx, stepID
}

func (s *defaultExclusiveStepLifecycle) publishStepBegan(options exclusiveStepOptions, stepCtx context.Context, stepID string) (context.Context, string, error) {
	if snapshot := s.Snapshot(); snapshot != nil && s.engine.cfg.StepLifecycle != nil {
		if err := s.engine.cfg.StepLifecycle.StepBegan(context.Background(), stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionBegan, *snapshot)); err != nil {
			finished := s.snapshotWithFinishedAt(time.Now().UTC(), RunStatusFailed)
			s.beginTerminalPublication()
			if options.EmitRunState {
				state := &RunState{Lifecycle: IdleRunLifecycle()}
				if finished != nil {
					mode := runModeFromActiveKind(finished.ActiveKind)
					state.Lifecycle = FinishedRunLifecycle(mode)
					state.RunID = finished.RunID
					state.ActiveKind = finished.ActiveKind
					state.Status = finished.Status
					state.StartedAt = finished.StartedAt
					state.FinishedAt = finished.FinishedAt
				}
				_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventRunStateChanged, StepID: stepID, RunState: state}))
			}
			if finished != nil {
				if endErr := s.engine.cfg.StepLifecycle.StepEnded(context.Background(), stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionEnded, *finished)); endErr != nil {
					err = errors.Join(err, fmt.Errorf("publish start-failed step ended: %w", endErr))
				}
			}
			if clearErr := s.engine.store.ClearPendingModelRecoveryForStep(stepID); clearErr != nil {
				err = errors.Join(err, fmt.Errorf("%w: %w", errPendingModelRecoveryClear, clearErr))
			}
			s.finishTerminalPublication()
			return nil, "", err
		}
	}
	return stepCtx, stepID, nil
}

func (s *defaultExclusiveStepLifecycle) reservationPendingLocked(reservation *exclusiveStepReservation) bool {
	if reservation != nil && reservation.queueable {
		return false
	}
	return reservation != nil && s.heldReservation != nil && s.heldReservation != reservation && s.heldReservation.Kind == reservation.Kind
}

func (s *defaultExclusiveStepLifecycle) cancelNextWaiter(waiter *exclusiveStepWaiter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if waiter.reservation != nil && waiter.reservation == s.heldReservation {
		return false
	}
	index := slices.Index(s.nextWaiters, waiter)
	removed := index >= 0
	if removed {
		s.nextWaiters = append(s.nextWaiters[:index], s.nextWaiters[index+1:]...)
	}
	s.notifyNextWaiterLocked()
	return removed && s.active == nil && !s.terminalPublishing && len(s.nextWaiters) == 0
}

func (s *defaultExclusiveStepLifecycle) notifyNextWaiterLocked() {
	if (s.active != nil && !s.boundaryReady) || s.terminalPublishing || len(s.nextWaiters) == 0 {
		return
	}
	if s.heldReservation != nil && s.heldWaiter == nil {
		return
	}
	select {
	case <-s.nextWaiters[0].ready:
	default:
		close(s.nextWaiters[0].ready)
	}
}

func (s *defaultExclusiveStepLifecycle) scheduleIdleWork(scheduleQueuedUserWork bool) error {
	if scheduleQueuedUserWork {
		if !s.engine.scheduleQueuedUserInjectionsIfIdle() && s.background != nil {
			s.background.ScheduleIfIdle()
		}
	} else if s.background != nil {
		s.background.ScheduleIfIdle()
	}
	return s.engine.startPendingGoalLoop()
}

func (s *defaultExclusiveStepLifecycle) end() {
	s.mu.Lock()
	s.active = nil
	s.notifyNextWaiterLocked()
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) beginTerminalPublication() {
	s.mu.Lock()
	s.active = nil
	s.terminalPublishing = true
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) finishTerminalPublication() {
	s.mu.Lock()
	s.terminalPublishing = false
	if s.suspended != nil {
		s.active = s.suspended
		s.suspended = nil
		if s.boundaryDone != nil {
			close(s.boundaryDone)
			s.boundaryDone = nil
		}
	}
	s.notifyNextWaiterLocked()
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) DrainAgentStepBoundary(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.active == nil || !isAgentStepCapable(s.active.activeKind) ||
			len(s.nextWaiters) == 0 || s.nextWaiters[0].reservation == nil {
			s.mu.Unlock()
			return nil
		}
		done := make(chan struct{})
		s.boundaryDone = done
		s.boundaryReady = true
		s.notifyNextWaiterLocked()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			nested := s.suspended != nil || (s.active != nil && s.active.activeKind == ActiveKindCompaction)
			if !nested && s.boundaryDone == done {
				s.boundaryDone = nil
				s.boundaryReady = false
				close(done)
			}
			s.mu.Unlock()
			if nested {
				<-done
			}
			return ctx.Err()
		case <-done:
		}
	}
}

func (s *defaultExclusiveStepLifecycle) EndAgentStepBoundary() {
	s.mu.Lock()
	s.boundaryReady = false
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) snapshotLocked() *RunSnapshot {
	active := s.active
	if active == nil {
		active = s.suspended
	}
	if active == nil || active.runID == "" {
		return nil
	}
	return &RunSnapshot{
		RunID:      active.runID,
		StepID:     active.stepID,
		Status:     RunStatusRunning,
		ActiveKind: active.activeKind,
		GoalLoop:   active.activeKind == ActiveKindGoalLoop,
		StartedAt:  active.startedAt,
	}
}

func (s *defaultExclusiveStepLifecycle) snapshotWithFinishedAt(finishedAt time.Time, status RunStatus) *RunSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.runID == "" {
		return nil
	}
	return &RunSnapshot{
		RunID:      s.active.runID,
		StepID:     s.active.stepID,
		Status:     status,
		ActiveKind: s.active.activeKind,
		GoalLoop:   s.active.activeKind == ActiveKindGoalLoop,
		StartedAt:  s.active.startedAt,
		FinishedAt: finishedAt,
	}
}

func stepLifecycleSnapshot(sessionID string, transition StepLifecycleTransition, snapshot RunSnapshot) StepLifecycleSnapshot {
	return StepLifecycleSnapshot{
		SessionID:   sessionID,
		RunID:       snapshot.RunID,
		StepID:      snapshot.StepID,
		ActiveKind:  snapshot.ActiveKind,
		Transition:  transition,
		Status:      snapshot.Status,
		StartedAt:   snapshot.StartedAt,
		FinishedAt:  snapshot.FinishedAt,
		PublishedAt: time.Now().UTC(),
	}
}

func runModeFromActiveKind(kind ActiveKind) RunMode {
	if kind == ActiveKindGoalLoop {
		return RunModeGoalLoop
	}
	return RunModeTurn
}

func cloneRunSnapshot(snapshot *RunSnapshot) *RunSnapshot {
	if snapshot == nil {
		return nil
	}
	cloned := *snapshot
	return &cloned
}

func statusFromRunError(err error) RunStatus {
	if err == nil {
		return RunStatusCompleted
	}
	if errors.Is(err, context.Canceled) {
		return RunStatusInterrupted
	}
	return RunStatusFailed
}
