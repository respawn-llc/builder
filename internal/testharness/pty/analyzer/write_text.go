package analyzer

import "fmt"

// WriteTextArena returns the one shared immutable write-text arena referenced
// by an analysis. Artifact writers serialize this byte sequence once and use
// TextSpan references from terminal records instead of copying every payload.
func WriteTextArena(analysis Analysis) ([]byte, error) {
	var arena *writeTextArena
	var visitPayload func(WritePayload, Operation) error
	visitPayload = func(payload WritePayload, record Operation) error {
		if payload.arena == nil || payload.Span.Validate() != nil || payload.Span.End > len(payload.arena.bytes) {
			return fmt.Errorf("invalid write payload in operation sequence=%d byte_range=%+v", record.Sequence, record.ByteRange)
		}
		if arena == nil {
			arena = payload.arena
			return nil
		}
		if arena != payload.arena {
			return fmt.Errorf("analysis has multiple write-text arenas at operation sequence=%d byte_range=%+v", record.Sequence, record.ByteRange)
		}
		return nil
	}
	var visitOperation func(Operation) error
	visitOperation = func(operation Operation) error {
		if operation.Write != nil {
			if err := visitPayload(*operation.Write, operation); err != nil {
				return err
			}
		}
		for _, segment := range operation.WriteSegments {
			record := Operation{
				Sequence: operation.Sequence, Kind: OperationWrite, ChunkIndex: segment.ChunkIndex,
				ByteRange: segment.ByteRange, Before: segment.Before, After: segment.After,
				Region: segment.Region, CapturedAt: segment.CapturedAt,
			}
			if err := visitPayload(segment.Write, record); err != nil {
				return err
			}
		}
		for _, control := range operation.Controls {
			if err := visitOperation(control); err != nil {
				return err
			}
		}
		return nil
	}
	for _, operation := range analysis.Operations {
		if err := visitOperation(operation); err != nil {
			return nil, err
		}
	}
	if arena == nil {
		return nil, nil
	}
	return arena.bytes, nil
}
