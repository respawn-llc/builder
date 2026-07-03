package analyzer

import (
	"fmt"
	"sort"

	"github.com/gdamore/tcell/v3/vt"
)

func Analyze(capture Capture) (Analysis, error) {
	backend := newTracingBackend(capture.Dimensions)
	emulator := vt.NewEmulator(backend)
	if err := emulator.Start(); err != nil {
		return Analysis{}, fmt.Errorf("start terminal emulator: %w", err)
	}
	defer func() {
		_ = emulator.Stop()
	}()

	offset := int64(0)
	sideChannel := newSequenceSideChannel(backend)
	resizeIndex := 0
	for resizeIndex < len(capture.Resizes) && capture.Resizes[resizeIndex].Placement.Kind == ResizeBeforeFirstChunk {
		resize := capture.Resizes[resizeIndex]
		backend.resize(resize.Dimensions, resize.At)
		emulator.ResizeEvent(vt.Coord{X: vt.Col(resize.Dimensions.Cols), Y: vt.Row(resize.Dimensions.Rows)})
		resizeIndex++
	}
	for _, chunk := range capture.Chunks {
		backend.beginChunk(chunk, offset)
		for i, b := range chunk.Payload {
			absoluteOffset := offset + int64(i)
			backend.beginByte(chunk, absoluteOffset)
			sideChannel.advance(b, chunk, absoluteOffset)
			if _, err := emulator.Write([]byte{b}); err != nil {
				return Analysis{}, fmt.Errorf("analyze chunk %d at byte offset %d: %w", chunk.Index, absoluteOffset, err)
			}
		}
		offset += int64(len(chunk.Payload))
		for resizeIndex < len(capture.Resizes) && capture.Resizes[resizeIndex].Placement.Kind == ResizeAfterChunk && capture.Resizes[resizeIndex].Placement.ChunkIndex == chunk.Index {
			resize := capture.Resizes[resizeIndex]
			backend.resize(resize.Dimensions, resize.At)
			emulator.ResizeEvent(vt.Coord{X: vt.Col(resize.Dimensions.Cols), Y: vt.Row(resize.Dimensions.Rows)})
			resizeIndex++
		}
	}
	if err := emulator.Drain(); err != nil {
		return Analysis{}, fmt.Errorf("drain terminal emulator: %w", err)
	}
	if err := sideChannel.error(); err != nil {
		return Analysis{}, err
	}
	privateModeChanges := sideChannel.privateModeChangeLog()
	screen := backend.snapshot()
	return Analysis{
		Dimensions:         screen.Dimensions,
		Operations:         mergePrivateModeOperations(backend.operations(), privateModeChanges),
		PrivateModeChanges: privateModeChanges,
		PhaseEvents:        sideChannel.phaseEventLog(),
		Screen:             screen,
	}, nil
}

func mergePrivateModeOperations(operations []Operation, changes []PrivateModeChange) []Operation {
	if len(changes) == 0 {
		return operations
	}
	merged := append([]Operation(nil), operations...)
	for _, change := range changes {
		change := change
		merged = append(merged, Operation{
			Sequence:    len(merged),
			Kind:        OperationModeChange,
			ChunkIndex:  change.ChunkIndex,
			ByteRange:   change.ByteRange,
			PrivateMode: &change,
			CapturedAt:  change.CapturedAt,
		})
	}
	sort.SliceStable(merged, func(i int, j int) bool {
		if merged[i].ByteRange.Start == merged[j].ByteRange.Start {
			return merged[i].Kind < merged[j].Kind
		}
		return merged[i].ByteRange.Start < merged[j].ByteRange.Start
	})
	for i := range merged {
		merged[i].Sequence = i
	}
	return merged
}
