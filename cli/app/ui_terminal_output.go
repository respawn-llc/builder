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
	err              error
	failures         chan error
	failuresDone     chan struct{}
	failuresStop     sync.Once
	started          bool
	readinessPending bool
}

func newUITerminalOutput(out io.Writer) *uiTerminalOutput {
	return &uiTerminalOutput{
		out:          out,
		failures:     make(chan error, 1),
		failuresDone: make(chan struct{}),
	}
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
	if w.err != nil {
		return 0, w.err
	}
	if w.readinessPending {
		if _, err := w.write([]byte(xansi.ShowCursor)); err != nil {
			return 0, w.recordFailureLocked(fmt.Errorf("announce terminal input readiness: %w", err))
		}
		w.readinessPending = false
	}
	n, err := w.write(payload)
	if err != nil {
		return n, w.recordFailureLocked(err)
	}
	w.started = true
	return n, err
}

func (w *uiTerminalOutput) write(payload []byte) (int, error) {
	n, err := w.out.Write(payload)
	if err != nil {
		return n, err
	}
	if n != len(payload) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (w *uiTerminalOutput) recordFailureLocked(err error) error {
	if err == nil {
		return nil
	}
	if w.err != nil {
		return w.err
	}
	w.err = err
	select {
	case w.failures <- err:
	case <-w.failuresDone:
	default:
	}
	return err
}

func (w *uiTerminalOutput) Err() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *uiTerminalOutput) waitFailure() error {
	if w == nil {
		return nil
	}
	select {
	case err := <-w.failures:
		return err
	case <-w.failuresDone:
		return nil
	}
}

func (w *uiTerminalOutput) stopFailureEvents() {
	if w == nil {
		return
	}
	w.failuresStop.Do(func() {
		close(w.failuresDone)
	})
}

func (w *uiTerminalOutput) Restore() {
	if w == nil || w.out == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.out.Write([]byte(
		xansi.ResetModeAltScreenSaveCursor +
			xansi.ShowCursor,
	))
}

func terminalOutputRunError(output *uiTerminalOutput, runErr error) error {
	if output == nil {
		return runErr
	}
	terminalErr := output.Err()
	if terminalErr == nil {
		return runErr
	}
	output.Restore()
	return fmt.Errorf("terminal output failed: %w", terminalErr)
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
