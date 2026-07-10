package tui

import (
	"testing"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/shared/clientui"
	"core/shared/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestDetailProjectionReservesRailBeforeContentAtNarrowWidths(t *testing.T) {
	for _, width := range []int{1, 2} {
		t.Run(string(rune('0'+width)), func(t *testing.T) {
			model := NewModel()
			model.viewportWidth = width
			next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
				Entries: []clientui.ChatEntry{{Role: detailRoleUser, Text: "content"}},
			}})
			model = next.(Model)

			lines := model.detailProjectedLines()
			if len(lines) == 0 {
				t.Fatal("projected no detail lines")
			}
			for _, line := range lines {
				if line.TargetWidth != width || line.ContentWidth != width-1 {
					t.Fatalf("projected geometry = target %d content %d, want %d/%d", line.TargetWidth, line.ContentWidth, width, width-1)
				}
				if line.Rail != detailRailSelected || !line.SelectedFill || line.EntryIndex == nil || *line.EntryIndex != 0 {
					t.Fatalf("selected projection metadata = %+v", line)
				}
				rendered := renderDetailProjectedLine(line, model.theme)
				if got := lipgloss.Width(rendered); got != width {
					t.Fatalf("rendered width = %d, want %d: %q", got, width, rendered)
				}
				if width == 1 && ansi.Strip(rendered) != theme.SelectionRailGlyph {
					t.Fatalf("one-cell detail = %q, want selected rail only", ansi.Strip(rendered))
				}
			}
		})
	}
}

func TestSelectedDetailAdapterPreservesSemanticStylesAcrossFullWidthFill(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	line := detailProjectedLine{
		Content: transcriptrender.Line{
			Spans: []transcriptrender.Span{
				{Text: "B", Role: transcriptrender.StyleRoleAssistant, Bold: true},
				{Text: "I", Role: transcriptrender.StyleRoleMarkdownCode, Italic: true},
				{Text: "U", Role: transcriptrender.StyleRoleToolShellError, Faint: true, Underline: true},
			},
		},
		Kind:         detailLineContent,
		Rail:         detailRailSelected,
		SelectedFill: true,
		TargetWidth:  8,
		ContentWidth: 7,
	}
	rendered := renderDetailProjectedLine(line, "dark")
	capture, err := pty.NewCapture(
		pty.MustDimensions(1, line.TargetWidth),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte(rendered))},
	)
	if err != nil {
		t.Fatalf("create selected detail capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze selected detail capture: %v", err)
	}

	cells := analysis.Screen.Cells[0]
	selectedBackground := cells[0].Background
	if selectedBackground == "" {
		t.Fatal("selected rail has no background")
	}
	for index, cell := range cells {
		if cell.Background != selectedBackground {
			t.Fatalf("cell %d background = %q, want continuous selected fill %q", index, cell.Background, selectedBackground)
		}
	}
	if !cells[1].Bold || cells[1].Foreground == "" {
		t.Fatalf("markdown cell = %+v, want bold semantic foreground", cells[1])
	}
	if !cells[2].Italic || cells[2].Foreground == "" {
		t.Fatalf("markdown code cell = %+v, want italic semantic foreground", cells[2])
	}
	if !cells[3].Faint || !cells[3].Underline || cells[3].Foreground == "" {
		t.Fatalf("shell error cell = %+v, want faint underlined semantic foreground", cells[3])
	}
	if cells[1].Foreground == cells[2].Foreground || cells[2].Foreground == cells[3].Foreground {
		t.Fatalf("semantic foregrounds collapsed under selection: markdown=%q shell=%q error=%q", cells[1].Foreground, cells[2].Foreground, cells[3].Foreground)
	}
}

func TestSelectedDetailContinuationGuideKeepsFaintStyleAndFill(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	model := NewModel()
	model.viewportWidth = 20
	entries := []clientui.ChatEntry{{Role: detailRoleAssistant, Text: "first line\nsecond line\nthird line\nfourth line"}}
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{Entries: entries}})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	lines := model.detailProjectedLines()
	if len(lines) < 2 {
		t.Fatalf("expanded projection has %d lines, want continuation", len(lines))
	}
	rendered := renderDetailProjectedLine(lines[1], model.theme)
	capture, err := pty.NewCapture(
		pty.MustDimensions(1, model.viewportWidth),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte(rendered))},
	)
	if err != nil {
		t.Fatalf("create continuation capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze continuation capture: %v", err)
	}
	cells := analysis.Screen.Cells[0]
	if !cells[1].Faint {
		t.Fatalf("continuation guide cell = %+v, want faint semantic style", cells[1])
	}
	for index, cell := range cells {
		if cell.Background != cells[0].Background {
			t.Fatalf("continuation cell %d background = %q, want selected fill %q", index, cell.Background, cells[0].Background)
		}
	}
}

