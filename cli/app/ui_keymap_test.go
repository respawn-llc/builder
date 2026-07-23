package app

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type testBubbleTeaUnknownCSISequence []byte

func TestNormalizeKeyMsgStripsConcatenatedMouseSGRRunes(t *testing.T) {
	message := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;74;25M[<64;74;25M[<65;74;25M")}
	_, ok := normalizeKeyMsg(message)
	if ok {
		t.Fatal("expected concatenated mouse sgr reports to be consumed")
	}
}

func TestNormalizeKeyMsgStripsEscPrefixedMouseSGRRunes(t *testing.T) {
	message := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[<64;74;25M")}
	_, ok := normalizeKeyMsg(message)
	if ok {
		t.Fatal("expected esc-prefixed mouse sgr report to be consumed")
	}
}

func TestNormalizeKeyMsgPreservesNonMouseRunes(t *testing.T) {
	message := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}
	normalized, ok := normalizeKeyMsg(message)
	if !ok {
		t.Fatal("expected non-mouse runes to be preserved")
	}
	if string(normalized.Runes) != "hello" {
		t.Fatalf("expected runes unchanged, got %q", string(normalized.Runes))
	}
}

func TestNormalizeKeyMsgConvertsBareEscapeRune(t *testing.T) {
	message := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\x1b'}}
	normalized, ok := normalizeKeyMsg(message)
	if !ok {
		t.Fatal("expected bare escape rune to normalize")
	}
	if normalized.Type != tea.KeyEsc {
		t.Fatalf("expected KeyEsc, got %v", normalized.Type)
	}
	if len(normalized.Runes) != 0 {
		t.Fatalf("expected normalized escape to clear runes, got %q", string(normalized.Runes))
	}
}

func TestNormalizeKeyMsgRecognizesShiftEnterCSIUVariants(t *testing.T) {
	tests := []struct {
		name string
		seq  string
	}{
		{name: "bare csi-u", seq: "\x1b[13;2u"},
		{name: "esc prefixed csi-u", seq: "\x1b[27;2;13u"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := adaptCustomKeyMsg(testBubbleTeaUnknownCSISequence(tc.seq))
			normalized, ok := normalizeKeyMsg(msg)
			if !ok {
				t.Fatal("expected shift+enter csi sequence to normalize")
			}
			if normalized.Type != keyTypeShiftEnterCSI {
				t.Fatalf("expected keyTypeShiftEnterCSI, got %v", normalized.Type)
			}
		})
	}
}

func TestNormalizeKeyMsgRecognizesHelpCSIUVariants(t *testing.T) {
	tests := []struct {
		name string
		seq  string
	}{
		{name: "cmd question mark", seq: "\x1b[63;10u"},
		{name: "cmd slash", seq: "\x1b[47;10u"},
		{name: "esc prefixed cmd question mark", seq: "\x1b[27;10;63u"},
		{name: "esc prefixed cmd slash", seq: "\x1b[27;10;47u"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := adaptCustomKeyMsg(testBubbleTeaUnknownCSISequence(tc.seq))
			normalized, ok := normalizeKeyMsg(msg)
			if !ok {
				t.Fatal("expected help csi sequence to normalize")
			}
			if normalized.Type != keyTypeHelpCSI {
				t.Fatalf("expected keyTypeHelpCSI, got %v", normalized.Type)
			}
		})
	}
}

func TestIsHelpKeyRecognizesPrimaryAndFallbackBindings(t *testing.T) {
	emptyPromptModel := &uiModel{}
	draftModel := &uiModel{uiInputFeatureState: uiInputFeatureState{mainEditor: newSingleLineEditor("draft")}}

	if !isHelpKey(tea.KeyMsg{Type: tea.KeyF1}, draftModel) {
		t.Fatal("expected f1 to toggle help")
	}
	if !isHelpKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}, Alt: true}, draftModel) {
		t.Fatal("expected alt+? rune key to toggle help")
	}
	if !isHelpKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}, Alt: true}, draftModel) {
		t.Fatal("expected alt+/ rune key to toggle help")
	}
	if !isHelpKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, emptyPromptModel) {
		t.Fatal("expected plain ? to toggle help from an empty prompt")
	}
	if isHelpKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, draftModel) {
		t.Fatal("did not expect plain ? rune key to toggle help while a draft is present")
	}
	if isHelpKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, emptyPromptModel) {
		t.Fatal("did not expect plain / rune key to toggle help")
	}
}

func TestAdaptCustomKeyMsgLeavesNonCustomUnknownCSIUntouched(t *testing.T) {
	msg := testBubbleTeaUnknownCSISequence("\x1b[1;9A")
	adapted := adaptCustomKeyMsg(msg)
	if reflect.TypeOf(adapted) != reflect.TypeOf(msg) {
		t.Fatalf("expected non-custom unknown csi to remain untouched, got %T", adapted)
	}
}
