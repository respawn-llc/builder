package ongoing

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"github.com/google/uuid"
)

func TestAssistantDeltaPromotesClosedParagraphAndKeepsTailMutable(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	streamID := uuid.New()

	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantDelta,
		AssistantDelta: &clientui.TranscriptAssistantDelta{
			StreamID: streamID,
			Delta:    "Stable paragraph.\n\nopen tail",
		},
	}, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
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
		assistantDeltaMessage(uuid.New(), "alpha beta gamma delta\n\nopen tail"),
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
	streamID := uuid.New()
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("previous user")), FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
		t.Fatalf("apply previous row: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageAssistantDelta,
		AssistantDelta: &clientui.TranscriptAssistantDelta{
			StreamID: streamID,
			Delta:    "Stable paragraph.\n\nopen tail",
		},
	}, FrameInput{Size: Size{Width: 40, Height: 5}}); err != nil {
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
	streamID := uuid.New()

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
	projector := newMarkdownProjector(&countingMarkdownRenderer{}, "")

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
	projector := newMarkdownProjector(renderer, "")

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
}

type countingMarkdownRenderer struct {
}

func (r *countingMarkdownRenderer) Render(source string, width int) []string {
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
