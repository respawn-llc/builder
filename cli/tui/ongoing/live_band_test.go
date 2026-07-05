package ongoing

import (
	"bytes"
	"strings"
	"testing"

	"core/shared/clientui"
	"github.com/charmbracelet/x/ansi"
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

	got := ansi.Strip(out.String())
	if !strings.Contains(got, "streaming commentary") || !strings.Contains(got, "status") {
		t.Fatalf("assistant-tail live band bytes = %q, want markdown tail and status", out.String())
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
