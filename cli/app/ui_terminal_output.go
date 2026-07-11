package app

import (
	"errors"
	"fmt"
	"io"
	"sync"

	xansi "github.com/charmbracelet/x/ansi"
)

type uiTerminalOutput struct {
	mu               sync.Mutex
	out              io.Writer
	started          bool
	readinessPending bool
}

func newUITerminalOutput(out io.Writer) *uiTerminalOutput {
	return &uiTerminalOutput{out: out}
}

func (w *uiTerminalOutput) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	if w == nil || w.out == nil {
		return 0, io.ErrClosedPipe
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.readinessPending {
		if _, err := w.out.Write([]byte(xansi.ShowCursor)); err != nil {
			return 0, fmt.Errorf("announce terminal input readiness: %w", err)
		}
		w.readinessPending = false
	}
	n, err := w.out.Write(payload)
	if err == nil && n == len(payload) {
		w.started = true
	}
	return n, err
}

// AnnounceInputReady queues the standard native-cursor readiness signal for
// the first renderer write, which occurs after Bubble Tea owns terminal mode.
func (w *uiTerminalOutput) AnnounceInputReady() error {
	if w == nil || w.out == nil {
		return io.ErrClosedPipe
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("terminal output already started before input readiness")
	}
	w.readinessPending = true
	return nil
}

// uiTerminalOutputFile forwards the terminal-cursor-file interface (Read/Close/
// Fd) through to the underlying file so the cursor/gate writer wrappers and the
// Bubble Tea program can still detect the real terminal file descriptor.
type uiTerminalOutputFile struct {
	*uiTerminalOutput
	file terminalCursorFile
}

func newUITerminalOutputFile(out io.Writer) uiTerminalOutputFile {
	base := newUITerminalOutput(out)
	if file, ok := out.(terminalCursorFile); ok {
		return uiTerminalOutputFile{uiTerminalOutput: base, file: file}
	}
	return uiTerminalOutputFile{uiTerminalOutput: base}
}

func (w uiTerminalOutputFile) Read(p []byte) (int, error) {
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Read(p)
}

func (w uiTerminalOutputFile) Close() error {
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w uiTerminalOutputFile) Fd() uintptr {
	if w.file == nil {
		return 0
	}
	return w.file.Fd()
}
