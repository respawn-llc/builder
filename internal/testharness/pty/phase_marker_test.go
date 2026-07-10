package pty_test

import (
	"testing"
	"time"

	"core/internal/testharness/pty"
)

func TestPhaseMarkerSplitAcrossChunksRecordsExactByteOffsets(t *testing.T) {
	t.Parallel()

	windowID := mustWindowID(t)
	start := marker(t, 1, "WindowStart", windowID)
	end := marker(t, 2, "WindowEnd", windowID)
	capture, err := pty.NewCapture(
		pty.MustDimensions(2, 10),
		[]pty.Chunk{
			pty.NewChunk(0, time.Millisecond, []byte("a"+start[:6])),
			pty.NewChunk(1, 2*time.Millisecond, []byte(start[6:]+"b"+end)),
		},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.PhaseEvents) != 2 {
		t.Fatalf("phase event count = %d, want 2: %#v", len(analysis.PhaseEvents), analysis.PhaseEvents)
	}
	if got := analysis.PhaseEvents[0].ByteRange; got != (pty.ByteRange{Start: 1, End: int64(1 + len(start))}) {
		t.Fatalf("start marker byte range = %#v, want exact split-marker range", got)
	}
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 2}); got != "ab" {
		t.Fatalf("screen text = %q, want marker excluded from screen", got)
	}
}

func TestPhaseMarkerMalformedAndOutOfOrderErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "bad base64", payload: []byte("\x1b]777;kent-pty-checkpoint;not-base64!\a")},
		{name: "unknown phase", payload: []byte(marker(t, 1, "Unknown", mustWindowID(t)))},
		{name: "out of order", payload: []byte(markerRaw(t, 2, "ScenarioStart", nil) + markerRaw(t, 1, "ScenarioComplete", nil))},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := pty.Analyze(mustCapture(t, tc.payload, pty.MustDimensions(2, 8)))
			if err == nil {
				t.Fatalf("Analyze succeeded for malformed marker")
			}
		})
	}
}
