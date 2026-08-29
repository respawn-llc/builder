package runtime

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeinput"
)

var ErrWorktreeDeleteBlockedByQueuedWork = errors.New("worktree deletion is blocked by accepted Session work")

const queuedUserSubmissionBusyRetryDelay = 25 * time.Millisecond

var ErrReviewerActive = errors.New("Reviewer is active")

// CommandAcceptance serializes caller cancellation with a candidate mutation that reports whether it committed.
type CommandAcceptance func(commit func() (bool, error)) (bool, error)

func runCommandAcceptance(accept CommandAcceptance, commit func() (bool, error)) (bool, error) {
	if accept == nil {
		return commit()
	}
	return accept(commit)
}

func commandAcceptanceResult(committed bool, err error) error {
	if committed || err != nil {
		return err
	}
	return context.Canceled
}

func (e *Engine) RunWhenIdle(ctx context.Context, activeKind ActiveKind, fn func() error) error {
	if fn == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	return runExclusiveStepWhenIdle(ctx, e.stepLifecycle, activeKind, nil, func(context.Context, string) error {
		return fn()
	})
}

func runExclusiveStepWhenIdle(ctx context.Context, steps exclusiveStepLifecycle, activeKind ActiveKind, reservation *exclusiveStepReservation, fn func(context.Context, string) error) error {
	if steps == nil {
		return errors.New("exclusive step lifecycle is required")
	}
	if fn == nil {
		return nil
	}
	return steps.RunNext(ctx, exclusiveStepOptions{ActiveKind: activeKind, Reservation: reservation}, fn)
}

func (e *Engine) RunWhenIdleBeforeQueuedUserWork(ctx context.Context, activeKind ActiveKind, fn func() error) error {
	if fn == nil {
		return nil
	}
	e.pauseQueuedUserAutoDrain()
	defer e.resumeQueuedUserAutoDrain()
	return e.RunWhenIdle(ctx, activeKind, fn)
}

func (e *Engine) RunIfIdleBeforeQueuedUserWork(ctx context.Context, activeKind ActiveKind, fn func() error) (bool, error) {
	if fn == nil {
		return false, nil
	}
	e.ensureOrchestrationCollaborators()
	e.pauseQueuedUserAutoDrain()
	defer e.resumeQueuedUserAutoDrain()
	started := false
	err := e.stepLifecycle.Run(ctx, exclusiveStepOptions{ActiveKind: activeKind}, func(context.Context, string) error {
		started = true
		return fn()
	})
	return started, err
}

func (e *Engine) ScheduleWorktreeTransition(
	ctx context.Context,
	operationID clientui.WorktreeTransitionID,
	transition runtimeinput.PendingWorkWorktreeTransition,
	fn func(context.Context) error,
) (*worktreepb.ScheduledAcknowledgement, error) {
	return e.ScheduleWorktreeTransitionWithAcceptance(ctx, operationID, transition, nil, fn)
}

