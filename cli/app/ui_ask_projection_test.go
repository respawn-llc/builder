package app

import (
	"slices"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

func TestAskProjectionAdmissionInitializesPromptWithoutRenderingOnUpdate(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	started := make(chan questionRenderRequest, 1)
	release := make(chan struct{})
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		started <- request
		<-release
		return questionRenderResultMsg{
			request: request,
			rows:    []string{"rendered question"},
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"# Choose carefully",
		"first",
		"second",
	)})
	updated := next.(*uiModel)

	select {
	case <-started:
		t.Fatal("question projector ran during Update")
	default:
	}
	if command == nil {
		t.Fatal("prompt admission did not return a projection command")
	}
	if updated.ask.current == nil {
		t.Fatal("prompt admission did not establish the current FIFO head")
	}
	if updated.ask.currentToken == 0 {
		t.Fatal("prompt admission did not establish a current token")
	}
	if updated.ask.cursor != 0 {
		t.Fatalf("prompt cursor = %d, want 0", updated.ask.cursor)
	}
	if updated.ask.freeform {
		t.Fatal("prompt with options initialized in freeform mode")
	}
	if updated.inputMode() != uiInputModeAsk {
		t.Fatalf("input mode = %q, want ask", updated.inputMode())
	}
	inputState := updated.inputModeState()
	if inputState.ShowsAskInput || inputState.ShowsMainInput {
		t.Fatalf("pending projection exposed input state %+v", inputState)
	}
	projection := updated.layout().inputPaneProjection(64, 20, uiThemeStyles(updated.theme))
	if len(projection.Lines) != 0 || projection.PanelHeight != 0 || projection.Cursor.Visible {
		t.Fatalf("pending projection exposed input pane %+v", projection)
	}

	result := make(chan tea.Msg, 1)
	go func() {
		result <- command()
	}()
	select {
	case request := <-started:
		if request.currentToken != updated.ask.currentToken {
			t.Fatalf("projection current token = %d, want %d", request.currentToken, updated.ask.currentToken)
		}
		if request.operationToken == uuid.Nil {
			t.Fatal("projection operation token is absent")
		}
		if request.identity.questionSource != "# Choose carefully" {
			t.Fatalf("projection question source = %q", request.identity.questionSource)
		}
	case <-time.After(time.Second):
		t.Fatal("projection command did not invoke projector")
	}
	close(release)
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("projection command did not return after projector release")
	}
}

func TestAskProjectionMatchingResultInstallsCompleteProjectionAtomically(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			rows:    []string{"first rendered row", "second rendered row"},
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"# Choose carefully",
		"first",
		"second",
	)})
	pending := next.(*uiModel)
	if command == nil {
		t.Fatal("prompt admission did not return a projection command")
	}

	next, _ = pending.Update(command())
	updated := next.(*uiModel)

	if updated.ask.activeProjection == nil {
		t.Fatal("matching render result did not install an active projection")
	}
	if got, want := updated.ask.activeProjection.rows, []string{"first rendered row", "second rendered row"}; !slices.Equal(got, want) {
		t.Fatalf("active projection rows = %q, want %q", got, want)
	}
	if updated.ask.inFlightProjection != nil {
		t.Fatal("matching render result left projection in flight")
	}
	inputState := updated.inputModeState()
	if !inputState.ShowsAskInput || inputState.ShowsMainInput {
		t.Fatalf("installed projection exposed input state %+v", inputState)
	}
	content, _ := testAskPaneContent(updated, 64)
	if len(content) < 2 || content[0].text != "first rendered row" || content[1].text != "second rendered row" {
		t.Fatalf("installed projection was not visible atomically: %+v", content)
	}
}
