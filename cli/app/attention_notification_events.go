package app

import (
	"core/shared/clientui"
	"core/shared/textutil"
)

type promptAttentionSink interface {
	onAttentionNotification(clientui.AttentionNotificationEvent, *string)
}

func tuiAcceptsAttentionNotification(event clientui.AttentionNotificationEvent) bool {
	return event.Type == clientui.AttentionNotificationEventPending &&
		event.Pending != nil &&
		tuiSupportsAttentionNotification(*event.Pending)
}

func tuiSupportsAttentionNotification(notification clientui.AttentionNotification) bool {
	switch notification.Kind {
	case clientui.AttentionNotificationKindQuestion,
		clientui.AttentionNotificationKindApproval,
		clientui.AttentionNotificationKindWorkflowApproval:
		return true
	default:
		return false
	}
}

func notifyTranscriptPromptActivation(hook promptAttentionSink, prompt clientui.TranscriptPrompt, projectedPreview string) {
	if hook == nil || prompt.Status != clientui.TranscriptPromptStatusPending {
		return
	}
	kind := clientui.AttentionNotificationKindQuestion
	if prompt.Kind == clientui.TranscriptPromptKindApproval {
		kind = clientui.AttentionNotificationKindApproval
	}
	notification := clientui.AttentionNotification{
		ID: clientui.AttentionNotificationID{
			Kind: kind,
			UUID: string(prompt.ToolCallID),
		},
		Kind:       kind,
		OccurredAt: prompt.CreatedAt,
		Revision:   1,
		Target: clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetSessionPrompt,
			SessionID: prompt.SessionID.String(),
		},
	}
	if prompt.Kind == clientui.TranscriptPromptKindApproval {
		notification.Approval = &clientui.AttentionNotificationApprovalState{Message: textutil.Value(projectedPreview)}
	} else {
		toolCallID := string(prompt.ToolCallID)
		notification.Question = &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{toolCallID},
			MaterializedAskIDs:      []string{toolCallID},
			CurrentUnresolvedAskIDs: []string{toolCallID},
			Preview:                 projectedPreview,
			DisplayCount:            1,
			MaterializedCount:       1,
		}
	}
	hook.onAttentionNotification(clientui.AttentionNotificationEvent{
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}, &projectedPreview)
}

func (m *uiModel) notifyPendingTranscriptPromptActivation() {
	if m == nil ||
		m.surface() != uiSurfaceOngoingTranscript ||
		m.inputMode() != uiInputModeAsk ||
		m.ask.current == nil ||
		m.ask.activeProjection == nil ||
		m.ask.activeProjection.pendingActivationPreview == nil {
		return
	}
	preview := *m.ask.activeProjection.pendingActivationPreview
	m.ask.activeProjection.pendingActivationPreview = nil
	notifyTranscriptPromptActivation(m.promptAttention, m.ask.current.prompt, preview)
}
