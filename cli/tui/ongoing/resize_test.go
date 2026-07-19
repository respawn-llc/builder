package ongoing

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/runtimeids"
)

func redrawablePromptBoundaryRows(ops []terminalOp) []int {
	rows := make([]int, 0)
	currentRow := 0
	for _, op := range ops {
		if op.kind == terminalOpCSI {
			var column int
			if _, err := fmt.Sscanf(op.value, "\x1b[%d;%dH", &currentRow, &column); err != nil {
				continue
			}
		}
		if op.kind == terminalOpOSC && op.value == redrawableSemanticPromptSequence() {
			rows = append(rows, currentRow)
		}
	}
	return rows
}

func TestHeightGrowthPreservesRetainedPhysicalBand(t *testing.T) {
	for _, policy := range []TerminalResizePolicy{
		TerminalResizeSemanticPrompt,
		TerminalResizeWidthRehydration,
	} {
		for _, visible := range []bool{false, true} {
			t.Run(fmt.Sprintf("policy-%d-visible-%t", policy, visible), func(t *testing.T) {
				var out bytes.Buffer
				surface := NewSurfaceWithOptions(&out, SurfaceOptions{
					TerminalResize: policy,
					MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
				})
				initial := FrameInput{
					Size:     Size{Width: 30, Height: 5},
					Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"one", "two"}}},
				}
				if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("immutable")), initial); err != nil {
					t.Fatalf("establish retained band with immutable output: %v", err)
				}
				out.Reset()
				resizedFrame := FrameInput{}
				if visible {
					resizedFrame.Sections = []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}}
				}

				result, err := surface.Resize(Size{Width: 30, Height: 10}, resizedFrame)
				if err != nil {
					t.Fatalf("grow terminal height: %v", err)
				}
				if result.Action != ResultNoop {
					t.Fatalf("height growth action = %q, want noop", result.Action)
				}
				if got, want := surface.retainedBandHeight, 7; got != want {
					t.Fatalf("retained height after growth = %d, want %d", got, want)
				}
				if surface.lastPaintedSize == nil || *surface.lastPaintedSize != (Size{Width: 30, Height: 10}) {
					t.Fatalf("painted size after growth = %+v", surface.lastPaintedSize)
				}
				ops := parseTerminalOps(out.String())
				if got, want := redrawablePromptBoundaryRows(ops), []int{4}; !slices.Equal(got, want) {
					t.Fatalf("redrawable boundary rows = %v, want %v; ops=%+v", got, want, ops)
				}
				if got := countTerminalKind(ops, terminalOpCRLF); got != 0 {
					t.Fatalf("height growth scrolled immutable region with %d CRLFs: ops=%+v", got, ops)
				}
				if visible {
					assertCursorAddress(t, ops, 10, 1)
				}
			})
		}
	}
}

func TestHeightShrinkClampsRetainedPhysicalBand(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	initial := FrameInput{
		Size: Size{Width: 30, Height: 10},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"one", "two", "three", "four", "five", "six", "seven"},
		}},
	}
	if _, err := surface.Render(initial); err != nil {
		t.Fatalf("render initial retained band: %v", err)
	}
	out.Reset()

	if _, err := surface.Resize(Size{Width: 30, Height: 5}, FrameInput{
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}); err != nil {
		t.Fatalf("shrink terminal height: %v", err)
	}

	if got, want := surface.retainedBandHeight, 5; got != want {
		t.Fatalf("retained height after shrink = %d, want %d", got, want)
	}
	if surface.lastPaintedSize == nil || *surface.lastPaintedSize != (Size{Width: 30, Height: 5}) {
		t.Fatalf("painted size after shrink = %+v", surface.lastPaintedSize)
	}
	ops := parseTerminalOps(out.String())
	if got, want := redrawablePromptBoundaryRows(ops), []int{1}; !slices.Equal(got, want) {
		t.Fatalf("redrawable boundary rows = %v, want %v; ops=%+v", got, want, ops)
	}
	if got := countTerminalKind(ops, terminalOpCRLF); got != 0 {
		t.Fatalf("height shrink scrolled immutable region with %d CRLFs: ops=%+v", got, ops)
	}
	assertCursorAddress(t, ops, 5, 1)
}

func TestWidthChangeBeforeImmutableScrollbackRepaintsOnly(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}, Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}}})
	if err != nil {
		t.Fatalf("width resize before immutable: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want noop", result.Action)
	}
	if got := out.String(); got == "" {
		t.Fatal("width resize before immutable did not repaint mutable band")
	}
}

func TestWidthChangeAfterCommittedRowRepaintsWithoutRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("immutable")), FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("append committed row: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{})
	if err != nil {
		t.Fatalf("width resize after immutable: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want repaint-only noop", result.Action)
	}
	if got := out.String(); got == "" {
		t.Fatal("width resize after immutable did not repaint mutable band")
	}
}

func TestWidthChangeAfterAssistantPromotionRepaintsWithoutRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(runtimeids.NewAssistantStreamID(), "stable\n\n"), FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("promote assistant row: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{})
	if err != nil {
		t.Fatalf("width resize after assistant promotion: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want repaint-only noop", result.Action)
	}
}

func TestAppleTerminalWidthChangeSchedulesRehydrationAfterImmutableScrollback(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurfaceWithOptions(&out, SurfaceOptions{
		TerminalResize: TerminalResizeWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
	if _, err := surface.ApplyTerminalMessage(
		committedMessage(userRow("immutable")),
		FrameInput{Size: Size{Width: 20, Height: 3}},
	); err != nil {
		t.Fatalf("append committed row: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{})
	if err != nil {
		t.Fatalf("resize fallback surface: %v", err)
	}
	if result.Action != ResultScheduleWidthRehydration {
		t.Fatalf("resize action = %q, want width rehydration schedule", result.Action)
	}
	if result.Reason != RehydrateReasonWidthChange {
		t.Fatalf("resize reason = %q, want width change", result.Reason)
	}
	if out.Len() != 0 {
		t.Fatalf("fallback resize wrote before debounce: %q", out.String())
	}
}

func TestAppleTerminalWidthChangeBeforeImmutableScrollbackRepaints(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurfaceWithOptions(&out, SurfaceOptions{
		TerminalResize: TerminalResizeWidthRehydration,
		MarkdownLinks:  transcriptrender.MarkdownLinkLabelOnly,
	})
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{})
	if err != nil {
		t.Fatalf("resize fallback surface: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want repaint before immutable scrollback", result.Action)
	}
	if out.Len() == 0 {
		t.Fatal("fallback resize before immutable scrollback did not repaint")
	}
}
