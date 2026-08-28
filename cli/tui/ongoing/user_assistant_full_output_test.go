package ongoing

import (
	"bytes"
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestCommittedAssistantCommentaryUsesFullStableOutput(t *testing.T) {
	condensed := "This compact preview must never be shown."
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text:          "The complete first commentary paragraph.\n\nThe complete second commentary paragraph.",
			CondensedText: &condensed,
			Phase:         transcript.AssistantPhaseCommentary,
		},
	}

	if got := committedRowRenderMode(row); got != transcriptrender.ModeOngoingStable {
		t.Fatalf("commentary render mode = %d, want stable full output", got)
	}
	rendered := transcriptrender.RenderCommittedRow(row, 32, "dark", committedRowRenderMode(row))
	text := strings.Join(transcriptrender.PlainLines(rendered.Lines), "\n")
	if !strings.Contains(text, "complete first commentary") ||
		!strings.Contains(text, "complete second commentary") {
		t.Fatalf("commentary output omitted source content: %q", text)
	}
	if strings.Contains(text, condensed) || strings.Contains(text, "…") {
		t.Fatalf("commentary output used a compact preview: %q", text)
	}

	for _, delivery := range []struct {
		name   string
		render func(*Surface) []string
	}{
		{
			name: "ordinary emission",
			render: func(surface *Surface) []string {
				return surface.renderCommittedRow(row, 32, "dark")
			},
		},
		{
			name: "hydration",
			render: func(surface *Surface) []string {
				return surface.renderHydratedCommittedRow(row, 32, "dark")
			},
		},
	} {
		t.Run(delivery.name, func(t *testing.T) {
			text := xansi.Strip(strings.Join(delivery.render(NewSurface()), "\n"))
			if !strings.Contains(text, "complete first commentary") ||
				!strings.Contains(text, "complete second commentary") {
				t.Fatalf("%s omitted commentary source: %q", delivery.name, text)
			}
			if strings.Contains(text, condensed) || strings.Contains(text, "…") {
				t.Fatalf("%s used a compact preview: %q", delivery.name, text)
			}
		})
	}
}

func TestStreamingAssistantCommentaryShowsCompleteReceivedSource(t *testing.T) {
	var output bytes.Buffer
	surface := NewSurface(&output)
	streamID := runtimeids.NewAssistantStreamID()
	source := "The complete streamed first paragraph.\n\nThe complete streamed second paragraph."

	if _, err := surface.applyAssistantDelta(
		streamID,
		source,
		transcript.AssistantPhaseCommentary,
		FrameInput{Size: Size{Width: 48, Height: 24}},
	); err != nil {
		t.Fatalf("apply assistant commentary delta: %v", err)
	}

	text := xansi.Strip(output.String())
	if !strings.Contains(text, "complete streamed first paragraph") ||
		!strings.Contains(text, "complete streamed second paragraph") {
		t.Fatalf("streaming commentary omitted received source: %q", text)
	}
	if strings.Contains(text, "…") {
		t.Fatalf("streaming commentary was ellipsized: %q", text)
	}
}
