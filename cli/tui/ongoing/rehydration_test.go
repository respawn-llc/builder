package ongoing

import (
	"bytes"
	"errors"
	"testing"

	"core/cli/tui/transcriptrender"
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
	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b]133;C\x1b\\\x1b[2;1H\x1b]133;C\x1b\\\x1b[2K\x1b[3;1H\x1b]133;C\x1b\\\x1b[2K\x1b[?25l"; got != want {
		t.Fatalf("reset bytes = %q, want %q", got, want)
	}
	if surface.activeAssistant.source != "" || surface.activeAssistant.streamID != nil {
		t.Fatalf("active assistant state after reset = %+v, want cleared", surface.activeAssistant)
	}
	if surface.lastPaintedSize == nil || *surface.lastPaintedSize != (Size{Width: 30, Height: 3}) {
		t.Fatalf("painted size after reset = %+v", surface.lastPaintedSize)
	}
}

func TestFreshAndZeroBandScratchResetUseNoPreviousGeometry(t *testing.T) {
	for _, established := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "established-zero"}[established], func(t *testing.T) {
			var out bytes.Buffer
			surface := NewSurface(&out)
			if established {
				if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 5}}); err != nil {
					t.Fatalf("establish zero band: %v", err)
				}
				out.Reset()
			}
			target := FrameInput{Size: Size{Width: 30, Height: 8}}

			if _, err := surface.ResetForScratchHydration(RehydrateReasonSequenceGap, target); err != nil {
				t.Fatalf("reset zero geometry: %v", err)
			}

			if surface.lastPaintedSize == nil || *surface.lastPaintedSize != target.Size ||
				surface.retainedBandHeight != 0 {
				t.Fatalf("reset geometry = size %+v retained %d", surface.lastPaintedSize, surface.retainedBandHeight)
			}
			ops := parseTerminalOps(out.String())
			if countTerminalOp(ops, terminalOpCSI, "\x1b[2K") != 0 ||
				countTerminalOp(ops, terminalOpOSC, redrawableSemanticPromptSequence()) != 0 ||
				countTerminalKind(ops, terminalOpCRLF) != 0 {
				t.Fatalf("zero-geometry reset erased, marked, or scrolled rows: ops=%+v", ops)
			}
		})
	}
}

func TestWidthRehydrationCombinedResizePreservesGeometryUntilAdjustedReset(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurfaceWithOptions(&out, SurfaceOptions{
		TerminalResize: TerminalResizeWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
	initial := FrameInput{
		Size:     Size{Width: 20, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"one", "two"}}},
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("immutable")), initial); err != nil {
		t.Fatalf("establish retained geometry: %v", err)
	}
	paintedBefore := *surface.lastPaintedSize
	retainedBefore := surface.retainedBandHeight
	out.Reset()
	target := FrameInput{Size: Size{Width: 30, Height: 10}}

	result, err := surface.Resize(target.Size, target)
	if err != nil {
		t.Fatalf("schedule combined resize: %v", err)
	}
	if result.Action != ResultScheduleWidthRehydration || out.Len() != 0 {
		t.Fatalf("combined resize result=%+v bytes=%q", result, out.String())
	}
	if surface.lastPaintedSize == nil || *surface.lastPaintedSize != paintedBefore ||
		surface.retainedBandHeight != retainedBefore {
		t.Fatal("scheduled combined resize advanced painted geometry")
	}

	if _, err := surface.ResetForScratchHydration(RehydrateReasonWidthChange, target); err != nil {
		t.Fatalf("apply debounced scratch reset: %v", err)
	}
	if surface.lastPaintedSize == nil || *surface.lastPaintedSize != target.Size ||
		surface.retainedBandHeight != 0 {
		t.Fatalf("reset geometry = size %+v retained %d", surface.lastPaintedSize, surface.retainedBandHeight)
	}
	ops := parseTerminalOps(out.String())
	if got, want := countTerminalOp(ops, terminalOpCSI, "\x1b[2K"), 7; got != want {
		t.Fatalf("reset erase count = %d, want %d", got, want)
	}
	for row := 4; row <= 10; row++ {
		assertCursorAddress(t, ops, row, 1)
	}
}

func TestScratchResetWriterFailurePreservesPaintedAndVolatileState(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	frame := FrameInput{
		Size:     Size{Width: 30, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("immutable")), frame); err != nil {
		t.Fatalf("establish group state: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(nonZeroStreamID(t), "volatile"), frame); err != nil {
		t.Fatalf("establish assistant state: %v", err)
	}
	paintedBefore := *surface.lastPaintedSize
	retainedBefore := surface.retainedBandHeight
	assistantBefore := surface.activeAssistant
	groupBefore := *surface.groupRegister
	wantErr := errors.New("terminal closed")
	surface.writer = failingWriter{err: wantErr}

	_, err := surface.ResetForScratchHydration(
		RehydrateReasonWidthChange,
		FrameInput{Size: Size{Width: 40, Height: 10}},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("reset error = %v, want %v", err, wantErr)
	}
	if surface.lastPaintedSize == nil || *surface.lastPaintedSize != paintedBefore ||
		surface.retainedBandHeight != retainedBefore ||
		surface.activeAssistant != assistantBefore ||
		surface.groupRegister == nil || *surface.groupRegister != groupBefore {
		t.Fatalf(
			"failed reset mutated state: size=%+v retained=%d assistant=%+v group=%+v",
			surface.lastPaintedSize,
			surface.retainedBandHeight,
			surface.activeAssistant,
			surface.groupRegister,
		)
	}
}
