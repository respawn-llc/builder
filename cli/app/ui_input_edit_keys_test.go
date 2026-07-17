package app

import (
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSharedInputEditKeyCtrlUUsesPlatformSpecificPolicy(t *testing.T) {
	var darwinAction string
	if !handleSharedInputEditKeyForGOOS(tea.KeyMsg{Type: tea.KeyCtrlU}, uiSharedInputEditActions{
		KillToLineStart:   func() bool { darwinAction = "kill-start"; return true },
		DeleteCurrentLine: func() bool { darwinAction = "delete-line"; return true },
	}, "darwin") {
		t.Fatal("expected darwin ctrl+u to be handled")
	}
	if darwinAction != "delete-line" {
		t.Fatalf("darwin ctrl+u action = %q, want delete-line", darwinAction)
	}

	var linuxAction string
	if !handleSharedInputEditKeyForGOOS(tea.KeyMsg{Type: tea.KeyCtrlU}, uiSharedInputEditActions{
		KillToLineStart:   func() bool { linuxAction = "kill-start"; return true },
		DeleteCurrentLine: func() bool { linuxAction = "delete-line"; return true },
	}, "linux") {
		t.Fatal("expected linux ctrl+u to be handled")
	}
	if linuxAction != "kill-start" {
		t.Fatalf("linux ctrl+u action = %q, want kill-start", linuxAction)
	}
}

func TestSharedInputEditKeyAltDeleteUsesForwardWord(t *testing.T) {
	var actions []string
	handled := handleSharedInputEditKeyForGOOS(tea.KeyMsg{Type: tea.KeyDelete, Alt: true}, uiSharedInputEditActions{
		DeleteForward:      func() bool { actions = append(actions, "delete-forward"); return true },
		DeleteBackwardWord: func() bool { actions = append(actions, "delete-backward-word"); return true },
		DeleteForwardWord:  func() bool { actions = append(actions, "delete-forward-word"); return true },
	}, "linux")
	if !handled {
		t.Fatal("expected alt+delete to be handled")
	}
	if got, want := actions, []string{"delete-forward-word"}; !slices.Equal(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
}
