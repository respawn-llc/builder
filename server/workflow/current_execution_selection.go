package workflow

import (
	"fmt"
	"strings"
)

// ThinkingValue is a non-empty workflow-owned thinking value. It intentionally
// accepts custom provider values without pretending they are one of Kent's
// finite named levels.
type ThinkingValue string

func NewThinkingValue(raw string) (ThinkingValue, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("thinking value is required")
	}
	return ThinkingValue(value), nil
}

func (value ThinkingValue) Validate() error {
	if strings.TrimSpace(string(value)) == "" {
		return fmt.Errorf("thinking value is required")
	}
	return nil
}

type AssigneeOrigin string

const (
	AssigneeOriginConfiguredFallback AssigneeOrigin = "configured_fallback"
	AssigneeOriginTransitionSelected AssigneeOrigin = "transition_selected"
	AssigneeOriginRetainedSession    AssigneeOrigin = "retained_session"
)

type AgentExecutionSelection struct {
	Assignee string         `json:"assignee"`
	Thinking *ThinkingValue `json:"thinking,omitempty"`
	Origin   AssigneeOrigin `json:"origin"`
}

func NewAgentExecutionSelection(assignee string, thinking *ThinkingValue, origin AssigneeOrigin) (AgentExecutionSelection, error) {
	selection := AgentExecutionSelection{
		Assignee: strings.TrimSpace(assignee),
		Origin:   AssigneeOrigin(strings.TrimSpace(string(origin))),
	}
	if selection.Assignee == "" {
		return AgentExecutionSelection{}, fmt.Errorf("agent execution assignee is required")
	}
	switch selection.Origin {
	case AssigneeOriginConfiguredFallback, AssigneeOriginTransitionSelected, AssigneeOriginRetainedSession:
	default:
		return AgentExecutionSelection{}, fmt.Errorf("agent execution assignee origin is invalid")
	}
	if thinking != nil {
		value, err := NewThinkingValue(string(*thinking))
		if err != nil {
			return AgentExecutionSelection{}, err
		}
		selection.Thinking = &value
	}
	return selection, nil
}

func (selection AgentExecutionSelection) Validate() error {
	_, err := NewAgentExecutionSelection(selection.Assignee, selection.Thinking, selection.Origin)
	return err
}

func (selection AgentExecutionSelection) Clone() AgentExecutionSelection {
	cloned := selection
	if selection.Thinking != nil {
		value := *selection.Thinking
		cloned.Thinking = &value
	}
	return cloned
}
