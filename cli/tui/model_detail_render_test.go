package tui

import (
	"strings"
	"testing"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/shared/clientui"
	"core/shared/theme"
	patchformat "core/shared/transcript/patchformat"

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
				Entries: []clientui.TranscriptCommittedRow{detailUser("content")},
			}})
			model = next.(Model)

			lines := model.detailVisibleProjectedLines()
			if len(lines) == 0 {
				t.Fatal("projected no detail lines")
			}
			for _, line := range lines {
				if line.TargetWidth != width || line.ContentWidth != width-1 {
					t.Fatalf("projected geometry = target %d content %d, want %d/%d", line.TargetWidth, line.ContentWidth, width, width-1)
				}
				if line.Rail != detailRailSelected || !line.SelectedFill || line.Kind != detailLineContent || line.EntryIndex != 0 {
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
				transcriptrender.SemanticSpan("B", transcriptrender.StyleRoleAssistant, transcriptrender.SpanAttributeBold),
				transcriptrender.SemanticSpan("I", transcriptrender.StyleRoleMarkdownCode, transcriptrender.SpanAttributeItalic),
				transcriptrender.SemanticSpan(
					"U",
					transcriptrender.StyleRoleError,
					transcriptrender.SpanAttributeFaint,
					transcriptrender.SpanAttributeUnderline,
				),
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
	palette := theme.ResolvePalette("dark")
	if !strings.EqualFold(selectedBackground, palette.App.Background.TrueColor) {
		t.Fatalf("selected background = %q, want brand background %q", selectedBackground, palette.App.Background.TrueColor)
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

func TestUnselectedDetailAdapterAppliesTypedDiffBackgroundAcrossContentWidth(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	tests := []struct {
		name       string
		background transcriptrender.LineBackground
	}{
		{
			name:       "added",
			background: transcriptrender.LineBackgroundDiffAdded,
		},
		{
			name:       "removed",
			background: transcriptrender.LineBackgroundDiffRemoved,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line := detailProjectedLine{
				Content: transcriptrender.Line{
					Spans:      []transcriptrender.Span{transcriptrender.SemanticSpan("x", transcriptrender.StyleRoleToolPatch)},
					Background: test.background,
				},
				Kind:         detailLineContent,
				Rail:         detailRailBlank,
				TargetWidth:  6,
				ContentWidth: 5,
			}
			rendered := renderDetailProjectedLine(line, "dark")
			capture, err := pty.NewCapture(
				pty.MustDimensions(1, line.TargetWidth),
				[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte(rendered))},
			)
			if err != nil {
				t.Fatalf("create diff capture: %v", err)
			}
			analysis, err := analyzer.Analyze(capture)
			if err != nil {
				t.Fatalf("analyze diff capture: %v", err)
			}

			cells := analysis.Screen.Cells[0]
			contentBackground := cells[1].Background
			if contentBackground == "" {
				t.Fatal("unselected diff content has no background")
			}
			if strings.EqualFold(cells[0].Background, contentBackground) {
				t.Fatalf("unselected rail unexpectedly inherited diff background %q", contentBackground)
			}
			for index, cell := range cells[1:] {
				if !strings.EqualFold(cell.Background, contentBackground) {
					t.Fatalf("content cell %d background = %q, want consistent diff fill %q", index+1, cell.Background, contentBackground)
				}
			}
		})
	}
}

func TestSelectedStructuredPatchKeepsChromaStylesUnderDiffFill(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	renderedPatch := patchformat.Render(
		"*** Begin Patch\n*** Update File: example.go\n@@\n+func newValue() string { return \"new\" }\n*** End Patch\n",
		"/workspace",
	)
	presentation := transcriptrender.NewDetailCompiler(50, "dark").Compile(detailTool(clientui.TranscriptToolRow{
		ToolCallID: "764a93e1-c17a-40d9-9021-4ce7b38dc984",
		ToolName:   "patch",
		Text:       renderedPatch.DetailText(),
		ToolPresentation: &clientui.ToolCallMeta{
			ToolName:    "patch",
			PatchRender: &renderedPatch,
			RenderHint:  &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff},
		},
	}))
	var added transcriptrender.Line
	for _, line := range presentation.Expanded {
		if line.Background == transcriptrender.LineBackgroundDiffAdded {
			added = line
			break
		}
	}
	if added.Background != transcriptrender.LineBackgroundDiffAdded {
		t.Fatalf("compiled structured patch has no added line: %q", transcriptrender.PlainLines(presentation.Expanded))
	}

	explicitForegrounds := make(map[string]struct{})
	boldForeground := ""
	for _, span := range added.Spans {
		if span.Style.Kind != transcriptrender.SpanStyleExplicitRGB {
			continue
		}
		foreground := span.Style.Foreground.Hex()
		explicitForegrounds[foreground] = struct{}{}
		if span.Style.Has(transcriptrender.SpanAttributeBold) {
			boldForeground = foreground
		}
	}
	if len(explicitForegrounds) < 2 || boldForeground == "" {
		t.Fatalf("compiled added line lacks varied Chroma styles: %+v", added.Spans)
	}

	projected := detailProjectedLine{
		Content:      added,
		Kind:         detailLineContent,
		Rail:         detailRailSelected,
		SelectedFill: true,
		TargetWidth:  51,
		ContentWidth: 50,
	}
	rendered := renderDetailProjectedLine(projected, "dark")
	capture, err := pty.NewCapture(
		pty.MustDimensions(1, projected.TargetWidth),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte(rendered))},
	)
	if err != nil {
		t.Fatalf("create selected patch capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze selected patch capture: %v", err)
	}

	cells := analysis.Screen.Cells[0]
	palette := theme.ResolvePalette("dark")
	if !strings.EqualFold(cells[0].Background, palette.App.Background.TrueColor) {
		t.Fatalf("rail background = %q, want brand background fill %q", cells[0].Background, palette.App.Background.TrueColor)
	}
	diffBackground := cells[1].Background
	if diffBackground == "" || strings.EqualFold(diffBackground, palette.App.Background.TrueColor) {
		t.Fatalf("selected diff content background = %q, want tinted fill distinct from selection", diffBackground)
	}
	for index, cell := range cells[1:] {
		if !strings.EqualFold(cell.Background, diffBackground) {
			t.Fatalf(
				"content cell %d background = %q, want selected diff fill %q",
				index+1,
				cell.Background,
				diffBackground,
			)
		}
	}
	seenForegrounds := make(map[string]struct{})
	boldStyleSurvived := false
	for _, cell := range cells {
		for foreground := range explicitForegrounds {
			if strings.EqualFold(cell.Foreground, foreground) {
				seenForegrounds[foreground] = struct{}{}
			}
		}
		if cell.Bold && strings.EqualFold(cell.Foreground, boldForeground) {
			boldStyleSurvived = true
		}
	}
	if len(seenForegrounds) < 2 {
		t.Fatalf("selected patch retained %d Chroma foregrounds, want at least 2: %+v", len(seenForegrounds), cells)
	}
	if !boldStyleSurvived {
		t.Fatalf("selected patch lost bold Chroma style for foreground %q: %+v", boldForeground, cells)
	}
}

