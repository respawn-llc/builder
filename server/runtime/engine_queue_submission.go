package runtime

import (
	"context"
	"errors"
	"maps"
	"strings"

	"core/server/llm"
	"core/server/session"
)

func (e *Engine) RunWhenIdle(ctx context.Context, activeKind ActiveKind, fn func() error) error {
	if fn == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	return runExclusiveStepWhenIdle(ctx, e.stepLifecycle, activeKind, func(context.Context, string) error {
		return fn()
	})
}

func runExclusiveStepWhenIdle(
	ctx context.Context,
	steps exclusiveStepLifecycle,
	activeKind ActiveKind,
	fn func(context.Context, string) error,
) error {
	if steps == nil {
		return errors.New("exclusive step lifecycle is required")
	}
	if fn == nil {
		return nil
	}
	return steps.RunNext(ctx, exclusiveStepOptions{ActiveKind: activeKind}, fn)
}

func (e *Engine) SubmitQueuedUserMessages(ctx context.Context) (assistant llm.Message, err error) {
	assistant, _, err = e.SubmitQueuedUserMessagesWithActiveHook(ctx, nil)
	e.surfaceRunError(err)
	return assistant, err
}

func (e *Engine) SubmitQueuedUserMessagesWithActiveHook(
	ctx context.Context,
	onActive func(),
) (assistant llm.Message, receipt session.CommitReceipt, err error) {
	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.Run(
		ctx,
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
		func(stepCtx context.Context, stepID string) error {
			if onActive != nil {
				onActive()
			}
			if e.failQueuedUserWorkIfTerminal() {
				return nil
			}
			if len(e.boundaryAgenda.pendingHuman()) == 0 {
				return nil
			}
			if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
				return err
			}
			applied, reduceErr := submitRuntimeEvent(
				e,
				stepID,
				func(admission runtimeEventAdmission, activeStepID string) (humanBoundaryApplyResult, error) {
					applied, applyErr := e.applyHumanBoundary(admission, activeStepID, idleBoundarySelection())
					if applyErr != nil || applied.applied == 0 {
						return applied, applyErr
					}
					snapshot := e.stepLifecycle.Snapshot()
					if snapshot == nil {
						return applied, ErrActiveStepInactive
					}
					_, registerErr := e.registerAgentProviderStep(admission, snapshot.RunID, false)
					return applied, registerErr
				},
			)
			if applied.receipt.Committed {
				receipt = applied.receipt
			}
			if reduceErr != nil || applied.applied == 0 {
				return errors.Join(reduceErr, e.failAgentStepScope(reduceErr))
			}
			msg, runErr := e.runStepLoopWithPendingUserInjectionObserver(
				stepCtx,
				stepID,
				func(flushReceipt session.CommitReceipt) {
					receipt = flushReceipt
				},
			)
			assistant = msg
			return runErr
		},
	)
	return assistant, receipt, err
}

func (e *Engine) SubmitUserMessageOrSteer(
	ctx context.Context,
	text string,
) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	return e.SubmitUserMessageOrSteerWithAcceptedHook(ctx, text, nil)
}

func (e *Engine) SubmitUserMessageOrSteerWithAcceptedHook(
	ctx context.Context,
	text string,
	onAccepted func(queued bool),
) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	return e.SubmitUserMessageOrSteerWithHooks(ctx, text, nil, onAccepted)
}

func (e *Engine) SubmitUserMessageOrSteerWithHooks(
	ctx context.Context,
	text string,
	onActive func(),
	onAccepted func(queued bool),
) (assistant llm.Message, queued *QueuedUserMessage, err error) {
	if strings.TrimSpace(text) == "" {
		return llm.Message{}, nil, errors.New("empty message")
	}
	msg, err := e.SubmitUserMessageWithHooks(ctx, text, onActive, func() {
		if onAccepted != nil {
			onAccepted(false)
		}
	})
	if errors.Is(err, ErrAgentBusy) {
		item, queueErr := newQueuedUserMessage(llm.Message{Role: llm.RoleUser, Content: &text})
		if queueErr != nil {
			return llm.Message{}, nil, queueErr
		}
		item, queueErr = e.acceptHumanAgendaItem(item, boundaryEligibilityStep, true)
		if queueErr != nil {
			return llm.Message{}, nil, queueErr
		}
		if onAccepted != nil {
			onAccepted(true)
		}
		return llm.Message{}, &item, nil
	}
	return msg, nil, err
}

func (e *Engine) HasQueuedUserWork() bool {
	e.ensureOrchestrationCollaborators()
	return len(e.boundaryAgenda.pendingHuman()) > 0
}

func cloneMapIfNonEmpty[M ~map[K]V, K comparable, V any](in M) M {
	if len(in) == 0 {
		return nil
	}
	return maps.Clone(in)
}

func (e *Engine) activeStepWasInterrupted() bool {
	lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle)
	return ok && lifecycle.activeStepWasInterrupted()
}
