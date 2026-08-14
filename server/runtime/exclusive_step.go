package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/llm"
	"core/shared/textutil"

	"github.com/google/uuid"
)

var ErrAgentBusy = errors.New("agent is busy")

var ErrEngineClosed = errors.New("runtime engine is closed")

type defaultExclusiveStepLifecycle struct {
	engine *Engine

	mu               sync.Mutex
	active           *exclusiveRunState
	runSeq           uint64
	publicationDepth uint8
	publicationDone  chan struct{}
}

type exclusiveRunState struct {
	sequence    uint64
	mode        RunMode
	activeKind  ActiveKind
	cancel      context.CancelFunc
	runID       string
	stepID      string
	startedAt   time.Time
	stepOpen    bool
	closing     bool
	interrupted bool
}

func (s *defaultExclusiveStepLifecycle) closeAgentStep(stepID string) error {
	s.mu.Lock()
	if s.active == nil || !s.active.stepOpen || s.active.stepID != stepID ||
		!isAgentStepCapable(s.active.activeKind) || s.active.interrupted {
		s.mu.Unlock()
		return ErrActiveStepInactive
	}
	finished := cloneRunSnapshot(s.snapshotLocked())
	finished.Status = RunStatusCompleted
	finished.FinishedAt = time.Now().UTC()
	s.active.stepOpen = false
	s.mu.Unlock()
	if s.engine.cfg.StepLifecycle != nil {
		if err := s.engine.cfg.StepLifecycle.StepEnded(
			context.Background(),
			stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionEnded, *finished),
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *defaultExclusiveStepLifecycle) openAgentStep() (string, error) {
	s.mu.Lock()
	if s.active == nil || s.active.stepOpen || !isAgentStepCapable(s.active.activeKind) ||
		s.active.interrupted || s.active.closing {
		s.mu.Unlock()
		return "", ErrActiveStepInactive
	}
	nextStepID := uuid.NewString()
	s.active.stepID = nextStepID
	s.active.startedAt = time.Now().UTC()
	s.active.stepOpen = true
	started := cloneRunSnapshot(s.snapshotLocked())
	s.mu.Unlock()
	if s.engine.cfg.StepLifecycle != nil {
		if err := s.engine.cfg.StepLifecycle.StepBegan(
			context.Background(),
			stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionBegan, *started),
		); err != nil {
			return "", err
		}
	}
	return nextStepID, nil
}

func (s *defaultExclusiveStepLifecycle) agentStepOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil && s.active.stepOpen && isAgentStepCapable(s.active.activeKind)
}

func (s *defaultExclusiveStepLifecycle) runCompactionAtBoundary(
	ctx context.Context,
	fn func(context.Context, string) error,
) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	if s.active == nil {
		s.mu.Unlock()
		return s.run(ctx, exclusiveStepOptions{ActiveKind: ActiveKindCompaction}, false, fn)
	}
	if s.active.stepOpen || !isAgentStepCapable(s.active.activeKind) ||
		s.active.interrupted || s.active.closing {
		s.mu.Unlock()
		return ErrAgentBusy
	}
	previousKind := s.active.activeKind
	previousMode := s.active.mode
	s.active.activeKind = ActiveKindCompaction
	s.active.mode = runModeFromActiveKind(ActiveKindCompaction)
	s.active.stepID = uuid.NewString()
	s.active.startedAt = time.Now().UTC()
	s.active.stepOpen = true
	stepID := s.active.stepID
	started := cloneRunSnapshot(s.snapshotLocked())
	s.mu.Unlock()
	if s.engine.cfg.StepLifecycle != nil {
		if err := s.engine.cfg.StepLifecycle.StepBegan(
			context.Background(),
			stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionBegan, *started),
		); err != nil {
			return err
		}
	}
	err := fn(ctx, stepID)
	s.mu.Lock()
	if s.active == nil || s.active.stepID != stepID {
		s.mu.Unlock()
		return errors.Join(err, ErrActiveStepInactive)
	}
	finished := cloneRunSnapshot(s.snapshotLocked())
	finished.Status = statusFromRunError(err)
	finished.FinishedAt = time.Now().UTC()
	s.active.stepOpen = false
	s.active.activeKind = previousKind
	s.active.mode = previousMode
	s.mu.Unlock()
	if s.engine.cfg.StepLifecycle != nil {
		err = errors.Join(err, s.engine.cfg.StepLifecycle.StepEnded(
			context.Background(),
			stepLifecycleSnapshot(s.engine.SessionID(), StepLifecycleTransitionEnded, *finished),
		))
	}
	return err
}

func (s *defaultExclusiveStepLifecycle) Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) (err error) {
	return s.run(ctx, options, true, fn)
}

