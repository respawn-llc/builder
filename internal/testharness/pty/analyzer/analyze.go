package analyzer

import (
	"fmt"
	"sort"
)

func Analyze(capture Capture) (Analysis, error) {
	stream, err := NewStream(capture.Dimensions)
	if err != nil {
		return Analysis{}, err
	}
	resizeIndex := 0
	applyResizes := func(offset int64) {
		for resizeIndex < len(capture.Resizes) && capture.Resizes[resizeIndex].Offset == offset {
			resize := capture.Resizes[resizeIndex]
			if err := stream.Resize(resize.Dimensions); err != nil {
				panic(fmt.Sprintf("apply validated replay resize at offset %d: %v", offset, err))
			}
			resizeIndex++
		}
	}
	for offset, b := range capture.Raw {
		absoluteOffset := int64(offset)
		applyResizes(absoluteOffset)
		if err := stream.Feed([]byte{b}); err != nil {
			return Analysis{}, err
		}
	}
	applyResizes(int64(len(capture.Raw)))
	if resizeIndex != len(capture.Resizes) {
		return Analysis{}, fmt.Errorf("resize event %d has invalid observer offset %d for %d bytes", resizeIndex, capture.Resizes[resizeIndex].Offset, len(capture.Raw))
	}
	return stream.Finish()
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
