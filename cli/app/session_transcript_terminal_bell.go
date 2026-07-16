package app

import (
	"core/shared/clientui"
	"core/shared/transcript"
)

func (h *bellHooks) OnTranscriptMessage(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageAssistantDelta:
		if delta := message.Payload.AssistantDelta; delta != nil && isNoopFinalText(delta.Delta) {
			h.clearPendingTurnCompletionForSilentFinal(delta.StepID.String())
		}
	case clientui.TranscriptMessageToolStart:
		if tool := message.Payload.ToolStart; tool != nil {
			h.recordToolCall(tool.StepID.String())
		}
	case clientui.TranscriptMessageCommittedRow:
		row := message.Payload.CommittedRow
		if row == nil || row.Kind != clientui.TranscriptRowAssistant || row.Assistant == nil {
			return
		}
		switch row.Assistant.Phase {
		case transcript.AssistantPhaseFinal:
			h.recordTurnCompletion(row.Assistant.StepID.String(), row.Assistant.Text)
		}
	}
}
