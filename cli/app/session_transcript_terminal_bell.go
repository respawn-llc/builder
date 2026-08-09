package app

import (
	"core/shared/clientui"
	"core/shared/transcript"
)

func (h *bellHooks) OnTranscriptMessage(message clientui.TranscriptMessage) {
	switch message.Kind() {
	case clientui.TranscriptMessageAssistantDelta:
		_ = message.Payload().(clientui.TranscriptAssistantDelta)
	case clientui.TranscriptMessageToolStart:
		tool := message.Payload().(clientui.TranscriptToolStart)
		h.recordToolCall(tool.StepID)
	case clientui.TranscriptMessageReviewerState:
		h.recordReviewerState(message.Payload().(clientui.TranscriptReviewerState))
	case clientui.TranscriptMessageStepState:
		step := message.Payload().(clientui.TranscriptStepState)
		if step.Lifecycle == clientui.StepLifecycleFinished {
			h.recordStepFinished(step.StepID)
		}
	case clientui.TranscriptMessageCommittedRow:
		row := message.Payload().(clientui.TranscriptCommittedRow)
		if row.Kind != clientui.TranscriptRowAssistant || row.Assistant == nil {
			return
		}
		switch row.Assistant.Phase {
		case transcript.AssistantPhaseFinal:
			h.recordTurnCompletion(row.Assistant.StepID, row.Assistant.Text)
		}
	}
}
