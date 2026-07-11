package main

import (
	"bufio"
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
	if len(os.Args) == 2 && os.Args[1] == "frame-sequence" {
		if _, err := fmt.Print("\x1b[2;1H"); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		for index := range 3 {
			input, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(2)
			}
			inputMarker, err := pty.EncodePhaseMarker(pty.PhaseMarker{
				Sequence: index + 2,
				Phase:    pty.PhaseInputApplied,
			})
			if err != nil {
				os.Exit(2)
			}
			if _, err := os.Stdout.Write(inputMarker); err != nil {
				os.Exit(2)
			}
			if _, err := fmt.Printf("input:%s\x1b[2;1H", input[:len(input)-1]); err != nil {
				os.Exit(2)
			}
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "typed-readiness-sequence" {
		if _, err := fmt.Print("\x1b[2;1H"); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Printf("input:%s", input[:len(input)-1]); err != nil {
			os.Exit(2)
		}
		inputMarker, err := pty.EncodePhaseMarker(pty.PhaseMarker{Sequence: 2, Phase: pty.PhaseInputApplied})
		if err != nil {
			os.Exit(2)
		}
		if _, err := os.Stdout.Write(inputMarker); err != nil {
			os.Exit(2)
		}
		input, err = reader.ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		inputMarker, err = pty.EncodePhaseMarker(pty.PhaseMarker{Sequence: 3, Phase: pty.PhaseInputApplied})
		if err != nil {
			os.Exit(2)
		}
		if _, err := os.Stdout.Write(inputMarker); err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Printf("input:%s\x1b[2;1H", input[:len(input)-1]); err != nil {
			os.Exit(2)
		}
		input, err = reader.ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		inputMarker, err = pty.EncodePhaseMarker(pty.PhaseMarker{Sequence: 4, Phase: pty.PhaseInputApplied})
		if err != nil {
			os.Exit(2)
		}
		if _, err := os.Stdout.Write(inputMarker); err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Printf("input:%s\x1b[?1049l", input[:len(input)-1]); err != nil {
			os.Exit(2)
		}
		input, err = reader.ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Printf("input:%s", input[:len(input)-1]); err != nil {
			os.Exit(2)
		}
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
