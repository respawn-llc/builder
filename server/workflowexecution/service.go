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
	ReasonSchedulerExecutionLost         = "workflow_execution_lost"
	ReasonSchedulerPendingAskUnavailable = "workflow_pending_ask_unavailable"
	ReasonSchedulerStartupOrphanedRun    = "workflow_startup_orphaned_run"
	ReasonSchedulerStartupUnstartedRun   = "workflow_startup_unstarted_run"
)

const schedulerExecutionLostDetail = `{"error":"exact execution ended before durable workflow completion or interruption"}`

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

	processGate  chan struct{}
	stoppedCh    chan struct{}
	mu           sync.Mutex
	active       map[workflow.RunID]SchedulerStartRunRequest
	finishing    map[workflow.RunID]SchedulerStartRunRequest
	compensating map[workflow.RunID]*schedulerRunCompensation
	stopped      bool
	started      bool
	loopCancel   context.CancelFunc
	loopWG       sync.WaitGroup
	processWG    sync.WaitGroup
	wake         chan struct{}
}

type schedulerRunCompensation struct {
	mu      sync.Mutex
	runs    []schedulerCompensatedRun
	reason  string
	detail  string
	durable bool
}

type schedulerCompensatedRun struct {
	request  SchedulerStartRunRequest
	prepared PreparedWorkflowRun
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
		active:           map[workflow.RunID]SchedulerStartRunRequest{},
		finishing:        map[workflow.RunID]SchedulerStartRunRequest{},
		compensating:     map[workflow.RunID]*schedulerRunCompensation{},
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
		if active.TaskID == taskID {
			s.mu.Unlock()
			return ErrTaskExecutionNotQuiescent
		}
	}
	s.mu.Unlock()
	for _, runID := range s.automaticIntents.PendingRunIDs() {
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
	s.mu.Lock()
	current, ok := s.active[runID]
	if ok && current.Generation == generation {
		s.finishing[runID] = current
	}
	s.mu.Unlock()
	if !ok || current.Generation != generation {
		return
	}
	if err := s.reconcileFinishedRun(context.Background(), current); err != nil {
		s.logf(
			"workflow.scheduler.runtime_finished run_id=%s generation=%d action=retain_ownership error=%q",
			runID,
			generation,
			err.Error(),
		)
		s.Notify()
	}
}

func (s *SchedulerService) reconcileFinishedRun(ctx context.Context, current SchedulerStartRunRequest) error {
	s.mu.Lock()
	compensating := s.compensating[current.RunID] != nil
	s.mu.Unlock()
	if compensating {
		return nil
	}
	interrupted := false
	err := s.mutationPermit.Run(ctx, func(ctx context.Context) error {
		run, err := s.store.GetRun(ctx, current.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if run.Generation != current.Generation ||
			run.StartedAt == nil ||
			run.CompletedAt != nil ||
			run.InterruptedAt != nil {
			return nil
		}
		if err := s.store.InterruptRunGeneration(
			ctx,
			current.RunID,
			current.Generation,
			ReasonSchedulerExecutionLost,
			schedulerExecutionLostDetail,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				latest, latestErr := s.store.GetRun(ctx, current.RunID)
				if latestErr != nil {
					return latestErr
				}
				if latest.Generation == current.Generation &&
					(latest.CompletedAt != nil || latest.InterruptedAt != nil) {
					return nil
				}
			}
			return err
		}
		interrupted = true
		return nil
	})
	if err != nil {
		return err
	}
	if interrupted {
		s.finalizeInterruptedRun(context.WithoutCancel(ctx), current.RunID)
	}
	s.releaseActive(current.RunID, current.Generation)
	return nil
}

