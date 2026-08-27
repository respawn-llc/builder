package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkspaceChangePromptDefaultsToNo(t *testing.T) {
	m := newWorkspaceChangePromptModel("/tmp/old", "/tmp/new", "dark")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*workspaceChangePromptModel)
	if m.result.Rebind {
		t.Fatal("expected default enter action to return to picker")
	}
}

func TestWorkspaceChangePromptYesHotkeyRebinds(t *testing.T) {
	m := newWorkspaceChangePromptModel("/tmp/old", "/tmp/new", "dark")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = next.(*workspaceChangePromptModel)
	if !m.result.Rebind {
		t.Fatal("expected y hotkey to choose rebind")
	}
}
