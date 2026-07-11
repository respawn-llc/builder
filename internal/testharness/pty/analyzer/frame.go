package analyzer

func LatestReadinessBoundaryAfter(
	analysis Analysis,
	dimensions Dimensions,
	kind ReadinessBoundaryKind,
	afterByteOffset int64,
) (ReadinessBoundary, bool) {
	return readinessLogFromAnalysis(analysis, dimensions).latestAfter(kind, afterByteOffset)
}

func readinessBoundaryFromOperation(operation Operation, dimensions Dimensions) (ReadinessBoundary, bool) {
	switch {
	case operation.Kind == OperationCursorMove &&
		operation.After.Row == dimensions.Rows-1 &&
		operation.After.Col == 0:
		return ReadinessBoundary{
			Kind:       ReadinessRendererFrame,
			ByteRange:  operation.ByteRange,
			CapturedAt: operation.CapturedAt,
		}, true
	case operation.Kind == OperationModeChange &&
		operation.PrivateMode != nil &&
		operation.PrivateMode.Mode == 1049 &&
		!operation.PrivateMode.Enabled:
		return ReadinessBoundary{
			Kind:       ReadinessNormalBufferRestored,
			ByteRange:  operation.ByteRange,
			CapturedAt: operation.CapturedAt,
		}, true
	default:
		return ReadinessBoundary{}, false
	}
}

func readinessBoundaryFromPhaseEvent(event PhaseEvent) (ReadinessBoundary, bool) {
	if event.Phase != PhaseInputApplied {
		return ReadinessBoundary{}, false
	}
	return ReadinessBoundary{
		Kind:       ReadinessInputApplied,
		ByteRange:  event.ByteRange,
		CapturedAt: event.CapturedAt,
	}, true
}
