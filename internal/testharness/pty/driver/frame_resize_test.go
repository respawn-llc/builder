//go:build !windows

package driver

import (
	"testing"

	"core/internal/testharness/pty/analyzer"
)

func TestFrameResizeDispatchesCompletionAfterPostResizeRendererFrame(t *testing.T) {
	initial := analyzer.MustDimensions(2, 8)
	resized := analyzer.MustDimensions(3, 10)
	tracker, err := analyzer.NewReadinessTracker(initial)
	if err != nil {
		t.Fatalf("NewReadinessTracker: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(); err != nil {
			t.Fatalf("Close readiness tracker: %v", err)
		}
	})
	marker, err := analyzer.Encode(analyzer.Marker{
		Sequence: 1,
		Kind:     analyzer.KindScenarioFinalApplied,
	})
	if err != nil {
		t.Fatalf("Encode marker: %v", err)
	}
	if err := tracker.AdvanceChunk(analyzer.NewChunk(0, 0, append(marker, []byte("\x1b[2;1H")...))); err != nil {
		t.Fatalf("AdvanceChunk initial frame: %v", err)
	}
	dispatcher, err := newFrameResizeDispatcher([]FrameResizeEvent{{
		Phase:           analyzer.KindScenarioFinalApplied,
		Readiness:       analyzer.ReadinessRendererFrame,
		Dimensions:      resized,
		CompletionBytes: []byte{3},
	}})
	if err != nil {
		t.Fatalf("newFrameResizeDispatcher: %v", err)
	}
	resizes := dispatcher.pendingResize(tracker)
	if len(resizes) != 1 || resizes[0].Dimensions != resized {
		t.Fatalf("ready frame resizes = %+v, want one resize to %+v", resizes, resized)
	}
	if err := tracker.AdvanceChunk(analyzer.NewChunk(1, 0, []byte("\x1b[2;1H"))); err != nil {
		t.Fatalf("AdvanceChunk pre-resize frame: %v", err)
	}
	dispatcher.markResized(resizes[0].Index, tracker.ByteCount())
	if completion := dispatcher.pendingCompletion(tracker); len(completion) != 0 {
		t.Fatalf("completion before post-resize frame = %+v, want none", completion)
	}
	if err := tracker.Resize(resized, 0); err != nil {
		t.Fatalf("Resize readiness tracker: %v", err)
	}
	if err := tracker.AdvanceChunk(analyzer.NewChunk(2, 0, []byte("\x1b[3;1H"))); err != nil {
		t.Fatalf("AdvanceChunk post-resize frame: %v", err)
	}
	completion := dispatcher.pendingCompletion(tracker)
	if len(completion) != 1 || completion[0].Phase != analyzer.KindScenarioFinalApplied || string(completion[0].Bytes) != string([]byte{3}) {
		t.Fatalf("post-resize completion = %+v", completion)
	}
}
