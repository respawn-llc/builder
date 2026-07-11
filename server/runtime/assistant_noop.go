package runtime

import (
	"core/server/llm"
	"core/shared/transcript"
)

func isNoopFinalAnswer(msg llm.Message) bool {
	return msg.Phase == llm.MessagePhaseFinal && transcript.IsNoopFinalText(msg.Content)
}
