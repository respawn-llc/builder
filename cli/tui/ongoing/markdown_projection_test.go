package ongoing

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	tuitest "core/internal/testharness/pty"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestTerminalMarkdownRendererAdaptsExplicitLinksToTerminalCapabilities(t *testing.T) {
	for _, presentation := range []struct {
		name                string
		links               transcriptrender.MarkdownLinkPresentation
		wantVisibleText     func(string) string
		wantLinkedText      func(string) string
		wantHyperlinkStarts int
	}{
		{
			name:                "supported terminal",
			links:               transcriptrender.MarkdownLinkLabelOnly,
			wantVisibleText:     func(string) string { return "target" },
			wantLinkedText:      func(string) string { return "target" },
			wantHyperlinkStarts: 1,
		},
		{
			name:                "fallback terminal",
			links:               transcriptrender.MarkdownLinkLabelAndDestination,
			wantVisibleText:     func(target string) string { return "target " + target },
			wantLinkedText:      func(target string) string { return "target" + target },
			wantHyperlinkStarts: 2,
		},
	} {
		t.Run(presentation.name, func(t *testing.T) {
			renderer := terminalMarkdownRenderer{
				themeName:        "dark",
				linkPresentation: presentation.links,
			}
			renders := map[string]func(string, int) []string{
				"volatile": renderer.Render,
				"stable":   renderer.RenderStable,
			}
			for name, render := range renders {
				t.Run(name, func(t *testing.T) {
					for _, target := range []string{
						"https://github.com/respawn-llc/kent/pull/574",
						"/Users/example/project/main.go:42",
					} {
						encoded := strings.Join(render("[target]("+target+")", 80), "\n")
						trace := tuitest.TraceTerminalHyperlinks(t, encoded)
						if got, want := trace.VisibleText(), presentation.wantVisibleText(target); got != want {
							t.Fatalf("visible text = %q, want %q", got, want)
						}
						if got, want := trace.LinkedText(target), presentation.wantLinkedText(target); got != want {
							t.Fatalf("linked text = %q, want %q", got, want)
						}
						if count := trace.OpenCount(target); count != presentation.wantHyperlinkStarts {
							t.Fatalf("hyperlink starts = %d, want %d for %q: %q", count, presentation.wantHyperlinkStarts, target, encoded)
						}
					}
				})
			}
		})
	}
}

func TestTerminalMarkdownRendererDoesNotDuplicateAutolinkDestination(t *testing.T) {
	const target = "https://example.com/autolink"
	for _, presentation := range []transcriptrender.MarkdownLinkPresentation{
		transcriptrender.MarkdownLinkLabelOnly,
		transcriptrender.MarkdownLinkLabelAndDestination,
	} {
		encoded := strings.Join(
			terminalMarkdownRenderer{
				themeName:        "dark",
				linkPresentation: presentation,
			}.Render("<"+target+">", 80),
			"\n",
		)
		trace := tuitest.TraceTerminalHyperlinks(t, encoded)
		if got := trace.VisibleText(); got != target {
			t.Fatalf("autolink visible text = %q, want %q", got, target)
		}
		if got := trace.LinkedText(target); got != target {
			t.Fatalf("autolink linked text = %q, want %q", got, target)
		}
		if got := trace.OpenCount(target); got != 1 {
			t.Fatalf("autolink OSC 8 start count = %d, want 1: %q", got, encoded)
		}
	}
}

func TestTerminalMarkdownRendererKeepsAdjacentHyperlinkTargetsDistinct(t *testing.T) {
	const first = "https://example.com/first"
	const second = "https://example.com/second"
	encoded := strings.Join(
		terminalMarkdownRenderer{
			themeName:        "dark",
			linkPresentation: transcriptrender.MarkdownLinkLabelOnly,
		}.Render(
			"[first]("+first+")[second]("+second+")",
			80,
		),
		"\n",
	)
	trace := tuitest.TraceTerminalHyperlinks(t, encoded)
	if got := trace.LinkedText(first); got != "first" {
		t.Fatalf("first linked text = %q, want first", got)
	}
	if got := trace.LinkedText(second); got != "second" {
		t.Fatalf("second linked text = %q, want second", got)
	}
}

func TestAssistantDeltaPromotesClosedParagraphAndKeepsTailMutable(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := runtimeids.NewAssistantStreamID()

	if _, err := surface.ApplyTerminalMessage(
		assistantDeltaMessage(streamID, "Stable paragraph.\n\nopen tail"),
		FrameInput{Size: Size{Width: 40, Height: 5}},
	); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}

	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(out.String())), []rowKind{
		{separator: true},
		{content: transcriptrender.AssistantSymbol + " Stable paragraph."},
	})
	if got, want := surface.activeAssistant.promotedSourceBoundary, len("Stable paragraph."); got != want {
		t.Fatalf("promotion boundary = %d, want %d", got, want)
	}
	out.Reset()

	if _, err := surface.Render(FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("render volatile assistant tail: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"open tail"})
}

func TestAssistantDeltaPromotesParagraphAsOneLogicalLineAtNarrowWidth(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	if _, err := surface.ApplyTerminalMessage(
		assistantDeltaMessage(runtimeids.NewAssistantStreamID(), "alpha beta gamma delta\n\nopen tail"),
		FrameInput{Size: Size{Width: 10, Height: 6}},
	); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}

	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(out.String())), []rowKind{
		{separator: true},
		{content: transcriptrender.AssistantSymbol + " alpha beta gamma delta"},
	})
}

