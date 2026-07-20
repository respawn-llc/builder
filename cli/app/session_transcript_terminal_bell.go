package app

import (
	"core/shared/clientui"
	"core/shared/transcript"
)

func (h *nativeTurnNotificationObserver) OnTranscriptMessage(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageAssistantDelta:
		if delta := message.Payload.AssistantDelta; delta != nil && isNoopFinalText(delta.Delta) {
			h.clearPendingTurnCompletionForSilentFinal(delta.StepID)
		}
	case clientui.TranscriptMessageToolStart:
		if tool := message.Payload.ToolStart; tool != nil {
			h.recordToolCall(tool.StepID)
		}
	case clientui.TranscriptMessageReviewerState:
		if reviewer := message.Payload.ReviewerState; reviewer != nil {
			h.recordReviewerState(*reviewer)
		}
	case clientui.TranscriptMessageStepState:
		if step := message.Payload.StepState; step != nil && step.Lifecycle == clientui.StepLifecycleFinished {
			h.recordStepFinished(step.StepID)
		}
	case clientui.TranscriptMessageCommittedRow:
		row := message.Payload.CommittedRow
		if row == nil || row.Kind != clientui.TranscriptRowAssistant || row.Assistant == nil {
			return
		}
		switch row.Assistant.Phase {
		case transcript.AssistantPhaseFinal:
			h.recordTurnCompletion(row.Assistant.StepID, row.Assistant.Text)
		}
	}
}
