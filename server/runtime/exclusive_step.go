package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/llm"

	"github.com/google/uuid"
)

var ErrAgentBusy = errors.New("agent is busy")

var ErrEngineClosed = errors.New("runtime engine is closed")

// errPendingModelRecoveryClear wraps failures to clear the recovery marker at
// step end after terminal transcript state has already been published.
var errPendingModelRecoveryClear = errors.New("clear pending model recovery")

type defaultExclusiveStepLifecycle struct {
	engine     *Engine
	background backgroundNoticeScheduler

	mu                 sync.Mutex
	active             *exclusiveRunState
	runSeq             uint64
	terminalPublishing bool
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
}

func (s *defaultExclusiveStepLifecycle) Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) (err error) {
	stepCtx, stepID, err := s.begin(ctx, options)
	if err != nil {
		return err
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
	if drainErr := s.engine.drainActiveStepGoalMutations(stepID); drainErr != nil {
		err = errors.Join(err, fmt.Errorf("drain active-step goal mutations: %w", drainErr))
	}
	finishedAt := time.Now().UTC()
	status := statusFromRunError(err)
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
	if clearErr := s.engine.store.ClearPendingModelRecoveryForStep(stepID); clearErr != nil {
		wrapped := fmt.Errorf("%w: %w", errPendingModelRecoveryClear, clearErr)
		_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventInFlightClearFailed, StepID: stepID, Error: wrapped.Error()}))
		err = errors.Join(err, wrapped)
	}
	s.finishTerminalPublication()
	if !errors.Is(err, errPendingModelRecoveryClear) {
		if status == RunStatusCompleted && snapshot != nil && snapshot.ActiveKind == ActiveKindUserTurn {
			s.engine.resumeSuspendedGoalAfterSuccessfulUserTurn()
		}
		if status != RunStatusFailed {
			if !s.engine.scheduleQueuedUserInjectionsIfIdle() && s.background != nil {
				s.background.ScheduleIfIdle()
			}
		} else if s.background != nil {
			s.background.ScheduleIfIdle()
		}
		if startErr := s.engine.startPendingGoalLoop(); startErr != nil {
			err = errors.Join(err, startErr)
		}
	}
	return err
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
	if err := s.engine.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeInterruption, Content: interruptMessage}})); err != nil {
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
	return s.active != nil || s.terminalPublishing
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
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	default:
	}
	s.mu.Lock()
	if s.active != nil || s.terminalPublishing {
		s.mu.Unlock()
		return nil, "", ErrAgentBusy
	}
	if !options.ActiveKind.Valid() {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("exclusive step active kind is required")
	}
	select {
	case <-ctx.Done():
		s.mu.Unlock()
		return nil, "", ctx.Err()
	default:
	}
	stepCtx, cancel := context.WithCancel(ctx)
	s.runSeq++
	runID := uuid.NewString()
	stepID := uuid.NewString()
	startedAt := time.Now().UTC()
	s.active = &exclusiveRunState{
		sequence:   s.runSeq,
		mode:       runModeFromActiveKind(options.ActiveKind),
		activeKind: options.ActiveKind,
		cancel:     cancel,
		runID:      runID,
		stepID:     stepID,
		startedAt:  startedAt,
	}
	s.mu.Unlock()

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

func (s *defaultExclusiveStepLifecycle) end() {
	s.mu.Lock()
	s.active = nil
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
