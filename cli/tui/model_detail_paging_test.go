package tui

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailTopEdgeEmitsOlderPageDirection(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{{Role: "assistant", Text: "current"}},
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
	if request.Direction != DetailTranscriptPageOlder {
		t.Fatalf("page direction = %v, want older", request.Direction)
	}
}

func TestDetailBottomEdgeEmitsNewerPageDirection(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{{Role: "user", Text: "one"}},
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
	if request.Direction != DetailTranscriptPageNewer {
		t.Fatalf("page direction = %v, want newer", request.Direction)
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
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected entry = %d/%t, want top entry 0/true", selected, ok)
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
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected entry = %d/%t, want bottom entry 1/true", selected, ok)
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
	model.setSelectedDetailIndex(0)
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

	if selected, ok := model.selectedDetailIndex(); model.detailScroll != 1 || !ok || selected != 0 {
		t.Fatalf("preserved scroll/selection = %d/%d/%t, want 1/0/true", model.detailScroll, selected, ok)
	}
	if _, ok := model.expanded[0]; !ok {
		t.Fatal("expanded entry was not preserved")
	}
}

func TestDetailPrependedPagePreservesLineScrollBoundary(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			OlderCursor:  int64Ptr(64),
			HasMoreAbove: true,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
			},
		},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	model.setSelectedDetailIndex(0)
	model.detailScroll = 0

	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			Entries: []clientui.ChatEntry{
				{Role: "user", Text: "older"},
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
			},
		},
		Anchor:                DetailTranscriptAnchorPreserve,
		PrependedEntriesCount: 1,
	})
	model = next.(Model)

	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected entry after prepend = %d/%t, want previous first entry shifted to 1/true", selected, ok)
	}
	if model.detailScroll == 0 {
		t.Fatal("detail scroll after prepend stayed at loaded page top; want previous boundary preserved below prepended rows")
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Fatal("up after prepended page requested another page instead of moving by one rendered line")
	}
	updated := next.(Model)
	if updated.detailScroll != model.detailScroll-1 {
		t.Fatalf("detail scroll after up = %d, want %d", updated.detailScroll, model.detailScroll-1)
	}
}

func TestDetailPrependedPageIgnoresHiddenEntriesWhenPreservingBoundary(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			OlderCursor:  int64Ptr(64),
			HasMoreAbove: true,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
			},
		},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	model.setSelectedDetailIndex(0)
	model.detailScroll = 0

	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			Entries: []clientui.ChatEntry{
				{Visibility: clientui.EntryVisibilityHidden, Role: "system", Text: "hidden older"},
				{Role: "user", Text: "visible older"},
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
			},
		},
		Anchor:                DetailTranscriptAnchorPreserve,
		PrependedEntriesCount: 2,
	})
	model = next.(Model)

	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected entry after hidden prepend = %d/%t, want previous first entry shifted by one visible row", selected, ok)
	}
	if model.detailScroll != 2 {
		t.Fatalf("detail scroll after hidden prepend = %d, want one visible prepended entry offset including separator", model.detailScroll)
	}
}

// Spec: detail scroll is line-oriented. Prepending an older page must keep the
// viewport on the same content line the user was reading, even when the entire
// prepended page is hidden (X visibility) entries — those add zero rendered
// lines, so the viewport content does not shift and scroll must not reset to
// the top of the new page. A subsequent Up at the new top should move by one
// rendered line, not re-fire a page request when content already sits above.
func TestDetailPrependedAllHiddenPageDoesNotJumpToTopOrRefire(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	// Load a page with more entries than the viewport so scrolling is possible.
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			OlderCursor:  int64Ptr(64),
			HasMoreAbove: true,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
				{Role: "assistant", Text: "current third"},
				{Role: "assistant", Text: "current fourth"},
			},
		},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	// User scrolled down one rendered line, so the top boundary is no longer at
	// scroll 0 — they are mid-page.
	model.detailScroll = 1
	beforeScroll := model.detailScroll
	if model.maxDetailScroll() < beforeScroll {
		t.Fatalf("test setup invalid: maxDetailScroll=%d < beforeScroll=%d", model.maxDetailScroll(), beforeScroll)
	}

	// Prepend an older page whose entries are ALL hidden.
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			Entries: []clientui.ChatEntry{
				{Visibility: clientui.EntryVisibilityHidden, Role: "system", Text: "hidden one"},
				{Visibility: clientui.EntryVisibilityHidden, Role: "system", Text: "hidden two"},
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
				{Role: "assistant", Text: "current third"},
				{Role: "assistant", Text: "current fourth"},
			},
		},
		Anchor:                DetailTranscriptAnchorPreserve,
		PrependedEntriesCount: 2,
	})
	model = next.(Model)

	// No visible prepended rows => viewport content unchanged => scroll must
	// stay where it was, not jump to 0 (top of the new page).
	if model.detailScroll != beforeScroll {
		t.Fatalf("detail scroll after all-hidden prepend = %d, want %d (same content line, no jump to top)", model.detailScroll, beforeScroll)
	}
	// Selection should remain on the same visible entry the user had selected.
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected entry after all-hidden prepend = %d/%t, want 0/true (unchanged)", selected, ok)
	}
}

