package main

import (
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"golang.org/x/term"

	"core/internal/testharness/pty"
)

func run(args []string) (err error) {
	if len(args) > 1 || (len(args) == 1 && args[0] != "await-dispatch") {
		return fmt.Errorf("unexpected arguments %q", args)
	}
	if len(args) == 1 {
		state, rawErr := term.MakeRaw(int(os.Stdin.Fd()))
		if rawErr != nil {
			return fmt.Errorf("make stdin raw: %w", rawErr)
		}
		defer func() {
			if restoreErr := term.Restore(int(os.Stdin.Fd()), state); err == nil && restoreErr != nil {
				err = fmt.Errorf("restore stdin: %w", restoreErr)
			}
		}()
	}
	windowID, err := pty.NewWindowID(uuid.NewString())
	if err != nil {
		return err
	}
	if err := emit(pty.PhaseMarker{Sequence: 1, Phase: pty.PhaseScenarioStart}); err != nil {
		return err
	}
	if err := emit(pty.PhaseMarker{Sequence: 2, Phase: pty.PhaseWindowStart, WindowID: &windowID}); err != nil {
		return err
	}
	if len(args) == 1 {
		input, err := readDispatchedInput()
		if err != nil {
			return err
		}
		fmt.Printf("received:%s", input)
	}
	fmt.Print("window")
	if err := emit(pty.PhaseMarker{Sequence: 3, Phase: pty.PhaseWindowEnd, WindowID: &windowID}); err != nil {
		return err
	}
	return emit(pty.PhaseMarker{Sequence: 4, Phase: pty.PhaseScenarioComplete})
}

func readDispatchedInput() (input []byte, err error) {
	input = make([]byte, 2)
	if _, err := io.ReadFull(os.Stdin, input); err != nil {
		return nil, fmt.Errorf("read dispatched input: %w", err)
	}
	return input, nil
}

func emit(marker pty.PhaseMarker) error {
	encoded, err := pty.EncodePhaseMarker(marker)
	if err != nil {
		return err
	}
	_, err = fmt.Print(string(encoded))
	return err
}
