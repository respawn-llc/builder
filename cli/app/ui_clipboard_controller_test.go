package app

import (
	"context"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type clipboardPasterFunc func(context.Context) (uiClipboardContent, error)

func (f clipboardPasterFunc) Paste(ctx context.Context) (uiClipboardContent, error) {
	return f(ctx)
}

type clipboardTestInputTarget uint8

const (
	clipboardTestInputMain clipboardTestInputTarget = iota
	clipboardTestInputAsk
)

func setupClipboardTestInput(t *testing.T, m *uiModel, target clipboardTestInputTarget) *uiModel {
	t.Helper()
	switch target {
	case clipboardTestInputMain:
		m.replaceMainInput("beforeafter", len([]rune("before")))
		return m
	case clipboardTestInputAsk:
		next, _ := m.Update(askEventMsg{event: askEvent{
			req:   clientui.PendingPromptEvent{Question: "Provide details"},
			reply: make(chan askReply, 1),
		}})
		updated := next.(*uiModel)
		if !updated.ask.freeform {
			t.Fatal("expected freeform ask input")
		}
		updated.ask.input = "beforeafter"
		updated.ask.inputCursor = len([]rune("before"))
		return updated
	default:
		t.Fatalf("unknown clipboard test input target %d", target)
		return nil
	}
}

func clipboardTestInputText(m *uiModel, target clipboardTestInputTarget) string {
	switch target {
	case clipboardTestInputMain:
		return m.input
	case clipboardTestInputAsk:
		return m.ask.input
	default:
		return ""
	}
}

type unexpectedClipboardContent struct{}

func (unexpectedClipboardContent) uiClipboardContent() {}

func TestClipboardPasteInsertsMultilineUnicodeTextAtMainCursor(t *testing.T) {
	calls := 0
	m := newProjectedStaticUIModel(WithUIClipboardPaster(clipboardPasterFunc(func(context.Context) (uiClipboardContent, error) {
		calls++
		return uiClipboardText{Text: "α\nβ"}, nil
	})))
	m.replaceMainInput("beforeafter", len([]rune("before")))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("expected explicit clipboard paste command")
	}
	if calls != 0 {
		t.Fatalf("clipboard read calls = %d, want 0 before command execution", calls)
	}

	next, _ = next.Update(cmd())
	updated := next.(*uiModel)
	if calls != 1 {
		t.Fatalf("clipboard read calls = %d, want 1", calls)
	}
	if got, want := updated.input, "beforeα\nβafter"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := updated.inputCursor, len([]rune("beforeα\nβ")); got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestClipboardPasteDoneInsertsTextAndImageAtActiveCursor(t *testing.T) {
	tests := []struct {
		name    string
		target  clipboardTestInputTarget
		content uiClipboardContent
		want    string
	}{
		{
			name:    "main text",
			target:  clipboardTestInputMain,
			content: uiClipboardText{Text: "α\nβ"},
			want:    "beforeα\nβafter",
		},
		{
			name:    "freeform ask text",
			target:  clipboardTestInputAsk,
			content: uiClipboardText{Text: "α\nβ"},
			want:    "beforeα\nβafter",
		},
		{
			name:    "main image path",
			target:  clipboardTestInputMain,
			content: newRetainedClipboardImage("/tmp/kent-clipboard.png"),
			want:    "before/tmp/kent-clipboard.pngafter",
		},
		{
			name:    "freeform ask image path",
			target:  clipboardTestInputAsk,
			content: newRetainedClipboardImage("/tmp/kent-clipboard.png"),
			want:    "before/tmp/kent-clipboard.pngafter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := setupClipboardTestInput(t, newProjectedStaticUIModel(), tc.target)
			msg := clipboardPasteDoneMsg{Content: tc.content}
			if tc.target == clipboardTestInputMain {
				msg.Target = uiClipboardPasteTargetMain
				msg.MainDraftToken = m.mainInputDraftToken
			} else {
				msg.Target = uiClipboardPasteTargetAsk
				msg.AskToken = m.ask.currentToken
			}

			next, _ := m.Update(msg)
			updated := next.(*uiModel)
			if got := clipboardTestInputText(updated, tc.target); got != tc.want {
				t.Fatalf("input = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClipboardPasteDoneRejectsStaleTarget(t *testing.T) {
	t.Run("replaced main draft", func(t *testing.T) {
		m := setupClipboardTestInput(t, newProjectedStaticUIModel(), clipboardTestInputMain)
		staleToken := m.mainInputDraftToken
		m.replaceMainInput("replacement", -1)

		next, _ := m.Update(clipboardPasteDoneMsg{
			Target:         uiClipboardPasteTargetMain,
			MainDraftToken: staleToken,
			Content:        uiClipboardText{Text: "clipboard"},
		})
		updated := next.(*uiModel)
		if got, want := updated.input, "replacement"; got != want {
			t.Fatalf("input = %q, want %q", got, want)
		}
	})

	t.Run("replaced ask", func(t *testing.T) {
		m := setupClipboardTestInput(t, newProjectedStaticUIModel(), clipboardTestInputAsk)
		staleToken := m.ask.currentToken
		testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: "Replacement"}, reply: make(chan askReply, 1)})
		m.ask.freeform = true
		m.ask.input = "replacement"

		next, _ := m.Update(clipboardPasteDoneMsg{
			Target:   uiClipboardPasteTargetAsk,
			AskToken: staleToken,
			Content:  uiClipboardText{Text: "clipboard"},
		})
		updated := next.(*uiModel)
		if got, want := updated.ask.input, "replacement"; got != want {
			t.Fatalf("ask input = %q, want %q", got, want)
		}
	})

	t.Run("closed ask", func(t *testing.T) {
		m := setupClipboardTestInput(t, newProjectedStaticUIModel(), clipboardTestInputAsk)
		staleToken := m.ask.currentToken
		testSetActiveAsk(m, nil)

		next, _ := m.Update(clipboardPasteDoneMsg{
			Target:   uiClipboardPasteTargetAsk,
			AskToken: staleToken,
			Content:  uiClipboardText{Text: "clipboard"},
		})
		updated := next.(*uiModel)
		if updated.ask.current != nil || updated.ask.input != "beforeafter" {
			t.Fatalf("closed ask changed: current=%+v input=%q", updated.ask.current, updated.ask.input)
		}
	})
}

func TestClipboardPasteDoneRemovesStaleTemporaryImage(t *testing.T) {
	tests := []struct {
		name   string
		target clipboardTestInputTarget
	}{
		{name: "main", target: clipboardTestInputMain},
		{name: "ask", target: clipboardTestInputAsk},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := setupClipboardTestInput(t, newProjectedStaticUIModel(), tc.target)
			removed := 0
			image := newTemporaryClipboardImage("/tmp/kent-stale-clipboard.png", &uiClipboardTempImage{
				path: "/tmp/kent-stale-clipboard.png",
				remove: func(string) error {
					removed++
					return nil
				},
			})
			msg := clipboardPasteDoneMsg{Content: image}
			if tc.target == clipboardTestInputMain {
				msg.Target = uiClipboardPasteTargetMain
				msg.MainDraftToken = m.mainInputDraftToken
				m.replaceMainInput("replacement", -1)
			} else {
				msg.Target = uiClipboardPasteTargetAsk
				msg.AskToken = m.ask.currentToken
				testSetActiveAsk(m, &askEvent{req: clientui.PendingPromptEvent{Question: "Replacement"}, reply: make(chan askReply, 1)})
				m.ask.freeform = true
			}

			next, _ := m.Update(msg)
			updated := next.(*uiModel)
			if removed != 1 {
				t.Fatalf("temporary image remove calls = %d, want 1", removed)
			}
			if tc.target == clipboardTestInputMain && updated.input != "replacement" {
				t.Fatalf("input = %q, want replacement", updated.input)
			}
		})
	}
}

