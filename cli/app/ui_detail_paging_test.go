package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailModeKeyForwardingReturnsPageRequestCommand(t *testing.T) {
	m := newProjectedStaticUIModel()
	olderCursor := int64(64)
	m.forwardToView(tui.SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		OlderCursor:  &olderCursor,
		HasMoreAbove: true,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "current"}},
	}})
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail})

	_, cmd := m.inputController().handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if cmd == nil {
		t.Fatal("expected detail page request command")
	}
	msg := cmd()
	request, ok := msg.(tui.RequestDetailTranscriptPageMsg)
	if !ok {
		t.Fatalf("command message = %T, want detail page request", msg)
	}
	if request.Request.Cursor == nil || *request.Request.Cursor != 64 {
		t.Fatalf("page request = %#v, want older cursor 64", request.Request)
	}
}
