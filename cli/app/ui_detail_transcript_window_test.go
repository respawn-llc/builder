package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailModeTransitionLoadsServerBackedTranscriptPage(t *testing.T) {
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: "session-1",
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "first page"}},
	}}
	runtimeClient := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: "session-1"}}
	model := newProjectedClosedUIModel(
		runtimeClient,
		WithUISessionID("session-1"),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.view = tui.NewModel()
	if cmd := model.detailLoadCmdForModeTransition(tui.ModeOngoing, tui.ModeDetail); cmd == nil {
		t.Fatal("detail transition did not create a transcript page load command")
	}

	cmd := model.loadDetailTranscriptPageCmd(clientui.TranscriptPageRequest{})
	for _, msg := range collectCmdMessages(t, cmd) {
		if load, ok := msg.(detailTranscriptLoadMsg); ok {
			model = updateUIModel(t, model, load)
		}
	}

	if !model.detailTranscript.loaded {
		t.Fatal("detail transcript window was not loaded")
	}
	got := model.detailTranscript.page()
	want := []clientui.ChatEntry{{Role: "user", Text: "first page"}}
	if !sameChatEntries(got.Entries, want) {
		t.Fatalf("detail transcript entries = %#v, want %#v", got.Entries, want)
	}
}

func TestDetailTranscriptLoadMergesAdjacentCursorPages(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	newest := clientui.TranscriptPage{
		SessionID:      "session-1",
		OlderCursor:    appInt64Ptr(40),
		HasMoreAbove:   true,
		NewerCursor:    appInt64Ptr(80),
		HasMoreBelow:   false,
		Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "newer"}},
		Streaming:      "",
		StreamingError: "",
	}
	older := clientui.TranscriptPage{
		SessionID:      "session-1",
		OlderCursor:    appInt64Ptr(20),
		HasMoreAbove:   false,
		NewerCursor:    appInt64Ptr(40),
		HasMoreBelow:   true,
		Entries:        []clientui.ChatEntry{{Role: "user", Text: "older"}},
		Streaming:      "",
		StreamingError: "",
	}

	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{}, page: newest})
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}, page: older})

	got := model.detailTranscript.page()
	wantEntries := []clientui.ChatEntry{
		{Role: "user", Text: "older"},
		{Role: "assistant", Text: "newer"},
	}
	if !sameChatEntries(got.Entries, wantEntries) {
		t.Fatalf("merged detail entries = %#v, want %#v", got.Entries, wantEntries)
	}
	if got.OlderCursor == nil || *got.OlderCursor != 20 || got.HasMoreAbove || got.NewerCursor == nil || *got.NewerCursor != 80 || got.HasMoreBelow {
		t.Fatalf("merged cursors = older:%v above:%t newer:%v below:%t", got.OlderCursor, got.HasMoreAbove, got.NewerCursor, got.HasMoreBelow)
	}
}

func TestDetailTranscriptLoadIgnoresDuplicateAdjacentCursorResponse(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	newest := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "newer"}},
		HasMoreBelow: false,
	}
	older := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.ChatEntry{{Role: "user", Text: "older"}},
	}
	request := clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}

	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{}, page: newest})
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: request, page: older})
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: request, page: older})

	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{
		{Role: "user", Text: "older"},
		{Role: "assistant", Text: "newer"},
	}
	if !sameChatEntries(got, want) {
		t.Fatalf("deduplicated detail entries = %#v, want %#v", got, want)
	}
}

func TestDetailTranscriptLoadIgnoresStaleSessionResponse(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID("session-current"))
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{
		sessionID: "session-current",
		request:   clientui.TranscriptPageRequest{},
		page: clientui.TranscriptPage{
			SessionID: "session-current",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "current"}},
		},
	})

	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{
		sessionID: "session-stale",
		request:   clientui.TranscriptPageRequest{},
		page: clientui.TranscriptPage{
			SessionID: "session-stale",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "stale"}},
		},
	})

	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{{Role: "assistant", Text: "current"}}
	if !sameChatEntries(got, want) {
		t.Fatalf("detail entries after stale response = %#v, want %#v", got, want)
	}
}