func (e *Engine) ScheduleWorktreeTransitionWithAcceptance(
	ctx context.Context,
	operationID clientui.WorktreeTransitionID,
	transition runtimeinput.PendingWorkWorktreeTransition,
	accept CommandAcceptance,
	fn func(context.Context) error,
) (*worktreepb.ScheduledAcknowledgement, error) {
	if fn == nil {
		return nil, errors.New("worktree transition executor is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	item, err := worktreePendingWorkItem(operationID, transition)
	if err != nil {
		return nil, err
	}
	err = e.scheduleOperationalPendingWork(ctx, operationalPendingWorkRequest{
		item:   item,
		accept: accept,
		run: func(pendingCtx context.Context, reservation *exclusiveStepReservation, pendingItem runtimeinput.PendingWorkItem) error {
			e.pauseQueuedUserAutoDrain()
			defer e.resumeQueuedUserAutoDrain()
			runErr := runExclusiveStepWhenIdle(
				pendingCtx,
				e.stepLifecycle,
				ActiveKindRuntimeMaintenance,
				reservation,
				func(stepCtx context.Context, _ string) error {
					runErr := fn(stepCtx)
					if worktreeFailureRequiresTechnicalRestoration(runErr) {
						runErr = errors.Join(runErr, e.publishPendingWorkTechnicalRestoration(pendingItem))
					}
					return runErr
				},
			)
			if worktreeFailureIsApplied(runErr) {
				e.surfaceRunError(runErr)
			}
			if worktreeFailureIsIndeterminate(runErr) {
				e.closeAdmissionAfterRuntimeAbort()
				e.FailQueuedUserMessages(QueuedUserMessageFailureRuntimeUnavailable)
			}
			return runErr
		},
	})
	if err != nil {
		return nil, err
	}
	return &worktreepb.ScheduledAcknowledgement{OperationId: operationID.String()}, nil
}

func (e *Engine) RunExecutionTargetTransition(ctx context.Context, onScheduled func(), fn func() error) error {
	if fn == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	reservation := &exclusiveStepReservation{
		Kind:      exclusiveStepReservationWorktreeTransition,
		queueable: true,
	}
	if err := e.stepLifecycle.AcquireReservation(reservation); err != nil {
		return err
	}
	defer e.stepLifecycle.ReleaseReservation(reservation)
	if onScheduled != nil {
		onScheduled()
	}
	e.pauseQueuedUserAutoDrain()
	defer e.resumeQueuedUserAutoDrain()
	return runExclusiveStepWhenIdle(
		ctx,
		e.stepLifecycle,
		ActiveKindRuntimeMaintenance,
		reservation,
		func(context.Context, string) error {
			return fn()
		},
	)
}

func (e *Engine) ApplyWorktreeTransitionTerminal(
	ctx context.Context,
	apply func(context.Context) error,
) error {
	if apply == nil {
		return errors.New("worktree transition terminal mutation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	deferred := submitEngineRuntimeOperation(e, func(operationCtx context.Context) (struct{}, error) {
		return struct{}{}, apply(operationCtx)
	})
	_, err := deferred.Await(context.WithoutCancel(ctx))
	return err
}

type worktreeSchedulingError struct{ technical, applied, indeterminate bool }

func classifyWorktreeSchedulingError(err error) worktreeSchedulingError {
	var indeterminate interface{ WorktreeTransitionIndeterminate() }
	if errors.As(err, &indeterminate) {
		return worktreeSchedulingError{indeterminate: true}
	}
	var applied interface{ WorktreeTransitionApplied() }
	if errors.As(err, &applied) {
		return worktreeSchedulingError{applied: true}
	}
	var technical interface{ WorktreeTechnicalFailure() }
	return worktreeSchedulingError{technical: errors.As(err, &technical)}
}

func worktreeFailureIsApplied(err error) bool {
	return classifyWorktreeSchedulingError(err).applied
}

func worktreeFailureIsIndeterminate(err error) bool {
	return classifyWorktreeSchedulingError(err).indeterminate
}

func worktreeFailureRequiresTechnicalRestoration(err error) bool {
	return classifyWorktreeSchedulingError(err).technical
}

// SubmitQueuedUserMessages starts a fresh step from already-queued injected user
// messages or background notices. This is used when a non-turn busy operation
// (for example manual compaction) completes while queued steering is waiting.
func (e *Engine) SubmitQueuedUserMessages(ctx context.Context) (assistant llm.Message, err error) {
	assistant, _, err = e.SubmitQueuedUserMessagesWithActiveHook(ctx, nil)
	e.surfaceRunError(err)
	return assistant, err
}

func (e *Engine) SubmitQueuedUserMessagesWithActiveHook(ctx context.Context, onActive func()) (assistant llm.Message, receipt session.CommitReceipt, err error) {
	assistant, receipt, _, err = e.submitQueuedUserMessages(ctx, nil, onActive)
	return
}

func (e *Engine) submitQueuedUserMessages(ctx context.Context, queueItemIDs map[string]struct{}, onActive func()) (assistant llm.Message, receipt session.CommitReceipt, consumedQueueItemIDs map[string]struct{}, err error) {
	e.ensureOrchestrationCollaborators()
	for {
		if e.failQueuedUserWorkIfTerminal() {
			return llm.Message{}, receipt, consumedQueueItemIDs, nil
		}
		if len(queueItemIDs) > 0 {
			if err := e.waitQueuedUserAutoDrainAllowed(ctx); err != nil {
				return llm.Message{}, receipt, consumedQueueItemIDs, err
			}
		}
		err = e.stepLifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			if onActive != nil {
				onActive()
			}
			if e.failQueuedUserWorkIfTerminal() {
				return nil
			}
			if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
				return err
			}
			var selection userInjectionSelection = allPendingUserInjectionSelection{}
			if len(queueItemIDs) > 0 {
				selection = steerUserInjections(queueItemIDs)
			}
			flushResult, err := e.flushPendingUserInjections(stepID, selection)
			if flushResult.receipt.Committed {
				receipt = flushResult.receipt
			}
			if err != nil {
				return err
			}
			consumedQueueItemIDs = flushResult.queueItemIDs
			if flushResult.disposition == userInjectionFlushStopped {
				return nil
			}
			if flushResult.flushed == 0 {
				return nil
			}
			msg, runErr := e.runStepLoopWithPendingUserInjectionObserver(stepCtx, stepID, func(flushReceipt session.CommitReceipt) {
				receipt = flushReceipt
			})
			assistant = msg
			return runErr
		})
		if receipt.Committed || !errors.Is(err, ErrAgentBusy) {
			return assistant, receipt, consumedQueueItemIDs, err
		}

		select {
		case <-ctx.Done():
			return llm.Message{}, receipt, consumedQueueItemIDs, ctx.Err()
		case <-time.After(queuedUserSubmissionBusyRetryDelay):
		}
	}
}

