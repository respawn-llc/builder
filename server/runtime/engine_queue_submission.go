package runtime

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
)

const queuedUserSubmissionBusyRetryDelay = 25 * time.Millisecond

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
			flushResult, err := e.flushPendingUserInjections(stepID, allPendingUserInjectionSelection{})
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

func (e *Engine) SubmitUserMessageOrSteer(ctx context.Context, text string, clientRequestID string) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	return e.SubmitUserMessageOrSteerWithAcceptedHook(ctx, text, clientRequestID, nil)
}

func (e *Engine) SubmitUserMessageOrSteerWithAcceptedHook(ctx context.Context, text string, clientRequestID string, onAccepted func(queued bool)) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	return e.SubmitUserMessageOrSteerWithHooks(ctx, text, clientRequestID, nil, onAccepted)
}

func (e *Engine) SubmitUserMessageOrSteerWithHooks(ctx context.Context, text string, clientRequestID string, onActive func(), onAccepted func(queued bool)) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	result, queued, err := e.SubmitUserMessageOrSteerWithOutcomeHooks(ctx, text, clientRequestID, onActive, onAccepted)
	if result.FinalAnswer != nil {
		assistant = *result.FinalAnswer
	}
	return assistant, queued, err
}

func (e *Engine) SubmitUserMessageOrSteerWithOutcomeHooks(ctx context.Context, text string, clientRequestID string, onActive func(), onAccepted func(queued bool)) (result UserTurnResult, queued *QueuedUserMessage, err error) {
	return e.submitUserMessageOrSteerWithOutcome(ctx, text, clientRequestID, onActive, onAccepted, nil)
}

func (e *Engine) SubmitUserMessageOrSteerWithAcceptance(ctx context.Context, text string, clientRequestID string, onActive func(), accept CommandAcceptance) (result UserTurnResult, queued *QueuedUserMessage, err error) {
	return e.submitUserMessageOrSteerWithOutcome(ctx, text, clientRequestID, onActive, nil, accept)
}

func (e *Engine) submitUserMessageOrSteerWithOutcome(ctx context.Context, text string, clientRequestID string, onActive func(), onAccepted func(queued bool), accept CommandAcceptance) (result UserTurnResult, queued *QueuedUserMessage, err error) {
	if strings.TrimSpace(text) == "" {
		return UserTurnResult{}, nil, errors.New("empty message")
	}
	result, err = e.submitUserMessageWithOutcome(ctx, text, onActive, func() {
		if onAccepted != nil {
			onAccepted(false)
		}
	}, accept)
	if errors.Is(err, ErrAgentBusy) {
		item, queueErr := e.QueueUserMessageForAutoDrainWithAcceptance(text, clientRequestID, accept)
		if queueErr != nil {
			return UserTurnResult{}, nil, queueErr
		}
		if onAccepted != nil {
			onAccepted(true)
		}
		return UserTurnResult{}, &item, nil
	}
	return result, nil, err
}

func (e *Engine) QueueUserMessageForAutoDrain(text string, clientRequestID string) (QueuedUserMessage, error) {
	return e.queueUserMessageWithClientRequestID(text, clientRequestID, true, nil)
}

func (e *Engine) QueueUserMessageForAutoDrainWithAcceptance(text string, clientRequestID string, accept CommandAcceptance) (QueuedUserMessage, error) {
	return e.queueUserMessageWithClientRequestID(text, clientRequestID, true, accept)
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
	if e.queuedUserWorkScheduled {
		e.queuedUserWorkMu.Unlock()
		return true
	}
	e.queuedUserWorkScheduled = true
	e.queuedUserWorkMu.Unlock()
	if !e.launchLifecycleTask(e.processQueuedUserWork) {
		e.clearQueuedUserWorkScheduled()
		return false
	}
	return true
}

func (e *Engine) processQueuedUserWork(ctx context.Context) (runtimeAbort *resultGroupFatal) {
	completed := false
	defer func() {
		e.clearQueuedUserWorkScheduled()
		if !completed {
			return
		}
		e.ensureOrchestrationCollaborators()
		if e.messageFlow.HasPendingUserInjections() {
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

func (e *Engine) clearQueuedUserWorkScheduled() {
	e.queuedUserWorkMu.Lock()
	e.queuedUserWorkScheduled = false
	e.queuedUserWorkMu.Unlock()
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
