package app

import (
	"strconv"
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

func TestDetailTranscriptDefaultRefreshDoesNotMutateViewLocalState(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	model.view = tui.NewModel()
	model.view = mustUpdateTUIModel(t, model.view, tui.SetViewportSizeMsg{Lines: 2, Width: 80})
	model.view = mustUpdateTUIModel(t, model.view, tui.SetModeMsg{Mode: tui.ModeDetail})
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
	model.view = mustUpdateTUIModel(t, model.view, tea.KeyMsg{Type: tea.KeyEnter})
	if got := model.view.DetailSelectionAction(); got != tui.DetailSelectionActionCollapse {
		t.Fatalf("detail selection action after expand = %v, want collapse", got)
	}

	model.handleDetailTranscriptLoad(detailTranscriptLoadMsg{request: clientui.TranscriptPageRequest{}, page: refreshedNewest})

	if got := model.view.DetailSelectionAction(); got != tui.DetailSelectionActionCollapse {
		t.Fatalf("detail selection action after default refresh = %v, want UI-local expansion preserved", got)
	}
	got := model.detailTranscript.page().Entries
	want := []clientui.ChatEntry{
		{Role: "user", Text: "older"},
		{Role: "assistant", Text: "newer"},
	}
	if !sameChatEntries(got, want) {
		t.Fatalf("detail entries after default refresh = %#v, want stale resident window %#v", got, want)
	}
}

func TestDetailTranscriptWindowTrimsFarResidentSegmentsAfterAppend(t *testing.T) {
	var window uiDetailTranscriptWindow
	window.replace(detailTestPage("session-1", 0, 599, appInt64Ptr(600), nil))
	window.appendCursorPage(detailTestPage("session-1", 600, 1199, appInt64Ptr(1200), appInt64Ptr(600)))
	window.appendCursorPage(detailTestPage("session-1", 1200, 1799, nil, appInt64Ptr(1200)))

	if len(window.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segment count = %d, want %d", len(window.segments), uiDetailTranscriptMinResidentSegments)
	}
	if len(window.entries) != 1200 {
		t.Fatalf("resident entry count = %d, want two retained 600-entry segments", len(window.entries))
	}
	if got := window.entries[0].Text; got != "entry-600" {
		t.Fatalf("first retained entry = %q, want appended window to unload oldest segment", got)
	}
}

func TestDetailTranscriptWindowTrimsFarResidentSegmentsAfterPrepend(t *testing.T) {
	var window uiDetailTranscriptWindow
	window.replace(detailTestPage("session-1", 1200, 1799, nil, appInt64Ptr(1200)))
	window.prependCursorPage(detailTestPage("session-1", 600, 1199, appInt64Ptr(600), appInt64Ptr(1200)))
	window.prependCursorPage(detailTestPage("session-1", 0, 599, appInt64Ptr(0), appInt64Ptr(600)))

	if len(window.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segment count = %d, want %d", len(window.segments), uiDetailTranscriptMinResidentSegments)
	}
	if len(window.entries) != 1200 {
		t.Fatalf("resident entry count = %d, want two retained 600-entry segments", len(window.entries))
	}
	if got := window.entries[len(window.entries)-1].Text; got != "entry-1199" {
		t.Fatalf("last retained entry = %q, want prepended window to unload newest far segment", got)
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

func mustUpdateTUIModel(t *testing.T, model tui.Model, msg tea.Msg) tui.Model {
	t.Helper()
	next, _ := model.Update(msg)
	updated, ok := next.(tui.Model)
	if !ok {
		t.Fatalf("updated TUI model type = %T, want tui.Model", next)
	}
	return updated
}

func detailTestPage(sessionID string, first int, last int, olderCursor *int64, newerCursor *int64) clientui.TranscriptPage {
	return clientui.TranscriptPage{
		SessionID:    sessionID,
		OlderCursor:  olderCursor,
		HasMoreAbove: olderCursor != nil,
		NewerCursor:  newerCursor,
		HasMoreBelow: newerCursor != nil,
		Entries:      detailTestEntries(first, last),
		SessionName:  sessionID,
	}
}

func detailTestEntries(first int, last int) []clientui.ChatEntry {
	if last < first {
		return nil
	}
	entries := make([]clientui.ChatEntry, 0, last-first+1)
	for idx := first; idx <= last; idx++ {
		entries = append(entries, clientui.ChatEntry{Role: "assistant", Text: "entry-" + strconv.Itoa(idx)})
	}
	return entries
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
