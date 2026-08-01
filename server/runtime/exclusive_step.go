package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

var ErrAgentBusy = errors.New("agent is busy")

var ErrEngineClosed = errors.New("runtime engine is closed")

var ErrExclusiveStepReservationPending = errors.New("manual compaction is already pending")

var errTerminalRunErrorPersisted = errors.New("terminal run error persisted")
var errAgentStepNotDispatched = errors.New("agent step ended before provider dispatch")
var errAgentStepBoundaryUncommitted = errors.New("agent step boundary finalization did not commit")

type agentStepBoundaryUncommittedError struct {
	cause error
}

func (e *agentStepBoundaryUncommittedError) Error() string {
	if e == nil || e.cause == nil {
		return errAgentStepBoundaryUncommitted.Error()
	}
	return fmt.Sprintf("%s: %v", errAgentStepBoundaryUncommitted, e.cause)
}

func (e *agentStepBoundaryUncommittedError) Unwrap() error {
	if e == nil {
		return errAgentStepBoundaryUncommitted
	}
	return e.cause
}

func (e *agentStepBoundaryUncommittedError) Is(target error) bool {
	return target == errAgentStepBoundaryUncommitted
}

func uncommittedBoundaryFinalizationError(cause error) error {
	return &agentStepBoundaryUncommittedError{cause: cause}
}

// errPendingModelRecoveryClear wraps failures to clear the recovery marker at
// step end after terminal transcript state has already been published.
var errPendingModelRecoveryClear = errors.New("clear pending model recovery")

type defaultExclusiveStepLifecycle struct {
	engine     *Engine
	background backgroundNoticeScheduler

	mu                 sync.Mutex
	active             *exclusiveRunState
	nextWaiters        []*exclusiveStepWaiter
	heldReservation    *exclusiveStepReservation
	heldWaiter         *exclusiveStepWaiter
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
	s.heldReservation = reservation
	s.heldWaiter = &exclusiveStepWaiter{ready: make(chan struct{}), reservation: reservation}
	s.nextWaiters = append(s.nextWaiters, s.heldWaiter)
	s.notifyNextWaiterLocked()
	return nil
}

func (s *defaultExclusiveStepLifecycle) ReleaseReservation(reservation *exclusiveStepReservation) {
	s.mu.Lock()
	if reservation == nil || s.heldReservation != reservation {
		s.mu.Unlock()
		panic("exclusive step reservation release does not match the held reservation")
	}
	if s.heldWaiter != nil {
		index := slices.Index(s.nextWaiters, s.heldWaiter)
		s.nextWaiters = append(s.nextWaiters[:index], s.nextWaiters[index+1:]...)
	}
	s.heldReservation = nil
	s.heldWaiter = nil
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
	s.engine.openAgentStepBoundary(stepID)
	err = fn(stepCtx, stepID)
	return s.finishStep(stepID, options, err)
}

