package transcriptrender

import (
	"strconv"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestWebSearchUsesCompactSentenceAndExpandedRawQuery(t *testing.T) {
	const query = "Go error handling"
	resultSummary := "summary"
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			ToolName:      string(toolspec.ToolWebSearch),
			Text:          "tool result",
			ResultSummary: &resultSummary,
			Presentation: &transcript.ToolCallMeta{
				ToolName:    string(toolspec.ToolWebSearch),
				Command:     query,
				CompactText: query,
			},
		},
	}
	compact := webSearchDisplayPrefix + strconv.Quote(query)

	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed} {
		rendered := RenderCommittedRow(row, 120, "dark", mode)
		if got := strings.Join(PlainLines(rendered.Lines), "\n"); got != "@ "+compact {
			t.Fatalf("compact web search = %q, want exact preview %q", got, "@ "+compact)
		}
	}

	expanded := strings.Join(PlainLines(RenderCommittedRow(row, 120, "dark", ModeDetailExpanded).Lines), "\n")
	if !strings.Contains(expanded, query) {
		t.Fatalf("expanded web search omits raw query: %q", expanded)
	}
	if strings.Contains(expanded, compact) {
		t.Fatalf("expanded web search used compact presentation: %q", expanded)
	}
	if !strings.Contains(expanded, resultSummary) {
		t.Fatalf("expanded web search omits server output summary: %q", expanded)
	}
}
