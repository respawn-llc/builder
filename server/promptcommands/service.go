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
		panic("nil prompt command error")
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
		panic("nil prompt command error")
	}
	return e.cause
}

type Service struct {
	persistenceRoot string
	workspaceRoot   string
}

func New(persistenceRoot, workspaceRoot string) (Service, error) {
	trimmedPersistenceRoot := strings.TrimSpace(persistenceRoot)
	if trimmedPersistenceRoot == "" {
		return Service{}, &Error{Kind: ErrorKindCatalogRead, cause: errors.New("persistence root is required")}
	}
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return Service{}, &Error{Kind: ErrorKindCatalogRead, cause: errors.New("workspace root is required")}
	}
	return Service{
		persistenceRoot: trimmedPersistenceRoot,
		workspaceRoot:   trimmedWorkspaceRoot,
	}, nil
}

func (s Service) Resolve(command, arguments string) (string, error) {
	name, parseErr := runtimeinput.ParsePromptCommandName(command)
	if parseErr != nil {
		return "", commandNotFoundError(command)
	}
	if kind, ok := runtimeinput.BuiltinPromptCommandForName(name); ok {
		return textutil.ExpandPromptTemplate(builtinPromptContent(*kind), arguments), nil
	}
	candidate, found, err := s.findCandidate(name)
	if err != nil {
		return "", err
	}
	if !found {
		commandName := name.String()
		return "", &Error{Kind: ErrorKindCommandNotFound, Command: &commandName}
	}
	return textutil.ExpandPromptTemplate(candidate.content, arguments), nil
}

func commandNotFoundError(command string) error {
	name := strings.TrimSpace(command)
	return &Error{Kind: ErrorKindCommandNotFound, Command: &name}
}

type builtinPromptCommand struct {
	kind    runtimeinput.BuiltinPromptCommand
	content string
}

func builtinPromptCommands() []builtinPromptCommand {
	commands := make([]builtinPromptCommand, 0, len(runtimeinput.BuiltinPromptCommands()))
	for _, kind := range runtimeinput.BuiltinPromptCommands() {
		commands = append(commands, builtinPromptCommand{kind: kind, content: builtinPromptContent(kind)})
	}
	return commands
}

func builtinPromptContent(kind runtimeinput.BuiltinPromptCommand) string {
	switch kind {
	case runtimeinput.BuiltinPromptCommandReview:
		return prompts.ReviewPrompt
	case runtimeinput.BuiltinPromptCommandInit:
		return prompts.InitPrompt
	default:
		panic("invalid built-in prompt command")
	}
}
