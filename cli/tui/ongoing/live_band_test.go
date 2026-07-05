package ongoing

import (
	"bytes"
	"fmt"
	"testing"

	"core/shared/clientui"
	"github.com/google/uuid"
)

func TestRenderPaintsLiveAreaWhenMinimumFits(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size: Size{Width: 20, Height: 4},
		Sections: []FrameSection{
			{Kind: FrameSectionPendingTools, Lines: []string{"tool running"}},
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	})
	if err != nil {
		t.Fatalf("render live band: %v", err)
	}

	want := "\x1b[r\x1b[?6l" +
		"\x1b[2;1H\x1b[2K\x1b[3;1H\x1b[2K\x1b[4;1H\x1b[2K" +
		"\x1b[2;1Htool running\x1b[3;1H> prompt\x1b[4;1Hready" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("live band bytes = %q, want %q", got, want)
	}
}

func TestRenderHidesEntireLiveAreaWhenMinimumDoesNotFit(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{
		Size:     Size{Width: 20, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionInput, Lines: []string{"one", "two", "three"}}},
	}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	_, err := surface.Render(FrameInput{
		Size:     Size{Width: 20, Height: 2},
		Sections: []FrameSection{{Kind: FrameSectionInput, Lines: []string{"one", "two", "three"}}},
	})
	if err != nil {
		t.Fatalf("render too-short frame: %v", err)
	}

	want := "\x1b[r\x1b[?6l" +
		"\x1b[1;1H\x1b[2K\x1b[2;1H\x1b[2K" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("too-short live band bytes = %q, want %q", got, want)
	}
}

func TestRenderAddsAssistantTailOnlyFromSurfaceState(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantDelta,
		AssistantDelta: &clientui.TranscriptAssistantDelta{
			StreamID: uuid.New(),
			Delta:    "streaming commentary",
		},
	}, FrameInput{Size: Size{Width: 30, Height: 4}}); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}
	out.Reset()

	_, err := surface.Render(FrameInput{
		Size:     Size{Width: 30, Height: 4},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"status"}}},
	})
	if err != nil {
		t.Fatalf("render assistant tail: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"streaming commentary", "status"})
}

func TestRenderShrinksLiveBandBeforeTerminalCoordinateWrites(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size: Size{Width: 20, Height: 4},
		Sections: []FrameSection{{
			Kind:  FrameSectionInput,
			Lines: []string{"one", "two", "three", "four", "five"},
		}},
	})
	if err != nil {
		t.Fatalf("render oversized live band: %v", err)
	}

	ops := parseTerminalOps(out.String())
	assertCursorAddressRowsAtLeastOne(t, ops)
	assertVisibleTextOps(t, ops, []string{"three", "four", "five"})
}

func TestCommittedRowsReserveTerminalSpaceBeforeLiveBand(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("committed")), FrameInput{
		Size: Size{Width: 40, Height: 3},
		Sections: []FrameSection{
			{Kind: FrameSectionPendingTools, Lines: []string{"tool"}},
			{Kind: FrameSectionInput, Lines: []string{"> prompt"}},
			{Kind: FrameSectionStatus, Lines: []string{"ready"}},
		},
	}); err != nil {
		t.Fatalf("apply committed row: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"❯ committed"})
}

func assertCursorAddressRowsAtLeastOne(t *testing.T, ops []terminalOp) {
	t.Helper()
	for _, op := range ops {
		if op.kind != terminalOpCSI {
			continue
		}
		var row int
		var column int
		if _, err := fmt.Sscanf(op.value, "\x1b[%d;%dH", &row, &column); err == nil && row < 1 {
			t.Fatalf("cursor address row = %d in op %q, want >= 1", row, op.value)
		}
	}
}

func TestHeightOnlyResizeRepaintsWithoutRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{
		Size:     Size{Width: 20, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 20, Height: 4}, FrameInput{
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	})
	if err != nil {
		t.Fatalf("height-only resize: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want noop", result.Action)
	}
	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b[4;1H\x1b[2K\x1b[4;1Hready\x1b[?25l"; got != want {
		t.Fatalf("height-only repaint bytes = %q, want %q", got, want)
	}
}
