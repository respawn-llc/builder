package workflowcontract

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrWorkflowNameRequired = errors.New("workflow name is required")
	ErrWorkflowNameTooLong  = errors.New("workflow name must be <= 120 characters")
)

type WorkflowName struct {
	value string
}

func NewWorkflowName(raw string) (WorkflowName, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return WorkflowName{}, ErrWorkflowNameRequired
	}
	if utf8.RuneCountInString(value) > MaxDisplayNameChars {
		return WorkflowName{}, ErrWorkflowNameTooLong
	}
	return WorkflowName{value: value}, nil
}

func (name WorkflowName) String() string {
	if name.value == "" {
		panic("invalid empty Workflow name")
	}
	return name.value
}
