package app

import (
	"bytes"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestTerminalOutputSharesRendererAndNativeWriteOrdering(t *testing.T) {
	var out bytes.Buffer
	terminal := newUITerminalOutput(&out)
	renderer := newUIRendererOutputGateWriter(terminal, newUIRendererOutputGateState())

	if _, err := renderer.Write([]byte("renderer-1|")); err != nil {
		t.Fatalf("renderer write: %v", err)
	}
	if _, err := terminal.Write([]byte("native-1|")); err != nil {
		t.Fatalf("native write: %v", err)
	}
	if _, err := renderer.Write([]byte("renderer-2")); err != nil {
		t.Fatalf("renderer write 2: %v", err)
	}

	if got := out.String(); got != "renderer-1|native-1|renderer-2" {
		t.Fatalf("output = %q", got)
	}
}

func TestTerminalOutputAnnouncesInputReadinessOnFirstWrite(t *testing.T) {
	var out bytes.Buffer
	terminal := newUITerminalOutput(&out)
	if err := terminal.AnnounceInputReady(); err != nil {
		t.Fatalf("announce input ready: %v", err)
	}
	if _, err := terminal.Write([]byte("frame")); err != nil {
		t.Fatalf("write first frame: %v", err)
	}
	if got, want := out.String(), xansi.ShowCursor+"frame"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestTerminalOutputRejectsLateInputReadinessAnnouncement(t *testing.T) {
	terminal := newUITerminalOutput(&bytes.Buffer{})
	if _, err := terminal.Write([]byte("frame")); err != nil {
		t.Fatalf("write first frame: %v", err)
	}
	if err := terminal.AnnounceInputReady(); err == nil {
		t.Fatal("expected late readiness announcement error")
	}
}

func TestTerminalOutputFilePreservesTerminalFileDescriptor(t *testing.T) {
	file := &rendererOutputGateTerminalFile{fd: 42}
	terminal := newUITerminalOutputFile(file)
	terminalFile, ok := any(terminal).(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("expected terminal output to preserve Fd for Bubble Tea TTY detection, got %T", terminal)
	}
	if got := terminalFile.Fd(); got != 42 {
		t.Fatalf("fd = %d, want 42", got)
	}
	if _, err := terminal.Write([]byte("native output")); err != nil {
		t.Fatalf("write terminal output: %v", err)
	}
	if got := file.String(); got != "native output" {
		t.Fatalf("forwarded output = %q", got)
	}
}
