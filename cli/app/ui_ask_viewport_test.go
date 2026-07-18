package app

import (
	"fmt"
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	tuitest "core/internal/testharness/pty"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestAskViewportTallQuestionPreservesBeginningAndPriorityRows(t *testing.T) {
	const (
		width  = 48
		height = 12
	)
	model := sizedTestUIModel(newProjectedStaticUIModel(), width, height)
	event := testQuestionAskEvent("ask-1", "Question source", "First", "Second")
	testSetActiveAsk(model, &event)
	rows := make([]string, 60)
	for index := range rows {
		rows[index] = fmt.Sprintf("question row %02d", index+1)
	}
	model.ask.activeProjection.rows = rows

	visible, cursor := testVisibleAskPaneContent(model, width)
	if cursor != nil {
		t.Fatalf("picker viewport unexpectedly exposed an editor cursor: %v", cursor)
	}
	if len(visible) > inputContentLineLimit(height) {
		t.Fatalf("visible rows = %d, content budget = %d", len(visible), inputContentLineLimit(height))
	}
	optionRows := 0
	hintRows := 0
	questionRows := make([]string, 0)
	for _, line := range visible {
		switch line.prompt.Kind {
		case askPromptLineKindQuestion:
			questionRows = append(questionRows, xansi.Strip(line.text))
		case askPromptLineKindOption:
			optionRows++
		case askPromptLineKindHint:
			hintRows++
		}
	}
	if optionRows != 3 || hintRows != 1 {
		t.Fatalf("priority rows = options %d hints %d, want all picker controls", optionRows, hintRows)
	}
	if len(questionRows) == 0 || questionRows[0] != "question row 01" {
		t.Fatalf("visible question prefix = %q, want the beginning", questionRows)
	}
	lastQuestion := []rune(questionRows[len(questionRows)-1])
	if len(lastQuestion) == 0 || lastQuestion[len(lastQuestion)-1] != '…' {
		t.Fatalf("final visible question row = %q, want forced ellipsis", questionRows[len(questionRows)-1])
	}

	pane := model.layout().inputPaneProjection(width, height, uiThemeStyles(model.theme))
	if pane.PanelHeight != len(visible)+2 || len(pane.Lines) != pane.PanelHeight {
		t.Fatalf("framed pane geometry = lines %d panel %d visible %d", len(pane.Lines), pane.PanelHeight, len(visible))
	}
}

func TestAskViewportMarkdownHyperlinkEllipsisIsWidthSafe(t *testing.T) {
	const (
		target = "https://example.com/long-destination"
		width  = 12
		height = 9
	)
	for _, presentation := range []transcriptrender.MarkdownLinkPresentation{
		transcriptrender.MarkdownLinkLabelOnly,
		transcriptrender.MarkdownLinkLabelAndDestination,
	} {
		t.Run(fmt.Sprintf("presentation-%d", presentation), func(t *testing.T) {
			model := sizedTestUIModel(newProjectedStaticUIModel(
				WithUIMarkdownLinkPresentation(presentation),
			), width, height)
			question := strings.Repeat("[linked text]("+target+")\n", 8)
			event := testQuestionAskEvent("ask-1", question)
			testSetActiveAsk(model, &event)

			visible, _ := testVisibleAskPaneContent(model, width)
			questionRows := make([]uiInputPaneContentLine, 0)
			for _, line := range visible {
				if line.prompt.Kind == askPromptLineKindQuestion {
					questionRows = append(questionRows, line)
				}
			}
			if len(questionRows) == 0 {
				t.Fatal("bounded viewport omitted all question rows despite available capacity")
			}
			for _, line := range questionRows {
				if got := lipgloss.Width(line.text); got > width {
					t.Fatalf("question row width = %d, want <= %d", got, width)
				}
				tuitest.TraceTerminalHyperlinks(t, line.text+" plain")
			}
			last := questionRows[len(questionRows)-1].text
			if !strings.HasSuffix(xansi.Strip(last), "…") {
				t.Fatalf("final visible question row = %q, want ellipsis", xansi.Strip(last))
			}
			trace := tuitest.TraceTerminalHyperlinks(t, last+" plain")
			afterEllipsis := false
			for _, fragment := range trace.Fragments {
				if fragment.Text == "…" {
					afterEllipsis = true
					continue
				}
				if afterEllipsis && fragment.Link != nil {
					t.Fatalf("fragment after ellipsis inherited hyperlink: %+v", fragment)
				}
			}
		})
	}
}

func TestAskViewportTallMixedMarkdownIsBoundedAcrossLinkPresentations(t *testing.T) {
	const (
		width  = 32
		height = 12
	)
	source := strings.Repeat(
		"Paragraph with wrapped words and a [link](https://example.com/destination).\n\n"+
			"- first list item\n- second list item\n\n"+
			"```go\nfmt.Println(\"bounded\")\n```\n\n",
		8,
	)
	for _, presentation := range []transcriptrender.MarkdownLinkPresentation{
		transcriptrender.MarkdownLinkLabelOnly,
		transcriptrender.MarkdownLinkLabelAndDestination,
	} {
		t.Run(fmt.Sprintf("presentation-%d", presentation), func(t *testing.T) {
			model := sizedTestUIModel(newProjectedStaticUIModel(
				WithUIMarkdownLinkPresentation(presentation),
			), width, height)
			next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
				"ask-1",
				source,
				"First",
				"Second",
			)})
			pending := next.(*uiModel)
			next, _ = pending.Update(command())
			ready := next.(*uiModel)

			visible, cursor := testVisibleAskPaneContent(ready, width)
			if cursor != nil {
				t.Fatalf("picker viewport unexpectedly exposed a cursor: %v", cursor)
			}
			if len(visible) > inputContentLineLimit(height) {
				t.Fatalf("visible rows = %d, content budget = %d", len(visible), inputContentLineLimit(height))
			}
			questionRows := 0
			optionRows := 0
			for _, line := range visible {
				if got := lipgloss.Width(line.text); got > width {
					t.Fatalf("visible row width = %d, want <= %d", got, width)
				}
				switch line.prompt.Kind {
				case askPromptLineKindQuestion:
					questionRows++
					tuitest.TraceTerminalHyperlinks(t, line.text+" plain")
				case askPromptLineKindOption:
					optionRows++
				}
			}
			if questionRows == 0 || optionRows != 3 {
				t.Fatalf("bounded mixed Markdown rows = question %d options %d", questionRows, optionRows)
			}
		})
	}
}

