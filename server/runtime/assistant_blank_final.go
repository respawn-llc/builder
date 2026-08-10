package runtime

import (
	"core/server/llm"
	"core/shared/transcript"
)

func isBlankFinalAnswer(msg llm.Message) bool {
	return transcript.IsBlankAssistantFinal(transcript.AssistantFinalCandidate{
		IsAssistant:    msg.Role == llm.RoleAssistant,
		IsFinal:        msg.Phase != nil && *msg.Phase == llm.MessagePhaseFinal,
		HasMessageType: msg.MessageType != nil,
		Content:        msg.Content,
	})
}