func TestAssistantDeltaPromotionOpensAssistantGroupAfterPriorGroup(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := runtimeids.NewAssistantStreamID()
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("previous user")), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply previous row: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(
		assistantDeltaMessage(streamID, "Stable paragraph.\n\nopen tail"),
		FrameInput{Size: Size{Width: 40, Height: 5}},
	); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}

	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(out.String())), []rowKind{
		{separator: true},
		{content: transcriptrender.AssistantSymbol + " Stable paragraph."},
	})
	if surface.groupRegister == nil || *surface.groupRegister != clientui.TranscriptRowAssistant {
		t.Fatalf("group register = %v, want assistant", surface.groupRegister)
	}
}

func TestAssistantDeltaPromotesInlineAndBlockCodeThroughTranscriptRenderer(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := runtimeids.NewAssistantStreamID()

	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(
		streamID,
		"Use `INLINE_CODE`.\n\n```text\nBLOCK_CODE\n```\n\nopen tail",
	), FrameInput{Size: Size{Width: 80, Height: 8}, Theme: "dark"}); err != nil {
		t.Fatalf("apply assistant delta: %v", err)
	}

	assertRowStructure(t, visibleTextRows(parseTerminalOps(out.String())), []rowKind{
		{content: transcriptrender.AssistantSymbol + " Use INLINE_CODE."},
		{content: "BLOCK_CODE"},
		{content: "open tail"},
	})
}

func TestMarkdownProjectionKeepsOpenBlocksMutableUntilBlankBoundary(t *testing.T) {
	projector := newMarkdownProjector(
		&countingMarkdownRenderer{},
		"",
		transcriptrender.MarkdownLinkLabelOnly,
	)

	openTable := projector.Project(markdownProjectionInput{
		Source:           "| a | b |\n| - | - |\n| 1 | 2 |",
		Width:            40,
		PromotedBoundary: 0,
	})
	if len(openTable.PromotedRows) != 0 {
		t.Fatalf("open table promoted rows = %v, want none", openTable.PromotedRows)
	}
	if got, want := openTable.VolatileRows, []string{"| a | b |", "| - | - |", "| 1 | 2 |"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("open table volatile rows = %v, want %v", got, want)
	}

	openParagraphAtEOF := projector.Project(markdownProjectionInput{
		Source:           "hello\n",
		Width:            40,
		PromotedBoundary: 0,
	})
	if len(openParagraphAtEOF.PromotedRows) != 0 {
		t.Fatalf("single-newline paragraph promoted rows = %v, want none", openParagraphAtEOF.PromotedRows)
	}
	if got, want := openParagraphAtEOF.VolatileRows, []string{"hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("single-newline paragraph volatile rows = %v, want %v", got, want)
	}

	closedTable := projector.Project(markdownProjectionInput{
		Source:           "| a | b |\n| - | - |\n| 1 | 2 |\n\n",
		Width:            40,
		PromotedBoundary: 0,
	})
	if got, want := closedTable.PromotedRows, []string{"| a | b |", "| - | - |", "| 1 | 2 |"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("closed table promoted rows = %v, want %v", got, want)
	}
	if len(closedTable.VolatileRows) != 0 {
		t.Fatalf("closed table volatile rows = %v, want none", closedTable.VolatileRows)
	}
}

func TestMarkdownProjectionPromotesOnlyLongestSafeCandidateWithTwoRenders(t *testing.T) {
	renderer := &countingMarkdownRenderer{}
	projector := newMarkdownProjector(renderer, "", transcriptrender.MarkdownLinkLabelOnly)

	result := projector.Project(markdownProjectionInput{
		Source:           "one\n\ntwo\n\nthree",
		Width:            40,
		PromotedBoundary: 0,
	})

	if got, want := result.PromotedRows, []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("promoted rows = %v, want %v", got, want)
	}
	if got, want := result.VolatileRows, []string{"three"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("volatile rows = %v, want %v", got, want)
	}
	result = projector.Project(markdownProjectionInput{Source: "stable\n\nopen", Width: 40})
	if len(result.PromotedRows) != 0 || result.PromotedBoundary != 0 || result.ProjectionFailure != nil || !reflect.DeepEqual(result.VolatileRows, []string{"changed", "open"}) {
		t.Fatalf("unstable candidate projection: %+v", result)
	}
}

type countingMarkdownRenderer struct{}

func (r *countingMarkdownRenderer) Render(source string, width int) []string {
	if source == "stable\n\nopen" {
		return []string{"changed", "open"}
	}
	return renderPlainMarkdownRows(source, width)
}

func (r *countingMarkdownRenderer) RenderStable(source string, _ int) []string {
	return renderPlainMarkdownRows(source, 0)
}

func renderPlainMarkdownRows(source string, width int) []string {
	source = strings.TrimRight(source, "\n")
	if source == "" {
		return nil
	}
	var rows []string
	for _, line := range strings.Split(source, "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, wrapPlainLine(line, width)...)
	}
	return rows
}

func wrapPlainLine(line string, width int) []string {
	if width <= 0 || len(line) <= width {
		return []string{line}
	}
	var rows []string
	for len(line) > width {
		rows = append(rows, line[:width])
		line = line[width:]
	}
	if line != "" {
		rows = append(rows, line)
	}
	return rows
}
