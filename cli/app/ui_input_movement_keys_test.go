package app

import (
	"slices"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSharedInputMovementKeyMapsNavigationAliases(t *testing.T) {
	cases := []struct {
		name   string
		key    tea.KeyMsg
		action string
	}{
		{name: "left", key: tea.KeyMsg{Type: tea.KeyLeft}, action: "left"},
		{name: "right", key: tea.KeyMsg{Type: tea.KeyRight}, action: "right"},
		{name: "alt-left", key: tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, action: "word-left"},
		{name: "alt-right", key: tea.KeyMsg{Type: tea.KeyRight, Alt: true}, action: "word-right"},
		{name: "ctrl-left", key: tea.KeyMsg{Type: tea.KeyCtrlLeft}, action: "word-left"},
		{name: "ctrl-right", key: tea.KeyMsg{Type: tea.KeyCtrlRight}, action: "word-right"},
		{name: "alt-b", key: tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}}, action: "word-left"},
		{name: "alt-f", key: tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}}, action: "word-right"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var actions []string
			handled := handleSharedInputMovementKey(tt.key, uiSharedInputMovementActions{
				MoveLeft:      func() { actions = append(actions, "left") },
				MoveRight:     func() { actions = append(actions, "right") },
				MoveWordLeft:  func() { actions = append(actions, "word-left") },
				MoveWordRight: func() { actions = append(actions, "word-right") },
			})
			if !handled {
				t.Fatal("expected movement key to be handled")
			}
			if got, want := actions, []string{tt.action}; !slices.Equal(got, want) {
				t.Fatalf("actions = %v, want %v", got, want)
			}
		})
	}
}

func TestSharedInputMovementKeyPreservesOtherRuneInput(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'b'}},
		{Type: tea.KeyRunes, Runes: []rune{'f'}},
		{Type: tea.KeyRunes, Alt: true, Runes: []rune("bf")},
		{Type: tea.KeyRunes, Alt: true, Runes: []rune{'x'}},
	}
	for _, key := range cases {
		if handleSharedInputMovementKey(key, uiSharedInputMovementActions{}) {
			t.Fatalf("unexpectedly handled %q", key.String())
		}
	}
}

func TestMainComposerAltRuneWordNavigation(t *testing.T) {
	model := newProjectedStaticUIModel()
	model.input = "alpha beta gamma"
	model.inputCursor = len([]rune(model.input))

	updated := updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}})
	if got, want := updated.input, "alpha beta gamma"; got != want {
		t.Fatalf("input after alt+b = %q, want %q", got, want)
	}
	if got, want := updated.inputCursor, moveBufferCursorWordLeft(updated.input, len([]rune(updated.input))); got != want {
		t.Fatalf("cursor after alt+b = %d, want %d", got, want)
	}

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}})
	if got, want := updated.input, "alpha beta gamma"; got != want {
		t.Fatalf("input after alt+f = %q, want %q", got, want)
	}
	if got, want := updated.inputCursor, len([]rune(updated.input)); got != want {
		t.Fatalf("cursor after alt+f = %d, want %d", got, want)
	}
}

func TestMainComposerPreservesOtherRuneInput(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		want string
	}{
		{name: "plain-b", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}, want: "b"},
		{name: "plain-f", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, want: "f"},
		{name: "alt-multi-rune", key: tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune("bf")}, want: "bf"},
		{name: "unrelated-alt-rune", key: tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'x'}}, want: "x"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			updated := updateUIModel(t, newProjectedStaticUIModel(), tt.key)
			if got := updated.input; got != tt.want {
				t.Fatalf("input = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAskFreeformAltRuneWordNavigation(t *testing.T) {
	model := newProjectedStaticUIModel()
	reply := make(chan askReply, 1)
	event := askEvent{req: clientui.PendingPromptEvent{Question: "Type answer"}, reply: reply}
	updated := updateUIModel(t, model, askEventMsg{event: event})
	updated.ask.input = "alpha beta gamma"
	updated.ask.inputCursor = len([]rune(updated.ask.input))

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}})
	if got, want := updated.ask.input, "alpha beta gamma"; got != want {
		t.Fatalf("ask input after alt+b = %q, want %q", got, want)
	}
	if got, want := updated.ask.inputCursor, moveBufferCursorWordLeft(updated.ask.input, len([]rune(updated.ask.input))); got != want {
		t.Fatalf("ask cursor after alt+b = %d, want %d", got, want)
	}

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}})
	if got, want := updated.ask.input, "alpha beta gamma"; got != want {
		t.Fatalf("ask input after alt+f = %q, want %q", got, want)
	}
	if got, want := updated.ask.inputCursor, len([]rune(updated.ask.input)); got != want {
		t.Fatalf("ask cursor after alt+f = %d, want %d", got, want)
	}
}

func TestSingleLineEditorAltRuneWordNavigation(t *testing.T) {
	editor := newSingleLineEditor("alpha beta gamma")
	editor.SetCursor(len(editor.Text()))

	updateSingleLineEditorWithAppKeys(&editor, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}})
	if got, want := editor.Text(), "alpha beta gamma"; got != want {
		t.Fatalf("editor text after alt+b = %q, want %q", got, want)
	}
	if got, want := editor.Cursor(), byteOffsetForRuneCursor(editor.Text(), moveBufferCursorWordLeft(editor.Text(), len([]rune(editor.Text())))); got != want {
		t.Fatalf("editor cursor after alt+b = %d, want %d", got, want)
	}

	updateSingleLineEditorWithAppKeys(&editor, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}})
	if got, want := editor.Text(), "alpha beta gamma"; got != want {
		t.Fatalf("editor text after alt+f = %q, want %q", got, want)
	}
	if got, want := editor.Cursor(), len(editor.Text()); got != want {
		t.Fatalf("editor cursor after alt+f = %d, want %d", got, want)
	}
}