func (s *defaultExclusiveStepLifecycle) RunExactPreparation(
	ctx context.Context,
	fn func(stepCtx context.Context, stepID string) error,
) error {
	options := exclusiveStepOptions{ActiveKind: ActiveKindInspection}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateExclusiveStepStart(ctx, options); err != nil {
		return err
	}
	s.mu.Lock()
	if s.active != nil || s.publicationDepth > 0 {
		s.mu.Unlock()
		return ErrAgentBusy
	}
	stepCtx, stepID := s.activateLocked(ctx, options)
	s.mu.Unlock()
	err := fn(stepCtx, stepID)
	s.beginTerminalPublication()
	s.finishTerminalPublication()
	return err
}

func (s *defaultExclusiveStepLifecycle) run(
	ctx context.Context,
	options exclusiveStepOptions,
	drainBoundary bool,
	fn func(stepCtx context.Context, stepID string) error,
) (err error) {
	stepCtx, stepID, err := s.begin(ctx, options)
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
	return s.finishStep(s.currentStepID(stepID), options, err, drainBoundary)
}

func (s *defaultExclusiveStepLifecycle) currentStepID(fallback string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.stepID == "" {
		return fallback
	}
	return s.active.stepID
}

func (s *defaultExclusiveStepLifecycle) finishStep(stepID string, options exclusiveStepOptions, err error, drainBoundary bool) error {
	if drainBoundary {
		if drainErr := s.engine.drainSteeringAtBoundary(context.Background(), stepID); drainErr != nil {
			err = errors.Join(err, fmt.Errorf("drain Steering at agent step termination: %w", drainErr))
		}
	}
	publishLifecycle := s.stepOpen(stepID)
	s.closeActiveStepQueue(stepID)
	if fatal, ok := resultGroupFatalFromError(err); ok {
		return s.finishRuntimeAbort(stepID, options, fatal, publishLifecycle)
	}
	if clearReasoningErr := s.engine.steer(stepID, steerClearReasoningStateIntent()); clearReasoningErr != nil {
		err = errors.Join(err, fmt.Errorf("clear reasoning state at agent step termination: %w", clearReasoningErr))
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
		publishLifecycle,
		nil,
	)
	resumeGoalLoop := status == RunStatusCompleted &&
		snapshot != nil &&
		snapshot.ActiveKind == ActiveKindUserTurn
	if resumeGoalLoop {
		s.engine.resumeSuspendedGoalAfterSuccessfulUserTurn()
	}
	if publishLiveRunFinished != nil {
		publishLiveRunFinished()
	}
	if resumeGoalLoop {
		if startErr := s.engine.startPendingGoalLoop(); startErr != nil {
			err = errors.Join(err, fmt.Errorf("resume Goal loop after user turn: %w", startErr))
		}
	}
	return err
}

func (s *defaultExclusiveStepLifecycle) finishRuntimeAbort(
	stepID string,
	options exclusiveStepOptions,
	fatal *resultGroupFatal,
	publishLifecycle bool,
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
		publishLifecycle,
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
	publishLifecycle bool,
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
		_ = s.engine.applyExactRuntimeMutation(stepID, &steeringEvent{event: Event{
			Kind:     EventRunStateChanged,
			StepID:   stepID,
			RunState: state,
		}})
	}
	var publicationErr error
	if publishLifecycle && snapshot != nil && s.engine.cfg.StepLifecycle != nil {
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

func (s *defaultExclusiveStepLifecycle) stepOpen(stepID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil && s.active.stepID == stepID && s.active.stepOpen
}

func (s *defaultExclusiveStepLifecycle) Interrupt() error {
	_, err := s.InterruptCurrent(nil)
	return err
}

func (s *defaultExclusiveStepLifecycle) InterruptCurrent(beforeCancel func(*RunSnapshot)) (*RunSnapshot, error) {
	s.mu.Lock()
	active := s.active
	if active == nil {
		s.mu.Unlock()
		return nil, nil
	}
	if active.interrupted {
		s.mu.Unlock()
		return nil, nil
	}
	snapshot := cloneRunSnapshot(s.snapshotLocked())
	if beforeCancel != nil {
		beforeCancel(cloneRunSnapshot(snapshot))
	}
	active.interrupted = true
	s.beginPublicationLocked()
	s.mu.Unlock()
	if active != nil && active.cancel != nil {
		active.cancel()
	}
	s.mu.Lock()
	if !s.runCurrentLocked(active) {
		s.finishPublicationLocked()
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	err := s.persistInterruption(snapshot.StepID)
	s.mu.Lock()
	if err != nil {
		s.clearCurrentInterruptedLocked(active)
	}
	s.finishPublicationLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *defaultExclusiveStepLifecycle) InterruptCurrentAgentTurn(afterPersist func(*RunSnapshot)) (*RunSnapshot, error) {
	s.mu.Lock()
	active := s.active
	if active == nil || !isAgentStepCapable(active.activeKind) || active.closing || active.interrupted {
		s.mu.Unlock()
		return nil, nil
	}
	snapshot := cloneRunSnapshot(s.snapshotLocked())
	active.interrupted = true
	s.beginPublicationLocked()
	s.mu.Unlock()
	err := s.persistInterruption(snapshot.StepID)
	s.mu.Lock()
	if err != nil {
		s.clearCurrentInterruptedLocked(active)
		s.finishPublicationLocked()
		s.mu.Unlock()
		return nil, err
	}
	if !s.runCurrentLocked(active) {
		s.finishPublicationLocked()
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	if afterPersist != nil {
		afterPersist(cloneRunSnapshot(snapshot))
	}
	if active.cancel != nil {
		active.cancel()
	}
	s.mu.Lock()
	s.finishPublicationLocked()
	s.mu.Unlock()
	return snapshot, nil
}

func (s *defaultExclusiveStepLifecycle) persistInterruption(stepID string) error {
	intent := steerMessagesWithPersistenceIntent(
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeInterruption),
			Content:     textutil.Value(interruptMessage),
		}},
	)
	return s.engine.applyExactRuntimeMutation(stepID, intent.items[0])
}

