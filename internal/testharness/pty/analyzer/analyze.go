package analyzer

import (
	"bytes"
	"fmt"
	"sort"
)

func Analyze(capture Capture) (Analysis, error) {
	stream, err := NewStream(capture.Dimensions)
	if err != nil {
		return Analysis{}, err
	}
	if err := validateReplayCapture(capture); err != nil {
		return Analysis{}, err
	}
	resizeIndex := 0
	applyResizes := func(offset int64, source Chunk) error {
		for resizeIndex < len(capture.Resizes) && capture.Resizes[resizeIndex].Offset == offset {
			resize := capture.Resizes[resizeIndex]
			if err := stream.ResizeFrom(resize.Dimensions, source, resize.At); err != nil {
				return fmt.Errorf("apply replay resize at offset %d: %w", offset, err)
			}
			resizeIndex++
		}
		return nil
	}
	var offset int64
	for _, chunk := range capture.Chunks {
		for _, b := range chunk.Payload {
			if err := applyResizes(offset, chunk); err != nil {
				return Analysis{}, err
			}
			if err := stream.FeedChunk(Chunk{Index: chunk.Index, At: chunk.At, Payload: []byte{b}}); err != nil {
				return Analysis{}, err
			}
			offset++
		}
	}
	source := Chunk{}
	if len(capture.Chunks) > 0 {
		source = capture.Chunks[len(capture.Chunks)-1]
	}
	if err := applyResizes(offset, source); err != nil {
		return Analysis{}, err
	}
	if resizeIndex != len(capture.Resizes) {
		return Analysis{}, fmt.Errorf("resize event %d has invalid observer offset %d for %d bytes", resizeIndex, capture.Resizes[resizeIndex].Offset, offset)
	}
	return stream.Finish()
}

func validateReplayCapture(capture Capture) error {
	if _, err := NewDimensions(capture.Dimensions.Rows, capture.Dimensions.Cols); err != nil {
		return err
	}
	rebuilt, err := NewCaptureWithEvents(capture.Dimensions, capture.Chunks, capture.Resizes)
	if err != nil {
		return err
	}
	if !bytes.Equal(rebuilt.Raw, capture.Raw) {
		return fmt.Errorf("capture raw bytes do not match chunk evidence: raw=%d chunk_bytes=%d", len(capture.Raw), len(rebuilt.Raw))
	}
	return nil
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
