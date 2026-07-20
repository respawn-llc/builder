package app

import "core/shared/clientui"

type promptAttentionSink interface {
	onAttentionNotification(clientui.AttentionNotificationEvent, *string)
}

type acceptedAttentionFanout struct {
	native    promptAttentionSink
	observers []func(clientui.AttentionNotificationEvent)
}

func newAcceptedAttentionFanout(
	native promptAttentionSink,
	observers ...func(clientui.AttentionNotificationEvent),
) *acceptedAttentionFanout {
	return &acceptedAttentionFanout{
		native:    native,
		observers: append([]func(clientui.AttentionNotificationEvent){}, observers...),
	}
}

func (f *acceptedAttentionFanout) onAttentionNotification(
	event clientui.AttentionNotificationEvent,
	projectedPreview *string,
) {
	if f == nil || !tuiAcceptsAttentionNotification(event) {
		return
	}
	if f.native != nil {
		f.native.onAttentionNotification(event, projectedPreview)
	}
	for _, observe := range f.observers {
		if observe != nil {
			observe(event)
		}
	}
}

func tuiAcceptsAttentionNotification(event clientui.AttentionNotificationEvent) bool {
	return event.Type == clientui.AttentionNotificationEventPending &&
		event.Pending != nil &&
		tuiSupportsAttentionNotification(*event.Pending)
}

func tuiSupportsAttentionNotification(notification clientui.AttentionNotification) bool {
	switch notification.Kind {
	case clientui.AttentionNotificationKindQuestion, clientui.AttentionNotificationKindApproval:
		return true
	default:
		return false
	}
}

func notifyTranscriptPromptActivation(hook promptAttentionSink, prompt clientui.TranscriptPrompt, projectedPreview string) {
	if hook == nil || prompt.State != clientui.TranscriptPromptStatePending {
		return
	}
	kind := clientui.AttentionNotificationKindQuestion
	if prompt.Kind == clientui.TranscriptPromptKindApproval {
		kind = clientui.AttentionNotificationKindApproval
	}
	notification := clientui.AttentionNotification{
		ID: clientui.AttentionNotificationID{
			Kind: kind,
			UUID: string(prompt.PromptID),
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
		notification.Approval = &clientui.AttentionNotificationApprovalState{Message: projectedPreview}
	} else {
		promptID := string(prompt.PromptID)
		notification.Question = &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{promptID},
			MaterializedAskIDs:      []string{promptID},
			CurrentUnresolvedAskIDs: []string{promptID},
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