func TestClipboardPasteDoneRejectsEmptyAndUnknownContent(t *testing.T) {
	tests := []struct {
		name    string
		content uiClipboardContent
	}{
		{name: "empty text", content: uiClipboardText{}},
		{name: "unknown content", content: unexpectedClipboardContent{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := setupClipboardTestInput(t, newProjectedStaticUIModel(), clipboardTestInputMain)
			next, _ := m.Update(clipboardPasteDoneMsg{
				Target:         uiClipboardPasteTargetMain,
				MainDraftToken: m.mainInputDraftToken,
				Content:        tc.content,
			})
			updated := next.(*uiModel)
			if got, want := updated.input, "beforeafter"; got != want {
				t.Fatalf("input = %q, want %q", got, want)
			}
			if updated.transientStatus == "" {
				t.Fatal("expected clipboard paste failure status")
			}
		})
	}
}

func TestClipboardPasteBindingsDispatchForMainAndFreeformAskInput(t *testing.T) {
	tests := []struct {
		name   string
		key    tea.KeyMsg
		target clipboardTestInputTarget
	}{
		{
			name:   "main ctrl+v",
			key:    tea.KeyMsg{Type: tea.KeyCtrlV},
			target: clipboardTestInputMain,
		},
		{
			name:   "main ctrl+d",
			key:    tea.KeyMsg{Type: tea.KeyCtrlD},
			target: clipboardTestInputMain,
		},
		{
			name:   "main alt+v",
			key:    tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true},
			target: clipboardTestInputMain,
		},
		{
			name:   "main alt+d",
			key:    tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}, Alt: true},
			target: clipboardTestInputMain,
		},
		{
			name:   "freeform ask ctrl+v",
			key:    tea.KeyMsg{Type: tea.KeyCtrlV},
			target: clipboardTestInputAsk,
		},
		{
			name:   "freeform ask ctrl+d",
			key:    tea.KeyMsg{Type: tea.KeyCtrlD},
			target: clipboardTestInputAsk,
		},
		{
			name:   "freeform ask alt+v",
			key:    tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true},
			target: clipboardTestInputAsk,
		},
		{
			name:   "freeform ask alt+d",
			key:    tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}, Alt: true},
			target: clipboardTestInputAsk,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			m := setupClipboardTestInput(t, newProjectedStaticUIModel(WithUIClipboardPaster(clipboardPasterFunc(func(context.Context) (uiClipboardContent, error) {
				calls++
				return uiClipboardText{Text: "clipboard"}, nil
			}))), tc.target)

			next, cmd := m.Update(tc.key)
			if cmd == nil {
				t.Fatal("expected explicit clipboard paste command")
			}
			next, _ = next.Update(cmd())
			updated := next.(*uiModel)

			if calls != 1 {
				t.Fatalf("clipboard read calls = %d, want 1", calls)
			}
			if got, want := clipboardTestInputText(updated, tc.target), "beforeclipboardafter"; got != want {
				t.Fatalf("input = %q, want %q", got, want)
			}
		})
	}
}

