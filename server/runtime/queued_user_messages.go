package runtime

import (
	"context"
	"fmt"

	"core/server/llm"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"errors"
	"strings"
)

var errInvalidQueuedUserMessage = errors.New("queued message requires a role and content")

type queuedUserMessage struct {
	message QueuedUserMessage
	agenda  *humanBoundaryAgendaItem
}

func (m QueuedUserMessage) DisplayText() (string, error) {
	if m.Message.Content == nil {
		return "", errInvalidQueuedUserMessage
	}
	return *m.Message.Content, nil
}

func queuedUserMessageWithID(id, text, clientRequestID string) QueuedUserMessage {
	return QueuedUserMessage{
		ID:              strings.TrimSpace(id),
		ClientRequestID: strings.TrimSpace(clientRequestID),
		Message:         llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)},
	}
}

func newQueuedUserMessage(
	message llm.Message,
	clientRequestID string,
) (QueuedUserMessage, error) {
	item := QueuedUserMessage{
		ID:              runtimeids.NewQueueItemID().String(),
		ClientRequestID: strings.TrimSpace(clientRequestID),
		Message:         message,
	}
	text, err := item.DisplayText()
	if err != nil || item.Message.Role == "" || strings.TrimSpace(text) == "" {
		return QueuedUserMessage{}, errInvalidQueuedUserMessage
	}
	return item, nil
}

func normalizeQueuedUserMessage(item QueuedUserMessage) (QueuedUserMessage, error) {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = runtimeids.NewQueueItemID().String()
	}
	if _, err := runtimeids.ParseQueueItemID(item.ID); err != nil {
		return QueuedUserMessage{}, fmt.Errorf("Queue Item ID: %w", err)
	}
	item.ClientRequestID = strings.TrimSpace(item.ClientRequestID)
	text, err := item.DisplayText()
	if err != nil || item.Message.Role == "" || strings.TrimSpace(text) == "" {
		return QueuedUserMessage{}, errInvalidQueuedUserMessage
	}
	return item, nil
}

func (e *Engine) acceptHumanAgendaItem(
	item QueuedUserMessage,
	eligibility boundaryEligibility,
	requireActiveScope bool,
) (QueuedUserMessage, error) {
	item, err := normalizeQueuedUserMessage(item)
	if err != nil {
		return QueuedUserMessage{}, err
	}
	return submitRuntimeEvent(
		e,
		item,
		func(admission runtimeEventAdmission, accepted QueuedUserMessage) (QueuedUserMessage, error) {
			binding, effectiveEligibility, bindingErr := e.humanAgendaBinding(
				eligibility,
				requireActiveScope,
			)
			if bindingErr != nil {
				return QueuedUserMessage{}, bindingErr
			}
			if err := e.boundaryAgenda.acceptHuman(
				accepted,
				binding,
				effectiveEligibility,
				func(settlement error) {
					e.settleHumanAgendaItem(accepted, settlement)
				},
			); err != nil {
				return QueuedUserMessage{}, err
			}
			e.emitQueuedUserMessageStatus(accepted, QueuedUserMessageAccepted, "", false)
			if _, runtimeBound := binding.(runtimeAgendaBinding); runtimeBound {
				if err := e.reduceIdleBoundary(admission); err != nil {
					return QueuedUserMessage{}, err
				}
			}
			return accepted, nil
		},
	)
}

func (e *Engine) startRuntimeBoundHumanExecution(admission runtimeEventAdmission) error {
	launcher, ok := e.cfg.StepLifecycle.(RuntimeBoundHumanExecutionLauncher)
	if !ok || e.runtimeEvents == nil {
		return nil
	}
	return admission.startWork(func(workCtx context.Context) {
		execution, registerErr := launcher.RegisterRuntimeBoundHumanExecution(workCtx)
		if registerErr != nil {
			e.surfaceRunError(registerErr)
			e.failIdleHumanAgendaItems(registerErr)
			return
		}
		if execution == nil {
			return
		}
		if launchErr := execution.Launch(workCtx); launchErr != nil {
			e.surfaceRunError(launchErr)
			e.failIdleHumanAgendaItems(launchErr)
		}
	})
}

func (e *Engine) AgentExecutionScopeReleased(scopeID runtimeids.ExecutionScopeID) error {
	if e == nil || scopeID.IsZero() {
		return nil
	}
	_, err := submitRuntimeEvent(
		e,
		scopeID,
		func(admission runtimeEventAdmission, released runtimeids.ExecutionScopeID) (struct{}, error) {
			e.invalidateAgentStepScope(released, errBoundaryScopeFinalized)
			return struct{}{}, e.reduceIdleBoundary(admission)
		},
	)
	return err
}

func (e *Engine) failIdleHumanAgendaItems(cause error) {
	_, err := submitRuntimeEvent(
		e,
		cause,
		func(_ runtimeEventAdmission, failure error) (struct{}, error) {
			for _, item := range e.boundaryAgenda.selectHumanItems(idleBoundarySelection()) {
				item.settleBoundaryAgenda(failure)
			}
			return struct{}{}, nil
		},
	)
	if err != nil && !errors.Is(err, ErrEngineClosed) {
		e.surfaceRunError(err)
	}
}