func (e *Engine) pauseQueuedUserAutoDrain() {
	e.queuedUserWorkMu.Lock()
	e.queuedUserWorkPauseCount++
	e.queuedUserWorkMu.Unlock()
}

func (e *Engine) resumeQueuedUserAutoDrain() {
	e.queuedUserWorkMu.Lock()
	if e.queuedUserWorkPauseCount > 0 {
		e.queuedUserWorkPauseCount--
	}
	e.queuedUserWorkMu.Unlock()
	e.scheduleQueuedUserInjectionsIfIdle()
}

func (e *Engine) waitQueuedUserAutoDrainAllowed(ctx context.Context) error {
	for {
		e.queuedUserWorkMu.Lock()
		paused := e.queuedUserWorkPauseCount > 0
		e.queuedUserWorkMu.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(queuedUserSubmissionBusyRetryDelay):
		}
	}
}

func (e *Engine) SubmitUserMessageOrSteerWithAcceptance(ctx context.Context, text string, accept CommandAcceptance) (result UserTurnResult, queued *QueuedUserMessage, err error) {
	if strings.TrimSpace(text) == "" {
		return UserTurnResult{}, nil, errors.New("empty message")
	}
	item, err := e.QueueUserMessageForAutoDrainWithAcceptance(ctx, text, accept)
	if err != nil {
		return UserTurnResult{}, nil, err
	}
	return UserTurnResult{}, &item, nil
}

func (e *Engine) QueueUserMessageForAutoDrain(ctx context.Context, text string) (QueuedUserMessage, error) {
	return e.queueUserMessage(ctx, text, true, nil)
}

func (e *Engine) QueueUserMessageForAutoDrainWithAcceptance(ctx context.Context, text string, accept CommandAcceptance) (QueuedUserMessage, error) {
	return e.queueUserMessage(ctx, text, true, accept)
}

func (e *Engine) HasQueuedUserWork() bool {
	e.ensureOrchestrationCollaborators()
	if e.messageFlow.HasPendingUserInjections() {
		return true
	}
	if e.backgroundFlow != nil && e.backgroundFlow.HasPendingNotices() {
		return true
	}
	return false
}

func (e *Engine) markQueuedUserInjectionForAutoDrain(queueItemID string) {
	queueItemID = strings.TrimSpace(queueItemID)
	if queueItemID == "" {
		return
	}
	e.queuedUserWorkMu.Lock()
	if e.queuedUserWorkAutoDrainIDs == nil {
		e.queuedUserWorkAutoDrainIDs = make(map[string]struct{})
	}
	e.queuedUserWorkAutoDrainIDs[queueItemID] = struct{}{}
	e.queuedUserWorkMu.Unlock()
}

func (e *Engine) unmarkQueuedUserInjectionForAutoDrain(queueItemIDs ...string) {
	e.queuedUserWorkMu.Lock()
	for _, queueItemID := range queueItemIDs {
		delete(e.queuedUserWorkAutoDrainIDs, strings.TrimSpace(queueItemID))
	}
	if len(e.queuedUserWorkAutoDrainIDs) == 0 {
		e.queuedUserWorkAutoDrainIDs = nil
	}
	e.queuedUserWorkMu.Unlock()
}

func (e *Engine) unmarkQueuedUserInjectionForAutoDrainSet(queueItemIDs map[string]struct{}) {
	if len(queueItemIDs) == 0 {
		return
	}
	ids := make([]string, 0, len(queueItemIDs))
	for queueItemID := range queueItemIDs {
		ids = append(ids, queueItemID)
	}
	e.unmarkQueuedUserInjectionForAutoDrain(ids...)
}