func (s *defaultExclusiveStepLifecycle) finishStep(stepID string, options exclusiveStepOptions, err error) error {
	s.closeActiveStepQueue(stepID)
	if assignmentErr := s.engine.flushPendingWorkflowAssignments(stepID); assignmentErr != nil {
		err = errors.Join(err, fmt.Errorf("flush workflow assignments: %w", assignmentErr))
	}
	var durableCleanupErr error
	if drainErr := s.engine.drainActiveStepGoalMutations(stepID); drainErr != nil {
		durableCleanupErr = fmt.Errorf("drain active-step goal mutations: %w", drainErr)
		err = errors.Join(err, durableCleanupErr)
	}
	if abortErr := s.engine.steer(stepID, steerLiveToolAbortIntent("terminal")); abortErr != nil {
		durableCleanupErr = errors.Join(durableCleanupErr, fmt.Errorf("cleanup dangling tools: %w", abortErr))
		err = errors.Join(err, abortErr)
	}
	boundary := s.engine.agentStepBoundary(stepID)
	status := statusFromRunError(err)
	if boundary != nil {
		if durableCleanupErr != nil {
			status = RunStatusFailed
			_ = boundary.Abort(durableCleanupErr)
			s.engine.compactionRuntimeState().manualBoundaryCoordinator().rejectDetached(boundary.TakeDetachedManual(), durableCleanupErr)
		} else if boundary.Dispatched() && err == nil {
			// A successful executor path normally commits its boundary before
			// returning. The lifecycle still owns the fallback for direct
			// terminal paths.
			if finalizationErr := s.finalizeAgentStep(stepID, boundary, nil); finalizationErr != nil {
				err = errors.Join(err, finalizationErr)
			}
		} else if boundary.Dispatched() {
			if finalizationErr := s.finalizeAgentStep(stepID, boundary, err); finalizationErr != nil {
				err = errors.Join(err, finalizationErr)
			}
		} else {
			notDispatchedErr := err
			if notDispatchedErr == nil {
				notDispatchedErr = errAgentStepNotDispatched
			}
			_ = boundary.Abort(notDispatchedErr)
			detached := boundary.TakeDetachedManual()
			s.engine.compactionRuntimeState().manualBoundaryCoordinator().rejectDetached(detached, notDispatchedErr)
			if len(detached) > 0 {
				err = errors.Join(err, notDispatchedErr)
				status = RunStatusFailed
			}
		}
	}
	if boundary != nil {
		if committedReceipt, _, committed := boundary.Committed(); committed && !committedReceipt.Committed {
			status = RunStatusFailed
		}
	}
	finishedAt := time.Now().UTC()
	snapshot := s.snapshotWithFinishedAt(finishedAt, status)
	if status != RunStatusCompleted {
		_ = s.engine.steer(stepID, steerClearStreamingStateIntent())
	}
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
		_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventRunStateChanged, StepID: stepID, RunState: state}))
	}
	if snapshot != nil && s.engine.cfg.StepLifecycle != nil {
		if publishErr := s.engine.cfg.StepLifecycle.StepEnded(context.Background(), stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionEnded, *snapshot)); publishErr != nil {
			err = errors.Join(err, fmt.Errorf("publish step ended: %w", publishErr))
		}
	}
	var publishLiveRunFinished func()
	if options.EmitRunState {
		publishLiveRunFinished = s.engine.finishLiveRunStep(snapshot, status, err)
	}
	s.finishTerminalPublication()
	s.engine.compactionRuntimeState().manualBoundaryCoordinator().endTurn()
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
	s.engine.closeAgentStepBoundary(stepID)
	return err
}

func (s *defaultExclusiveStepLifecycle) finalizeAgentStep(
	stepID string,
	boundary *agentStepBoundaryFinalizer,
	terminalErr error,
) error {
	if boundary == nil {
		return nil
	}
	receipt, commitErr, committed := boundary.Committed()
	if committed {
		return finalizeAgentStepBoundaryCommit(boundary, stepID, receipt, commitErr)
	}
	if terminalErr != nil && !errors.Is(terminalErr, context.Canceled) {
		entry := storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       llm.UserFacingError(terminalErr),
		}
		if entry.Text == "" {
			entry.Text = terminalErr.Error()
		}
		record, err := sessionLocalEntryRecordFromRuntime(entry)
		if err != nil {
			_ = boundary.Abort(err)
			s.engine.compactionRuntimeState().manualBoundaryCoordinator().rejectDetached(boundary.TakeDetachedManual(), err)
			return err
		}
		receipt, commitErr := boundary.Commit(stepID, []session.EventRecordPayload{record})
		if receipt.Committed {
			return errors.Join(
				finalizeAgentStepBoundaryCommit(boundary, stepID, receipt, commitErr),
				errTerminalRunErrorPersisted,
			)
		}
		return finalizeAgentStepBoundaryCommit(boundary, stepID, receipt, commitErr)
	}
	receipt, commitErr = boundary.Commit(stepID, nil)
	return finalizeAgentStepBoundaryCommit(boundary, stepID, receipt, commitErr)
}

