package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptCommandTokenReservesNamespaceCaseInsensitively(t *testing.T) {
	for _, input := range []string{"/prompt:missing", "/PROMPT:missing", "/prompt:"} {
		if _, ok := promptCommandToken(input); !ok {
			t.Fatalf("prompt command token %q was not reserved", input)
		}
	}
}

func TestUnknownPromptCommandFailsClosedBeforeOrdinarySubmission(t *testing.T) {
	model := newProjectedStaticUIModel()
	handled, _, cmd := model.inputController().handleEnteredSlashCommandInput("/prompt:doesnotexist")
	if !handled {
		t.Fatal("unknown prompt command was not handled locally")
	}
	if cmd == nil {
		t.Fatal("unknown prompt command did not produce local feedback")
	}
}

func TestUnknownPromptCommandFailsClosedThroughEnterPath(t *testing.T) {
	model := newProjectedStaticUIModel()
	testSetMainInput(model, "/prompt:doesnotexist")

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("unknown prompt command did not produce local feedback")
	}
	updated := next.(*uiModel)
	if updated.activeSubmit.token != 0 || len(updated.queued) != 0 {
		t.Fatalf("unknown prompt command entered submission path: active=%+v queued=%+v", updated.activeSubmit, updated.queued)
	}
}

func TestQueuedUnknownPromptCommandFailsClosedBeforeOrdinarySubmission(t *testing.T) {
	model := newProjectedStaticUIModel()
	model.queued = queuedInputsForTest("/prompt:doesnotexist")

	_, cmd := model.inputController().flushQueuedInputs(queueDrainAuto)
	if cmd == nil {
		t.Fatal("queued unknown prompt command did not produce local feedback")
	}
	if model.activeSubmit.token != 0 {
		t.Fatalf("queued unknown prompt command entered submission path: %+v", model.activeSubmit)
	}
}
