package app

import (
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
)

func (p *clientLifecycleProxy) AcceptTranscript(message clientui.TranscriptMessage) {
	if p == nil {
		return
	}
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		p.acceptSessionIdentity(message.Payload.Hydration.SessionIdentity)
		p.acceptSessionStatus(message.Payload.Hydration.SessionStatus)
		if p.transcriptAttention {
			for _, prompt := range message.Payload.Hydration.PendingPrompts {
				p.acceptTranscriptPrompt(prompt)
			}
		}
	case clientui.TranscriptMessageSessionIdentity:
		p.acceptSessionIdentity(*message.Payload.SessionIdentity)
	case clientui.TranscriptMessageSessionStatus:
		p.acceptSessionStatus(*message.Payload.SessionStatus)
	case clientui.TranscriptMessageLiveRunFinished:
		p.acceptLiveRunFinished(*message.Payload.LiveRunFinished)
	case clientui.TranscriptMessageCompactionStatus:
		status := message.Payload.CompactionStatus
		if status.State == clientui.CompactionStarted {
			p.enqueue(lifecyclecontract.NewCompactionStarted(
				time.Now().UTC(),
				p.isFocused(),
				p.context(),
				status.Mode,
			))
		}
	case clientui.TranscriptMessagePromptPending:
		if p.transcriptAttention {
			p.acceptTranscriptPrompt(*message.Payload.PromptPending)
		}
	}
}
