package app

import (
	"testing"

	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSharedInputEditKeyCtrlUUsesPlatformSpecificPolicy(t *testing.T) {
	darwin := tuiinput.NewEditor()
	darwin.Replace("first\nsecond\nthird")
	darwin.SetCursor(byteOffsetForRuneCursor(darwin.Text(), len([]rune("first\nsec"))))
	if result := applySharedInputEditKeyForGOOS(tea.KeyMsg{Type: tea.KeyCtrlU}, &darwin, "darwin"); !result.Handled || !result.Mutated {
		t.Fatal("expected darwin ctrl+u to be handled")
	}
	if got, want := darwin.Text(), "first\nthird"; got != want {
		t.Fatalf("darwin ctrl+u text = %q, want %q", got, want)
	}

	linux := tuiinput.NewEditor()
	linux.Replace("first\nsecond\nthird")
	linux.SetCursor(byteOffsetForRuneCursor(linux.Text(), len([]rune("first\nsec"))))
	if result := applySharedInputEditKeyForGOOS(tea.KeyMsg{Type: tea.KeyCtrlU}, &linux, "linux"); !result.Handled || !result.Mutated {
		t.Fatal("expected linux ctrl+u to be handled")
	}
	if got, want := linux.Text(), "first\nond\nthird"; got != want {
		t.Fatalf("linux ctrl+u text = %q, want %q", got, want)
	}
}

func TestSharedInputEditKeyAltDeleteUsesForwardWord(t *testing.T) {
	editor := tuiinput.NewEditor()
	editor.Replace("alpha beta gamma")
	editor.SetCursor(byteOffsetForRuneCursor(editor.Text(), len([]rune("alpha "))))
	result := applySharedInputEditKeyForGOOS(tea.KeyMsg{Type: tea.KeyDelete, Alt: true}, &editor, "linux")
	if !result.Handled || !result.Mutated {
		t.Fatal("expected alt+delete to be handled")
	}
	if got, want := editor.Text(), "alpha  gamma"; got != want {
		t.Fatalf("alt+delete text = %q, want %q", got, want)
	}
}