func (s *defaultExclusiveStepLifecycle) runCurrentLocked(run *exclusiveRunState) bool {
	if run == nil {
		return false
	}
	return s.active != nil && s.active.sequence == run.sequence
}

func (s *defaultExclusiveStepLifecycle) clearCurrentInterruptedLocked(run *exclusiveRunState) {
	if run == nil {
		return
	}
	if s.active != nil && s.active.sequence == run.sequence {
		s.active.interrupted = false
	}
}

func (s *defaultExclusiveStepLifecycle) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil || s.publicationDepth > 0
}

func (s *defaultExclusiveStepLifecycle) Snapshot() *RunSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRunSnapshot(s.snapshotLocked())
}

func (s *defaultExclusiveStepLifecycle) ResolveActiveOutputStep(expectedStepID *string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.stepID == "" {
		return nil, nil
	}
	if expectedStepID != nil && s.active.stepID != *expectedStepID {
		return nil, ErrActiveStepInactive
	}
	if s.active.closing || s.active.interrupted {
		return nil, ErrActiveStepInactive
	}
	stepID := s.active.stepID
	return &stepID, nil
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

func (s *defaultExclusiveStepLifecycle) ApplyForExactGoalStep(runID string, stepID string, apply func() error) error {
	if s == nil || apply == nil {
		return errors.New("exact Goal active-step action is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil ||
		s.active.runID != runID ||
		s.active.stepID != stepID ||
		s.active.interrupted {
		return ErrActiveStepInactive
	}
	return apply()
}

func (s *defaultExclusiveStepLifecycle) ValidateExactOutput(stepID string, allowClosing bool) error {
	if s == nil {
		return ErrActiveStepInactive
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.stepID != stepID ||
		((s.active.interrupted || s.active.closing) && !allowClosing) {
		return ErrActiveStepInactive
	}
	return nil
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
	if s.active != nil || s.publicationDepth > 0 {
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

func validateExclusiveStepStart(ctx context.Context, options exclusiveStepOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !options.ActiveKind.Valid() {
		return errors.New("exclusive step active kind is required")
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
		sequence:   s.runSeq,
		mode:       runModeFromActiveKind(options.ActiveKind),
		activeKind: options.ActiveKind,
		cancel:     cancel,
		runID:      runID,
		stepID:     stepID,
		startedAt:  startedAt,
		stepOpen:   true,
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
	s.waitForPublicationLocked()
	s.active = nil
	s.beginPublicationLocked()
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) finishTerminalPublication() {
	s.mu.Lock()
	s.finishPublicationLocked()
	s.mu.Unlock()
}

func (s *defaultExclusiveStepLifecycle) beginPublicationLocked() {
	if s.publicationDepth == 0 {
		if s.publicationDone != nil {
			panic("exclusive step idle publication retained a completion channel")
		}
		s.publicationDone = make(chan struct{})
	}
	s.publicationDepth++
}

func (s *defaultExclusiveStepLifecycle) finishPublicationLocked() {
	if s.publicationDepth == 0 {
		panic("exclusive step publication depth underflow")
	}
	s.publicationDepth--
	if s.publicationDepth == 0 {
		if s.publicationDone == nil {
			panic("exclusive step publication completed without a completion channel")
		}
		close(s.publicationDone)
		s.publicationDone = nil
	}
}

func (s *defaultExclusiveStepLifecycle) waitForPublicationLocked() {
	for s.publicationDepth > 0 {
		done := s.publicationDone
		if done == nil {
			panic("exclusive step active publication is missing its completion channel")
		}
		s.mu.Unlock()
		<-done
		s.mu.Lock()
	}
}

func (s *defaultExclusiveStepLifecycle) snapshotLocked() *RunSnapshot {
	active := s.active
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
