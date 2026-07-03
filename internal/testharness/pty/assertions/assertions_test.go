package assertions_test

import (
	"testing"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/assertions"
)

func TestNoWritesAboveFailsForAboveBoundaryWrite(t *testing.T) {
	t.Parallel()

	err := assertions.NoWritesAbove(analysisWithOps(
		writeOp(0, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 3}, "old"),
	), pty.OperationWindow{Start: 0, End: 1}, 1)
	if err == nil {
		t.Fatalf("NoWritesAbove succeeded for write above boundary")
	}
}

func TestErasesOnlyWithinFailsForRegionScopedErase(t *testing.T) {
	t.Parallel()

	err := assertions.ErasesOnlyWithin(analysisWithOps(
		eraseOp(0, pty.Region{Top: 0, Bottom: 2, Left: 0, Right: 4}),
	), pty.OperationWindow{Start: 0, End: 1}, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4})
	if err == nil {
		t.Fatalf("ErasesOnlyWithin succeeded for erase outside allowed region")
	}
}

func TestNoFullScreenReEmissionFailsForClearAndRefill(t *testing.T) {
	t.Parallel()

	err := assertions.NoFullScreenReEmission(analysisWithOps(
		eraseOp(0, pty.Region{Top: 0, Bottom: 2, Left: 0, Right: 4}),
		writeOp(1, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}, "top"),
		writeOp(2, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}, "bottom"),
	), pty.OperationWindow{Start: 0, End: 3})
	if err == nil {
		t.Fatalf("NoFullScreenReEmission succeeded for full-screen clear and refill")
	}
}

func TestNoFullScreenReEmissionAggregatesSplitWrites(t *testing.T) {
	t.Parallel()

	err := assertions.NoFullScreenReEmission(analysisWithOps(
		eraseOp(0, pty.Region{Top: 0, Bottom: 2, Left: 0, Right: 4}),
		writeOp(1, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 2}, "to"),
		writeOp(2, pty.Region{Top: 0, Bottom: 1, Left: 2, Right: 4}, "p!"),
		writeOp(3, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}, "bottom"),
	), pty.OperationWindow{Start: 0, End: 4})
	if err == nil {
		t.Fatalf("NoFullScreenReEmission succeeded for split full-screen refill")
	}
}

func TestNoFullScreenReEmissionAggregatesSplitErases(t *testing.T) {
	t.Parallel()

	err := assertions.NoFullScreenReEmission(analysisWithOps(
		eraseOp(0, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}),
		eraseOp(1, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}),
		writeOp(2, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}, "top"),
		writeOp(3, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}, "bottom"),
	), pty.OperationWindow{Start: 0, End: 4})
	if err == nil {
		t.Fatalf("NoFullScreenReEmission succeeded for split full-screen erase and refill")
	}
}

func TestNoFullScreenReEmissionDetectsRowByRowEraseThenRewrite(t *testing.T) {
	t.Parallel()

	err := assertions.NoFullScreenReEmission(analysisWithOps(
		eraseOp(0, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}),
		writeOp(1, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}, "top"),
		eraseOp(2, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}),
		writeOp(3, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}, "bottom"),
	), pty.OperationWindow{Start: 0, End: 4})
	if err == nil {
		t.Fatalf("NoFullScreenReEmission succeeded for row-by-row erase and rewrite")
	}
}

func TestNoRegionReEmissionFailsForImmutableRegionRefill(t *testing.T) {
	t.Parallel()

	err := assertions.NoRegionReEmission(analysisWithOps(
		eraseOp(0, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}),
		writeOp(1, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 2}, "im"),
		writeOp(2, pty.Region{Top: 0, Bottom: 1, Left: 2, Right: 4}, "mu"),
	), pty.OperationWindow{Start: 0, End: 3}, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4})
	if err == nil {
		t.Fatalf("NoRegionReEmission succeeded for immutable-region refill")
	}
}

