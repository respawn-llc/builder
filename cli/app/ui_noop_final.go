package app

import (
	"strings"

	"core/shared/clientui"
)

const uiNoopFinalToken = "NO_OP"

func isNoopFinalText(text string) bool {
	return strings.TrimSpace(text) == uiNoopFinalToken
}

func isNoopProjectedAssistantEvent(evt clientui.Event) bool {
	switch evt.Kind {
	case clientui.EventAssistantDelta:
		return isNoopFinalText(evt.AssistantDelta)
	default:
		return false
	}
}
