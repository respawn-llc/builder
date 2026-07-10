package analyzer

import "fmt"

// WriteTextArena returns the one shared immutable write-text arena referenced
// by an analysis. Artifact writers serialize this byte sequence once and use
// TextSpan references from terminal records instead of copying every payload.
func WriteTextArena(analysis Analysis) ([]byte, error) {
	var arena *writeTextArena
	for _, operation := range analysis.Operations {
		for _, record := range OperationRecords(operation) {
			if record.Write == nil {
				continue
			}
			payload := record.Write
			if payload.arena == nil || payload.Span.Validate() != nil || payload.Span.End > len(payload.arena.bytes) {
				return nil, fmt.Errorf("invalid write payload in operation sequence=%d byte_range=%+v", record.Sequence, record.ByteRange)
			}
			if arena == nil {
				arena = payload.arena
				continue
			}
			if arena != payload.arena {
				return nil, fmt.Errorf("analysis has multiple write-text arenas at operation sequence=%d byte_range=%+v", record.Sequence, record.ByteRange)
			}
		}
	}
	if arena == nil {
		return nil, nil
	}
	return arena.bytes, nil
}
