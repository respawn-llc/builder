package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
	patchformat "core/shared/transcript/patchformat"

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
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(80),
		HasMoreBelow: false,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "newer"}},
	}
	older := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.ChatEntry{{Role: "user", Text: "older"}},
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

func TestDetailTranscriptDefaultRefreshDoesNotCollapseResidentWindow(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	newest := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(80),
		HasMoreBelow: false,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "newer"}},
	}
	older := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.ChatEntry{{Role: "user", Text: "older"}},
	}
	refreshedNewest := newest
	refreshedNewest.Entries = []clientui.ChatEntry{{Role: "assistant", Text: "newer refreshed"}}

	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{}, page: newest})
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}, page: older})
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{}, page: refreshedNewest})

	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{
		{Role: "user", Text: "older"},
		{Role: "assistant", Text: "newer"},
	}
	if !sameChatEntries(got, want) {
		t.Fatalf("detail entries after default refresh = %#v, want stale resident window %#v", got, want)
	}
	if len(model.detailTranscript.segments) != 2 {
		t.Fatalf("resident segment count = %d, want 2", len(model.detailTranscript.segments))
	}
}

func TestDetailTranscriptPageDeepClonesPatchRender(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	sourcePatch := &patchformat.RenderedPatch{Files: []patchformat.RenderedFile{{
		RelPath: "file.txt",
		Diff:    []string{"old"},
	}}}
	page := clientui.TranscriptPage{
		SessionID: "session-1",
		Entries: []clientui.ChatEntry{{
			Role:     "tool_result_ok",
			ToolCall: &clientui.ToolCallMeta{PatchRender: sourcePatch},
		}},
	}

	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{}, page: page})
	sourcePatch.Files[0].Diff[0] = "source changed"
	firstRead := model.detailTranscript.page()
	if got := firstRead.Entries[0].ToolCall.PatchRender.Files[0].Diff[0]; got != "old" {
		t.Fatalf("stored patch diff = %q, want source-isolated old diff", got)
	}

	firstRead.Entries[0].ToolCall.PatchRender.Files[0].Diff[0] = "read changed"
	secondRead := model.detailTranscript.page()
	if got := secondRead.Entries[0].ToolCall.PatchRender.Files[0].Diff[0]; got != "old" {
		t.Fatalf("page patch diff = %q, want page-isolated old diff", got)
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

func TestDetailTranscriptIgnoresRuntimeCommittedChangesUntilPageIsRequested(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID("session-1"))
	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{
		sessionID: "session-1",
		request:   clientui.TranscriptPageRequest{},
		page: clientui.TranscriptPage{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "loaded page"}},
		},
	})

	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{{Role: "assistant", Text: "loaded page"}}
	if !sameChatEntries(got, want) {
		t.Fatalf("detail entries after committed runtime event = %#v, want stale loaded page %#v", got, want)
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