func (e *Engine) scheduleQueuedUserInjectionsIfIdle() bool {
	if e == nil {
		return false
	}
	e.ensureOrchestrationCollaborators()
	if e.stepLifecycle != nil && e.stepLifecycle.IsBusy() {
		return false
	}
	if !e.messageFlow.HasPendingUserInjections() {
		return false
	}
	if e.failQueuedUserWorkIfTerminal() {
		return false
	}
	e.queuedUserWorkMu.Lock()
	if len(e.queuedUserWorkAutoDrainIDs) == 0 {
		e.queuedUserWorkMu.Unlock()
		return false
	}
	if e.queuedUserWorkScheduled {
		e.queuedUserWorkMu.Unlock()
		return true
	}
	completion := newRuntimeDeferred[struct{}]()
	e.queuedUserWorkScheduled = true
	e.queuedUserWorkCompletion = completion
	e.queuedUserWorkMu.Unlock()
	if !e.launchLifecycleTask(func(ctx context.Context) error {
		return e.processQueuedUserWork(ctx, completion)
	}) {
		e.clearQueuedUserWorkScheduled(completion, ErrEngineClosed)
		return false
	}
	return true
}

func (e *Engine) processQueuedUserWork(
	ctx context.Context,
	completion runtimeDeferred[struct{}],
) (runtimeAbort *resultGroupFatal) {
	completed := false
	defer func() {
		e.clearQueuedUserWorkScheduled(completion, nil)
		if !completed {
			return
		}
		e.ensureOrchestrationCollaborators()
		if e.hasQueuedUserAutoDrainIDs() {
			e.scheduleQueuedUserInjectionsIfIdle()
		}
	}()
	if err := e.waitQueuedUserAutoDrainAllowed(ctx); err != nil {
		if fatal, abort := resultGroupFatalFromError(err); abort {
			return fatal
		}
		e.surfaceRunError(err)
		return nil
	}
	ids := e.queuedUserAutoDrainIDSnapshot()
	_, _, consumedQueueItemIDs, err := e.submitQueuedUserMessages(ctx, ids, nil)
	if err != nil {
		if fatal, abort := resultGroupFatalFromError(err); abort {
			return fatal
		}
		e.surfaceRunError(err)
		return nil
	}
	e.completeLiveRunQueueItems(consumedQueueItemIDs)
	completed = true
	return nil
}

func (e *Engine) HasScheduledQueuedUserWork() bool {
	if e == nil {
		return false
	}
	e.queuedUserWorkMu.Lock()
	defer e.queuedUserWorkMu.Unlock()
	return e.queuedUserWorkScheduled
}

func (e *Engine) WaitForScheduledQueuedUserWork(ctx context.Context) error {
	if e == nil {
		return ErrEngineClosed
	}
	e.queuedUserWorkMu.Lock()
	if !e.queuedUserWorkScheduled {
		e.queuedUserWorkMu.Unlock()
		return nil
	}
	completion := e.queuedUserWorkCompletion
	e.queuedUserWorkMu.Unlock()
	_, err := completion.Await(ctx)
	return err
}

func (e *Engine) clearQueuedUserWorkScheduled(
	completion runtimeDeferred[struct{}],
	err error,
) {
	e.queuedUserWorkMu.Lock()
	if e.queuedUserWorkCompletion.state != completion.state {
		e.queuedUserWorkMu.Unlock()
		completion.complete(struct{}{}, err)
		return
	}
	e.queuedUserWorkScheduled = false
	e.queuedUserWorkCompletion = runtimeDeferred[struct{}]{}
	e.queuedUserWorkMu.Unlock()
	completion.complete(struct{}{}, err)
}

func (e *Engine) hasQueuedUserAutoDrainIDs() bool {
	e.queuedUserWorkMu.Lock()
	defer e.queuedUserWorkMu.Unlock()
	return len(e.queuedUserWorkAutoDrainIDs) > 0
}

func (e *Engine) queuedUserAutoDrainIDSnapshot() map[string]struct{} {
	e.queuedUserWorkMu.Lock()
	defer e.queuedUserWorkMu.Unlock()
	return cloneMapIfNonEmpty(e.queuedUserWorkAutoDrainIDs)
}

func cloneMapIfNonEmpty[M ~map[K]V, K comparable, V any](in M) M {
	if len(in) == 0 {
		return nil
	}
	return maps.Clone(in)
}

func (e *Engine) DrainQueuedUserMessagesBeforeClose(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if e.failQueuedUserWorkIfTerminal() {
		return nil
	}
	if !e.HasQueuedUserWork() {
		return nil
	}
	_, err := e.SubmitQueuedUserMessages(ctx)
	if err != nil {
		if e.failQueuedUserWorkIfTerminal() {
			return nil
		}
		if !e.HasQueuedUserWork() {
			return err
		}
		e.FailQueuedUserMessages(QueuedUserMessageFailureClosing)
		return err
	}
	return nil
}
