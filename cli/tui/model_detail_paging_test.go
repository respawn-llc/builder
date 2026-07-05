package tui

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailTopEdgeRequestsOlderCursorPage(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		OlderCursor:  int64Ptr(64),
		HasMoreAbove: true,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "current"}},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd == nil {
		t.Fatal("expected older page request command")
	}
	msg := cmd()
	request, ok := msg.(RequestDetailTranscriptPageMsg)
	if !ok {
		t.Fatalf("page request message type = %T", msg)
	}
	if request.Request.Cursor == nil || *request.Request.Cursor != 64 || request.Request.NewerCursor != nil {
		t.Fatalf("page request = %#v, want cursor 64", request.Request)
	}
}

func TestDetailBottomEdgeRequestsNewerCursorPage(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		NewerCursor:  int64Ptr(96),
		HasMoreBelow: true,
		Entries:      []clientui.ChatEntry{{Role: "user", Text: "one"}},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("expected newer page request command")
	}
	msg := cmd()
	request, ok := msg.(RequestDetailTranscriptPageMsg)
	if !ok {
		t.Fatalf("page request message type = %T", msg)
	}
	if request.Request.NewerCursor == nil || *request.Request.NewerCursor != 96 || request.Request.Cursor != nil {
		t.Fatalf("page request = %#v, want newer cursor 96", request.Request)
	}
}

func TestDetailPageAnchorKeepsLoadedOlderPageVisible(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 1, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "older"},
			{Role: "assistant", Text: "newer"},
		}},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = next.(Model)

	if model.detailScroll != 0 {
		t.Fatalf("detail scroll = %d, want top anchor", model.detailScroll)
	}
}

func TestDetailPageAnchorKeepsLoadedNewerPageVisible(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 1, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "older"},
			{Role: "assistant", Text: "newer"},
		}},
		Anchor: DetailTranscriptAnchorBottom,
	})
	model = next.(Model)

	if model.detailScroll != model.maxDetailScroll() {
		t.Fatalf("detail scroll = %d, want bottom anchor %d", model.detailScroll, model.maxDetailScroll())
	}
}

func TestDetailPagePreserveAnchorKeepsUILocalState(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 1, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "first"},
			{Role: "assistant", Text: "second"},
		}},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = next.(Model)
	model.selected = 0
	model.expanded = map[int]struct{}{0: {}}
	model.detailScroll = 1

	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "first"},
			{Role: "assistant", Text: "second"},
			{Role: "assistant", Text: "third"},
		}},
		Anchor: DetailTranscriptAnchorPreserve,
	})
	model = next.(Model)

	if model.detailScroll != 1 || model.selected != 0 {
		t.Fatalf("preserved scroll/selection = %d/%d, want 1/0", model.detailScroll, model.selected)
	}
	if _, ok := model.expanded[0]; !ok {
		t.Fatal("expanded entry was not preserved")
	}
}

func TestDetailLineMovementSelectsItemNearestViewportCenter(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 3, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "first"},
			{Role: "assistant", Text: "second"},
			{Role: "assistant", Text: "third"},
			{Role: "assistant", Text: "fourth"},
		}},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)

	if model.selected != 2 {
		t.Fatalf("selected entry after line movement = %d, want center entry 2", model.selected)
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}
