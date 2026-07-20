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
		if hydration := message.Payload.Hydration; hydration != nil {
			p.acceptSessionIdentity(hydration.SessionIdentity)
			p.acceptSessionStatus(hydration.SessionStatus)
		}
	case clientui.TranscriptMessageSessionIdentity:
		if identity := message.Payload.SessionIdentity; identity != nil {
			p.acceptSessionIdentity(*identity)
		}
	case clientui.TranscriptMessageSessionStatus:
		if status := message.Payload.SessionStatus; status != nil {
			p.acceptSessionStatus(*status)
		}
	case clientui.TranscriptMessageLiveRunFinished:
		if result := message.Payload.LiveRunFinished; result != nil {
			p.acceptLiveRunFinished(*result)
		}
	case clientui.TranscriptMessageCompactionStatus:
		status := message.Payload.CompactionStatus
		if status != nil && status.State == clientui.CompactionStarted {
			p.enqueue(lifecyclecontract.NewCompactionStarted(
				time.Now().UTC(),
				p.isFocused(),
				p.context(),
				status.Mode,
			))
		}
	}
}