func (s *SchedulerService) reconcileFinishedRuns(ctx context.Context) error {
	s.mu.Lock()
	pending := make([]SchedulerStartRunRequest, 0, len(s.finishing))
	for _, current := range s.finishing {
		pending = append(pending, current)
	}
	s.mu.Unlock()
	for _, current := range pending {
		if err := s.reconcileFinishedRun(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

func (s *SchedulerService) compensatePreparedRun(
	ctx context.Context,
	request SchedulerStartRunRequest,
	prepared PreparedWorkflowRun,
	reason string,
	detail string,
) error {
	return s.compensatePreparedRuns(ctx, []schedulerCompensatedRun{{
		request:  request,
		prepared: prepared,
	}}, reason, detail)
}

func (s *SchedulerService) compensatePreparedRuns(
	ctx context.Context,
	runs []schedulerCompensatedRun,
	reason string,
	detail string,
) error {
	if len(runs) == 0 {
		return errors.New("workflow run compensation requires at least one run")
	}
	compensation := &schedulerRunCompensation{
		runs:   append([]schedulerCompensatedRun(nil), runs...),
		reason: reason,
		detail: detail,
	}
	s.mu.Lock()
	for _, run := range compensation.runs {
		if existing := s.compensating[run.request.RunID]; existing != nil && existing != compensation {
			s.mu.Unlock()
			return fmt.Errorf(
				"workflow run %s generation %d already has pending compensation",
				run.request.RunID,
				run.request.Generation,
			)
		}
	}
	for _, run := range compensation.runs {
		s.compensating[run.request.RunID] = compensation
	}
	s.mu.Unlock()
	err := s.reconcileRunCompensation(ctx, compensation)
	if err != nil {
		s.Notify()
	}
	return err
}

func (s *SchedulerService) reconcileRunCompensations(ctx context.Context) error {
	s.mu.Lock()
	pending := make([]*schedulerRunCompensation, 0, len(s.compensating))
	seen := make(map[*schedulerRunCompensation]struct{}, len(s.compensating))
	for _, compensation := range s.compensating {
		if _, exists := seen[compensation]; exists {
			continue
		}
		seen[compensation] = struct{}{}
		pending = append(pending, compensation)
	}
	s.mu.Unlock()
	for _, compensation := range pending {
		if err := s.reconcileRunCompensation(ctx, compensation); err != nil {
			return err
		}
	}
	return nil
}

func (s *SchedulerService) reconcileRunCompensation(ctx context.Context, compensation *schedulerRunCompensation) error {
	compensation.mu.Lock()
	defer compensation.mu.Unlock()
	s.mu.Lock()
	current := true
	for _, run := range compensation.runs {
		if s.compensating[run.request.RunID] != compensation {
			current = false
			break
		}
	}
	s.mu.Unlock()
	if !current {
		return nil
	}
	if !compensation.durable {
		var activeRefs []workflowstore.ExactRunRef
		var interrupted []workflowstore.RunRecord
		if err := s.mutationPermit.Run(ctx, func(ctx context.Context) error {
			activeRefs = make([]workflowstore.ExactRunRef, 0, len(compensation.runs))
			for _, run := range compensation.runs {
				durable, err := s.store.GetRun(ctx, run.request.RunID)
				if err != nil {
					return err
				}
				if durable.Generation != run.request.Generation {
					return fmt.Errorf(
						"workflow run compensation generation changed run_id=%s got=%d want=%d",
						run.request.RunID,
						durable.Generation,
						run.request.Generation,
					)
				}
				if durable.CompletedAt != nil || durable.InterruptedAt != nil {
					continue
				}
				if durable.StartedAt == nil {
					return fmt.Errorf(
						"workflow run compensation found unstarted admitted run run_id=%s generation=%d",
						run.request.RunID,
						run.request.Generation,
					)
				}
				activeRefs = append(activeRefs, workflowstore.ExactRunRef{
					TaskID:     run.request.TaskID,
					RunID:      run.request.RunID,
					Generation: run.request.Generation,
				})
			}
			if len(activeRefs) == 0 {
				return nil
			}
			var err error
			interrupted, err = s.store.InterruptExactRuns(ctx, activeRefs, compensation.reason, compensation.detail)
			return err
		}); err != nil {
			return err
		}
		if len(interrupted) != len(activeRefs) {
			panic(fmt.Sprintf(
				"workflow run compensation interrupted %d runs for %d exact scopes",
				len(interrupted),
				len(activeRefs),
			))
		}
		compensation.durable = true
		for _, run := range interrupted {
			s.finalizeInterruptedRun(context.WithoutCancel(ctx), run.ID)
		}
	}
	var compensationErrs []error
	for _, run := range compensation.runs {
		if err := run.prepared.Compensate(context.WithoutCancel(ctx)); err != nil {
			compensationErrs = append(compensationErrs, fmt.Errorf(
				"compensate workflow run %s generation %d: %w",
				run.request.RunID,
				run.request.Generation,
				err,
			))
		}
	}
	if err := errors.Join(compensationErrs...); err != nil {
		return err
	}
	s.mu.Lock()
	for _, run := range compensation.runs {
		if s.compensating[run.request.RunID] == compensation {
			delete(s.compensating, run.request.RunID)
		}
	}
	s.mu.Unlock()
	for _, run := range compensation.runs {
		s.releaseActive(run.request.RunID, run.request.Generation)
	}
	return nil
}

func (s *SchedulerService) releaseActive(runID workflow.RunID, generation int64) {
	s.mu.Lock()
	current, ok := s.active[runID]
	if ok && current.Generation == generation {
		delete(s.active, runID)
	}
	finishing, ok := s.finishing[runID]
	if ok && finishing.Generation == generation {
		delete(s.finishing, runID)
	}
	s.mu.Unlock()
	s.Notify()
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

func (s *SchedulerService) ResumeTaskRuns(ctx context.Context, taskID workflow.TaskID) (workflowstore.ResumeTaskRunsResult, error) {
	if s == nil {
		return workflowstore.ResumeTaskRunsResult{}, errors.New("workflow scheduler is required")
	}
	candidates, err := s.store.ListTaskResumeCandidates(ctx, taskID)
	if err != nil {
		return workflowstore.ResumeTaskRunsResult{}, err
	}
	type preparedRun struct {
		request  SchedulerStartRunRequest
		source   workflowstore.RunRecord
		prepared PreparedWorkflowRun
	}
	prepared := make([]preparedRun, 0, len(candidates))
	abortAll := func() error {
		var abortErrs []error
		for _, item := range prepared {
			if err := item.prepared.Abort(context.WithoutCancel(ctx)); err != nil {
				abortErrs = append(abortErrs, fmt.Errorf("abort workflow run %s preparation: %w", item.request.RunID, err))
			}
			s.releaseActive(item.request.RunID, item.request.Generation)
		}
		return errors.Join(abortErrs...)
	}
	for _, candidate := range candidates {
		request, preparation, err := s.prepareRun(ctx, workflowstore.RunnableRunRecord{RunRecord: candidate}, RunAdmissionResume)
		if err != nil {
			return workflowstore.ResumeTaskRunsResult{}, errors.Join(
				fmt.Errorf(
					"prepare workflow task resume task_id=%s run_id=%s source_generation=%d: %w",
					taskID,
					candidate.ID,
					candidate.Generation,
					err,
				),
				abortAll(),
			)
		}
		prepared = append(prepared, preparedRun{request: request, source: candidate, prepared: preparation})
	}
	admissions := make([]workflowstore.RunAdmission, 0, len(prepared))
	for _, item := range prepared {
		admission := item.prepared.Admission()
		admissions = append(admissions, workflowstore.RunAdmission{
			RunID:                   item.request.RunID,
			ExpectedGeneration:      item.source.Generation,
			SessionID:               admission.SessionID,
			EffectiveCompletionMode: admission.EffectiveCompletionMode,
		})
	}
	var resumed workflowstore.ResumeTaskRunsResult
	if err := s.mutationPermit.Run(ctx, func(ctx context.Context) error {
		var admitErr error
		resumed, admitErr = s.store.AdmitTaskResume(ctx, taskID, admissions)
		return admitErr
	}); err != nil {
		return workflowstore.ResumeTaskRunsResult{}, errors.Join(
			fmt.Errorf("admit workflow task resume task_id=%s: %w", taskID, err),
			abortAll(),
		)
	}
	if len(resumed.Runs) != len(prepared) {
		panic(fmt.Sprintf(
			"workflow resume admission returned %d runs for %d prepared exact execution scopes task_id=%s",
			len(resumed.Runs),
			len(prepared),
			taskID,
		))
	}
	for _, item := range prepared {
		if err := item.prepared.Commit(); err != nil {
			compensated := make([]schedulerCompensatedRun, 0, len(prepared))
			for _, admitted := range prepared {
				compensated = append(compensated, schedulerCompensatedRun{
					request:  admitted.request,
					prepared: admitted.prepared,
				})
			}
			return resumed, errors.Join(
				fmt.Errorf("%w: %w", ErrSchedulerRuntimeStartFailed, err),
				s.compensatePreparedRuns(
					context.WithoutCancel(ctx),
					compensated,
					ReasonSchedulerRuntimeStartFailed,
					fmt.Sprintf(`{"error":%q}`, err.Error()),
				),
			)
		}
	}
	return resumed, nil
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
	if err := s.reconcileRunCompensations(ctx); err != nil {
		return err
	}
	if err := s.reconcileFinishedRuns(ctx); err != nil {
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
		for index, runID := range runIDs {
			run, err := s.store.GetRun(ctx, runID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					s.automaticIntents.Resolve(runID)
					continue
				}
				s.automaticIntents.ReturnFront(runIDs[index:])
				return err
			}
			if err := s.startRun(ctx, workflowstore.RunnableRunRecord{RunRecord: run}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					s.automaticIntents.Resolve(runID)
					continue
				}
				retry := runIDs[index+1:]
				if errors.Is(err, ErrSchedulerClaimFailed) {
					retry = runIDs[index:]
				}
				if !errors.Is(err, ErrSchedulerStopped) {
					s.automaticIntents.ReturnFront(retry)
				}
				return err
			}
		}
	}
}

func (s *SchedulerService) startRun(ctx context.Context, candidate workflowstore.RunnableRunRecord) error {
	req, prepared, err := s.prepareRun(ctx, candidate, RunAdmissionInitial)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		s.logf("workflow.scheduler.runtime_prepare run_id=%s action=interrupt reason=%s", candidate.ID, ReasonSchedulerRuntimeStartFailed)
		interruptErr := s.mutationPermit.Run(context.WithoutCancel(ctx), func(ctx context.Context) error {
			return s.store.InterruptRunGeneration(
				ctx,
				candidate.ID,
				candidate.Generation,
				ReasonSchedulerRuntimeStartFailed,
				fmt.Sprintf(`{"error":%q}`, err.Error()),
			)
		})
		if interruptErr == nil {
			s.finalizeInterruptedRun(context.WithoutCancel(ctx), candidate.ID)
		}
		return errors.Join(fmt.Errorf("%w: %w", ErrSchedulerRuntimeStartFailed, err), interruptErr)
	}
	admission := prepared.Admission()
	_, err = s.admitPreparedRun(ctx, candidate, req, admission)
	if err != nil {
		abortErr := prepared.Abort(context.WithoutCancel(ctx))
		s.releaseActive(req.RunID, req.Generation)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Join(err, abortErr)
		}
		return errors.Join(fmt.Errorf("%w: %w", ErrSchedulerClaimFailed, err), abortErr)
	}
	s.logf("workflow.scheduler.selection run_id=%s task_id=%s generation=%d action=start", req.RunID, req.TaskID, req.Generation)
	if err := prepared.Commit(); err != nil {
		return errors.Join(
			fmt.Errorf("%w: %w", ErrSchedulerRuntimeStartFailed, err),
			s.compensatePreparedRun(
				context.WithoutCancel(ctx),
				req,
				prepared,
				ReasonSchedulerRuntimeStartFailed,
				fmt.Sprintf(`{"error":%q}`, err.Error()),
			),
		)
	}
	return nil
}

