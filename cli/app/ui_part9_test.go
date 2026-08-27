package app

import (
	"context"
	"core/cli/tui"
	"core/server/llm"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMouseSGRSplitEscAndRunesDoNotArmRollback(t *testing.T) {
	m := newProjectedStaticUIModel()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected esc to arm rollback window before potential sgr continuation")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;63;24M")})
	updated = next.(*uiModel)
	if !updated.lastEscAt.IsZero() {
		t.Fatal("expected split mouse sgr continuation to clear rollback esc arming")
	}
	if testMainInput(updated) != "" {
		t.Fatalf("expected split sgr payload ignored, got %q", testMainInput(updated))
	}
}

type statusLineFakeClient struct{}

func (statusLineFakeClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (statusLineFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func TestHelpDismissesOnRegisteredKeyAndAppliesAction(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected help dismissed by registered key")
	}
	if testMainInput(updated) != "x" {
		t.Fatalf("expected keypress to keep its normal behavior, got %q", testMainInput(updated))
	}
}

func TestHelpToggleClearsRollbackEscArming(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
	m.layout().syncViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*uiModel)
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected first esc to arm rollback window")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}, Alt: true})
	updated = next.(*uiModel)
	if !updated.helpVisible {
		t.Fatal("expected alt+/ to open help")
	}
	if !updated.lastEscAt.IsZero() {
		t.Fatal("expected help toggle to clear rollback esc arming")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated = next.(*uiModel)
	if updated.helpVisible {
		t.Fatal("expected esc to dismiss help")
	}
	if updated.rollback.isSelecting() {
		t.Fatal("did not expect esc after help toggle to open rollback selection")
	}
	if updated.lastEscAt.IsZero() {
		t.Fatal("expected esc after help toggle to start a fresh rollback arming window")
	}
}

func TestHelpToggleKeyHidesVisibleHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(customKeyMsg{Kind: customKeyHelp})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected help toggle key to hide visible help")
	}
}

func TestHelpToggleIgnoredInDetailMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
	m.forwardToView(tui.ToggleModeMsg{})
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("did not expect help to open in detail mode")
	}
}

func TestTranscriptToggleClosesVisibleHelp(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(80, 24)
	m.layout().syncViewport()

	next, _ := m.Update(customKeyMsg{Kind: customKeyHelp})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = next.(*uiModel)

	if updated.helpVisible {
		t.Fatal("expected transcript toggle to hide help")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after transcript toggle, got %q", updated.view.Mode())
	}
}
