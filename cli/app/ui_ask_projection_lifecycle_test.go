package app

import (
	"errors"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestAskProjectionSubsequentPromptStartsWithCleanViewportAndEditorState(t *testing.T) {
	const (
		width  = 32
		height = 9
	)
	model := sizedTestUIModel(newProjectedStaticUIModel(), width, height)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		rows := []string{request.questionSource + " rendered"}
		if request.questionSource == "First question" {
			rows = append(rows,
				"first overflow row two",
				"first overflow row three",
				"first overflow row four",
				"first overflow row five",
			)
		}
		return questionRenderResultMsg{request: request, rows: rows}
	}

	next, firstCommand := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"First question",
		"First",
		"Second",
	)})
	firstPending := next.(*uiModel)
	next, _ = firstPending.Update(firstCommand())
	firstReady := next.(*uiModel)
	next, _ = firstReady.Update(tea.KeyMsg{Type: tea.KeyTab})
	firstReady = next.(*uiModel)
	next, _ = firstReady.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("old draft")})
	firstReady = next.(*uiModel)
	next, _ = firstReady.Update(tea.KeyMsg{Type: tea.KeyLeft})
	firstReady = next.(*uiModel)

	next, _ = firstReady.Update(askEventMsg{event: askEvent{resolvedToolCallID: "ask-1"}})
	resolved := next.(*uiModel)
	next, secondCommand := resolved.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-2",
		"Second question",
		"Only",
	)})
	secondPending := next.(*uiModel)
	if secondCommand == nil {
		t.Fatal("subsequent prompt did not schedule its own projection")
	}
	if secondPending.ask.activeProjection != nil {
		t.Fatal("subsequent pending prompt inherited the previous active projection")
	}
	if secondPending.ask.freeform || secondPending.ask.cursor != 0 ||
		secondPending.ask.editor.Text() != "" || secondPending.ask.editor.Cursor() != 0 {
		t.Fatalf(
			"subsequent pending prompt inherited local state: cursor=%d freeform=%t editor=%q/%d",
			secondPending.ask.cursor,
			secondPending.ask.freeform,
			secondPending.ask.editor.Text(),
			secondPending.ask.editor.Cursor(),
		)
	}
	if pane := secondPending.layout().inputPaneProjection(width, height, uiThemeStyles(secondPending.theme)); len(pane.Lines) != 0 {
		t.Fatalf("subsequent pending prompt exposed previous viewport rows: %d", len(pane.Lines))
	}

	next, _ = secondPending.Update(secondCommand())
	secondReady := next.(*uiModel)
	visible, cursor := testVisibleAskPaneContent(secondReady, width)
	if cursor != nil {
		t.Fatalf("subsequent picker unexpectedly exposed an editor cursor: %v", cursor)
	}
	questionRows := make([]string, 0)
	for _, line := range visible {
		if line.prompt.Kind == askPromptLineKindQuestion {
			questionRows = append(questionRows, xansi.Strip(line.text))
		}
	}
	if got, want := questionRows, []string{"Second question rendered"}; !slices.Equal(got, want) {
		t.Fatalf("subsequent question rows = %q, want %q", got, want)
	}
	if len(visible) > inputContentLineLimit(height) {
		t.Fatalf("subsequent viewport rows = %d, content budget = %d", len(visible), inputContentLineLimit(height))
	}
}

func projectionFailureFinalModel(t *testing.T) *uiModel {
	t.Helper()
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			err:     errors.New("render failed"),
		}
	}
	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
	pending := next.(*uiModel)
	next, _ = pending.Update(command())
	failed := next.(*uiModel)
	if !failed.forcedLocalExit {
		t.Fatal("projection failure helper did not produce a forced local exit")
	}
	return failed
}