func TestDetailProjectionUsesBlankRailWithoutFillForUnselectedRows(t *testing.T) {
	model := NewModel()
	model.viewportWidth = 24
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{
			{Role: detailRoleUser, Text: "first"},
			{Role: detailRoleUser, Text: "second"},
		},
	}})
	model = next.(Model)

	lines := model.detailProjectedLines()
	if len(lines) < 2 {
		t.Fatalf("projected lines = %+v, want both entries", lines)
	}
	first := lines[0]
	if first.EntryIndex == nil || *first.EntryIndex != 0 || first.Rail != detailRailBlank || first.SelectedFill {
		t.Fatalf("first unselected line metadata = %+v", first)
	}
	selected := lines[len(lines)-1]
	if selected.EntryIndex == nil || *selected.EntryIndex != 1 || selected.Rail != detailRailSelected || !selected.SelectedFill {
		t.Fatalf("latest selected line metadata = %+v", selected)
	}
}

func TestDetailSelectionSpacersAreVisualOnlyAndCarrySelectedLens(t *testing.T) {
	model := NewModel()
	model.viewportWidth = 24
	model.viewportLines = 5
	entries := make([]clientui.ChatEntry, 0, 7)
	for index := 0; index < 7; index++ {
		entries = append(entries, clientui.ChatEntry{Role: detailRoleUser, Text: string(rune('a' + index))})
	}
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{Entries: entries}})
	model = next.(Model)
	model.detailScroll = 1
	model.setSelectedDetailIndex(3)

	logical := model.detailProjectedLines()
	if len(logical) != 7 {
		t.Fatalf("logical line count = %d, want 7", len(logical))
	}
	for _, line := range logical {
		if line.Kind == detailLineVisualSpacer {
			t.Fatalf("logical projection contains render-only spacer: %+v", line)
		}
	}

	visible := model.detailVisibleProjectedLines()
	if len(visible) != model.viewportLines {
		t.Fatalf("visible line count = %d, want %d", len(visible), model.viewportLines)
	}
	for _, index := range []int{1, 3} {
		line := visible[index]
		if line.Kind != detailLineVisualSpacer || line.EntryIndex != nil || line.Rail != detailRailSelected || !line.SelectedFill {
			t.Fatalf("visual spacer %d = %+v, want unowned selected lens", index, line)
		}
	}
	if visible[2].EntryIndex == nil || *visible[2].EntryIndex != 3 {
		t.Fatalf("selected owner line = %+v, want entry 3", visible[2])
	}
}

func TestDetailSelectionSpacersInsertAtPinnedEdgesWithoutReplacingNeighbor(t *testing.T) {
	entries := make([]clientui.ChatEntry, 0, 7)
	for index := 0; index < 7; index++ {
		entries = append(entries, clientui.ChatEntry{Role: detailRoleUser, Text: string(rune('a' + index))})
	}

	cases := []struct {
		name          string
		scroll        int
		selected      int
		neighborIndex int
		neighborOwner int
		spacerIndex   int
	}{
		{name: "top", scroll: 0, selected: 1, neighborIndex: 0, neighborOwner: 0, spacerIndex: 1},
		{name: "bottom", scroll: 2, selected: 5, neighborIndex: 4, neighborOwner: 6, spacerIndex: 3},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			model := NewModel()
			model.viewportWidth = 24
			model.viewportLines = 5
			next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{Entries: entries}})
			model = next.(Model)
			model.detailScroll = tt.scroll
			model.setSelectedDetailIndex(tt.selected)

			visible := model.detailVisibleProjectedLines()
			if len(visible) != model.viewportLines {
				t.Fatalf("visible line count = %d, want %d", len(visible), model.viewportLines)
			}
			neighbor := visible[tt.neighborIndex]
			if neighbor.EntryIndex == nil || *neighbor.EntryIndex != tt.neighborOwner {
				t.Fatalf("pinned neighbor = %+v, want owner %d retained", neighbor, tt.neighborOwner)
			}
			spacer := visible[tt.spacerIndex]
			if spacer.Kind != detailLineVisualSpacer || spacer.Rail != detailRailSelected || !spacer.SelectedFill {
				t.Fatalf("pinned spacer = %+v, want selected visual spacer", spacer)
			}
		})
	}
}
