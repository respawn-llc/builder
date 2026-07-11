package analyzer

type readinessLog struct {
	boundaries []ReadinessBoundary
}

func readinessLogFromAnalysis(analysis Analysis, dimensions Dimensions) readinessLog {
	var log readinessLog
	for _, operation := range analysis.Operations {
		if boundary, ok := readinessBoundaryFromOperation(operation, dimensions); ok {
			log.append(boundary)
		}
	}
	for _, event := range analysis.PhaseEvents {
		if boundary, ok := readinessBoundaryFromPhaseEvent(event); ok {
			log.append(boundary)
		}
	}
	return log
}

func (log *readinessLog) append(boundary ReadinessBoundary) {
	if log == nil || !boundary.Kind.Valid() {
		return
	}
	log.boundaries = append(log.boundaries, boundary)
}

func (log readinessLog) latestAfter(
	kind ReadinessBoundaryKind,
	afterByteOffset int64,
) (ReadinessBoundary, bool) {
	if !kind.Valid() {
		return ReadinessBoundary{}, false
	}
	return latestAfterByteOffset(
		log.boundaries,
		afterByteOffset,
		func(boundary ReadinessBoundary) bool { return boundary.Kind == kind },
		func(boundary ReadinessBoundary) ByteRange { return boundary.ByteRange },
	)
}

func latestAfterByteOffset[T any](
	values []T,
	afterByteOffset int64,
	matches func(T) bool,
	byteRange func(T) ByteRange,
) (T, bool) {
	var latest T
	var latestEnd int64
	found := false
	for _, value := range values {
		rangeValue := byteRange(value)
		if !matches(value) || rangeValue.End <= afterByteOffset {
			continue
		}
		if !found || rangeValue.End > latestEnd {
			latest = value
			latestEnd = rangeValue.End
			found = true
		}
	}
	return latest, found
}
