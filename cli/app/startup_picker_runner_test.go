package app

import (
	"errors"
	"testing"
	"time"

	"core/internal/testharness/pty"
)

func TestStartupPickerTerminalBoundaryBalancesOwnedModes(t *testing.T) {
	var output []string
	originalWrite := writeTerminalSequence
	writeTerminalSequence = func(sequence string) error {
		output = append(output, sequence)
		return nil
	}
	t.Cleanup(func() { writeTerminalSequence = originalWrite })

	terminal := startupPickerTerminal{state: startupPickerTerminalInactive}
	if err := terminal.Enter(); err != nil {
		t.Fatalf("enter terminal: %v", err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatalf("close terminal: %v", err)
	}

	analysis := analyzeStartupPickerTerminalOutput(t, output)
	if err := assertBalancedStartupPickerModes(analysis); err != nil {
		t.Fatal(err)
	}
}

func TestStartupPickerTerminalEntryFailureRestoresAlternateScreen(t *testing.T) {
	var output []string
	originalWrite := writeTerminalSequence
	writeTerminalSequence = func(sequence string) error {
		if sequence == "\x1b[?1007h" {
			return errors.New("alternate scroll unavailable")
		}
		output = append(output, sequence)
		return nil
	}
	t.Cleanup(func() { writeTerminalSequence = originalWrite })

	terminal := startupPickerTerminal{state: startupPickerTerminalInactive}
	if err := terminal.Enter(); err == nil {
		t.Fatal("entry unexpectedly succeeded")
	}

	analysis := analyzeStartupPickerTerminalOutput(t, output)
	if err := assertBalancedStartupPickerModes(analysis); err != nil {
		t.Fatal(err)
	}
}

func analyzeStartupPickerTerminalOutput(t *testing.T, output []string) pty.Analysis {
	t.Helper()
	var bytes []byte
	for _, sequence := range output {
		bytes = append(bytes, sequence...)
	}
	capture, err := pty.NewCapture(
		pty.MustDimensions(10, 80),
		[]pty.Chunk{pty.NewChunk(0, time.Second, bytes)},
	)
	if err != nil {
		t.Fatalf("capture terminal output: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze terminal output: %v", err)
	}
	return analysis
}

func assertBalancedStartupPickerModes(analysis pty.Analysis) error {
	altScreen := 0
	alternateScroll := 0
	for _, operation := range analysis.Operations {
		if operation.Kind != pty.OperationModeChange || operation.PrivateMode == nil {
			continue
		}
		switch operation.PrivateMode.Mode {
		case 1049:
			if operation.PrivateMode.Enabled {
				altScreen++
			} else {
				altScreen--
			}
		case 1007:
			if operation.PrivateMode.Enabled {
				alternateScroll++
			} else {
				alternateScroll--
			}
		case 1000, 1002, 1003, 1006:
			return errors.New("startup picker terminal enabled mouse capture")
		}
		if altScreen < 0 || alternateScroll < 0 {
			return errors.New("startup picker terminal disabled an unowned mode")
		}
	}
	if altScreen != 0 || alternateScroll != 0 {
		return errors.New("startup picker terminal left an owned mode enabled")
	}
	return nil
}