func TestDetailAppendedPagePreservesLineScrollBoundary(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			NewerCursor:  int64Ptr(96),
			HasMoreBelow: true,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
			},
		},
		Anchor: DetailTranscriptAnchorBottom,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	model.detailScroll = model.maxDetailScroll()
	beforeScroll := model.detailScroll

	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "current first"},
				{Role: "assistant", Text: "current second"},
				{Role: "user", Text: "newer"},
			},
		},
		Anchor: DetailTranscriptAnchorPreserve,
	})
	model = next.(Model)

	if model.detailScroll != beforeScroll {
		t.Fatalf("detail scroll after append = %d, want previous boundary %d", model.detailScroll, beforeScroll)
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatal("down after appended page requested another page instead of moving by one rendered line")
	}
	updated := next.(Model)
	if updated.detailScroll != beforeScroll+1 {
		t.Fatalf("detail scroll after down = %d, want %d", updated.detailScroll, beforeScroll+1)
	}
}

func TestDetailAppendedPageWithFrontTrimPreservesLineScrollBoundary(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 2, Width: 80})
	model = next.(Model)
	trimmedFront := clientui.ChatEntry{Role: "assistant", Text: "trimmed front"}
	keptFirst := clientui.ChatEntry{Role: "assistant", Text: "kept first"}
	keptSecond := clientui.ChatEntry{Role: "assistant", Text: "kept second"}
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			NewerCursor:  int64Ptr(96),
			HasMoreBelow: true,
			Entries:      []clientui.ChatEntry{trimmedFront, keptFirst, keptSecond},
		},
		Anchor: DetailTranscriptAnchorBottom,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	model.setSelectedDetailIndex(1)
	model.expanded = map[int]struct{}{1: {}}
	model.detailScroll = model.detailLineOffsetForEntryIndex(1)
	beforeScroll := model.detailScroll
	trimmedOffset := model.detailLineOffsetForEntryIndex(1)

	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			Entries: []clientui.ChatEntry{
				keptFirst,
				keptSecond,
				{Role: "user", Text: "newer"},
			},
		},
		Anchor:              DetailTranscriptAnchorPreserve,
		TrimmedFrontEntries: []clientui.ChatEntry{trimmedFront},
	})
	model = next.(Model)

	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected entry after front trim = %d/%t, want kept entry shifted to 0/true", selected, ok)
	}
	if _, ok := model.expanded[0]; !ok {
		t.Fatal("expanded entry was not shifted after front trim")
	}
	if model.detailScroll != beforeScroll-trimmedOffset {
		t.Fatalf("detail scroll after front trim = %d, want %d", model.detailScroll, beforeScroll-trimmedOffset)
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatal("down after front-trimmed append requested another page instead of moving by one rendered line")
	}
	updated := next.(Model)
	if updated.detailScroll != model.detailScroll {
		t.Fatalf("detail scroll after down = %d, want pinned boundary %d", updated.detailScroll, model.detailScroll)
	}
	if selected, ok := updated.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("detail selection after down = %d/%t, want center entry 1/true", selected, ok)
	}
}

func TestDetailCollapseClampsStoredScroll(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 1, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "one"},
			{Role: "assistant", Text: "two\nthree\nfour"},
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	model.setSelectedDetailIndex(1)

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	model.detailScroll = model.maxDetailScroll()
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)

	if got, maxScroll := model.detailScroll, model.maxDetailScroll(); got != maxScroll {
		t.Fatalf("detail scroll after collapse = %d, want clamped max %d", got, maxScroll)
	}
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("down after local collapse exhaustion did not emit a newer-page direction")
	}
	request, ok := cmd().(RequestDetailTranscriptPageMsg)
	if !ok || request.Direction != DetailTranscriptPageNewer {
		t.Fatalf("down after collapse message = %#v, want newer-page direction", request)
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

	if model.detailScroll != 0 {
		t.Fatalf("detail scroll after top-edge reverse movement = %d, want 0", model.detailScroll)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected entry after line movement = %d/%t, want center entry 1/true", selected, ok)
	}
}

func TestDetailPageInvalidAnchorPanics(t *testing.T) {
	model := NewModel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid anchor panic")
		}
	}()

	_, _ = model.Update(SetDetailTranscriptPageMsg{
		Page:   clientui.TranscriptPage{Entries: []clientui.ChatEntry{{Role: "assistant", Text: "entry"}}},
		Anchor: DetailTranscriptPageAnchor(99),
	})
}

func int64Ptr(value int64) *int64 {
	return &value
}