func TestContentAppendedExactlyOnceFailsForDuplicateAppend(t *testing.T) {
	t.Parallel()

	err := assertions.ContentAppendedExactlyOnce([]pty.AppendOperation{
		{Operation: writeOp(0, pty.Region{Top: 1, Bottom: 2, Left: 0, Right: 4}, "same")},
		{Operation: writeOp(1, pty.Region{Top: 1, Bottom: 2, Left: 4, Right: 8}, "same")},
	}, "same")
	if err == nil {
		t.Fatalf("ContentAppendedExactlyOnce succeeded for duplicate append")
	}
}

func TestNoAlternateScroll1007FailsForEnabledMode(t *testing.T) {
	t.Parallel()

	err := assertions.NoAlternateScroll1007(pty.Analysis{
		Operations: []pty.Operation{{
			Kind:        pty.OperationModeChange,
			ChunkIndex:  1,
			ByteRange:   pty.ByteRange{Start: 2, End: 8},
			PrivateMode: &pty.PrivateModeChange{Mode: 1007, Enabled: true, ChunkIndex: 1, ByteRange: pty.ByteRange{Start: 2, End: 8}},
		}},
	})
	if err == nil {
		t.Fatalf("NoAlternateScroll1007 succeeded for enabled ?1007")
	}
}

func TestNoAlternateScroll1007CanBeWindowScoped(t *testing.T) {
	t.Parallel()

	analysis := pty.Analysis{
		Operations: []pty.Operation{
			writeOp(0, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 1}, "a"),
			{
				Sequence:    1,
				Kind:        pty.OperationModeChange,
				ChunkIndex:  1,
				ByteRange:   pty.ByteRange{Start: 2, End: 8},
				PrivateMode: &pty.PrivateModeChange{Mode: 1007, Enabled: true, ChunkIndex: 1, ByteRange: pty.ByteRange{Start: 2, End: 8}},
			},
		},
	}
	if err := assertions.NoAlternateScroll1007(analysis, pty.OperationWindow{Start: 0, End: 1}); err != nil {
		t.Fatalf("NoAlternateScroll1007 outside window: %v", err)
	}
	if err := assertions.NoAlternateScroll1007(analysis, pty.OperationWindow{Start: 1, End: 2}); err == nil {
		t.Fatalf("NoAlternateScroll1007 succeeded inside forbidden window")
	}
}

func TestNoAlternateScroll1007ChecksEveryProvidedWindow(t *testing.T) {
	t.Parallel()

	analysis := pty.Analysis{
		Operations: []pty.Operation{
			writeOp(0, pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 1}, "a"),
			{
				Sequence:    1,
				Kind:        pty.OperationModeChange,
				ChunkIndex:  1,
				ByteRange:   pty.ByteRange{Start: 2, End: 8},
				PrivateMode: &pty.PrivateModeChange{Mode: 1007, Enabled: true, ChunkIndex: 1, ByteRange: pty.ByteRange{Start: 2, End: 8}},
			},
		},
	}

	if err := assertions.NoAlternateScroll1007(analysis, pty.OperationWindow{Start: 0, End: 1}, pty.OperationWindow{Start: 1, End: 2}); err == nil {
		t.Fatalf("NoAlternateScroll1007 succeeded when second provided window enabled ?1007")
	}
}

func analysisWithOps(ops ...pty.Operation) pty.Analysis {
	return pty.Analysis{
		Dimensions: pty.MustDimensions(2, 4),
		Operations: ops,
	}
}

func writeOp(sequence int, region pty.Region, text string) pty.Operation {
	payload := pty.MustWritePayload(text)
	return pty.Operation{Sequence: sequence, Kind: pty.OperationWrite, Region: region, Write: &payload, ByteRange: pty.ByteRange{Start: int64(sequence), End: int64(sequence + 1)}}
}

func eraseOp(sequence int, region pty.Region) pty.Operation {
	return pty.Operation{Sequence: sequence, Kind: pty.OperationErase, Region: region, ByteRange: pty.ByteRange{Start: int64(sequence), End: int64(sequence + 1)}}
}
