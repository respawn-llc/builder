package app

import (
	"testing"

	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSharedInputMovementKeyMapsNavigationAliases(t *testing.T) {
	cases := []struct {
		name          string
		key           tea.KeyMsg
		initialCursor int
		wantCursor    int
	}{
		{name: "left", key: tea.KeyMsg{Type: tea.KeyLeft}, initialCursor: len([]rune("alpha beta gamma")), wantCursor: len([]rune("alpha beta gamm"))},
		{name: "right", key: tea.KeyMsg{Type: tea.KeyRight}, initialCursor: len([]rune("alpha beta gamm")), wantCursor: len([]rune("alpha beta gamma"))},
		{name: "alt-left", key: tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, initialCursor: len([]rune("alpha beta gamma")), wantCursor: len([]rune("alpha beta "))},
		{name: "alt-right", key: tea.KeyMsg{Type: tea.KeyRight, Alt: true}, initialCursor: len([]rune("alpha beta ")), wantCursor: len([]rune("alpha beta gamma"))},
		{name: "ctrl-left", key: tea.KeyMsg{Type: tea.KeyCtrlLeft}, initialCursor: len([]rune("alpha beta gamma")), wantCursor: len([]rune("alpha beta "))},
		{name: "ctrl-right", key: tea.KeyMsg{Type: tea.KeyCtrlRight}, initialCursor: len([]rune("alpha beta ")), wantCursor: len([]rune("alpha beta gamma"))},
		{name: "alt-b", key: tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}}, initialCursor: len([]rune("alpha beta gamma")), wantCursor: len([]rune("alpha beta "))},
		{name: "alt-f", key: tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}}, initialCursor: len([]rune("alpha beta ")), wantCursor: len([]rune("alpha beta gamma"))},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			editor := tuiinput.NewEditor()
			editor.Replace("alpha beta gamma")
			editor.SetCursor(byteOffsetForRuneCursor(editor.Text(), tt.initialCursor))
			if !applySharedInputMovementKey(tt.key, &editor) {
				t.Fatal("expected movement key to be handled")
			}
			if got := runeOffsetForByteCursor(editor.Text(), editor.Cursor()); got != tt.wantCursor {
				t.Fatalf("cursor = %d, want %d", got, tt.wantCursor)
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
		editor := tuiinput.NewEditor()
		if applySharedInputMovementKey(key, &editor) {
			t.Fatalf("unexpectedly handled %q", key.String())
		}
	}
}

func TestMainComposerAltRuneWordNavigation(t *testing.T) {
	model := newProjectedStaticUIModel()
	testSetMainInput(model, "alpha beta gamma")

	updated := updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}})
	if got, want := testMainInput(updated), "alpha beta gamma"; got != want {
		t.Fatalf("input after alt+b = %q, want %q", got, want)
	}
	if got, want := testMainInputRuneCursor(updated), len([]rune("alpha beta ")); got != want {
		t.Fatalf("cursor after alt+b = %d, want %d", got, want)
	}

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}})
	if got, want := testMainInput(updated), "alpha beta gamma"; got != want {
		t.Fatalf("input after alt+f = %q, want %q", got, want)
	}
	if got, want := testMainInputRuneCursor(updated), len([]rune(testMainInput(updated))); got != want {
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
			if got := testMainInput(updated); got != tt.want {
				t.Fatalf("input = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAskFreeformAltRuneWordNavigation(t *testing.T) {
	model := newProjectedStaticUIModel()
	event := testQuestionAskEvent("ask-1", "Type answer")
	updated := updateUIModel(t, model, askEventMsg{event: event})
	testSetAskInput(updated, "alpha beta gamma")

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}})
	if got, want := testAskInput(updated), "alpha beta gamma"; got != want {
		t.Fatalf("ask input after alt+b = %q, want %q", got, want)
	}
	if got, want := testAskInputRuneCursor(updated), len([]rune("alpha beta ")); got != want {
		t.Fatalf("ask cursor after alt+b = %d, want %d", got, want)
	}

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}})
	if got, want := testAskInput(updated), "alpha beta gamma"; got != want {
		t.Fatalf("ask input after alt+f = %q, want %q", got, want)
	}
	if got, want := testAskInputRuneCursor(updated), len([]rune(testAskInput(updated))); got != want {
		t.Fatalf("ask cursor after alt+f = %d, want %d", got, want)
	}
}

func TestMainAndAskHomeEndMoveWithinLogicalLine(t *testing.T) {
	const text = "one\ntwo\nthree"
	cursor := len([]rune("one\ntw"))
	lineStart := len([]rune("one\n"))
	lineEnd := len([]rune("one\ntwo"))

	t.Run("main", func(t *testing.T) {
		for _, test := range []struct {
			key  tea.KeyType
			want int
		}{{tea.KeyHome, lineStart}, {tea.KeyEnd, lineEnd}} {
			m := newProjectedStaticUIModel()
			testSetMainInputAtRuneCursor(m, text, cursor)
			m = updateUIModel(t, m, tea.KeyMsg{Type: test.key})
			if got := testMainInputRuneCursor(m); got != test.want {
				t.Fatalf("%s cursor = %d, want %d", test.key, got, test.want)
			}
		}
	})

	t.Run("ask", func(t *testing.T) {
		for _, test := range []struct {
			key  tea.KeyType
			want int
		}{{tea.KeyHome, lineStart}, {tea.KeyEnd, lineEnd}} {
			m := newProjectedStaticUIModel()
			event := testQuestionAskEvent("ask-1", "Type answer")
			m = updateUIModel(t, m, askEventMsg{event: event})
			testSetAskInputAtRuneCursor(m, text, cursor)
			m = updateUIModel(t, m, tea.KeyMsg{Type: test.key})
			if got := testAskInputRuneCursor(m); got != test.want {
				t.Fatalf("%s cursor = %d, want %d", test.key, got, test.want)
			}
		}
	})
}
