package main

import (
	"fmt"
	"io"
	"os"
	"time"

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
	if len(os.Args) == 2 && os.Args[1] == "close-stdio" {
		if err := os.Stdin.Close(); err != nil {
			os.Exit(2)
		}
		if err := os.Stdout.Close(); err != nil {
			os.Exit(2)
		}
		if err := os.Stderr.Close(); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Second)
		return
	}
	var input [1]byte
	if _, err := io.ReadFull(os.Stdin, input[:]); err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Printf("input:%s", input[:]); err != nil {
		os.Exit(2)
	}
}
