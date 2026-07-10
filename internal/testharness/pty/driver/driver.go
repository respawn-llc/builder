package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"core/internal/testharness/pty/analyzer"
	creackpty "github.com/creack/pty"
)

func RunCommand(ctx context.Context, spec CommandSpec) (analyzer.Capture, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if spec.Timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, spec.Timeout)
		defer timeoutCancel()
	}
	if _, err := analyzer.NewDimensions(spec.Dimensions.Rows, spec.Dimensions.Cols); err != nil {
		return analyzer.Capture{}, err
	}
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Dir = spec.Dir

	started := time.Now()
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{Rows: uint16(spec.Dimensions.Rows), Cols: uint16(spec.Dimensions.Cols)})
	if err != nil {
		return analyzer.Capture{}, fmt.Errorf("start pty command path=%s args=%v dimensions=%+v: %w", spec.Path, spec.Args, spec.Dimensions, err)
	}
	defer func() {
		_ = ptmx.Close()
	}()

	var mu sync.Mutex
	var eventWG sync.WaitGroup
	var eventErrors concurrentErrors
	chunks := make([]analyzer.Chunk, 0)
	resizes := make([]analyzer.ResizeEvent, 0, len(spec.Resizes))
	phaseInputs := newPhaseInputDispatcher(spec.PhaseInputs)
	parseableInputs := newParseableInputDispatcher(spec.ParseableInputs)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buffer)
			if n > 0 {
				mu.Lock()
				chunks = append(chunks, analyzer.NewChunk(len(chunks), time.Since(started), buffer[:n]))
				copied := append([]analyzer.Chunk(nil), chunks...)
				copiedResizes := append([]analyzer.ResizeEvent(nil), resizes...)
				mu.Unlock()
				pendingPhaseInputs, dispatchErr := phaseInputs.pending(copied, spec.Dimensions, copiedResizes)
				if dispatchErr != nil {
					eventErrors.Add(fmt.Errorf("resolve phase-relative PTY input: %w", dispatchErr))
					cancel()
					return
				}
				for _, input := range pendingPhaseInputs {
					input := input
					eventWG.Add(1)
					go func() {
						defer eventWG.Done()
						timer := time.NewTimer(input.After)
						defer timer.Stop()
						select {
						case <-timer.C:
							if err := writeFull(ptmx, input.Bytes); err != nil {
								eventErrors.Add(fmt.Errorf("write phase-relative PTY input for phase=%d: %w", input.Phase, err))
								cancel()
							}
						case <-ctx.Done():
						}
					}()
				}
				pendingParseableInputs, dispatchErr := parseableInputs.pending(copied, spec.Dimensions, copiedResizes)
				if dispatchErr != nil {
					eventErrors.Add(fmt.Errorf("resolve parseable PTY input: %w", dispatchErr))
					cancel()
					return
				}
				for _, payload := range pendingParseableInputs {
					if err := writeFull(ptmx, payload); err != nil {
						eventErrors.Add(fmt.Errorf("write parseable PTY input: %w", err))
						cancel()
						return
					}
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO) {
					return
				}
				eventErrors.Add(fmt.Errorf("read PTY output: %w", err))
				cancel()
				return
			}
		}
	}()

	for _, resize := range spec.Resizes {
		resize := resize
		eventWG.Add(1)
		go func() {
			defer eventWG.Done()
			timer := time.NewTimer(resize.After)
			defer timer.Stop()
			select {
			case <-timer.C:
				if err := creackpty.Setsize(ptmx, &creackpty.Winsize{Rows: uint16(resize.Dimensions.Rows), Cols: uint16(resize.Dimensions.Cols)}); err != nil {
					eventErrors.Add(fmt.Errorf("resize PTY to dimensions=%+v: %w", resize.Dimensions, err))
					cancel()
					return
				}
				mu.Lock()
				placement := analyzer.BeforeFirstChunk()
				if len(chunks) > 0 {
					placement = analyzer.AfterChunk(len(chunks) - 1)
				}
				resizes = append(resizes, analyzer.ResizeEvent{Placement: placement, At: time.Since(started), Dimensions: resize.Dimensions})
				mu.Unlock()
			case <-ctx.Done():
			}
		}()
	}
	for _, input := range spec.Inputs {
		input := input
		eventWG.Add(1)
		go func() {
			defer eventWG.Done()
			timer := time.NewTimer(input.After)
			defer timer.Stop()
			select {
			case <-timer.C:
				if err := writeFull(ptmx, input.Bytes); err != nil {
					eventErrors.Add(fmt.Errorf("write scheduled PTY input: %w", err))
					cancel()
				}
			case <-ctx.Done():
			}
		}()
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	var waitErr error
	timeout := false
	select {
	case waitErr = <-waitDone:
		timeout = errors.Is(ctx.Err(), context.DeadlineExceeded)
	case <-ctx.Done():
		timeout = errors.Is(ctx.Err(), context.DeadlineExceeded)
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-waitDone
	}
	cancel()
	_ = ptmx.Close()
	<-readDone
	eventWG.Wait()

	mu.Lock()
	copiedChunks := append([]analyzer.Chunk(nil), chunks...)
	copiedResizes := append([]analyzer.ResizeEvent(nil), resizes...)
	mu.Unlock()
	capture, captureErr := analyzer.NewCaptureWithEvents(spec.Dimensions, copiedChunks, copiedResizes)
	if captureErr != nil {
		return analyzer.Capture{}, captureErr
	}
	capture.ProcessExit = processExit(cmd.ProcessState)
	capture.ReadLoopDone = true
	eventErr := eventErrors.Err()
	if timeout {
		return capture, errors.Join(&TimeoutError{Command: spec.Path, Elapsed: time.Since(started)}, eventErr)
	}
	if eventErr != nil {
		return capture, eventErr
	}
	if waitErr != nil {
		return capture, fmt.Errorf("pty command exited with error path=%s args=%v: %w", spec.Path, spec.Args, waitErr)
	}
	return capture, nil
}