func TestAskViewportZeroQuestionCapacityDoesNotEmitEllipsisRow(t *testing.T) {
	const (
		width  = 48
		height = 8
	)
	model := sizedTestUIModel(newProjectedStaticUIModel(), width, height)
	event := testQuestionAskEvent("ask-1", "Question source", "First", "Second")
	testSetActiveAsk(model, &event)
	model.ask.activeProjection.rows = []string{
		"question row one",
		"question row two",
	}

	visible, cursor := testVisibleAskPaneContent(model, width)
	if cursor != nil {
		t.Fatalf("picker viewport unexpectedly exposed an editor cursor: %v", cursor)
	}
	questionRows := 0
	optionRows := 0
	hintRows := 0
	for _, line := range visible {
		switch line.prompt.Kind {
		case askPromptLineKindQuestion:
			questionRows++
		case askPromptLineKindOption:
			optionRows++
		case askPromptLineKindHint:
			hintRows++
		}
	}
	if questionRows != 0 {
		t.Fatalf("question rows = %d, want zero when priority consumes capacity", questionRows)
	}
	if optionRows != 3 || hintRows != 1 || len(visible) != inputContentLineLimit(height) {
		t.Fatalf("priority viewport = options %d hints %d rows %d", optionRows, hintRows, len(visible))
	}
}

func TestAskViewportPickerFreeformRoundTripPreservesBoundedDraftAndCursor(t *testing.T) {
	const (
		width  = 24
		height = 9
	)
	model := sizedTestUIModel(newProjectedStaticUIModel(), width, height)
	event := testQuestionAskEvent("ask-1", "Question source", "First", "Second")
	testSetActiveAsk(model, &event)
	model.ask.activeProjection.rows = []string{
		"question row one",
		"question row two",
		"question row three",
		"question row four",
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	freeform := next.(*uiModel)
	next, _ = freeform.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft text")})
	freeform = next.(*uiModel)
	next, _ = freeform.Update(tea.KeyMsg{Type: tea.KeyLeft})
	freeform = next.(*uiModel)
	wantEditorCursor := freeform.ask.editor.Cursor()
	freeformViewport := freeform.layout().askInputViewport(width, inputContentLineLimit(height))
	if !freeformViewport.cursor.Visible {
		t.Fatal("bounded freeform viewport hid the active editor cursor")
	}
	if len(freeformViewport.lines) > inputContentLineLimit(height) {
		t.Fatalf("freeform viewport rows = %d, content budget = %d", len(freeformViewport.lines), inputContentLineLimit(height))
	}

	next, _ = freeform.Update(tea.KeyMsg{Type: tea.KeyTab})
	picker := next.(*uiModel)
	pickerViewport := picker.layout().askInputViewport(width, inputContentLineLimit(height))
	if picker.ask.freeform {
		t.Fatal("round trip did not return to picker mode")
	}
	if picker.ask.editor.Text() != "draft text" || picker.ask.editor.Cursor() != wantEditorCursor {
		t.Fatalf("picker mode changed draft/editor cursor: %q/%d", picker.ask.editor.Text(), picker.ask.editor.Cursor())
	}
	if pickerViewport.cursor.Visible {
		t.Fatal("picker viewport exposed the disabled draft cursor")
	}
	if len(pickerViewport.lines) > inputContentLineLimit(height) {
		t.Fatalf("picker viewport rows = %d, content budget = %d", len(pickerViewport.lines), inputContentLineLimit(height))
	}

	next, _ = picker.Update(tea.KeyMsg{Type: tea.KeyTab})
	restored := next.(*uiModel)
	restoredViewport := restored.layout().askInputViewport(width, inputContentLineLimit(height))
	if !restored.ask.freeform {
		t.Fatal("round trip did not restore freeform mode")
	}
	if restored.ask.editor.Text() != "draft text" || restored.ask.editor.Cursor() != wantEditorCursor {
		t.Fatalf("restored freeform changed draft/editor cursor: %q/%d", restored.ask.editor.Text(), restored.ask.editor.Cursor())
	}
	if !restoredViewport.cursor.Visible {
		t.Fatal("restored bounded freeform viewport hid the active editor cursor")
	}
	if len(restoredViewport.lines) > inputContentLineLimit(height) {
		t.Fatalf("restored viewport rows = %d, content budget = %d", len(restoredViewport.lines), inputContentLineLimit(height))
	}
}
