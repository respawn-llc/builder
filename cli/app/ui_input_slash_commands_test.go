package app

import "testing"

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
