package runtimeinput

import (
	"errors"
	"strings"
)

func CanonicalCommandText(name string, arguments string) (string, error) {
	parsed, err := ParsePromptCommandName(name)
	if err != nil {
		return "", err
	}
	text := "/" + parsed.String()
	if trimmed := strings.TrimSpace(arguments); trimmed != "" {
		text += " " + trimmed
	}
	return text, nil
}

func (i Input) CanonicalHistoryText() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	if i.Kind == KindText {
		return *i.Text, nil
	}
	return CanonicalCommandText(i.PromptCommand.Name, i.PromptCommand.Arguments)
}

func (i Input) ExecutionText(resolve func(PromptCommand) (string, error)) (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	if i.Kind == KindText {
		return *i.Text, nil
	}
	if resolve == nil {
		return "", errors.New("prompt command resolver is required")
	}
	return resolve(*i.PromptCommand)
}
