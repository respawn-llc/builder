package app

import (
	"core/shared/clientui"
	"core/shared/transcript"
)

const uiNoopFinalToken = transcript.NoopFinalToken

func isNoopFinalText(text string) bool {
	return transcript.IsNoopFinalText(text)
}

func isNoopProjectedAssistantEvent(evt clientui.Event) bool {
	switch evt.Kind {
	case clientui.EventAssistantDelta:
		return isNoopFinalText(evt.AssistantDelta)
	default:
		return false
	}
}
