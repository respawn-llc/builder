package driver

import (
	"time"

	"core/internal/testharness/pty/analyzer"
)

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
	After time.Duration
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
