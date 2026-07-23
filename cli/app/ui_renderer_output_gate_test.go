package app

import (
	"bytes"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestRendererOutputGateDropsSuppressedNormalBufferRendererPayloads(t *testing.T) {
	state := newUIRendererOutputGateState()
	state.SetSuppressRendererWrites(true)

	var out bytes.Buffer
	writer := newUIRendererOutputGateWriter(&out, state)
	payload := []byte("\rnormal frame" + xansi.EraseLineRight + "\r")
	n, err := writer.Write(payload)
	if err != nil {
		t.Fatalf("write suppressed renderer payload: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("suppressed write length = %d, want %d", n, len(payload))
	}
	if got := out.String(); got != "" {
		t.Fatalf("suppressed renderer payload leaked: %q", got)
	}

	if _, err := writer.Write([]byte(xansi.SetModeFocusEvent)); err != nil {
		t.Fatalf("write focus mode control: %v", err)
	}
	if got := out.String(); got != xansi.SetModeFocusEvent {
		t.Fatalf("focus mode control should pass through while suppressed, got %q", got)
	}
}

func TestRendererOutputGateAllowsAltScreenPayloadsWhileSuppressed(t *testing.T) {
	state := newUIRendererOutputGateState()
	state.SetSuppressRendererWrites(true)

	var out bytes.Buffer
	writer := newUIRendererOutputGateWriter(&out, state)
	if _, err := writer.Write([]byte(xansi.SetModeAltScreenSaveCursor)); err != nil {
		t.Fatalf("write alt-screen enter: %v", err)
	}
	if _, err := writer.Write([]byte(xansi.EraseEntireScreen)); err != nil {
		t.Fatalf("write alt-screen clear: %v", err)
	}
	if _, err := writer.Write([]byte("detail frame")); err != nil {
		t.Fatalf("write alt-screen frame: %v", err)
	}
	if got := out.String(); got != xansi.SetModeAltScreenSaveCursor+xansi.EraseEntireScreen+"detail frame" {
		t.Fatalf("alt-screen output = %q", got)
	}

	if _, err := writer.Write([]byte(xansi.ResetModeAltScreenSaveCursor)); err != nil {
		t.Fatalf("write alt-screen exit: %v", err)
	}
	out.Reset()
	if _, err := writer.Write([]byte("ongoing frame")); err != nil {
		t.Fatalf("write ongoing renderer frame: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("normal-buffer renderer payload leaked after alt-screen exit: %q", got)
	}
}

func TestRendererOutputGateAllowsBatchedAltScreenEnterFrameWhileSuppressed(t *testing.T) {
	state := newUIRendererOutputGateState()
	state.SetSuppressRendererWrites(true)

	var out bytes.Buffer
	writer := newUIRendererOutputGateWriter(&out, state)
	payload := []byte(xansi.SetModeAltScreenSaveCursor + xansi.EraseEntireScreen + "detail frame")
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write batched alt-screen enter frame: %v", err)
	}
	if got := out.String(); got != string(payload) {
		t.Fatalf("batched alt-screen output = %q, want %q", got, string(payload))
	}
	if !state.PhysicalAltScreenActive() {
		t.Fatal("batched alt-screen enter did not mark physical alt-screen active")
	}
}

func TestRendererOutputGateTracksSplitAltScreenExitWhileSuppressed(t *testing.T) {
	state := newUIRendererOutputGateState()
	state.SetSuppressRendererWrites(true)

	var out bytes.Buffer
	writer := newUIRendererOutputGateWriter(&out, state)
	if _, err := writer.Write([]byte(xansi.SetModeAltScreenSaveCursor)); err != nil {
		t.Fatalf("write alt-screen enter: %v", err)
	}
	for _, chunk := range [][]byte{[]byte("\x1b[?10"), []byte("49"), []byte("l")} {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatalf("write split alt-screen exit chunk %q: %v", string(chunk), err)
		}
	}
	if state.PhysicalAltScreenActive() {
		t.Fatal("split alt-screen exit did not clear physical alt-screen state")
	}

	out.Reset()
	payload := []byte("\rnormal frame" + xansi.EraseLineRight + "\r")
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write suppressed normal frame: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("normal-buffer renderer payload leaked after split alt-screen exit: %q", got)
	}
}

func TestRendererOutputGatePreservesTerminalFileDescriptor(t *testing.T) {
	file := &rendererOutputGateTerminalFile{fd: 42}
	writer := newUIRendererOutputGateWriter(file, newUIRendererOutputGateState())
	terminalFile, ok := writer.(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("expected renderer output gate to preserve Fd for Bubble Tea TTY detection, got %T", writer)
	}
	if got := terminalFile.Fd(); got != 42 {
		t.Fatalf("fd = %d, want 42", got)
	}
}

func TestRendererOutputGateClosesWrappedTerminalFile(t *testing.T) {
	file := &rendererOutputGateTerminalFile{fd: 42}
	writer := newUIRendererOutputGateWriter(file, newUIRendererOutputGateState())
	closer, ok := writer.(interface{ Close() error })
	if !ok {
		t.Fatalf("expected renderer output gate to preserve Close for Bubble Tea file cleanup, got %T", writer)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close returned error: %v", err)
	}
	if !file.closed {
		t.Fatal("wrapped terminal file was not closed")
	}
}

type rendererOutputGateTerminalFile struct {
	bytes.Buffer
	fd     uintptr
	closed bool
}

func (f *rendererOutputGateTerminalFile) Fd() uintptr {
	return f.fd
}

func (f *rendererOutputGateTerminalFile) Close() error {
	f.closed = true
	return nil
}
