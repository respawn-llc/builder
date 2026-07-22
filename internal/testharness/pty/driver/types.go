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
		if command.Bytes != nil {
			return errors.New("resize command must not include bytes")
		}
		if command.Dimensions == nil {
			return errors.New("resize command dimensions are required")
		}
		if _, err := analyzer.NewDimensions(command.Dimensions.Rows, command.Dimensions.Cols); err != nil {
			return err
		}
	case SessionCommandTerminateProcess:
		if command.Bytes != nil || command.Dimensions != nil {
			return errors.New("terminate command must not include bytes or dimensions")
		}
	default:
		return fmt.Errorf("unknown session command kind %d", command.Kind)
	}
	return nil
}

type CommandSpec struct {
	Path                string
	Args                []string
	Env                 []string
	Dir                 string
	Dimensions          analyzer.Dimensions
	Inputs              []InputEvent
	PhaseInputs         []PhaseInputEvent
	FrameInputSequences []FrameInputSequence
	FrameResizes        []FrameResizeEvent
	ParseableInputs     []ParseableInputEvent
	Resizes             []ResizeEvent
	Timeout             time.Duration
}

type InputEvent struct {
	After time.Duration
	Bytes []byte
}

type PhaseInputEvent struct {
	Phase analyzer.PhaseKind
	After time.Duration
	Bytes []byte
}

// FrameInputSequence dispatches inputs in order. The first input waits for its
// typed readiness boundary after Phase; every later input waits for its
// boundary after the preceding input was dispatched.
type FrameInputSequence struct {
	Phase  analyzer.PhaseKind
	Inputs []FrameInput
}

type FrameInput struct {
	Readiness  analyzer.ReadinessBoundaryKind
	AfterPhase *analyzer.PhaseKind
	Bytes      []byte
}

// FrameResizeEvent resizes after a typed readiness boundary and sends its
// completion input only after the first renderer frame produced after that
// resize.
type FrameResizeEvent struct {
	Phase           analyzer.PhaseKind
	Readiness       analyzer.ReadinessBoundaryKind
	Dimensions      analyzer.Dimensions
	CompletionBytes []byte
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