func TestSelectedDetailContinuationGuideKeepsFaintStyleAndFill(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	model := NewModel()
	model.viewportWidth = 20
	entries := []clientui.TranscriptCommittedRow{detailAssistant("first line\nsecond line\nthird line\nfourth line")}
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{Entries: entries}})
	model = next.(Model)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	lines := model.detailVisibleProjectedLines()
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
		Entries: []clientui.TranscriptCommittedRow{
			detailUser("first"),
			detailUser("second"),
		},
	}})
	model = next.(Model)

	lines := model.detailVisibleProjectedLines()
	if len(lines) < 2 {
		t.Fatalf("projected lines = %+v, want both entries", lines)
	}
	first := lines[0]
	if first.Kind != detailLineContent || first.EntryIndex != 0 || first.Rail != detailRailBlank || first.SelectedFill {
		t.Fatalf("first unselected line metadata = %+v", first)
	}
	selected := lines[len(lines)-1]
	if selected.Kind != detailLineContent || selected.EntryIndex != 1 || selected.Rail != detailRailSelected || !selected.SelectedFill {
		t.Fatalf("latest selected line metadata = %+v", selected)
	}
}

func TestDetailSelectionSpacersAreVisualOnlyAndCarrySelectedLens(t *testing.T) {
	model := NewModel()
	model.viewportWidth = 24
	model.viewportLines = 5
	entries := make([]clientui.TranscriptCommittedRow, 0, 7)
	for index := 0; index < 7; index++ {
		entries = append(entries, detailUser(string(rune('a'+index))))
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
		if line.Kind != detailLineVisualSpacer || line.Rail != detailRailSelected || !line.SelectedFill {
			t.Fatalf("visual spacer %d = %+v, want unowned selected lens", index, line)
		}
	}
	if visible[2].Kind != detailLineContent || visible[2].EntryIndex != 3 {
		t.Fatalf("selected owner line = %+v, want entry 3", visible[2])
	}
}

func TestDetailSelectionSpacersInsertAtPinnedEdgesWithoutReplacingNeighbor(t *testing.T) {
	entries := make([]clientui.TranscriptCommittedRow, 0, 7)
	for index := 0; index < 7; index++ {
		entries = append(entries, detailUser(string(rune('a'+index))))
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
			if neighbor.Kind != detailLineContent || neighbor.EntryIndex != tt.neighborOwner {
				t.Fatalf("pinned neighbor = %+v, want owner %d retained", neighbor, tt.neighborOwner)
			}
			spacer := visible[tt.spacerIndex]
			if spacer.Kind != detailLineVisualSpacer || spacer.Rail != detailRailSelected || !spacer.SelectedFill {
				t.Fatalf("pinned spacer = %+v, want selected visual spacer", spacer)
			}
		})
	}
}
