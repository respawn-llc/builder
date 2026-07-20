package app

import "core/shared/clientui"

func tuiSupportsAttentionNotification(notification clientui.AttentionNotification) bool {
	switch notification.Kind {
	case clientui.AttentionNotificationKindQuestion, clientui.AttentionNotificationKindApproval:
		return true
	default:
		return false
	}
}

func normalizeTranscriptPromptActivation(prompt clientui.TranscriptPrompt) attentionStreamOutcome {
	if prompt.State != clientui.TranscriptPromptStatePending {
		return attentionStreamDiscontinuity{reason: attentionStreamDiscontinuityInvalidEvent}
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
		notification.Approval = &clientui.AttentionNotificationApprovalState{Message: prompt.Question}
	} else {
		promptID := string(prompt.PromptID)
		notification.Question = &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{promptID},
			MaterializedAskIDs:      []string{promptID},
			CurrentUnresolvedAskIDs: []string{promptID},
			Preview:                 prompt.Question,
			DisplayCount:            1,
			MaterializedCount:       1,
		}
	}
	return normalizePendingAttentionNotification(notification)
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
	m.ask.activeProjection.pendingActivationPreview = nil
	if m.promptAttention == nil {
		return
	}
	m.reduceAcceptedAttentionEventWithNative(
		normalizeTranscriptPromptActivation(m.ask.current.prompt),
		m.promptAttention,
	)
}
