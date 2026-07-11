//go:build !windows

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
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
	phaseInputs := newPhaseInputDispatcher(spec.PhaseInputs)
	frameInputs, err := newFrameInputDispatcher(spec.FrameInputSequences)
	if err != nil {
		return analyzer.Capture{}, err
	}
	parseableInputs := newParseableInputDispatcher(spec.ParseableInputs)
	readiness, err := analyzer.NewReadinessTracker(spec.Dimensions)
	if err != nil {
		return analyzer.Capture{}, err
	}
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Dir = spec.Dir

	started := time.Now()
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{Rows: uint16(spec.Dimensions.Rows), Cols: uint16(spec.Dimensions.Cols)})
	if err != nil {
		return analyzer.Capture{}, errors.Join(
			fmt.Errorf("start pty command path=%s args=%v dimensions=%+v: %w", spec.Path, spec.Args, spec.Dimensions, err),
			readiness.Close(),
		)
	}
	defer func() {
		_ = ptmx.Close()
	}()
	waitDone := make(chan error, 1)
	var processExited atomic.Bool
	go func() {
		waitErr := cmd.Wait()
		processExited.Store(true)
		waitDone <- waitErr
	}()

	var mu sync.Mutex
	var eventWG sync.WaitGroup
	var eventErrors concurrentErrors
	chunks := make([]analyzer.Chunk, 0)
	resizes := make([]analyzer.ResizeEvent, 0, len(spec.Resizes))
	phaseInputDispatches := make([]analyzer.PhaseInputDispatch, 0, len(spec.PhaseInputs))
	frameInputDispatches := make([]analyzer.FrameInputDispatch, 0)
	dispatchPhaseInputs := func(inputs []PhaseInputEvent) error {
		for _, input := range inputs {
			if processExited.Load() {
				return nil
			}
			recordPhaseDispatch := func(startedAt time.Duration) {
				mu.Lock()
				phaseInputDispatches = append(phaseInputDispatches, analyzer.PhaseInputDispatch{
					Phase:          input.Phase,
					ScheduledAfter: input.After,
					StartedAt:      startedAt,
				})
				mu.Unlock()
			}
			if input.After == 0 {
				startedAt := time.Since(started)
				if err := writeFull(ptmx, input.Bytes); err != nil {
					if processExited.Load() {
						return nil
					}
					return fmt.Errorf("write phase-relative PTY input for phase=%d: %w", input.Phase, err)
				}
				recordPhaseDispatch(startedAt)
				continue
			}
			input := input
			eventWG.Add(1)
			go func() {
				defer eventWG.Done()
				timer := time.NewTimer(input.After)
				defer timer.Stop()
				select {
				case <-timer.C:
					if processExited.Load() || ctx.Err() != nil {
						return
					}
					startedAt := time.Since(started)
					if err := writeFull(ptmx, input.Bytes); err != nil {
						if processExited.Load() {
							return
						}
						eventErrors.Add(fmt.Errorf("write phase-relative PTY input for phase=%d: %w", input.Phase, err))
						cancel()
						return
					}
					recordPhaseDispatch(startedAt)
				case <-ctx.Done():
				}
			}()
		}
		return nil
	}
	dispatchPendingInputs := func() error {
		if processExited.Load() {
			return nil
		}
		if err := dispatchPhaseInputs(phaseInputs.pending(readiness.PhaseEvents())); err != nil {
			return err
		}
		pendingFrameInputs := frameInputs.pending(readiness)
		for _, input := range pendingFrameInputs {
			if processExited.Load() {
				return nil
			}
			startedAt := time.Since(started)
			if err := writeFull(ptmx, input.Bytes); err != nil {
				if processExited.Load() {
					return nil
				}
				return fmt.Errorf(
					"write frame-gated PTY input for phase=%d input_index=%d: %w",
					input.Phase,
					input.InputIndex,
					err,
				)
			}
			mu.Lock()
			frameInputDispatches = append(frameInputDispatches, analyzer.FrameInputDispatch{
				Phase:                      input.Phase,
				InputIndex:                 input.InputIndex,
				ReadyBoundary:              input.ReadyBoundary,
				ReadyBoundaryEndByteOffset: input.ReadyBoundaryEndByteOffset,
				StartedAt:                  startedAt,
			})
			mu.Unlock()
		}
		if len(phaseInputs.events) == 0 || allDispatchesTriggered(phaseInputs.triggered) {
			for _, payload := range parseableInputs.pending(analyzer.Analysis{}) {
				if err := writeFull(ptmx, payload); err != nil {
					if processExited.Load() {
						return nil
					}
					return fmt.Errorf("write parseable PTY input: %w", err)
				}
			}
		}
		return nil
	}

	stream, err := analyzer.NewStream(spec.Dimensions)
	if err != nil {
		return analyzer.Capture{}, fmt.Errorf("start PTY dispatcher stream: %w", err)
	}
	streamFinished := false
	defer func() {
		if !streamFinished {
			_, _ = stream.Finish()
		}
	}()
	readDone := make(chan struct{})
	analysisWake := make(chan struct{}, 1)
	requestAnalysis := func() {
		select {
		case analysisWake <- struct{}{}:
		default:
		}
	}
	applyResize := func(resize ResizeEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if err := creackpty.Setsize(ptmx, &creackpty.Winsize{Rows: uint16(resize.Dimensions.Rows), Cols: uint16(resize.Dimensions.Cols)}); err != nil {
			return fmt.Errorf("resize PTY to dimensions=%+v: %w", resize.Dimensions, err)
		}
		placement := analyzer.BeforeFirstChunk()
		source := analyzer.Chunk{Index: 0}
		if len(chunks) > 0 {
			placement = analyzer.AfterChunk(len(chunks) - 1)
			source = chunks[len(chunks)-1]
		}
		at := time.Since(started)
		resizes = append(resizes, analyzer.ResizeEvent{Placement: placement, At: at, Dimensions: resize.Dimensions})
		if err := stream.ResizeFrom(resize.Dimensions, source, at); err != nil {
			return fmt.Errorf("apply live PTY resize: %w", err)
		}
		requestAnalysis()
		return nil
	}
	for _, resize := range spec.Resizes {
		if resize.After != 0 {
			continue
		}
		if err := applyResize(resize); err != nil {
			eventErrors.Add(err)
			cancel()
			break
		}
	}
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buffer)
			if n > 0 {
				mu.Lock()
				chunk := analyzer.NewChunk(len(chunks), time.Since(started), buffer[:n])
				chunks = append(chunks, chunk)
				if feedErr := stream.FeedChunk(chunk); feedErr != nil {
					mu.Unlock()
					eventErrors.Add(fmt.Errorf("feed live PTY analysis: %w", feedErr))
					cancel()
					return
				}
				analysis, snapshotErr := stream.Snapshot()
				mu.Unlock()
				if snapshotErr != nil {
					eventErrors.Add(fmt.Errorf("snapshot live PTY analysis: %w", snapshotErr))
					cancel()
					return
				}
				if err := dispatchPhaseInputs(phaseInputs.pending(analysis.PhaseEvents)); err != nil {
					eventErrors.Add(err)
					cancel()
					return
				}
				requestAnalysis()
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

	analysisDone := make(chan struct{})
	go func() {
		defer close(analysisDone)
		analyzedChunkCount := 0
		analyzedResizeCount := 0
		for {
			select {
			case <-analysisWake:
			case <-readDone:
			}
			for {
				mu.Lock()
				copied := append([]analyzer.Chunk(nil), chunks...)
				copiedResizes := append([]analyzer.ResizeEvent(nil), resizes...)
				mu.Unlock()
				if len(copied) == analyzedChunkCount && len(copiedResizes) == analyzedResizeCount {
					break
				}
				if err := advanceReadinessTracker(
					readiness,
					copied,
					copiedResizes,
					&analyzedChunkCount,
					&analyzedResizeCount,
				); err != nil {
					eventErrors.Add(fmt.Errorf("advance PTY input readiness: %w", err))
					cancel()
					return
				}
				if err := dispatchPendingInputs(); err != nil {
					eventErrors.Add(err)
					cancel()
					return
				}
				mu.Lock()
				caughtUp := len(chunks) == analyzedChunkCount && len(resizes) == analyzedResizeCount
				mu.Unlock()
				if caughtUp {
					break
				}
			}
			select {
			case <-readDone:
				return
			default:
			}
		}
	}()

	for _, resize := range spec.Resizes {
		resize := resize
		if resize.After == 0 {
			continue
		}
		eventWG.Add(1)
		go func() {
			defer eventWG.Done()
			timer := time.NewTimer(resize.After)
			defer timer.Stop()
			select {
			case <-timer.C:
				if processExited.Load() || ctx.Err() != nil {
					return
				}
				if err := applyResize(resize); err != nil {
					if processExited.Load() {
						return
					}
					eventErrors.Add(err)
					cancel()
				}
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
				if processExited.Load() || ctx.Err() != nil {
					return
				}
				if err := writeFull(ptmx, input.Bytes); err != nil {
					if processExited.Load() {
						return
					}
					eventErrors.Add(fmt.Errorf("write scheduled PTY input: %w", err))
					cancel()
				}
			case <-ctx.Done():
			}
		}()
	}

	var waitErr error
	timeout := false
	select {
	case waitErr = <-waitDone:
		timeout = errors.Is(ctx.Err(), context.DeadlineExceeded)
	case <-ctx.Done():
		timeout = errors.Is(ctx.Err(), context.DeadlineExceeded)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-waitDone
	}
	cancel()
	eventWG.Wait()
	_ = ptmx.Close()
	<-readDone
	<-analysisDone
	readinessErr := readiness.Close()

	mu.Lock()
	copiedChunks := append([]analyzer.Chunk(nil), chunks...)
	copiedResizes := append([]analyzer.ResizeEvent(nil), resizes...)
	copiedPhaseInputDispatches := append([]analyzer.PhaseInputDispatch(nil), phaseInputDispatches...)
	copiedFrameInputDispatches := append([]analyzer.FrameInputDispatch(nil), frameInputDispatches...)
	mu.Unlock()
	capture, captureErr := analyzer.NewCaptureWithEvents(spec.Dimensions, copiedChunks, copiedResizes)
	if captureErr != nil {
		return analyzer.Capture{}, captureErr
	}
	sort.Slice(copiedPhaseInputDispatches, func(i, j int) bool {
		return copiedPhaseInputDispatches[i].StartedAt < copiedPhaseInputDispatches[j].StartedAt
	})
	capture.PhaseInputDispatches = copiedPhaseInputDispatches
	sort.Slice(copiedFrameInputDispatches, func(i, j int) bool {
		return copiedFrameInputDispatches[i].StartedAt < copiedFrameInputDispatches[j].StartedAt
	})
	capture.FrameInputDispatches = copiedFrameInputDispatches
	capture.ProcessExit = processExit(cmd.ProcessState)
	capture.ReadLoopDone = true
	if _, err := stream.Finish(); err != nil {
		eventErrors.Add(fmt.Errorf("finish live PTY analysis: %w", err))
	}
	streamFinished = true
	eventErr := errors.Join(eventErrors.Err(), readinessErr)
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

func advanceReadinessTracker(
	readiness *analyzer.ReadinessTracker,
	chunks []analyzer.Chunk,
	resizes []analyzer.ResizeEvent,
	chunkCount *int,
	resizeCount *int,
) error {
	if readiness == nil || chunkCount == nil || resizeCount == nil {
		return errors.New("advance PTY readiness with missing state")
	}
	for {
	resizeLoop:
		for *resizeCount < len(resizes) {
			resize := resizes[*resizeCount]
			switch resize.Placement.Kind {
			case analyzer.ResizeBeforeFirstChunk:
				if *chunkCount != 0 {
					return fmt.Errorf("late before-first-chunk resize at index %d", *resizeCount)
				}
			case analyzer.ResizeAfterChunk:
				if resize.Placement.ChunkIndex >= *chunkCount {
					break resizeLoop
				}
			default:
				return fmt.Errorf("unknown readiness resize placement kind %d", resize.Placement.Kind)
			}
			if err := readiness.Resize(resize.Dimensions, resize.At); err != nil {
				return fmt.Errorf("apply readiness resize %d: %w", *resizeCount, err)
			}
			(*resizeCount)++
		}

		if *chunkCount >= len(chunks) {
			break
		}
		if err := readiness.AdvanceChunk(chunks[*chunkCount]); err != nil {
			return err
		}
		(*chunkCount)++
	}
	if *resizeCount != len(resizes) {
		return fmt.Errorf(
			"readiness resizes remain after available chunks: applied=%d total=%d chunks=%d",
			*resizeCount,
			len(resizes),
			len(chunks),
		)
	}
	return nil
}

type phaseInputDispatcher struct {
	events    []PhaseInputEvent
	triggered []bool
}

func newPhaseInputDispatcher(events []PhaseInputEvent) *phaseInputDispatcher {
	return &phaseInputDispatcher{events: append([]PhaseInputEvent(nil), events...), triggered: make([]bool, len(events))}
}

func (d *phaseInputDispatcher) pending(phases []analyzer.PhaseEvent) []PhaseInputEvent {
	if d == nil || len(d.events) == 0 || len(phases) == 0 || allDispatchesTriggered(d.triggered) {
		return nil
	}
	out := make([]PhaseInputEvent, 0)
	for _, phase := range phases {
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
	return out
}

type frameInputDispatcher struct {
	sequences []frameInputSequenceState
}

type frameInputSequenceState struct {
	phase           analyzer.PhaseKind
	inputs          []FrameInput
	phaseObserved   bool
	nextInput       int
	afterByteOffset int64
	inputApplied    *analyzer.ReadinessBoundary
}

type readyFrameInput struct {
	Phase                      analyzer.PhaseKind
	InputIndex                 int
	ReadyBoundary              analyzer.ReadinessBoundaryKind
	ReadyBoundaryEndByteOffset int64
	Bytes                      []byte
}

func newFrameInputDispatcher(sequences []FrameInputSequence) (*frameInputDispatcher, error) {
	states := make([]frameInputSequenceState, 0, len(sequences))
	for sequenceIndex, sequence := range sequences {
		if len(sequence.Inputs) == 0 {
			return nil, fmt.Errorf("frame input sequence %d has no inputs", sequenceIndex)
		}
		inputs := make([]FrameInput, len(sequence.Inputs))
		for inputIndex, input := range sequence.Inputs {
			if !input.Readiness.Valid() {
				return nil, fmt.Errorf(
					"frame input sequence %d input %d has invalid readiness boundary %d",
					sequenceIndex,
					inputIndex,
					input.Readiness,
				)
			}
			if len(input.Bytes) == 0 {
				return nil, fmt.Errorf("frame input sequence %d input %d is empty", sequenceIndex, inputIndex)
			}
			if inputIndex == 0 && input.Readiness == analyzer.ReadinessInputApplied {
				return nil, fmt.Errorf(
					"frame input sequence %d first input cannot wait for input-applied readiness",
					sequenceIndex,
				)
			}
			var afterPhase *analyzer.PhaseKind
			if input.AfterPhase != nil {
				phaseCopy := *input.AfterPhase
				afterPhase = &phaseCopy
			}
			inputs[inputIndex] = FrameInput{
				Readiness:  input.Readiness,
				AfterPhase: afterPhase,
				Bytes:      append([]byte(nil), input.Bytes...),
			}
		}
		states = append(states, frameInputSequenceState{phase: sequence.Phase, inputs: inputs})
	}
	return &frameInputDispatcher{sequences: states}, nil
}

func (d *frameInputDispatcher) pending(readiness *analyzer.ReadinessTracker) []readyFrameInput {
	if d == nil || readiness == nil || len(d.sequences) == 0 || readiness.ByteCount() == 0 || d.allDispatched() {
		return nil
	}
	phases := readiness.PhaseEvents()
	out := make([]readyFrameInput, 0, len(d.sequences))
	for stateIndex := range d.sequences {
		state := &d.sequences[stateIndex]
		if state.nextInput >= len(state.inputs) {
			continue
		}
		if !state.phaseObserved {
			for _, phase := range phases {
				if phase.Phase != state.phase {
					continue
				}
				state.phaseObserved = true
				state.afterByteOffset = phase.ByteRange.End
				break
			}
		}
		if !state.phaseObserved {
			continue
		}
		input := state.inputs[state.nextInput]
		boundary, ok := state.nextBoundary(readiness, input)
		if !ok {
			continue
		}
		inputIndex := state.nextInput
		out = append(out, readyFrameInput{
			Phase:                      state.phase,
			InputIndex:                 inputIndex,
			ReadyBoundary:              boundary.Kind,
			ReadyBoundaryEndByteOffset: boundary.ByteRange.End,
			Bytes:                      append([]byte(nil), input.Bytes...),
		})
		state.nextInput++
		state.afterByteOffset = readiness.ByteCount()
		state.inputApplied = nil
	}
	return out
}

func (s *frameInputSequenceState) nextBoundary(
	readiness *analyzer.ReadinessTracker,
	input FrameInput,
) (analyzer.ReadinessBoundary, bool) {
	if s == nil || readiness == nil {
		return analyzer.ReadinessBoundary{}, false
	}
	afterByteOffset := s.afterByteOffset
	if s.nextInput == 0 {
		afterByteOffset = s.afterByteOffset
	} else if s.inputApplied == nil {
		applied, ok := readiness.LatestBoundaryAfter(
			analyzer.ReadinessInputApplied,
			s.afterByteOffset,
		)
		if !ok {
			return analyzer.ReadinessBoundary{}, false
		}
		s.inputApplied = &applied
		afterByteOffset = applied.ByteRange.End
	} else {
		afterByteOffset = s.inputApplied.ByteRange.End
	}
	if input.AfterPhase != nil {
		phase, ok := readiness.LatestPhaseAfter(*input.AfterPhase, afterByteOffset)
		if !ok {
			return analyzer.ReadinessBoundary{}, false
		}
		afterByteOffset = phase.ByteRange.End
	}
	if input.Readiness == analyzer.ReadinessInputApplied && input.AfterPhase == nil {
		return *s.inputApplied, true
	}
	return readiness.LatestBoundaryAfter(input.Readiness, afterByteOffset)
}

func (d *frameInputDispatcher) allDispatched() bool {
	for _, state := range d.sequences {
		if state.nextInput < len(state.inputs) {
			return false
		}
	}
	return true
}

type parseableInputDispatcher struct {
	events    []ParseableInputEvent
	triggered []bool
}

func newParseableInputDispatcher(events []ParseableInputEvent) *parseableInputDispatcher {
	return &parseableInputDispatcher{events: append([]ParseableInputEvent(nil), events...), triggered: make([]bool, len(events))}
}

func (d *parseableInputDispatcher) pending(analysis analyzer.Analysis) [][]byte {
	if d == nil || len(d.events) == 0 {
		return nil
	}
	out := make([][]byte, 0)
	for i, event := range d.events {
		if d.triggered[i] {
			continue
		}
		d.triggered[i] = true
		out = append(out, append([]byte(nil), event.Bytes...))
	}
	return out
}

func allDispatchesTriggered(triggered []bool) bool {
	for _, dispatched := range triggered {
		if !dispatched {
			return false
		}
	}
	return true
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
