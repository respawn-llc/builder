package tui

import (
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	tuitest "core/internal/testharness/pty"
)

func TestAskQuestionMarkdownLinksAdaptToTerminalPresentation(t *testing.T) {
	const target = "https://example.com/question"
	for _, test := range []struct {
		name                string
		presentation        transcriptrender.MarkdownLinkPresentation
		wantVisibleText     string
		wantLinkedText      string
		wantHyperlinkStarts int
	}{
		{
			name:                "supported terminal",
			presentation:        transcriptrender.MarkdownLinkLabelOnly,
			wantVisibleText:     "question",
			wantLinkedText:      "question",
			wantHyperlinkStarts: 1,
		},
		{
			name:                "fallback terminal",
			presentation:        transcriptrender.MarkdownLinkLabelAndDestination,
			wantVisibleText:     "question " + target,
			wantLinkedText:      "question" + target,
			wantHyperlinkStarts: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded := strings.Join(
				RenderAskQuestionMarkdownLines(
					"[question]("+target+")",
					"dark",
					80,
					test.presentation,
				),
				"\n",
			)
			trace := tuitest.TraceTerminalHyperlinks(t, encoded)
			if got := trace.VisibleText(); got != test.wantVisibleText {
				t.Fatalf("visible text = %q, want %q", got, test.wantVisibleText)
			}
			if got := trace.LinkedText(target); got != test.wantLinkedText {
				t.Fatalf("linked text = %q, want %q", got, test.wantLinkedText)
			}
			if got := trace.OpenCount(target); got != test.wantHyperlinkStarts {
				t.Fatalf("OSC 8 start count = %d, want %d: %q", got, test.wantHyperlinkStarts, encoded)
			}
		})
	}
}
