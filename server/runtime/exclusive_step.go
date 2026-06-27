package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/llm"
	"core/server/session"

	"github.com/google/uuid"
)

var errExclusiveStepBusy = errors.New("agent is busy")

func IsAgentBusyError(err error) bool {
	return errors.Is(err, errExclusiveStepBusy)
}

// errMarkInFlightFalse wraps failures to clear the in-flight marker at step end.
var errMarkInFlightFalse = errors.New("mark in-flight false")

type defaultExclusiveStepLifecycle struct {
	engine     *Engine
	background backgroundNoticeScheduler

	mu     sync.Mutex
	active *exclusiveRunState
	runSeq uint64
}

type exclusiveRunState struct {
	sequence  uint64
	mode      RunMode
	cancel    context.CancelFunc
	runID     string
	stepID    string
	startedAt time.Time
}

func (s *defaultExclusiveStepLifecycle) Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) (err error) {
	stepCtx, stepID, err := s.begin(ctx, options)
	if err != nil {
		return err
	}
	if options.EmitRunState {
		if snapshot := s.Snapshot(); snapshot != nil {
			if options.PersistRunLifecycle {
				if _, persistErr := s.engine.store.AppendRunStarted(session.RunRecord{
					RunID:     snapshot.RunID,
					StepID:    snapshot.StepID,
					Status:    session.RunStatusRunning,
					StartedAt: snapshot.StartedAt,
				}); persistErr != nil {
					s.end()
					if clearErr := s.engine.store.MarkInFlight(false); clearErr != nil {
						persistErr = errors.Join(persistErr, fmt.Errorf("%w: %w", errMarkInFlightFalse, clearErr))
					}
					return persistErr
				}
			}
			mode := RunModeTurn
			if snapshot.GoalLoop {
				mode = RunModeGoalLoop
			}
			_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventRunStateChanged, StepID: stepID, RunState: &RunState{
				Lifecycle: RunningRunLifecycle(mode),
				RunID:     snapshot.RunID,
				Status:    snapshot.Status,
				StartedAt: snapshot.StartedAt,
			}}))

		}
	}
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			if drainErr := s.engine.drainActiveStepGoalMutations(stepID); drainErr != nil {
				err = errors.Join(err, fmt.Errorf("drain active-step goal mutations: %w", drainErr))
			}
		}
		finishedAt := time.Now().UTC()
		status := statusFromRunError(err)
		if panicValue != nil {
			status = RunStatusFailed
		}
		snapshot := s.snapshotWithFinishedAt(finishedAt, status)
		if status != RunStatusCompleted {
			_ = s.engine.steer(stepID, steerClearStreamingStateIntent())
		}
		s.end()
		if options.EmitRunState {
			state := &RunState{Lifecycle: IdleRunLifecycle()}
			if snapshot != nil {
				mode := RunModeTurn
				if snapshot.GoalLoop {
					mode = RunModeGoalLoop
				}
				state.Lifecycle = FinishedRunLifecycle(mode)
				state.RunID = snapshot.RunID
				state.Status = snapshot.Status
				state.StartedAt = snapshot.StartedAt
				state.FinishedAt = snapshot.FinishedAt
			}
			_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventRunStateChanged, StepID: stepID, RunState: state}))
		}
		if options.PersistRunLifecycle && snapshot != nil {
			if _, persistErr := s.engine.store.AppendRunFinished(session.RunRecord{
				RunID:      snapshot.RunID,
				StepID:     snapshot.StepID,
				Status:     session.RunStatus(snapshot.Status),
				StartedAt:  snapshot.StartedAt,
				FinishedAt: snapshot.FinishedAt,
			}); persistErr != nil {
				err = errors.Join(err, fmt.Errorf("append run finished: %w", persistErr))
			}
		}
		if clearErr := s.engine.store.MarkInFlight(false); clearErr != nil {
			wrapped := fmt.Errorf("%w: %w", errMarkInFlightFalse, clearErr)
			_ = s.engine.steer(stepID, steerEventIntent(Event{Kind: EventInFlightClearFailed, StepID: stepID, Error: wrapped.Error()}))
			err = errors.Join(err, wrapped)
		} else if status != RunStatusFailed {
			if !s.engine.scheduleQueuedUserInjectionsIfIdle() && s.background != nil {
				s.background.ScheduleIfIdle()
			}
		} else if s.background != nil {
			s.background.ScheduleIfIdle()
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	return fn(stepCtx, stepID)
}

func (s *defaultExclusiveStepLifecycle) Interrupt() error {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()

	if active == nil || active.cancel == nil {
		return nil
	}
	active.cancel()
	s.mu.Lock()
	if s.active == nil || s.active.sequence != active.sequence {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.engine.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeInterruption, Content: interruptMessage}})); err != nil {
		return err
	}
	if err := s.engine.store.MarkInFlight(false); err != nil {
		return err
	}
	return nil
}

func (s *defaultExclusiveStepLifecycle) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
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
	return true, fn(s.active.stepID)
}

func (s *defaultExclusiveStepLifecycle) begin(ctx context.Context, options exclusiveStepOptions) (context.Context, string, error) {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return nil, "", errExclusiveStepBusy
	}
	stepCtx, cancel := context.WithCancel(ctx)
	s.runSeq++
	runID := uuid.NewString()
	stepID := uuid.NewString()
	startedAt := time.Now().UTC()
	mode := RunModeTurn
	if options.GoalLoop {
		mode = RunModeGoalLoop
	}
	s.active = &exclusiveRunState{
		sequence:  s.runSeq,
		mode:      mode,
		cancel:    cancel,
		runID:     runID,
		stepID:    stepID,
		startedAt: startedAt,
	}
	s.mu.Unlock()

	if err := s.engine.store.MarkInFlight(true); err != nil {
		s.end()
		return nil, "", err
	}
	return stepCtx, stepID, nil
}

func (s *defaultExclusiveStepLifecycle) end() {
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) snapshotLocked() *RunSnapshot {
	if s.active == nil || s.active.runID == "" {
		return nil
	}
	return &RunSnapshot{
		RunID:     s.active.runID,
		StepID:    s.active.stepID,
		Status:    RunStatusRunning,
		GoalLoop:  s.active.mode == RunModeGoalLoop,
		StartedAt: s.active.startedAt,
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
		GoalLoop:   s.active.mode == RunModeGoalLoop,
		StartedAt:  s.active.startedAt,
		FinishedAt: finishedAt,
	}
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
