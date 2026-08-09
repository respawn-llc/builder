package app

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAuthMethodPickerSelectsSecondOption(t *testing.T) {
	m := newAuthMethodPickerModel("dark", startupPickerNotice{}, false, true)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(*startupPickerModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*startupPickerModel)
	if m.result.ChoiceID != string(authMethodChoiceDevice) {
		t.Fatalf("choice=%q want %q", m.result.ChoiceID, authMethodChoiceDevice)
	}
}

func TestStartupPickerEnterDoesNothingWhenThereAreNoItems(t *testing.T) {
	m := newStartupPickerModel("**Header**", "Header", "dark", startupPickerNotice{}, nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*startupPickerModel)
	if cmd != nil {
		t.Fatal("did not expect quit command for empty picker")
	}
	if updated.result.ChoiceID != "" || updated.result.Canceled {
		t.Fatalf("expected empty result for empty picker, got %+v", updated.result)
	}
}

func TestAuthMethodPickerCancel(t *testing.T) {
	m := newAuthMethodPickerModel("dark", startupPickerNotice{}, false, true)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = next.(*startupPickerModel)
	if !m.result.Canceled {
		t.Fatal("expected canceled result")
	}
}

func TestAuthMethodPickerMarksRemoteFlowFailureAsErrorNotice(t *testing.T) {
	notice := authMethodPickerNoticeForRequest(authInteraction{FlowErr: errors.New("remote failure")})
	if notice.Kind != startupPickerNoticeError {
		t.Fatalf("notice kind = %q, want %q", notice.Kind, startupPickerNoticeError)
	}
}
