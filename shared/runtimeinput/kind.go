package runtimeinput

import (
	"errors"
	"fmt"
	"strings"
)

type Kind string

const (
	KindText          Kind = "text"
	KindPromptCommand Kind = "prompt_command"
)

type PromptCommand struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Input struct {
	Kind          Kind           `json:"kind"`
	Text          *string        `json:"text,omitempty"`
	PromptCommand *PromptCommand `json:"prompt_command,omitempty"`
}

func Text(text string) Input {
	return Input{Kind: KindText, Text: &text}
}

func Command(name, arguments string) Input {
	return Input{
		Kind:          KindPromptCommand,
		PromptCommand: &PromptCommand{Name: name, Arguments: arguments},
	}
}

func NewBuiltinPromptCommand(command BuiltinPromptCommand, arguments string) PromptCommand {
	return PromptCommand{Name: command.Name(), Arguments: arguments}
}

func BuiltinCommand(command BuiltinPromptCommand, arguments string) Input {
	prompt := NewBuiltinPromptCommand(command, arguments)
	return Input{
		Kind:          KindPromptCommand,
		PromptCommand: &prompt,
	}
}

func (i Input) Validate() error {
	switch i.Kind {
	case KindText:
		if i.Text == nil || i.PromptCommand != nil || strings.TrimSpace(*i.Text) == "" {
			return errors.New("text user-turn input requires non-empty text only")
		}
	case KindPromptCommand:
		if i.Text != nil || i.PromptCommand == nil {
			return errors.New("prompt-command user-turn input requires prompt_command only")
		}
		_, err := ParsePromptCommandName(i.PromptCommand.Name)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("user-turn input kind %q is invalid", i.Kind)
	}
	return nil
}