func TestDetailTranscriptSuffixAppendsCommittedChangesWithoutOngoingMirror(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{
		request: clientui.TranscriptPageRequest{},
		page: clientui.TranscriptPage{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "first"}},
		},
	})

	model.handleDetailTranscriptSuffixLoad(detailTranscriptSuffixLoadMsg{suffix: clientui.CommittedTranscriptSuffix{
		SessionID: "session-1",
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "first"},
			{Role: "assistant", Text: "second"},
		},
	}})

	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{
		{Role: "assistant", Text: "first"},
		{Role: "assistant", Text: "second"},
	}
	if !sameChatEntries(got, want) {
		t.Fatalf("suffix-updated detail entries = %#v, want %#v", got, want)
	}
}

func TestDetailTranscriptSuffixClearsStreamingOnFullOverlap(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID("session-1"))
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{
		sessionID: "session-1",
		request:   clientui.TranscriptPageRequest{},
		page: clientui.TranscriptPage{
			SessionID:   "session-1",
			Entries:     []clientui.ChatEntry{{Role: "assistant", Text: "first"}},
			Streaming:   "old live tail",
			OlderCursor: nil,
		},
	})

	model.handleDetailTranscriptSuffixLoad(detailTranscriptSuffixLoadMsg{
		sessionID: "session-1",
		suffix: clientui.CommittedTranscriptSuffix{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "first"}},
		},
	})

	if streaming := model.detailTranscript.page().Streaming; streaming != "" {
		t.Fatalf("streaming after overlapping suffix = %q, want empty", streaming)
	}
}

func TestDetailTranscriptSuffixNoOverlapAtBottomReloadsNewestPage(t *testing.T) {
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: "session-1",
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "post compaction"}},
	}}
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		WithUISessionID("session-1"),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{
		sessionID: "session-1",
		request:   clientui.TranscriptPageRequest{},
		page: clientui.TranscriptPage{
			SessionID:    "session-1",
			HasMoreBelow: false,
			Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "pre compaction"}},
		},
	})

	cmd := model.handleDetailTranscriptSuffixLoad(detailTranscriptSuffixLoadMsg{
		sessionID: "session-1",
		suffix: clientui.CommittedTranscriptSuffix{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "post compaction"}},
		},
	})
	if cmd == nil {
		t.Fatal("expected no-overlap newest suffix at bottom edge to reload newest page")
	}

	var loaded bool
	for _, msg := range collectCmdMessages(t, cmd) {
		load, ok := msg.(detailTranscriptLoadMsg)
		if !ok {
			continue
		}
		loaded = true
		if load.sessionID != "session-1" || load.request.Cursor != nil || load.request.NewerCursor != nil {
			t.Fatalf("reload request = session:%q request:%#v, want newest session-1", load.sessionID, load.request)
		}
		model = updateUIModel(t, model, load)
	}
	if !loaded {
		t.Fatal("newest reload command did not return a detail transcript load message")
	}
	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{{Role: "assistant", Text: "post compaction"}}
	if !sameChatEntries(got, want) {
		t.Fatalf("reloaded detail entries = %#v, want %#v", got, want)
	}
}

func TestDetailEdgeRequestMessageLoadsAdjacentPage(t *testing.T) {
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: "session-1",
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "older page"}},
	}}
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: "session-1"}},
		WithUISessionID("session-1"),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)

	next, cmd := model.Update(tui.RequestDetailTranscriptPageMsg{Request: clientui.TranscriptPageRequest{Cursor: appInt64Ptr(25)}})
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		if load, ok := msg.(detailTranscriptLoadMsg); ok {
			model = updateUIModel(t, model, load)
		}
	}

	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{{Role: "user", Text: "older page"}}
	if !sameChatEntries(got, want) {
		t.Fatalf("loaded adjacent entries = %#v, want %#v", got, want)
	}
}

func sameChatEntries(left []clientui.ChatEntry, right []clientui.ChatEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx].Role != right[idx].Role || left[idx].Text != right[idx].Text {
			return false
		}
	}
	return true
}

var _ tea.Msg = detailTranscriptLoadMsg{}

func appInt64Ptr(value int64) *int64 {
	return &value
}
