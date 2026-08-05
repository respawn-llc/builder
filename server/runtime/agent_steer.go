package runtime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

type AgentSteer struct {
	sourceSessionID runtimeids.SessionID
	message         llm.Message
}

func NewAgentSteer(sourceSessionID runtimeids.SessionID, text string) (AgentSteer, error) {
	if sourceSessionID.IsZero() {
		return AgentSteer{}, errors.New("source session ID is required")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return AgentSteer{}, errors.New("agent steer text is required")
	}
	content := fmt.Sprintf(
		"Agent from session %s said:\n> %s\n\nTo respond, run: kent run steer %s \"message\"",
		sourceSessionID.String(),
		text,
		sourceSessionID.String(),
	)
	messageType := llm.MessageTypeAgentSteer
	return AgentSteer{sourceSessionID: sourceSessionID, message: llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: &messageType,
		Content:     textutil.Value(content),
	}}, nil
}

func (s AgentSteer) SourceSessionID() runtimeids.SessionID {
	return s.sourceSessionID
}

func (s AgentSteer) Message() llm.Message {
	return s.message
}
