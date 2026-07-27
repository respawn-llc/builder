package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/workflow"
	"core/server/workflowstore"
)

const (
	ReasonSchedulerRuntimeStartFailed    = "workflow_runtime_start_failed"
	ReasonSchedulerExplicitQueueFailed   = "workflow_explicit_queue_failed"
	ReasonSchedulerPendingAskUnavailable = "workflow_pending_ask_unavailable"
	ReasonSchedulerStartupOrphanedRun    = "workflow_startup_orphaned_run"
	ReasonSchedulerStartupUnstartedRun   = "workflow_startup_unstarted_run"
)

type SchedulerService struct {
	store              SchedulerStore
	starter            SchedulerRuntimeStarter
	pendingAskResolver SchedulerPendingAskResolver
	concurrency        int
	claimRetries       int
	claimBackoff       time.Duration
	processInterval    time.Duration
	logger             SchedulerLogger
	attentionFinalizer SchedulerInterruptedRunFinalizer
	automaticIntents   *AutomaticIntents
	mutationPermit     *MutationPermit

	processGate    chan struct{}
	stoppedCh      chan struct{}
	mu             sync.Mutex
	active         map[workflow.RunID]schedulerActiveRun
	activeRevision SchedulerActiveRunRevision
	stopped        bool
	started        bool
	loopCancel     context.CancelFunc
	loopWG         sync.WaitGroup
	processWG      sync.WaitGroup
	wake           chan struct{}
	explicit       []workflow.RunID
}

const (
	defaultClaimRetries    = 3
	defaultClaimBackoff    = 10 * time.Millisecond
	defaultProcessInterval = 5000 * time.Millisecond
	defaultWakeBuffer      = 1
)

func NewSchedulerService(store SchedulerStore, starter SchedulerRuntimeStarter, mutationPermit *MutationPermit, cfg SchedulerConfig, opts ...SchedulerOption) (*SchedulerService, error) {
	if store == nil {
		return nil, errors.New("workflow scheduler store is required")
	}
	if mutationPermit == nil {
		return nil, errors.New("workflow mutation permit is required")
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	service := &SchedulerService{
		store:            store,
		starter:          starter,
		concurrency:      concurrency,
		claimRetries:     defaultClaimRetries,
		claimBackoff:     defaultClaimBackoff,
		processInterval:  defaultProcessInterval,
		automaticIntents: NewAutomaticIntents(),
		mutationPermit:   mutationPermit,
		active:           map[workflow.RunID]schedulerActiveRun{},
		wake:             make(chan struct{}, defaultWakeBuffer),
		processGate:      make(chan struct{}, 1),
		stoppedCh:        make(chan struct{}),
	}
	service.processGate <- struct{}{}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

type SchedulerOption func(*SchedulerService)

func WithSchedulerPendingAskResolver(resolver SchedulerPendingAskResolver) SchedulerOption {
	return func(s *SchedulerService) {
		s.pendingAskResolver = resolver
	}
}

func WithSchedulerAttentionFinalizer(finalizer SchedulerInterruptedRunFinalizer) SchedulerOption {
	return func(s *SchedulerService) {
		s.attentionFinalizer = finalizer
	}
}

func WithAutomaticIntents(intents *AutomaticIntents) SchedulerOption {
	return func(s *SchedulerService) {
		if intents != nil {
			s.automaticIntents = intents
		}
	}
}

func WithSchedulerProcessInterval(interval time.Duration) SchedulerOption {
	return func(s *SchedulerService) {
		if interval > 0 {
			s.processInterval = interval
		}
	}
}

func (s *SchedulerService) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}
	if s.started {
		s.mu.Unlock()
		return s.Process(ctx)
	}
	s.mu.Unlock()
	if err := s.Reconcile(ctx); err != nil {
		return err
	}
	if err := s.Process(ctx); err != nil {
		if errors.Is(err, ErrSchedulerRuntimeStartFailed) {
			s.logf("workflow.scheduler.startup_process_error error=%q", err.Error())
		} else {
			return err
		}
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	s.loopCancel = cancel
	s.loopWG.Add(1)
	s.started = true
	s.mu.Unlock()
	go s.runLoop(loopCtx)
	return nil
}

func (s *SchedulerService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	close(s.stoppedCh)
	cancel := s.loopCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.loopWG.Wait()
	s.processWG.Wait()
	return nil
}

func (s *SchedulerService) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *SchedulerService) Stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *SchedulerService) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

