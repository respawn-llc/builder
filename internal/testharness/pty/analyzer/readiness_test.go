package analyzer

import (
	"slices"
	"testing"
	"time"
)

func TestReadinessTrackerConsumesPhaseAndFramesIncrementally(t *testing.T) {
	marker, err := EncodePhaseMarker(PhaseMarker{Sequence: 1, Phase: PhaseScenarioComplete})
	if err != nil {
		t.Fatalf("encode phase marker: %v", err)
	}
	tracker, err := NewReadinessTracker(MustDimensions(2, 16))
	if err != nil {
		t.Fatalf("new readiness tracker: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Errorf("close readiness tracker: %v", err)
		}
	})

	if err := tracker.AdvanceChunk(NewChunk(0, time.Millisecond, marker)); err != nil {
		t.Fatalf("advance marker chunk: %v", err)
	}
	if got := tracker.PhaseEvents(); len(got) != 1 || got[0].Phase != PhaseScenarioComplete {
		t.Fatalf("phase events = %+v, want scenario complete", got)
	}
	if _, ok := tracker.LatestBoundaryAfter(ReadinessRendererFrame, 0); ok {
		t.Fatal("phase marker alone produced a completed frame")
	}

	if err := tracker.AdvanceChunk(NewChunk(1, 2*time.Millisecond, []byte("\x1b[2;1H"))); err != nil {
		t.Fatalf("advance frame chunk: %v", err)
	}
	frame, ok := tracker.LatestBoundaryAfter(ReadinessRendererFrame, int64(len(marker)))
	if !ok {
		t.Fatal("bottom-left cursor park did not complete a frame")
	}
	if frame.ByteRange.End != tracker.ByteCount() {
		t.Fatalf("frame end = %d, byte count = %d", frame.ByteRange.End, tracker.ByteCount())
	}
}

func TestReadinessTrackerSeparatesInputAndSurfaceBoundaries(t *testing.T) {
	tracker, err := NewReadinessTracker(MustDimensions(2, 16))
	if err != nil {
		t.Fatalf("new readiness tracker: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Errorf("close readiness tracker: %v", err)
		}
	})

	inputMarker, err := EncodePhaseMarker(PhaseMarker{Sequence: 1, Phase: PhaseInputApplied})
	if err != nil {
		t.Fatalf("encode input marker: %v", err)
	}
	if err := tracker.AdvanceChunk(NewChunk(0, time.Millisecond, inputMarker)); err != nil {
		t.Fatalf("advance input marker: %v", err)
	}
	inputBoundary, ok := tracker.LatestBoundaryAfter(ReadinessInputApplied, 0)
	if !ok || inputBoundary.ByteRange.End != int64(len(inputMarker)) {
		t.Fatalf("input boundary = %+v/%t, want marker ending at %d", inputBoundary, ok, len(inputMarker))
	}
	if _, ok := tracker.LatestBoundaryAfter(ReadinessRendererFrame, 0); ok {
		t.Fatal("input marker was classified as a renderer frame")
	}

	restore := []byte("\x1b[?1049l")
	if err := tracker.AdvanceChunk(NewChunk(1, 2*time.Millisecond, restore)); err != nil {
		t.Fatalf("advance normal-buffer restore: %v", err)
	}
	restoreBoundary, ok := tracker.LatestBoundaryAfter(ReadinessNormalBufferRestored, int64(len(inputMarker)))
	if !ok || restoreBoundary.ByteRange.End != tracker.ByteCount() {
		t.Fatalf("normal-buffer boundary = %+v/%t, byte count = %d", restoreBoundary, ok, tracker.ByteCount())
	}
	if _, ok := tracker.LatestBoundaryAfter(ReadinessRendererFrame, int64(len(inputMarker))); ok {
		t.Fatal("normal-buffer restore was classified as a renderer frame")
	}
}

func TestReadinessTrackerAndFullAnalysisUseEquivalentBoundaryLookup(t *testing.T) {
	marker, err := EncodePhaseMarker(PhaseMarker{Sequence: 1, Phase: PhaseInputApplied})
	if err != nil {
		t.Fatalf("encode input-applied marker: %v", err)
	}
	chunks := []Chunk{
		NewChunk(0, time.Millisecond, marker),
		NewChunk(1, 2*time.Millisecond, []byte("\x1b[2;1H")),
		NewChunk(2, 3*time.Millisecond, []byte("\x1b[?1049l")),
	}
	dimensions := MustDimensions(2, 16)
	tracker, err := NewReadinessTracker(dimensions)
	if err != nil {
		t.Fatalf("new readiness tracker: %v", err)
	}
	for _, chunk := range chunks {
		if err := tracker.AdvanceChunk(chunk); err != nil {
			t.Fatalf("advance chunk %d: %v", chunk.Index, err)
		}
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Errorf("close readiness tracker: %v", err)
		}
	})

	capture, err := NewCapture(dimensions, chunks)
	if err != nil {
		t.Fatalf("new capture: %v", err)
	}
	analysis, err := Analyze(capture)
	if err != nil {
		t.Fatalf("analyze capture: %v", err)
	}
	kinds := []ReadinessBoundaryKind{
		ReadinessInputApplied,
		ReadinessRendererFrame,
		ReadinessNormalBufferRestored,
	}
	for _, kind := range kinds {
		incremental, incrementalOK := tracker.LatestBoundaryAfter(kind, 0)
		full, fullOK := LatestReadinessBoundaryAfter(analysis, dimensions, kind, 0)
		if incrementalOK != fullOK || !slices.Equal(
			[]ReadinessBoundary{incremental},
			[]ReadinessBoundary{full},
		) {
			t.Fatalf(
				"boundary kind %d incremental=%+v/%t full=%+v/%t",
				kind,
				incremental,
				incrementalOK,
				full,
				fullOK,
			)
		}
	}
}