func (s *SchedulerService) prepareRun(ctx context.Context, candidate workflowstore.RunnableRunRecord, admission RunAdmissionKind) (SchedulerStartRunRequest, PreparedWorkflowRun, error) {
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
	request := SchedulerStartRunRequest{
		RunID:       candidate.ID,
		TaskID:      candidate.TaskID,
		PlacementID: candidate.PlacementID,
		NodeID:      candidate.NodeID,
		Generation:  targetGeneration,
	}
	s.active[candidate.ID] = request
	s.mu.Unlock()
	prepared, err := s.starter.PrepareWorkflowRun(ctx, SchedulerPrepareRunRequest{
		RunID:            candidate.ID,
		TaskID:           candidate.TaskID,
		PlacementID:      candidate.PlacementID,
		NodeID:           candidate.NodeID,
		Admission:        admission,
		SourceGeneration: candidate.Generation,
		Generation:       targetGeneration,
	})
	if err != nil {
		s.releaseActive(candidate.ID, targetGeneration)
		return SchedulerStartRunRequest{}, nil, err
	}
	return request, prepared, nil
}

func (s *SchedulerService) admitPreparedRun(
	ctx context.Context,
	candidate workflowstore.RunnableRunRecord,
	request SchedulerStartRunRequest,
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
	if admitted.Generation != request.Generation {
		panic(fmt.Sprintf(
			"workflow run admission generation mismatch run_id=%s got=%d want=%d",
			admitted.ID,
			admitted.Generation,
			request.Generation,
		))
	}
	s.automaticIntents.Resolve(candidate.ID)
	return admitted, nil
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
