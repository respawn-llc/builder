package ongoing

import (
	"bytes"
	"testing"
)

func TestResetForScratchHydrationErasesMutableBandAndClearsVolatileState(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(nonZeroStreamID(t), "volatile"), FrameInput{Size: Size{Width: 30, Height: 3}}); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}
	if _, err := surface.Render(FrameInput{Size: Size{Width: 30, Height: 3}, Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}}}); err != nil {
		t.Fatalf("render frame: %v", err)
	}
	out.Reset()

	result, err := surface.ResetForScratchHydration(RehydrateReasonSequenceGap, FrameInput{Size: Size{Width: 30, Height: 3}})
	if err != nil {
		t.Fatalf("reset for scratch hydration: %v", err)
	}

	if result.Action != ResultRequestScratchRehydration {
		t.Fatalf("reset action = %q, want scratch rehydration", result.Action)
	}
	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b[1;1H\x1b[2K\x1b[2;1H\x1b[2K\x1b[3;1H\x1b[2K\x1b[?25l"; got != want {
		t.Fatalf("reset bytes = %q, want %q", got, want)
	}
	if surface.activeAssistant.source != "" || surface.activeAssistant.streamID != nil {
		t.Fatalf("active assistant state after reset = %+v, want cleared", surface.activeAssistant)
	}
}
