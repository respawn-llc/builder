package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/llm"
)

const queuedUserSubmissionBusyRetryDelay = 25 * time.Millisecond

func (e *Engine) RunWhenIdle(ctx context.Context, fn func() error) error {
	if fn == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	for {
		err := e.stepLifecycle.Run(ctx, exclusiveStepOptions{}, func(context.Context, string) error {
			return fn()
		})
		if !errors.Is(err, errExclusiveStepBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(queuedUserSubmissionBusyRetryDelay):
		}
	}
}

// SubmitQueuedUserMessages starts a fresh step from already-queued injected user
// messages or background notices. This is used when a non-turn busy operation
// (for example manual compaction) completes while queued steering is waiting.
func (e *Engine) SubmitQueuedUserMessages(ctx context.Context) (assistant llm.Message, err error) {
	return e.submitQueuedUserMessages(ctx, nil)
}

func (e *Engine) submitQueuedUserMessages(ctx context.Context, queueItemIDs map[string]struct{}) (assistant llm.Message, err error) {
	e.ensureOrchestrationCollaborators()
	for {
		if e.failQueuedUserWorkIfTerminal() {
			return llm.Message{}, nil
		}
		err = e.stepLifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, PersistRunLifecycle: true}, func(stepCtx context.Context, stepID string) error {
			if e.failQueuedUserWorkIfTerminal() {
				return nil
			}
			if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
				return err
			}
			flushed, err := e.flushPendingUserInjections(stepID, queueItemIDs)
			if err != nil {
				return err
			}
			if flushed == 0 {
				return nil
			}
			msg, runErr := e.runStepLoopWithPendingUserInjectionIDs(stepCtx, stepID, queueItemIDs)
			assistant = msg
			return runErr
		})
		if !errors.Is(err, errExclusiveStepBusy) {
			return assistant, err
		}

		select {
		case <-ctx.Done():
			return llm.Message{}, ctx.Err()
		case <-time.After(queuedUserSubmissionBusyRetryDelay):
		}
	}
}

func (e *Engine) SubmitUserMessageOrSteer(ctx context.Context, text string, clientRequestID string) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	if strings.TrimSpace(text) == "" {
		return llm.Message{}, nil, errors.New("empty message")
	}
	msg, err := e.SubmitUserMessage(ctx, text)
	if errors.Is(err, errExclusiveStepBusy) {
		item := e.QueueUserMessageWithClientRequestID(text, clientRequestID)
		return llm.Message{}, &item, nil
	}
	return msg, nil, err
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
	if len(e.queuedUserWorkAutoDrainIDs) == 0 {
		e.queuedUserWorkMu.Unlock()
		return false
	}
	e.queuedUserWorkScheduled = true
	e.queuedUserWorkMu.Unlock()
	if !e.launchLifecycleTask(e.processQueuedUserWork) {
		e.clearQueuedUserWorkScheduled()
		return false
	}
	return true
}

func (e *Engine) processQueuedUserWork(ctx context.Context) {
	completed := false
	defer func() {
		e.clearQueuedUserWorkScheduled()
		if !completed {
			return
		}
		e.ensureOrchestrationCollaborators()
		if e.messageFlow.HasPendingUserInjections() && e.hasQueuedUserAutoDrainIDs() {
			e.scheduleQueuedUserInjectionsIfIdle()
		}
	}()
	if !e.hasQueuedUserAutoDrainIDs() {
		if e.backgroundFlow != nil {
			e.backgroundFlow.ScheduleIfIdle()
		}
		return
	}
	ids := e.queuedUserAutoDrainIDSnapshot()
	if _, err := e.submitQueuedUserMessages(ctx, ids); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		e.AppendCommittedEntry("error", fmt.Sprintf("queued steering continuation failed: %v", err))
		return
	}
	completed = true
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
	if len(e.queuedUserWorkAutoDrainIDs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(e.queuedUserWorkAutoDrainIDs))
	for id := range e.queuedUserWorkAutoDrainIDs {
		out[id] = struct{}{}
	}
	return out
}

// pushActiveUserInjectionScope records the queued user-injection IDs the in-flight
// step (and its nested reviewer follow-up) should flush, returning a restore func
// that reinstates the prior scope. The step executor reads this scope so reviewer
// follow-ups inherit it without the supervisor carrying injection IDs.
func (e *Engine) pushActiveUserInjectionScope(ids map[string]struct{}) func() {
	e.userInjectionScopeMu.Lock()
	previous := e.activeUserInjectionScope
	e.activeUserInjectionScope = cloneStringSet(ids)
	e.userInjectionScopeMu.Unlock()
	return func() {
		e.userInjectionScopeMu.Lock()
		e.activeUserInjectionScope = previous
		e.userInjectionScopeMu.Unlock()
	}
}

func (e *Engine) activeUserInjectionScopeSnapshot() map[string]struct{} {
	if e == nil {
		return nil
	}
	e.userInjectionScopeMu.Lock()
	defer e.userInjectionScopeMu.Unlock()
	return cloneStringSet(e.activeUserInjectionScope)
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for item := range in {
		out[item] = struct{}{}
	}
	return out
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
		e.FailQueuedUserMessages(QueuedUserMessageFailureClosing)
		return err
	}
	return nil
}
