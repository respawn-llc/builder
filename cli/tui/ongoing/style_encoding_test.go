package ongoing

import (
	"strings"
	"testing"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
)

func TestTranscriptStyleEncodingPreservesOngoingGeometryAndForegroundOnlyPaint(t *testing.T) {
	line := transcriptrender.Line{Spans: []transcriptrender.Span{
		transcriptrender.SemanticSpan("F", transcriptrender.StyleRoleMarkdownCode, transcriptrender.SpanAttributeFaint),
		transcriptrender.SemanticSpan("B", transcriptrender.StyleRoleToolShellSecondary, transcriptrender.SpanAttributeBold),
		transcriptrender.SemanticSpan("I", transcriptrender.StyleRoleWarning, transcriptrender.SpanAttributeItalic),
		transcriptrender.SemanticSpan("U", transcriptrender.StyleRoleError, transcriptrender.SpanAttributeUnderline),
	}}

	var encoded strings.Builder
	for _, span := range line.Spans {
		encoded.WriteString("\x1b[48;2;1;2;3m")
		encoded.WriteString(encodeTranscriptSpan(span, "dark"))
	}
	capture, err := pty.NewCapture(
		pty.MustDimensions(1, 8),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte(encoded.String()))},
	)
	if err != nil {
		t.Fatalf("create style capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze style capture: %v", err)
	}

	cells := analysis.Screen.Cells[0]
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 4}); got != line.Plain() {
		t.Fatalf("styled visible text = %q, want %q", got, line.Plain())
	}
	if cells[0].Foreground == "" || !cells[0].Faint {
		t.Fatalf("faint foreground cell = %+v, want colored faint text", cells[0])
	}
	if cells[1].Foreground == "" || !cells[1].Bold {
		t.Fatalf("bold foreground cell = %+v, want colored bold text", cells[1])
	}
	if cells[2].Foreground == "" || !cells[2].Italic {
		t.Fatalf("italic foreground cell = %+v, want colored italic text", cells[2])
	}
	if cells[3].Foreground == "" || !cells[3].Underline {
		t.Fatalf("underline foreground cell = %+v, want colored underlined text", cells[3])
	}
	paintedBackground := cells[0].Background
	if paintedBackground == "" {
		t.Fatal("sentinel terminal background was not applied")
	}
	for index, cell := range cells {
		if index < 4 && cell.Background != paintedBackground {
			t.Fatalf("cell %d background = %q, want inherited terminal background %q", index, cell.Background, paintedBackground)
		}
		if index >= 4 && (cell.Content != "" || cell.Foreground != "" || cell.Faint || cell.Bold || cell.Italic || cell.Underline) {
			t.Fatalf("trailing cell %d = %+v, ongoing transcript must not add full-width padding", index, cell)
		}
	}
}