func (s *SchedulerService) EnsureTaskQuiescent(ctx context.Context, taskID workflow.TaskID) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	if taskID == "" {
		return errors.New("workflow task id is required")
	}
	s.mu.Lock()
	for _, active := range s.active {
		if active.request.TaskID == taskID {
			s.mu.Unlock()
			return ErrTaskExecutionNotQuiescent
		}
	}
	explicitRunIDs := append([]workflow.RunID(nil), s.explicit...)
	s.mu.Unlock()
	pendingRunIDs := append(explicitRunIDs, s.automaticIntents.PendingRunIDs()...)
	for _, runID := range pendingRunIDs {
		run, err := s.store.GetRun(ctx, runID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if run.TaskID == taskID {
			return ErrTaskExecutionNotQuiescent
		}
	}
	return nil
}

func (s *SchedulerService) RuntimeFinished(runID workflow.RunID, generation int64) {
	s.releaseActive(runID, generation)
	s.Notify()
}

func (s *SchedulerService) releaseActive(runID workflow.RunID, generation int64) {
	s.mu.Lock()
	current, ok := s.active[runID]
	if ok && current.request.Generation == generation {
		s.automaticIntents.SourceFinished(runID)
		delete(s.active, runID)
		s.recordActiveRunMutationLocked()
	}
	s.mu.Unlock()
}

func (s *SchedulerService) Notify() {
	if s == nil {
		return
	}
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *SchedulerService) RegisterAutomaticStarts(runIDs []workflow.RunID) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	return s.automaticIntents.RegisterAutomaticStarts(runIDs)
}

func (s *SchedulerService) StartExplicitRuns(ctx context.Context, runIDs []workflow.RunID) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	for _, runID := range runIDs {
		if runID == "" {
			continue
		}
		run, err := s.store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if err := s.startRun(ctx, workflowstore.RunnableRunRecord{RunRecord: run}); err != nil {
			return err
		}
	}
	return nil
}

func (s *SchedulerService) QueueExplicitRuns(runIDs []workflow.RunID) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	queued := append([]workflow.RunID(nil), runIDs...)
	for index, runID := range queued {
		if runID == "" {
			return fmt.Errorf("explicit workflow start run id at index %d is blank", index)
		}
	}
	if len(queued) == 0 {
		return nil
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}
	s.explicit = append(s.explicit, queued...)
	s.mu.Unlock()
	s.Notify()
	return nil
}

func (s *SchedulerService) runLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(s.processInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-s.automaticIntents.Notifications():
		case <-ticker.C:
		}
		if err := s.Process(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrSchedulerStopped) {
			s.logf("workflow.scheduler.process_error error=%q", err.Error())
		}
	}
}

func (s *SchedulerService) Reconcile(ctx context.Context) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	return s.mutationPermit.Run(ctx, s.reconcile)
}

func (s *SchedulerService) reconcile(ctx context.Context) error {
	waiting, err := s.store.ListWaitingAskRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range waiting {
		if run.WaitingAskID == nil {
			return fmt.Errorf("waiting ask run %q has no ask id", run.ID)
		}
		canRehydrate := false
		if s.pendingAskResolver != nil {
			canRehydrate, err = s.pendingAskResolver.CanRehydrate(ctx, run.SessionID, run.ID, *run.WaitingAskID)
			if err != nil {
				return err
			}
		}
		if !canRehydrate {
			s.logf("workflow.scheduler.recovery run_id=%s action=interrupt reason=%s", run.ID, ReasonSchedulerPendingAskUnavailable)
			if err := s.store.InterruptRun(ctx, run.ID, ReasonSchedulerPendingAskUnavailable, "{}"); err != nil {
				return err
			}
			s.finalizeInterruptedRun(ctx, run.ID)
		} else {
			s.logf("workflow.scheduler.recovery run_id=%s action=preserve_waiting_ask ask_id=%s", run.ID, *run.WaitingAskID)
		}
	}
	s.logf("workflow.scheduler.recovery action=interrupt_orphaned_started reason=%s", ReasonSchedulerStartupOrphanedRun)
	interrupted, err := s.store.ReconcileStartedRuns(ctx, ReasonSchedulerStartupOrphanedRun)
	if err != nil {
		return err
	}
	for _, run := range interrupted {
		s.finalizeInterruptedRun(ctx, run.ID)
	}
	s.logf("workflow.scheduler.recovery action=interrupt_unstarted_runs reason=%s", ReasonSchedulerStartupUnstartedRun)
	interrupted, err = s.store.ReconcileUnstartedRuns(ctx, ReasonSchedulerStartupUnstartedRun)
	if err != nil {
		return err
	}
	for _, run := range interrupted {
		s.finalizeInterruptedRun(ctx, run.ID)
	}
	return nil
}

