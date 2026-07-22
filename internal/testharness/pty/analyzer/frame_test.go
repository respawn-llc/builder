package analyzer

import "testing"

func TestLatestReadinessBoundaryAfterSeparatesNormalBufferRestore(t *testing.T) {
	restore := PrivateModeChange{
		Mode:      1049,
		Enabled:   false,
		ByteRange: ByteRange{Start: 8, End: 16},
	}
	operations := []Operation{
		{
			Kind:        OperationCursorMove,
			ByteRange:   ByteRange{Start: 2, End: 4},
			After:       Position{Row: 23, Col: 0},
			PrivateMode: nil,
		},
		{
			Kind:        OperationModeChange,
			ByteRange:   ByteRange{Start: 8, End: 16},
			PrivateMode: &restore,
		},
	}

	analysis := Analysis{Operations: operations}
	frame, ok := LatestReadinessBoundaryAfter(
		analysis,
		MustDimensions(24, 80),
		ReadinessNormalBufferRestored,
		4,
	)
	if !ok {
		t.Fatal("normal-buffer restore was not recognized")
	}
	if frame.ByteRange != restore.ByteRange {
		t.Fatalf("normal-buffer restore range = %+v, want %+v", frame.ByteRange, restore.ByteRange)
	}
	if frame, ok := LatestReadinessBoundaryAfter(
		analysis,
		MustDimensions(24, 80),
		ReadinessRendererFrame,
		4,
	); ok {
		t.Fatalf("normal-buffer restore was recognized as a renderer frame: %+v", frame)
	}
}

func TestLatestReadinessBoundaryAfterRejectsAltScreenEntry(t *testing.T) {
	entry := PrivateModeChange{Mode: 1049, Enabled: true}
	analysis := Analysis{Operations: []Operation{{
		Kind:        OperationModeChange,
		ByteRange:   ByteRange{Start: 8, End: 16},
		PrivateMode: &entry,
	}}}

	if frame, ok := LatestReadinessBoundaryAfter(
		analysis,
		MustDimensions(24, 80),
		ReadinessNormalBufferRestored,
		0,
	); ok {
		t.Fatalf("alt-screen entry was recognized as a completed frame: %+v", frame)
	}
}

func TestLatestReadinessBoundaryAfterRecognizesInputAppliedPhase(t *testing.T) {
	event := PhaseEvent{
		Phase:     PhaseInputApplied,
		ByteRange: ByteRange{Start: 8, End: 16},
	}
	boundary, ok := LatestReadinessBoundaryAfter(
		Analysis{PhaseEvents: []PhaseEvent{event}},
		MustDimensions(24, 80),
		ReadinessInputApplied,
		0,
	)
	if !ok || boundary.ByteRange != event.ByteRange {
		t.Fatalf("input-applied boundary = %+v/%t, want %+v", boundary, ok, event.ByteRange)
	}
}
