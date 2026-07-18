package analyzer

import (
	"errors"
	"fmt"
	"slices"
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
	nativeFrame            nativeOngoingFrameReadiness
	readiness              readinessLog
	closed                 bool
}

type nativeOngoingFrameReadiness struct {
	startedAt       *ByteRange
	originModeReset bool
	contentWritten  bool
}

type readinessOperation struct {
	operation Operation
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
	if err := t.emulator.Drain(); err != nil {
		return fmt.Errorf("drain readiness terminal emulator after chunk %d: %w", chunk.Index, err)
	}
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

func (t *ReadinessTracker) ScreenSnapshot() ScreenSnapshot {
	if t == nil {
		return ScreenSnapshot{}
	}
	return t.backend.snapshot()
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
	operations := t.backend.operations()
	if err := t.backend.error(); err != nil {
		t.sideChannel.err = err
		return
	}
	records := make([]Operation, 0, len(operations))
	for _, operation := range operations {
		records = append(records, OperationRecords(operation)...)
	}
	pending := make([]readinessOperation, 0,
		len(records)-t.observedOperationCount+
			len(t.sideChannel.privateModeChanges)-t.observedModeCount,
	)
	for ; t.observedOperationCount < len(records); t.observedOperationCount++ {
		operation := records[t.observedOperationCount]
		pending = append(pending, readinessOperation{operation: operation})
		boundary, ok := readinessBoundaryFromOperation(operation, t.dimensions)
		if ok && boundary.Kind == ReadinessRendererFrame {
			t.readiness.append(boundary)
		}
	}
	for ; t.observedModeCount < len(t.sideChannel.privateModeChanges); t.observedModeCount++ {
		change := t.sideChannel.privateModeChanges[t.observedModeCount]
		changeCopy := change
		operation := Operation{
			Kind:        OperationModeChange,
			ChunkIndex:  change.ChunkIndex,
			ByteRange:   change.ByteRange,
			PrivateMode: &changeCopy,
			CapturedAt:  change.CapturedAt,
		}
		pending = append(pending, readinessOperation{operation: operation})
		boundary, ok := readinessBoundaryFromOperation(operation, t.dimensions)
		if ok && boundary.Kind == ReadinessNormalBufferRestored {
			t.readiness.append(boundary)
		}
	}
	slices.SortStableFunc(pending, func(left, right readinessOperation) int {
		if left.operation.ByteRange.Start < right.operation.ByteRange.Start {
			return -1
		}
		if left.operation.ByteRange.Start > right.operation.ByteRange.Start {
			return 1
		}
		return 0
	})
	for _, item := range pending {
		if boundary, ok := t.nativeFrame.observe(item.operation, t.dimensions); ok {
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

func (state *nativeOngoingFrameReadiness) observe(
	operation Operation,
	dimensions Dimensions,
) (ReadinessBoundary, bool) {
	if operation.Kind == OperationScrollRegionChange &&
		operation.Region == (Region{
			Top:    0,
			Bottom: dimensions.Rows,
			Left:   0,
			Right:  dimensions.Cols,
		}) {
		start := operation.ByteRange
		state.startedAt = &start
		state.originModeReset = false
		state.contentWritten = false
		return ReadinessBoundary{}, false
	}
	if state.startedAt == nil {
		return ReadinessBoundary{}, false
	}
	if operation.Kind == OperationWrite {
		state.contentWritten = true
		return ReadinessBoundary{}, false
	}
	if operation.Kind != OperationModeChange || operation.PrivateMode == nil {
		return ReadinessBoundary{}, false
	}
	if operation.PrivateMode.Mode == 6 && !operation.PrivateMode.Enabled {
		state.originModeReset = true
		return ReadinessBoundary{}, false
	}
	if operation.PrivateMode.Mode != 25 ||
		!state.originModeReset ||
		!state.contentWritten {
		return ReadinessBoundary{}, false
	}
	boundary := ReadinessBoundary{
		Kind: ReadinessNativeOngoingFrame,
		ByteRange: ByteRange{
			Start: state.startedAt.Start,
			End:   operation.ByteRange.End,
		},
		CapturedAt: operation.CapturedAt,
	}
	state.startedAt = nil
	state.originModeReset = false
	state.contentWritten = false
	return boundary, true
}