func (s *SchedulerService) Process(ctx context.Context) error {
	if s == nil {
		return errors.New("workflow scheduler is required")
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}
	s.processWG.Add(1)
	s.mu.Unlock()
	defer s.processWG.Done()
	select {
	case <-s.stoppedCh:
		return ErrSchedulerStopped
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-s.processGate:
	}
	defer func() { s.processGate <- struct{}{} }()
	if err := s.processExplicitRuns(ctx); err != nil {
		return err
	}
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return ErrSchedulerStopped
		}
		if s.starter == nil {
			s.mu.Unlock()
			return nil
		}
		capacity := s.concurrency - len(s.active)
		s.mu.Unlock()
		if capacity <= 0 {
			return nil
		}
		runIDs := s.automaticIntents.Take(capacity)
		if len(runIDs) == 0 {
			return nil
		}
		if err := s.startRunBatch(
			ctx,
			runIDs,
			func(runID workflow.RunID) { s.automaticIntents.Resolve(runID) },
			s.automaticIntents.ReturnFront,
		); err != nil {
			return err
		}
	}
}

func (s *SchedulerService) processExplicitRuns(ctx context.Context) error {
	s.mu.Lock()
	starterAvailable := s.starter != nil
	s.mu.Unlock()
	if !starterAvailable {
		return nil
	}
	return s.startRunBatch(ctx, s.takeExplicitRuns(), nil, s.returnExplicitRuns)
}

func (s *SchedulerService) startRunBatch(
	ctx context.Context,
	runIDs []workflow.RunID,
	resolveMissing func(workflow.RunID),
	returnFront func([]workflow.RunID),
) error {
	for index, runID := range runIDs {
		run, err := s.store.GetRun(ctx, runID)
		if errors.Is(err, sql.ErrNoRows) {
			if resolveMissing != nil {
				resolveMissing(runID)
			}
			continue
		}
		if err != nil {
			returnFront(runIDs[index:])
			return err
		}
		if err := s.startRun(ctx, workflowstore.RunnableRunRecord{RunRecord: run}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if resolveMissing != nil {
					resolveMissing(runID)
				}
				continue
			}
			retry := runIDs[index+1:]
			if errors.Is(err, ErrSchedulerClaimFailed) {
				retry = runIDs[index:]
			}
			if !errors.Is(err, ErrSchedulerStopped) {
				returnFront(retry)
			}
			return err
		}
	}
	return nil
}

func (s *SchedulerService) takeExplicitRuns() []workflow.RunID {
	s.mu.Lock()
	defer s.mu.Unlock()
	runIDs := s.explicit
	s.explicit = nil
	return runIDs
}

func (s *SchedulerService) returnExplicitRuns(runIDs []workflow.RunID) {
	if len(runIDs) == 0 {
		return
	}
	s.mu.Lock()
	s.explicit = append(append([]workflow.RunID(nil), runIDs...), s.explicit...)
	s.mu.Unlock()
}

func (s *SchedulerService) startRun(ctx context.Context, candidate workflowstore.RunnableRunRecord) error {
	req, prepared, err := s.prepareRun(ctx, candidate)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		s.logf("workflow.scheduler.runtime_prepare run_id=%s action=interrupt reason=%s", candidate.ID, ReasonSchedulerRuntimeStartFailed)
		return s.interruptReservedRun(ctx, candidate, req, nil, err)
	}
	if err := prepared.Commit(); err != nil {
		s.logf("workflow.scheduler.runtime_commit run_id=%s action=interrupt reason=%s", candidate.ID, ReasonSchedulerRuntimeStartFailed)
		return s.interruptReservedRun(ctx, candidate, req, prepared, err)
	}
	admitted, err := s.admitPreparedRun(ctx, candidate, prepared.Admission())
	if err != nil {
		abortErr := prepared.Abort(context.WithoutCancel(ctx))
		s.releaseActive(req.RunID, req.Generation)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Join(err, abortErr)
		}
		return errors.Join(fmt.Errorf("%w: %w", ErrSchedulerClaimFailed, err), abortErr)
	}
	if admitted.Generation != req.Generation {
		panic(fmt.Sprintf(
			"workflow run admission generation mismatch run_id=%s got=%d want=%d",
			admitted.ID,
			admitted.Generation,
			req.Generation,
		))
	}
	s.logf("workflow.scheduler.selection run_id=%s task_id=%s generation=%d action=start", req.RunID, req.TaskID, req.Generation)
	prepared.Activate()
	s.markActiveRunRunning(req)
	return nil
}