type phaseInputDispatcher struct {
	events    []PhaseInputEvent
	triggered []bool
}

func newPhaseInputDispatcher(events []PhaseInputEvent) *phaseInputDispatcher {
	return &phaseInputDispatcher{events: append([]PhaseInputEvent(nil), events...), triggered: make([]bool, len(events))}
}

func (d *phaseInputDispatcher) pending(chunks []analyzer.Chunk, dimensions analyzer.Dimensions, resizes []analyzer.ResizeEvent) ([]PhaseInputEvent, error) {
	if d == nil || len(d.events) == 0 || len(chunks) == 0 {
		return nil, nil
	}
	capture, err := analyzer.NewCaptureWithEvents(dimensions, chunks, resizes)
	if err != nil {
		return nil, err
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		return nil, err
	}
	out := make([]PhaseInputEvent, 0)
	for _, phase := range analysis.PhaseEvents {
		for i, event := range d.events {
			if d.triggered[i] || event.Phase != phase.Phase {
				continue
			}
			d.triggered[i] = true
			copyEvent := event
			copyEvent.Bytes = append([]byte(nil), event.Bytes...)
			out = append(out, copyEvent)
		}
	}
	return out, nil
}

type parseableInputDispatcher struct {
	events    []ParseableInputEvent
	triggered []bool
}

func newParseableInputDispatcher(events []ParseableInputEvent) *parseableInputDispatcher {
	return &parseableInputDispatcher{events: append([]ParseableInputEvent(nil), events...), triggered: make([]bool, len(events))}
}

func (d *parseableInputDispatcher) pending(chunks []analyzer.Chunk, dimensions analyzer.Dimensions, resizes []analyzer.ResizeEvent) ([][]byte, error) {
	if d == nil || len(d.events) == 0 || len(chunks) == 0 {
		return nil, nil
	}
	capture, err := analyzer.NewCaptureWithEvents(dimensions, chunks, resizes)
	if err != nil {
		return nil, err
	}
	if len(capture.Raw) == 0 {
		return nil, nil
	}
	if _, err := analyzer.Analyze(capture); err != nil {
		return nil, err
	}
	out := make([][]byte, 0)
	for i, event := range d.events {
		if d.triggered[i] {
			continue
		}
		d.triggered[i] = true
		out = append(out, append([]byte(nil), event.Bytes...))
	}
	return out, nil
}

type concurrentErrors struct {
	mu     sync.Mutex
	values []error
}

func (e *concurrentErrors) Add(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = append(e.values, err)
}

func (e *concurrentErrors) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return errors.Join(e.values...)
}

func writeFull(writer io.Writer, payload []byte) error {
	written, err := writer.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return fmt.Errorf("short PTY write: wrote=%d expected=%d: %w", written, len(payload), io.ErrShortWrite)
	}
	return nil
}

func processExit(state *os.ProcessState) *analyzer.ProcessExit {
	if state == nil {
		return nil
	}
	exit := &analyzer.ProcessExit{Code: state.ExitCode()}
	if status, ok := state.Sys().(syscall.WaitStatus); ok {
		exit.Signaled = status.Signaled()
	}
	return exit
}
