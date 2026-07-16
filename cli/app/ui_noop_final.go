package app

import (
	"core/shared/transcript"
)

const uiNoopFinalToken = transcript.NoopFinalToken

func isNoopFinalText(text string) bool {
	return transcript.IsNoopFinalText(text)
}
