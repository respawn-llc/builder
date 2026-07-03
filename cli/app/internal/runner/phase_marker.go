package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type TerminalPhase string

const (
	TerminalPhaseScenarioStart    TerminalPhase = "ScenarioStart"
	TerminalPhaseWindowStart      TerminalPhase = "WindowStart"
	TerminalPhaseWindowEnd        TerminalPhase = "WindowEnd"
	TerminalPhaseReadyForQuit     TerminalPhase = "ReadyForQuit"
	TerminalPhaseScenarioComplete TerminalPhase = "ScenarioComplete"
)

type TerminalPhaseMarker struct {
	Phase    TerminalPhase
	WindowID *uuid.UUID
}

func (m TerminalPhaseMarker) Validate() error {
	if m.WindowID == nil {
		return nil
	}
	if *m.WindowID == uuid.Nil {
		return errors.New("terminal phase marker window id must not be nil UUID")
	}
	if m.WindowID.Version() != 7 {
		return fmt.Errorf("terminal phase marker window id must be UUIDv7: got version %d", m.WindowID.Version())
	}
	return nil
}

type TerminalPhaseMarkerEncoder interface {
	EncodeTerminalPhaseMarker(TerminalPhaseMarker) ([]byte, error)
}

type TerminalPhaseMarkerSink interface {
	RequestTerminalPhaseMarker(TerminalPhaseMarker) error
}

type TerminalPhaseMarkerSinkFunc func(TerminalPhaseMarker) error

func (fn TerminalPhaseMarkerSinkFunc) RequestTerminalPhaseMarker(marker TerminalPhaseMarker) error {
	if fn == nil {
		return nil
	}
	return fn(marker)
}

type TerminalPhaseMarkerSinkObserver interface {
	TerminalPhaseMarkerSinkReady(context.Context, TerminalPhaseMarkerSink) error
}
