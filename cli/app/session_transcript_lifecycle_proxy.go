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
			for _, prompt := range hydration.PendingPrompts {
				p.acceptPendingPrompt(prompt)
			}
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
	case clientui.TranscriptMessagePromptPending:
		if prompt := message.Payload.PromptPending; prompt != nil {
			p.acceptPendingPrompt(*prompt)
		}
	}
}

func (p *clientLifecycleProxy) acceptPendingPrompt(prompt clientui.TranscriptPrompt) {
	var kind lifecyclecontract.InputKind
	switch prompt.Kind {
	case clientui.TranscriptPromptKindQuestion:
		kind = lifecyclecontract.InputKindQuestion
	case clientui.TranscriptPromptKindApproval:
		kind = lifecyclecontract.InputKindApproval
	default:
		return
	}
	p.enqueue(lifecyclecontract.NewInputRequired(
		prompt.CreatedAt,
		p.isFocused(),
		p.context(),
		kind,
		prompt.Question,
	))
}
