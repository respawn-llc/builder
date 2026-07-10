package analyzer

import (
	"errors"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v3/vt"
)

// ReadinessTracker incrementally consumes terminal output and retains only the
// typed phase and frame facts needed to coordinate PTY input dispatch.
type ReadinessTracker struct {
	dimensions             Dimensions
	backend                *tracingBackend
	emulator               vt.Emulator
	sideChannel            *sequenceSideChannel
	byteOffset             int64
	nextChunkIndex         int
	lastChunkAt            *time.Duration
	observedOperationCount int
	observedModeCount      int
	observedPhaseCount     int
	readiness              readinessLog
	closed                 bool
}

func NewReadinessTracker(dimensions Dimensions) (*ReadinessTracker, error) {
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return nil, err
	}
	backend := newTracingBackend(dimensions)
	emulator := vt.NewEmulator(backend)
	if err := emulator.Start(); err != nil {
		return nil, fmt.Errorf("start readiness terminal emulator: %w", err)
	}
	return &ReadinessTracker{
		dimensions:  dimensions,
		backend:     backend,
		emulator:    emulator,
		sideChannel: newSequenceSideChannel(backend),
	}, nil
}

func (t *ReadinessTracker) AdvanceChunk(chunk Chunk) error {
	if t == nil || t.closed {
		return errors.New("advance closed readiness tracker")
	}
	if chunk.Index != t.nextChunkIndex {
		return fmt.Errorf("readiness chunk index mismatch: got=%d want=%d", chunk.Index, t.nextChunkIndex)
	}
	if t.lastChunkAt != nil && chunk.At < *t.lastChunkAt {
		return fmt.Errorf("readiness chunk timestamp moved backwards: current=%s previous=%s", chunk.At, *t.lastChunkAt)
	}
	t.backend.beginChunk(chunk, t.byteOffset)
	for index, value := range chunk.Payload {
		absoluteOffset := t.byteOffset + int64(index)
		t.backend.beginByte(chunk, absoluteOffset)
		t.sideChannel.advance(value, chunk, absoluteOffset)
		if _, err := t.emulator.Write([]byte{value}); err != nil {
			return fmt.Errorf("analyze readiness chunk %d at byte offset %d: %w", chunk.Index, absoluteOffset, err)
		}
	}
	t.byteOffset += int64(len(chunk.Payload))
	t.nextChunkIndex++
	chunkAt := chunk.At
	t.lastChunkAt = &chunkAt
	t.collectReadinessBoundaries()
	if err := t.sideChannel.error(); err != nil {
		return err
	}
	return nil
}

func (t *ReadinessTracker) Resize(dimensions Dimensions, at time.Duration) error {
	if t == nil || t.closed {
		return errors.New("resize closed readiness tracker")
	}
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return err
	}
	t.backend.resize(dimensions, at)
	t.emulator.ResizeEvent(vt.Coord{X: vt.Col(dimensions.Cols), Y: vt.Row(dimensions.Rows)})
	t.dimensions = dimensions
	return nil
}

func (t *ReadinessTracker) PhaseEvents() []PhaseEvent {
	if t == nil {
		return nil
	}
	return t.sideChannel.phaseEventLog()
}

func (t *ReadinessTracker) LatestPhaseAfter(
	phase PhaseKind,
	afterByteOffset int64,
) (PhaseEvent, bool) {
	if t == nil {
		return PhaseEvent{}, false
	}
	return latestAfterByteOffset(
		t.sideChannel.phaseEvents,
		afterByteOffset,
		func(event PhaseEvent) bool { return event.Phase == phase },
		func(event PhaseEvent) ByteRange { return event.ByteRange },
	)
}

func (t *ReadinessTracker) LatestBoundaryAfter(
	kind ReadinessBoundaryKind,
	afterByteOffset int64,
) (ReadinessBoundary, bool) {
	if t == nil {
		return ReadinessBoundary{}, false
	}
	return t.readiness.latestAfter(kind, afterByteOffset)
}

func (t *ReadinessTracker) ByteCount() int64 {
	if t == nil {
		return 0
	}
	return t.byteOffset
}

func (t *ReadinessTracker) Close() error {
	if t == nil || t.closed {
		return nil
	}
	t.closed = true
	drainErr := t.emulator.Drain()
	t.collectReadinessBoundaries()
	sideChannelErr := t.sideChannel.error()
	stopErr := t.emulator.Stop()
	return errors.Join(drainErr, sideChannelErr, stopErr)
}

func (t *ReadinessTracker) collectReadinessBoundaries() {
	for ; t.observedOperationCount < len(t.backend.ops); t.observedOperationCount++ {
		operation := t.backend.ops[t.observedOperationCount]
		boundary, ok := readinessBoundaryFromOperation(operation, t.dimensions)
		if ok && boundary.Kind == ReadinessRendererFrame {
			t.readiness.append(boundary)
		}
	}
	for ; t.observedModeCount < len(t.sideChannel.privateModeChanges); t.observedModeCount++ {
		change := t.sideChannel.privateModeChanges[t.observedModeCount]
		changeCopy := change
		boundary, ok := readinessBoundaryFromOperation(Operation{
			Kind:        OperationModeChange,
			ChunkIndex:  change.ChunkIndex,
			ByteRange:   change.ByteRange,
			PrivateMode: &changeCopy,
			CapturedAt:  change.CapturedAt,
		}, t.dimensions)
		if ok && boundary.Kind == ReadinessNormalBufferRestored {
			t.readiness.append(boundary)
		}
	}
	phaseEvents := t.sideChannel.phaseEvents
	for ; t.observedPhaseCount < len(phaseEvents); t.observedPhaseCount++ {
		boundary, ok := readinessBoundaryFromPhaseEvent(phaseEvents[t.observedPhaseCount])
		if ok {
			t.readiness.append(boundary)
		}
	}
}
