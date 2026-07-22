package runtime

import (
	"core/server/llm"
)

func messagePhaseIs(message llm.Message, phase llm.MessagePhase) bool {
	return message.Phase != nil && *message.Phase == phase
}
