package app

import (
	"io"
	"sync"
)

type uiTerminalOutput struct {
	mu  sync.Mutex
	out io.Writer
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
	return w.out.Write(payload)
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
