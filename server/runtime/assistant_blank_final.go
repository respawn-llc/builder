package runtime

import (
	"core/server/llm"
	"strings"
)

func isBlankFinalAnswer(msg llm.Message) bool {
	return msg.Phase != nil &&
		*msg.Phase == llm.MessagePhaseFinal &&
		msg.Content != nil &&
		strings.TrimSpace(*msg.Content) == ""
}
