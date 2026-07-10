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

	sideChannel := newSequenceSideChannel(backend)
	resizeIndex := 0
	source := Chunk{Index: 0}
	applyResizes := func(offset int64) {
		for resizeIndex < len(capture.Resizes) && capture.Resizes[resizeIndex].Offset == offset {
			resize := capture.Resizes[resizeIndex]
			backend.resize(resize.Dimensions, resize.At)
			emulator.ResizeEvent(vt.Coord{X: vt.Col(resize.Dimensions.Cols), Y: vt.Row(resize.Dimensions.Rows)})
			resizeIndex++
		}
	}
	for offset, b := range capture.Raw {
		absoluteOffset := int64(offset)
		applyResizes(absoluteOffset)
		backend.beginByte(source, absoluteOffset)
		sideChannel.advance(b, source, absoluteOffset)
		if _, err := emulator.Write([]byte{b}); err != nil {
			return Analysis{}, fmt.Errorf("analyze byte at offset %d: %w", absoluteOffset, err)
		}
		if err := backend.error(); err != nil {
			return Analysis{}, fmt.Errorf("analyze byte at offset %d: %w", absoluteOffset, err)
		}
	}
	applyResizes(int64(len(capture.Raw)))
	if resizeIndex != len(capture.Resizes) {
		return Analysis{}, fmt.Errorf("resize event %d has invalid observer offset %d for %d bytes", resizeIndex, capture.Resizes[resizeIndex].Offset, len(capture.Raw))
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
