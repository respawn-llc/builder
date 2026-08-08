package runtime

import (
	"core/server/llm"
	"core/shared/transcript"
)

func isNoopFinalAnswer(msg llm.Message) bool {
	return msg.Phase != nil &&
		*msg.Phase == llm.MessagePhaseFinal &&
		msg.Content != nil &&
		transcript.IsNoopFinalText(*msg.Content)
}
