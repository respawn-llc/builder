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
	switch message.Kind() {
	case clientui.TranscriptMessageHydration:
		hydration := message.Payload().(clientui.TranscriptHydration)
		p.acceptSessionIdentity(hydration.SessionIdentity)
		p.acceptSessionStatus(hydration.SessionStatus)
		for _, prompt := range hydration.PendingPrompts {
			p.acceptPendingPrompt(prompt)
		}
	case clientui.TranscriptMessageSessionIdentity:
		p.acceptSessionIdentity(message.Payload().(clientui.TranscriptSessionIdentity))
	case clientui.TranscriptMessageSessionStatus:
		p.acceptSessionStatus(message.Payload().(clientui.TranscriptSessionStatus))
	case clientui.TranscriptMessageLiveRunFinished:
		p.acceptLiveRunFailure(message.Payload().(clientui.TranscriptLiveRunResult))
	case clientui.TranscriptMessageCompactionStatus:
		status := message.Payload().(clientui.TranscriptCompactionStatus)
		if status.State == clientui.CompactionStarted {
			p.enqueue(lifecyclecontract.NewCompactionStarted(
				time.Now().UTC(),
				p.isFocused(),
				p.context(),
				string(status.Mode),
			))
		}
	case clientui.TranscriptMessagePrompt:
		prompt := message.Payload().(clientui.TranscriptPrompt)
		if prompt.Status == clientui.TranscriptPromptStatusPending {
			p.acceptPendingPrompt(prompt)
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
