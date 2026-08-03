package promptcommands

import (
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/shared/runtimeinput"
	"core/shared/textutil"
)

type ErrorKind string

const (
	ErrorKindCatalogRead     ErrorKind = "catalog_read"
	ErrorKindCommandNotFound ErrorKind = "command_not_found"
	ErrorKindCommandRead     ErrorKind = "command_read"
)

type Error struct {
	Kind    ErrorKind
	Command *string
	cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case ErrorKindCatalogRead:
		return "prompt command catalog is unavailable"
	case ErrorKindCommandNotFound:
		if e.Command == nil {
			return "prompt command was not found"
		}
		return fmt.Sprintf("prompt command %q was not found", *e.Command)
	case ErrorKindCommandRead:
		if e.Command == nil {
			return "prompt command could not be read"
		}
		return fmt.Sprintf("prompt command %q could not be read", *e.Command)
	default:
		return "prompt command operation failed"
	}
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type Service struct {
	persistenceRoot string
	workspaceRoot   string
}

func New(persistenceRoot, workspaceRoot string) Service {
	return Service{
		persistenceRoot: strings.TrimSpace(persistenceRoot),
		workspaceRoot:   strings.TrimSpace(workspaceRoot),
	}
}

func (s Service) Resolve(command, arguments string) (string, error) {
	if err := s.validateRoots(ErrorKindCommandRead); err != nil {
		return "", err
	}
	if name, parseErr := runtimeinput.ParsePromptCommandName(command); parseErr == nil {
		if content, ok := builtinPromptContent(name); ok {
			return textutil.ExpandPromptTemplate(content, arguments), nil
		}
	}
	candidate, found, err := s.findCandidate(command)
	if err != nil {
		return "", err
	}
	if !found {
		parsed, parseErr := runtimeinput.ParsePromptCommandName(command)
		name := strings.TrimSpace(command)
		if parseErr == nil {
			name = parsed.String()
		}
		return "", &Error{Kind: ErrorKindCommandNotFound, Command: &name}
	}
	return textutil.ExpandPromptTemplate(candidate.content, arguments), nil
}

type builtinPromptCommand struct {
	name    string
	content string
}

func builtinPromptCommands() []builtinPromptCommand {
	return []builtinPromptCommand{
		{name: "prompt:review", content: prompts.ReviewPrompt},
		{name: "prompt:init", content: prompts.InitPrompt},
	}
}

func builtinPromptContent(name runtimeinput.PromptCommandName) (string, bool) {
	for _, command := range builtinPromptCommands() {
		if command.name == name.String() {
			return command.content, true
		}
	}
	return "", false
}

func (s Service) validateRoots(kind ErrorKind) error {
	if s.persistenceRoot == "" {
		return &Error{Kind: kind, cause: errors.New("persistence root is required")}
	}
	if s.workspaceRoot == "" {
		return &Error{Kind: kind, cause: errors.New("workspace root is required")}
	}
	return nil
}