func (e *Engine) humanAgendaBinding(
	eligibility boundaryEligibility,
	requireActiveScope bool,
) (boundaryAgendaBinding, boundaryEligibility, error) {
	current := e.agentSteps.current
	if current == nil {
		current = e.agentSteps.boundary
	}
	if current != nil {
		if requireActiveScope {
			if lifecycle, ok := e.cfg.StepLifecycle.(AgentStepScopeLifecycle); ok &&
				!lifecycle.AgentStepScopeLive(e.lifecycleCtx, current.scopeID) {
				return nil, 0, ErrNoActiveLiveRun
			}
		}
		return scopeBoundaryBinding(current.scopeID, current.origin), eligibility, nil
	}
	if requireActiveScope {
		return nil, 0, ErrNoActiveLiveRun
	}
	return runtimeBoundaryBinding(), boundaryEligibilityIdle, nil
}

func (e *Engine) settleHumanAgendaItem(item QueuedUserMessage, settlement error) {
	reason := QueuedUserMessageFailureRuntimeUnavailable
	switch {
	case errors.Is(settlement, errBoundaryRuntimeClosed):
		reason = QueuedUserMessageFailureClosing
	case errors.Is(settlement, errBoundaryScopeStopped):
		reason = QueuedUserMessageFailureStopped
	}
	e.emitQueuedUserMessageStatus(item, QueuedUserMessageFailed, reason, true)
}

type humanBoundaryApplyResult struct {
	applied int
	receipt session.CommitReceipt
}

func (e *Engine) applyHumanBoundary(
	admission runtimeEventAdmission,
	stepID string,
	selection boundarySelection,
) (humanBoundaryApplyResult, error) {
	if !e.humanBoundarySelectionLive(selection) {
		return humanBoundaryApplyResult{}, ErrNoActiveLiveRun
	}
	return e.applyHumanBoundaryItems(
		admission,
		stepID,
		e.boundaryAgenda.selectHumanItems(selection),
	)
}

func (e *Engine) applyHumanBoundaryPrefix(
	admission runtimeEventAdmission,
	stepID string,
	selection boundarySelection,
) (humanBoundaryApplyResult, error) {
	if !e.humanBoundarySelectionLive(selection) {
		return humanBoundaryApplyResult{}, ErrNoActiveLiveRun
	}
	return e.applyHumanBoundaryItems(
		admission,
		stepID,
		e.boundaryAgenda.selectHumanPrefix(selection),
	)
}

func (e *Engine) humanBoundarySelectionLive(selection boundarySelection) bool {
	var scopeID runtimeids.ExecutionScopeID
	switch selected := selection.(type) {
	case scopeStepBoundarySelection:
		scopeID = selected.scopeID
	case scopeTurnBoundarySelection:
		scopeID = selected.scopeID
	default:
		return true
	}
	lifecycle, ok := e.cfg.StepLifecycle.(AgentStepScopeLifecycle)
	return !ok || lifecycle.AgentStepScopeLive(e.lifecycleCtx, scopeID)
}

func (e *Engine) applyHumanBoundaryItems(
	admission runtimeEventAdmission,
	stepID string,
	selected []*humanBoundaryAgendaItem,
) (humanBoundaryApplyResult, error) {
	if len(selected) == 0 {
		return humanBoundaryApplyResult{}, nil
	}
	pending := make([]queuedUserMessage, 0, len(selected))
	for _, item := range selected {
		pending = append(pending, queuedUserMessage{message: item.message, agenda: item})
	}
	groups, err := queuedUserMessageFlushGroups(pending)
	if err != nil {
		e.restoreHumanAgendaItems(selected)
		return humanBoundaryApplyResult{}, err
	}
	result := humanBoundaryApplyResult{}
	for groupIndex, group := range groups {
		receipt := session.CommitReceipt{}
		intent := steerQueuedUserMessageFlushIntent(group.message, group.batch, group.queueItems)
		intent.items[0].commitReceipt = &receipt
		applyErr := admission.applySteering(stepID, intent)
		if receipt.Committed {
			result.receipt = receipt
		}
		if applyErr != nil {
			restoreFrom := groupIndex
			if receipt.Committed {
				result.applied += len(group.pending)
				restoreFrom++
			}
			if restoreFrom < len(groups) {
				e.restoreHumanAgendaItems(humanAgendaTail(groups[restoreFrom:]))
			}
			return result, applyErr
		}
		result.applied += len(group.pending)
	}
	return result, nil
}

func humanAgendaTail(groups []queuedUserMessageFlushGroup) []*humanBoundaryAgendaItem {
	var items []*humanBoundaryAgendaItem
	for _, group := range groups {
		for _, pending := range group.pending {
			if pending.agenda == nil {
				panic(fmt.Sprintf(
					"selected human Queue Item %q lost its canonical Boundary Agenda item",
					pending.message.ID,
				))
			}
			items = append(items, pending.agenda)
		}
	}
	return items
}

func (e *Engine) restoreHumanAgendaItems(items []*humanBoundaryAgendaItem) {
	e.boundaryAgenda.restoreHumanFront(items)
}