func finalizeAgentStepBoundaryCommit(
	boundary *agentStepBoundaryFinalizer,
	stepID string,
	receipt session.CommitReceipt,
	commitErr error,
) error {
	if boundary == nil {
		return commitErr
	}
	boundary.Complete(receipt)
	entries := boundary.TakeDetachedManual()
	if receipt.Committed {
		if compactor, ok := boundary.engine.compactionFlow.(*defaultContextCompactor); ok {
			compactor.drainPendingManualCompactions(stepID, entries)
		}
		return commitErr
	}
	finalizationErr := uncommittedBoundaryFinalizationError(commitErr)
	boundary.engine.compactionRuntimeState().manualBoundaryCoordinator().rejectDetached(entries, finalizationErr)
	return finalizationErr
}

func (e *Engine) pendingModelRecoveryForStep(stepID string) bool {
	if e == nil || e.store == nil {
		return false
	}
	recovery := e.store.Meta().PendingModelRecovery
	return recovery != nil && strings.TrimSpace(recovery.StepID) == strings.TrimSpace(stepID)
}

func (s *defaultExclusiveStepLifecycle) Interrupt() error {
	_, err := s.InterruptCurrent(nil)
	return err
}

func (s *defaultExclusiveStepLifecycle) InterruptCurrent(beforeCancel func(*RunSnapshot)) (*RunSnapshot, error) {
	s.mu.Lock()
	active := s.active
	if active == nil || active.cancel == nil {
		s.mu.Unlock()
		return nil, nil
	}
	if s.active.interrupted {
		s.mu.Unlock()
		return nil, nil
	}
	snapshot := cloneRunSnapshot(s.snapshotLocked())
	if beforeCancel != nil {
		beforeCancel(cloneRunSnapshot(snapshot))
	}
	s.active.interrupted = true
	s.mu.Unlock()
	active.cancel()
	s.mu.Lock()
	if s.active == nil || s.active.sequence != active.sequence {
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	if err := s.engine.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeInterruption), Content: textutil.Value(interruptMessage)}})); err != nil {
		s.mu.Lock()
		if s.active != nil && s.active.sequence == active.sequence {
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
		waiter = s.heldWaiter
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
	if len(s.nextWaiters) == 0 || s.nextWaiters[0] != waiter || s.active != nil || s.terminalPublishing {
		s.mu.Unlock()
		return nil, "", errors.New("exclusive step next-boundary reservation invariant violated")
	}
	s.nextWaiters = s.nextWaiters[1:]
	if waiter == s.heldWaiter {
		s.heldWaiter = nil
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
	if isAgentStepCapable(options.ActiveKind) {
		s.engine.compactionRuntimeState().manualBoundaryCoordinator().armNextGeneration()
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
			if isAgentStepCapable(options.ActiveKind) {
				s.engine.compactionRuntimeState().manualBoundaryCoordinator().endTurn()
			}
			s.finishTerminalPublication()
			return nil, "", err
		}
	}
	return stepCtx, stepID, nil
}

func (s *defaultExclusiveStepLifecycle) reservationPendingLocked(reservation *exclusiveStepReservation) bool {
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
	if s.active != nil || s.terminalPublishing || len(s.nextWaiters) == 0 {
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
	s.notifyNextWaiterLocked()
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) snapshotLocked() *RunSnapshot {
	if s.active == nil || s.active.runID == "" {
		return nil
	}
	return &RunSnapshot{
		RunID:      s.active.runID,
		StepID:     s.active.stepID,
		Status:     RunStatusRunning,
		ActiveKind: s.active.activeKind,
		GoalLoop:   s.active.activeKind == ActiveKindGoalLoop,
		StartedAt:  s.active.startedAt,
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
