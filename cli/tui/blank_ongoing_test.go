package tui

import "testing"

func TestOngoingModeIsBlankStub(t *testing.T) {
	model := NewModel()
	if model.Mode() != ModeOngoing {
		t.Fatalf("mode = %q, want %q", model.Mode(), ModeOngoing)
	}
	if got := model.View(); got != "" {
		t.Fatalf("ongoing view = %q, want blank stub", got)
	}
}
