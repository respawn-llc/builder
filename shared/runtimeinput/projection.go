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
	return formatCommandText(parsed.String(), arguments), nil
}

func formatCommandText(name string, arguments string) string {
	text := "/" + name
	if trimmed := strings.TrimSpace(arguments); trimmed != "" {
		text += " " + trimmed
	}
	return text
}

func (c PromptCommand) CanonicalHistoryText() (string, error) {
	name, err := ParsePromptCommandName(c.Name)
	if err != nil {
		return "", err
	}
	if command, ok := BuiltinPromptCommandForName(name); ok {
		return formatCommandText(command.Alias(), c.Arguments), nil
	}
	return CanonicalCommandText(c.Name, c.Arguments)
}

func (i Input) CanonicalHistoryText() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	if i.Kind == KindText {
		return *i.Text, nil
	}
	return i.PromptCommand.CanonicalHistoryText()
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
