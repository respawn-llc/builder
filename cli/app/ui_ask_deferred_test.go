package app

import (
	"strings"
	"testing"

	"core/cli/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAskEventDefersWhileDetailModeActive(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	ringer := &countRinger{}
	m.promptAttention = newUnfocusedNativeTurnNotificationObserver(ringer)
	m.terminalGeometry = terminalGeometryKnown(90, 12)
	testSetMainInput(m, "hidden draft")
	m.layout().syncViewport()

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode, got %q", m.view.Mode())
	}

	next, projectionCommand := m.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Proceed?", "Yes", "No")})
	m = next.(*uiModel)
	m = updateUIModel(t, m, projectionCommand())
	if got := m.inputMode(); got != uiInputModeMain {
		t.Fatalf("expected detail mode to defer ask input, got %q", got)
	}
	if ringer.total() != 0 {
		t.Fatal("hidden detail-mode ask emitted attention before becoming visible")
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if testMainInput(m) != "hidden draft" {
		t.Fatalf("expected deferred ask not to mutate hidden main input, got %q", testMainInput(m))
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgUp})

	if len(control.askRequests) != 0 {
		t.Fatal("did not expect ask answered before leaving detail mode")
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected ongoing mode, got %q", m.view.Mode())
	}
	if got := m.inputMode(); got != uiInputModeAsk {
		t.Fatalf("expected ask input after leaving detail mode, got %q", got)
	}
	if ringer.total() != 1 {
		t.Fatalf("visible ask attention count = %d, want 1 after leaving detail mode", ringer.total())
	}
	view := stripANSIAndTrimRight(m.View())
	if !strings.Contains(view, "Proceed?") {
		t.Fatalf("expected ask prompt visible after returning to ongoing mode, got %q", view)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = runPromptDeliveryCommand(t, next.(*uiModel), cmd)
	request := requireAskRequest(t, control)
	if request.SelectedOptionNumber == nil || *request.SelectedOptionNumber != 1 {
		t.Fatalf("expected first option selected by default, got %+v", request)
	}
}

func TestAskEventDefersWhileProcessListOverlayIsOpen(t *testing.T) {
	m, control := newProjectedPromptTestUIModel(t)
	ringer := &countRinger{}
	m.promptAttention = newUnfocusedNativeTurnNotificationObserver(ringer)
	m.terminalGeometry = terminalGeometryKnown(100, 14)
	testSetMainInput(m, "/ps")

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.processList.open || m.surface() != uiSurfaceProcessList {
		t.Fatalf("expected process list surface open, visible=%t surface=%q", m.processList.open, m.surface())
	}

	next, projectionCommand := m.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Pick one", "a", "b")})
	m = next.(*uiModel)
	m = updateUIModel(t, m, projectionCommand())
	if got := m.inputMode(); got != uiInputModeProcessList {
		t.Fatalf("expected process list to keep input focus while ask is pending, got %q", got)
	}
	if ringer.total() != 0 {
		t.Fatal("hidden process-list ask emitted attention before becoming visible")
	}

	if len(control.askRequests) != 0 {
		t.Fatal("did not expect ask answered while process list overlay was open")
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.processList.open {
		t.Fatal("expected esc to close process list overlay")
	}
	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected ongoing mode after closing process list, got %q", m.view.Mode())
	}
	if got := m.inputMode(); got != uiInputModeAsk {
		t.Fatalf("expected ask to become interactive after closing process list, got %q", got)
	}
	if ringer.total() != 1 {
		t.Fatalf("visible ask attention count = %d, want 1 after closing process list", ringer.total())
	}
	view := stripANSIAndTrimRight(m.View())
	if !strings.Contains(view, "Pick one") {
		t.Fatalf("expected deferred ask prompt visible after closing process list, got %q", view)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = runPromptDeliveryCommand(t, next.(*uiModel), cmd)
	request := requireAskRequest(t, control)
	if request.SelectedOptionNumber == nil || *request.SelectedOptionNumber != 1 {
		t.Fatalf("expected first option selected by default, got %+v", request)
	}
}

func TestDetailModeIgnoresHiddenMainInputKeys(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(90, 12)
	testSetMainInput(m, "draft")
	m.layout().syncViewport()

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode, got %q", m.view.Mode())
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if testMainInput(m) != "draft" {
		t.Fatalf("expected hidden main input unchanged in detail mode, got %q", testMainInput(m))
	}
}
