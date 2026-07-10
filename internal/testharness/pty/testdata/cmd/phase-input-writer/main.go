package main

import (
	"fmt"
	"io"
	"os"

	"core/internal/testharness/pty"
)

func main() {
	marker, err := pty.EncodePhaseMarker(pty.PhaseMarker{Sequence: 1, Phase: pty.PhaseScenarioStart})
	if err != nil {
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(marker); err != nil {
		os.Exit(2)
	}
	var input [1]byte
	if _, err := io.ReadFull(os.Stdin, input[:]); err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Printf("input:%s", input[:])
}
