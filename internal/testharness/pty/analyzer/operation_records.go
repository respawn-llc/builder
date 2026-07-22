package analyzer

import "sort"

// OperationRecords projects a top-level transaction into its ordered terminal
// records. A write batch retains its writes and controls separately for compact
// storage; their non-overlapping observer byte ranges define the exact order.
// Callers that reason about terminal effects must use this projection instead
// of inspecting the batch summary region.
func OperationRecords(operation Operation) []Operation {
	if len(operation.WriteSegments) == 0 && len(operation.Controls) == 0 {
		return []Operation{operation}
	}

	records := make([]Operation, 0, len(operation.WriteSegments)+len(operation.Controls))
	for _, segment := range operation.WriteSegments {
		segment := segment
		records = append(records, Operation{
			Sequence:   operation.Sequence,
			Kind:       OperationWrite,
			ChunkIndex: segment.ChunkIndex,
			ByteRange:  segment.ByteRange,
			Before:     segment.Before,
			After:      segment.After,
			Region:     segment.Region,
			Write:      &segment.Write,
			CapturedAt: segment.CapturedAt,
		})
	}
	for _, control := range operation.Controls {
		records = append(records, OperationRecords(control)...)
	}
	sort.SliceStable(records, func(left, right int) bool {
		if records[left].ByteRange.Start != records[right].ByteRange.Start {
			return records[left].ByteRange.Start < records[right].ByteRange.Start
		}
		return records[left].ByteRange.End < records[right].ByteRange.End
	})
	return records
}