func TestClipboardBracketedPasteInsertsOnceWithoutClipboardRead(t *testing.T) {
	tests := []struct {
		name   string
		target clipboardTestInputTarget
	}{
		{
			name:   "main",
			target: clipboardTestInputMain,
		},
		{
			name:   "freeform ask",
			target: clipboardTestInputAsk,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			m := setupClipboardTestInput(t, newProjectedStaticUIModel(WithUIClipboardPaster(clipboardPasterFunc(func(context.Context) (uiClipboardContent, error) {
				calls++
				return uiClipboardText{Text: "clipboard"}, nil
			}))), tc.target)

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("α\nβ"), Paste: true})
			updated := next.(*uiModel)

			if calls != 0 {
				t.Fatalf("clipboard read calls = %d, want 0", calls)
			}
			if got, want := clipboardTestInputText(updated, tc.target), "beforeα\nβafter"; got != want {
				t.Fatalf("input = %q, want %q", got, want)
			}
		})
	}
}

func TestClipboardPasteBindingLeavesNonFreeformAskUnchanged(t *testing.T) {
	calls := 0
	m := newProjectedStaticUIModel(WithUIClipboardPaster(clipboardPasterFunc(func(context.Context) (uiClipboardContent, error) {
		calls++
		return uiClipboardText{Text: "clipboard"}, nil
	})))
	next, _ := m.Update(askEventMsg{event: askEvent{
		req: clientui.PendingPromptEvent{
			Question:    "Choose one",
			Suggestions: []string{"first", "second"},
		},
		reply: make(chan askReply, 1),
	}})
	m = next.(*uiModel)
	if m.ask.freeform {
		t.Fatal("expected non-freeform ask")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated := next.(*uiModel)
	if cmd != nil {
		t.Fatal("did not expect clipboard paste command for non-freeform ask")
	}
	if calls != 0 {
		t.Fatalf("clipboard read calls = %d, want 0", calls)
	}
	if updated.ask.freeform || updated.ask.input != "" {
		t.Fatalf("non-freeform ask changed: freeform=%v input=%q", updated.ask.freeform, updated.ask.input)
	}
}
