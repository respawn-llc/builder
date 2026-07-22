package analyzer

import (
	"bytes"
	"fmt"
	"sort"
)

type ReplayCheckpoint struct {
	ByteOffset int64
}

func Analyze(capture Capture) (Analysis, error) {
	normalized, err := normalizeReplayCapture(capture)
	if err != nil {
		return Analysis{}, err
	}
	capture = normalized
	stream, err := NewStream(capture.Dimensions)
	if err != nil {
		return Analysis{}, err
	}
	finished := false
	defer func() {
		if !finished {
			_, _ = stream.Finish()
		}
	}()
	finish := func() (Analysis, error) {
		finished = true
		return stream.Finish()
	}
	if _, err := replayCapture(capture, stream, nil); err != nil {
		return Analysis{}, err
	}
	return finish()
}

func ReplayCheckpointScreens(capture Capture, checkpoints []ReplayCheckpoint) ([]ScreenSnapshot, error) {
	normalized, err := normalizeReplayCapture(capture)
	if err != nil {
		return nil, err
	}
	stream, err := NewStream(normalized.Dimensions)
	if err != nil {
		return nil, err
	}
	finished := false
	defer func() {
		if !finished {
			_, _ = stream.Finish()
		}
	}()
	screens, err := replayCapture(normalized, stream, checkpoints)
	if err != nil {
		return nil, err
	}
	_, err = stream.Finish()
	finished = true
	if err != nil {
		return nil, err
	}
	return screens, nil
}

func replayCapture(capture Capture, stream *Stream, checkpoints []ReplayCheckpoint) ([]ScreenSnapshot, error) {
	type indexedCheckpoint struct {
		index  int
		offset int64
	}
	ordered := make([]indexedCheckpoint, len(checkpoints))
	for index, checkpoint := range checkpoints {
		if checkpoint.ByteOffset < 0 || checkpoint.ByteOffset > int64(len(capture.Raw)) {
			return nil, fmt.Errorf("checkpoint byte offset %d outside capture bytes %d", checkpoint.ByteOffset, len(capture.Raw))
		}
		ordered[index] = indexedCheckpoint{index: index, offset: checkpoint.ByteOffset}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].offset < ordered[right].offset
	})
	screens := make([]ScreenSnapshot, len(checkpoints))
	checkpointIndex := 0
	resizeIndex := 0
	applyResizes := func(offset int64, source Chunk) error {
		for resizeIndex < len(capture.Resizes) && *capture.Resizes[resizeIndex].Offset == offset {
			resize := capture.Resizes[resizeIndex]
			if err := stream.ResizeFrom(resize.Dimensions, source, resize.At); err != nil {
				return fmt.Errorf("apply replay resize at offset %d: %w", offset, err)
			}
			resizeIndex++
		}
		return nil
	}
	snapshotAt := func(offset int64, source Chunk) error {
		if err := applyResizes(offset, source); err != nil {
			return err
		}
		for checkpointIndex < len(ordered) && ordered[checkpointIndex].offset == offset {
			screen, err := stream.ScreenSnapshot()
			if err != nil {
				return err
			}
			screens[ordered[checkpointIndex].index] = screen
			checkpointIndex++
		}
		return nil
	}
	var offset int64
	for _, chunk := range capture.Chunks {
		for byteIndex := range chunk.Payload {
			if err := snapshotAt(offset, chunk); err != nil {
				return nil, err
			}
			if err := stream.FeedChunk(Chunk{Index: chunk.Index, At: chunk.At, Payload: chunk.Payload[byteIndex : byteIndex+1]}); err != nil {
				return nil, err
			}
			offset++
		}
	}
	source := Chunk{}
	if len(capture.Chunks) > 0 {
		source = capture.Chunks[len(capture.Chunks)-1]
	}
	if err := snapshotAt(offset, source); err != nil {
		return nil, err
	}
	if resizeIndex != len(capture.Resizes) {
		return nil, fmt.Errorf("resize event %d has invalid observer offset %d for %d bytes", resizeIndex, *capture.Resizes[resizeIndex].Offset, offset)
	}
	if checkpointIndex != len(ordered) {
		return nil, fmt.Errorf("checkpoint %d was not replayed", checkpointIndex)
	}
	return screens, nil
}

func normalizeReplayCapture(capture Capture) (Capture, error) {
	if _, err := NewDimensions(capture.Dimensions.Rows, capture.Dimensions.Cols); err != nil {
		return Capture{}, err
	}
	rebuilt, err := NewCaptureWithEvents(capture.Dimensions, capture.Chunks, capture.Resizes)
	if err != nil {
		return Capture{}, err
	}
	if !bytes.Equal(rebuilt.Raw, capture.Raw) {
		return Capture{}, fmt.Errorf("capture raw bytes do not match chunk evidence: raw=%d chunk_bytes=%d", len(capture.Raw), len(rebuilt.Raw))
	}
	return rebuilt, nil
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
