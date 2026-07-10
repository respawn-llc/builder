package main

import (
	"fmt"

	"github.com/google/uuid"

	"core/internal/testharness/pty"
)

func run() error {
	rawID := uuid.New()
	windowID, err := pty.NewWindowID(rawID.String())
	if err != nil {
		return err
	}
	if err := emit(pty.PhaseMarker{Sequence: 1, Phase: pty.PhaseScenarioStart}); err != nil {
		return err
	}
	if err := emit(pty.PhaseMarker{Sequence: 2, Phase: pty.PhaseWindowStart, WindowID: &windowID}); err != nil {
		return err
	}
	fmt.Print("window")
	if err := emit(pty.PhaseMarker{Sequence: 3, Phase: pty.PhaseWindowEnd, WindowID: &windowID}); err != nil {
		return err
	}
	return emit(pty.PhaseMarker{Sequence: 4, Phase: pty.PhaseScenarioComplete})
}

func emit(marker pty.PhaseMarker) error {
	encoded, err := pty.EncodePhaseMarker(marker)
	if err != nil {
		return err
	}
	_, err = fmt.Print(string(encoded))
	return err
}
