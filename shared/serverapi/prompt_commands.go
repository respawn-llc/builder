package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
	"core/shared/runtimeinput"
)

type PromptCommandCatalogRequest struct{}

func (PromptCommandCatalogRequest) Validate() error {
	return nil
}

type PromptCommandCatalogEntry = runtimeinput.PromptCommandCatalogEntry

type PromptCommandCatalogResponse struct {
	Commands []PromptCommandCatalogEntry `json:"commands"`
}

func (r PromptCommandCatalogResponse) Validate() error {
	seen := make(map[string]struct{}, len(r.Commands))
	for _, command := range r.Commands {
		if strings.TrimSpace(command.Name) == "" {
			return errors.New("prompt command name is required")
		}
		name := command.Name
		parsed, parseErr := runtimeinput.ParsePromptCommandName(name)
		if parseErr != nil || parsed.String() != name {
			return fmt.Errorf("prompt command %q is not canonical", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("prompt command %q is duplicated", name)
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(command.Preview) == "" ||
			strings.Join(strings.Fields(command.Preview), " ") != command.Preview {
			return fmt.Errorf("prompt command %q preview must be one-line whitespace-collapsed text", name)
		}
		if len([]rune(command.Preview)) > 256 {
			return fmt.Errorf("prompt command %q preview exceeds 256 characters", name)
		}
	}
	return nil
}

type PromptCommandErrorKind string

const (
	PromptCommandErrorKindCatalogRead     PromptCommandErrorKind = "catalog_read"
	PromptCommandErrorKindCommandNotFound PromptCommandErrorKind = "command_not_found"
	PromptCommandErrorKindCommandRead     PromptCommandErrorKind = "command_read"
)

type PromptCommandError struct {
	Kind    PromptCommandErrorKind `json:"kind"`
	Command *string                `json:"command,omitempty"`
}

func (e *PromptCommandError) Error() string {
	if e == nil {
		return "prompt command error"
	}
	if e.Command == nil {
		return "prompt command error: " + string(e.Kind)
	}
	return fmt.Sprintf("prompt command %q error: %s", *e.Command, e.Kind)
}

func (e *PromptCommandError) RPCErrorCode() int {
	return protocol.ErrCodePromptCommands
}

func (e *PromptCommandError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type    string                 `json:"type"`
		Kind    PromptCommandErrorKind `json:"kind"`
		Command *string                `json:"command,omitempty"`
	}{
		Type:    "prompt_command_error",
		Kind:    e.Kind,
		Command: e.Command,
	})
}

func (e *PromptCommandError) Validate() error {
	if e == nil {
		return errors.New("prompt command error is required")
	}
	switch e.Kind {
	case PromptCommandErrorKindCatalogRead, PromptCommandErrorKindCommandNotFound, PromptCommandErrorKindCommandRead:
	default:
		return fmt.Errorf("unknown prompt command error kind %q", e.Kind)
	}
	if e.Command != nil {
		if strings.TrimSpace(*e.Command) == "" {
			return errors.New("prompt command error command cannot be blank")
		}
		if parsed, err := runtimeinput.ParsePromptCommandName(*e.Command); err != nil || parsed.String() != *e.Command {
			return errors.New("prompt command error command must be canonical")
		}
	}
	if (e.Kind == PromptCommandErrorKindCommandNotFound || e.Kind == PromptCommandErrorKindCommandRead) && e.Command == nil {
		return errors.New("command-specific prompt command error requires command")
	}
	return nil
}

func DecodePromptCommandError(data json.RawMessage, message string) error {
	var payload struct {
		Kind    PromptCommandErrorKind `json:"kind"`
		Command *string                `json:"command"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return errors.New(message)
	}
	err := &PromptCommandError{Kind: payload.Kind, Command: payload.Command}
	if validateErr := err.Validate(); validateErr != nil {
		return errors.New(message)
	}
	return err
}