func (s *SchedulerService) prepareRun(
	ctx context.Context,
	candidate workflowstore.RunnableRunRecord,
) (SchedulerStartRunRequest, PreparedWorkflowRun, error) {
	targetGeneration := candidate.Generation + 1
	if targetGeneration <= candidate.Generation {
		return SchedulerStartRunRequest{}, nil, errors.New("workflow run generation overflow")
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return SchedulerStartRunRequest{}, nil, ErrSchedulerStopped
	}
	if _, ok := s.active[candidate.ID]; ok {
		s.mu.Unlock()
		s.automaticIntents.Resolve(candidate.ID)
		return SchedulerStartRunRequest{}, nil, sql.ErrNoRows
	}
	reserved := SchedulerStartRunRequest{
		RunID:       candidate.ID,
		TaskID:      candidate.TaskID,
		PlacementID: candidate.PlacementID,
		NodeID:      candidate.NodeID,
		Generation:  targetGeneration,
	}
	s.active[candidate.ID] = schedulerActiveRun{
		request: reserved,
		phase:   SchedulerActiveRunPhaseStarting,
	}
	s.recordActiveRunMutationLocked()
	s.automaticIntents.SourceStarted(candidate.ID)
	s.mu.Unlock()

	prepared, err := s.starter.PrepareWorkflowRun(ctx, SchedulerPrepareRunRequest{
		RunID:            candidate.ID,
		TaskID:           candidate.TaskID,
		PlacementID:      candidate.PlacementID,
		NodeID:           candidate.NodeID,
		SourceGeneration: candidate.Generation,
		Generation:       targetGeneration,
	})
	if err != nil {
		s.releaseActive(candidate.ID, targetGeneration)
		return reserved, nil, err
	}
	if prepared == nil {
		s.releaseActive(candidate.ID, targetGeneration)
		return reserved, nil, errors.New("workflow runtime preparation is required")
	}
	return reserved, prepared, nil
}

func (s *SchedulerService) admitPreparedRun(
	ctx context.Context,
	candidate workflowstore.RunnableRunRecord,
	admission RunAdmission,
) (workflowstore.RunnableRunRecord, error) {
	var admitted workflowstore.RunnableRunRecord
	err := s.mutationPermit.Run(ctx, func(ctx context.Context) error {
		var err error
		admitted, err = s.admitRunWithRetry(ctx, workflowstore.RunAdmission{
			RunID:                   candidate.ID,
			ExpectedGeneration:      candidate.Generation,
			SessionID:               admission.SessionID,
			EffectiveCompletionMode: admission.EffectiveCompletionMode,
		})
		return err
	})
	if err != nil {
		return workflowstore.RunnableRunRecord{}, err
	}
	s.automaticIntents.Resolve(candidate.ID)
	return admitted, nil
}

func (s *SchedulerService) interruptReservedRun(
	ctx context.Context,
	candidate workflowstore.RunnableRunRecord,
	req SchedulerStartRunRequest,
	prepared PreparedWorkflowRun,
	cause error,
) error {
	interruptErr := s.mutationPermit.Run(context.WithoutCancel(ctx), func(ctx context.Context) error {
		return s.store.InterruptRunGeneration(
			ctx,
			candidate.ID,
			candidate.Generation,
			ReasonSchedulerRuntimeStartFailed,
			fmt.Sprintf(`{"error":%q}`, cause.Error()),
		)
	})
	if interruptErr == nil {
		s.automaticIntents.Resolve(candidate.ID)
		s.finalizeInterruptedRun(context.WithoutCancel(ctx), candidate.ID)
	}
	var abortErr error
	if prepared != nil {
		abortErr = prepared.Abort(context.WithoutCancel(ctx))
	}
	s.releaseActive(req.RunID, req.Generation)
	runtimeErr := fmt.Errorf("%w: %w", ErrSchedulerRuntimeStartFailed, cause)
	if interruptErr != nil {
		return errors.Join(
			runtimeErr,
			fmt.Errorf("%w: persist failed workflow runtime preparation: %w", ErrSchedulerClaimFailed, interruptErr),
			abortErr,
		)
	}
	return errors.Join(runtimeErr, abortErr)
}

func (s *SchedulerService) finalizeInterruptedRun(ctx context.Context, runID workflow.RunID) {
	if s == nil || s.attentionFinalizer == nil || runID == "" {
		return
	}
	s.attentionFinalizer.PublishPendingInterruptedRun(ctx, runID)
}

func (s *SchedulerService) admitRunWithRetry(ctx context.Context, admission workflowstore.RunAdmission) (workflowstore.RunnableRunRecord, error) {
	var lastErr error
	for attempt := 0; attempt <= s.claimRetries; attempt++ {
		claimed, err := s.store.AdmitRun(ctx, admission)
		if err == nil {
			return claimed, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return workflowstore.RunnableRunRecord{}, err
		}
		lastErr = err
		s.logf("workflow.scheduler.claim_retry run_id=%s attempt=%d error=%q", admission.RunID, attempt+1, err.Error())
		if s.claimBackoff > 0 && attempt < s.claimRetries {
			timer := time.NewTimer(s.claimBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return workflowstore.RunnableRunRecord{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return workflowstore.RunnableRunRecord{}, lastErr
}

func (s *SchedulerService) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Logf(format, args...)
	}
}
