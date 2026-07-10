package driver

import (
	"errors"
	"fmt"
	"time"

	"core/internal/testharness/pty/analyzer"

	"github.com/google/uuid"
)

type SessionSpec struct {
	Path       string
	Args       []string
	Env        []string
	Dir        string
	Dimensions analyzer.Dimensions
}

type SessionCommandKind int

const (
	SessionCommandWrite SessionCommandKind = iota + 1
	SessionCommandResize
	SessionCommandRuntimeControlByte
	SessionCommandTerminateProcess
)

type SessionCommand struct {
	ID         uuid.UUID
	Kind       SessionCommandKind
	Bytes      []byte
	Dimensions *analyzer.Dimensions
}

func (command SessionCommand) Validate() error {
	if command.ID == uuid.Nil || command.ID.Version() != 4 {
		return fmt.Errorf("session command id must be UUIDv4")
	}
	switch command.Kind {
	case SessionCommandWrite, SessionCommandRuntimeControlByte:
		if len(command.Bytes) == 0 {
			return errors.New("write command bytes are required")
		}
		if command.Dimensions != nil {
			return errors.New("write command must not include dimensions")
		}
	case SessionCommandResize:
		if len(command.Bytes) != 0 {
			return errors.New("resize command must not include bytes")
		}
		if command.Dimensions == nil {
			return errors.New("resize command dimensions are required")
		}
		if _, err := analyzer.NewDimensions(command.Dimensions.Rows, command.Dimensions.Cols); err != nil {
			return err
		}
	case SessionCommandTerminateProcess:
		if len(command.Bytes) != 0 || command.Dimensions != nil {
			return errors.New("terminate command must not include bytes or dimensions")
		}
	default:
		return fmt.Errorf("unknown session command kind %d", command.Kind)
	}
	return nil
}

type CommandSpec struct {
	Path            string
	Args            []string
	Env             []string
	Dir             string
	Dimensions      analyzer.Dimensions
	Inputs          []InputEvent
	PhaseInputs     []PhaseInputEvent
	ParseableInputs []ParseableInputEvent
	Resizes         []ResizeEvent
	Timeout         time.Duration
}

type InputEvent struct {
	After time.Duration
	Bytes []byte
}

type PhaseInputEvent struct {
	Phase analyzer.PhaseKind
	Bytes []byte
}

type ParseableInputEvent struct {
	Bytes []byte
}

type ResizeEvent struct {
	After      time.Duration
	Dimensions analyzer.Dimensions
}

type TimeoutError struct {
	Command string
	Elapsed time.Duration
}

func (e *TimeoutError) Error() string {
	return "pty command timed out: command=" + e.Command + " elapsed=" + e.Elapsed.String()
}
