package driver

import (
	"time"

	"core/internal/testharness/pty/analyzer"
)

type CommandSpec struct {
	Path                string
	Args                []string
	Env                 []string
	Dir                 string
	Dimensions          analyzer.Dimensions
	Inputs              []InputEvent
	PhaseInputs         []PhaseInputEvent
	FrameInputSequences []FrameInputSequence
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
