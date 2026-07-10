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
		SessionID: detailTestSessionID,
		Entries:   []clientui.TranscriptCommittedRow{detailTestUserRow("first page")},
	}}
	runtimeClient := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: detailTestSessionID}}
	model := newProjectedClosedUIModel(
		runtimeClient,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.view = tui.NewModel()
	cmd := model.detailLoadCmdForModeTransition(tui.ModeOngoing, tui.ModeDetail)
	if cmd == nil {
		t.Fatal("detail transition did not create a transcript page load command")
	}

	for _, msg := range collectCmdMessages(t, cmd) {
		if load, ok := msg.(detailTranscriptLoadMsg); ok {
			model = updateUIModel(t, model, load)
		}
	}

	if !model.detailTranscript.loaded {
		t.Fatal("detail transcript window was not loaded")
	}
	got := model.detailTranscript.page()
	want := []clientui.TranscriptCommittedRow{detailTestUserRow("first page")}
	if !detailTestRowsEqual(got.Entries, want) {
		t.Fatalf("detail transcript entries = %#v, want %#v", got.Entries, want)
	}
}

func TestDetailModeReentryReloadsNewestPageAndSelectsItsEnd(t *testing.T) {
	stale := clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("stale detail page"),
		},
	}
	fresh := clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("fresh expandable\nline two\nline three\nline four"),
			detailTestUserRow("fresh newest"),
		},
	}
	sessionViews := &countingSessionViewClient{page: fresh}
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.detailTranscript.replace(stale)
	model.view = tui.NewModel()
	model.forwardToView(tui.SetViewportSizeMsg{Lines: 1, Width: 80})
	model.forwardToView(tui.SetDetailTranscriptPageMsg{Page: stale})

	cmd := model.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeDetail,
		suppressAltScreen: true,
		preserveSurface:   true,
	})
	if cmd == nil {
		t.Fatal("detail reentry did not request a fresh newest page")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		if load, ok := msg.(detailTranscriptLoadMsg); ok {
			model = updateUIModel(t, model, load)
		}
	}

	if sessionViews.lastPageReq.Cursor != nil || sessionViews.lastPageReq.NewerCursor != nil {
		t.Fatalf("detail reentry request = %#v, want newest-page request without cursors", sessionViews.lastPageReq)
	}
	if got := model.detailTranscript.page().Entries; !detailTestRowsEqual(got, fresh.Entries) {
		t.Fatalf("detail reentry entries = %#v, want fresh newest page %#v", got, fresh.Entries)
	}
	if action := model.view.DetailSelectionAction(); action != tui.DetailSelectionActionNone {
		t.Fatalf("detail reentry selected action = %v, want non-expandable newest row at page end", action)
	}
}

func TestDetailTranscriptLoadMergesAdjacentCursorPages(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	newest := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(80),
		HasMoreBelow: false,
		Entries:      []clientui.TranscriptCommittedRow{detailTestAssistantRow("newer")},
	}
	older := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.TranscriptCommittedRow{detailTestUserRow("older")},
	}

	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, newest)
	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}, older)

	got := model.detailTranscript.page()
	wantEntries := []clientui.TranscriptCommittedRow{
		detailTestUserRow("older"),
		detailTestAssistantRow("newer"),
	}
	if !detailTestRowsEqual(got.Entries, wantEntries) {
		t.Fatalf("merged detail entries = %#v, want %#v", got.Entries, wantEntries)
	}
	if got.OlderCursor == nil || *got.OlderCursor != 20 || got.HasMoreAbove || got.NewerCursor == nil || *got.NewerCursor != 80 || got.HasMoreBelow {
		t.Fatalf("merged cursors = older:%v above:%t newer:%v below:%t", got.OlderCursor, got.HasMoreAbove, got.NewerCursor, got.HasMoreBelow)
	}
}

func TestDetailTranscriptLoadIgnoresDuplicateAdjacentCursorResponse(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	newest := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		Entries:      []clientui.TranscriptCommittedRow{detailTestAssistantRow("newer")},
		HasMoreBelow: false,
	}
	older := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.TranscriptCommittedRow{detailTestUserRow("older")},
	}
	request := clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}

	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, newest)
	model.applyDetailTranscriptLoad("", request, older)
	model.applyDetailTranscriptLoad("", request, older)

	got := model.detailTranscript.page().Entries
	want := []clientui.TranscriptCommittedRow{
		detailTestUserRow("older"),
		detailTestAssistantRow("newer"),
	}
	if !detailTestRowsEqual(got, want) {
		t.Fatalf("deduplicated detail entries = %#v, want %#v", got, want)
	}
}

func TestDetailTranscriptDefaultRefreshDoesNotCollapseResidentWindow(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{})
	newest := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(80),
		HasMoreBelow: false,
		Entries:      []clientui.TranscriptCommittedRow{detailTestAssistantRow("newer")},
	}
	older := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.TranscriptCommittedRow{detailTestUserRow("older")},
	}
	refreshedNewest := newest
	refreshedNewest.Entries = []clientui.TranscriptCommittedRow{
		detailTestAssistantRow("newer refreshed\nline two\nline three\nline four"),
	}

	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, newest)
	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}, older)
	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, refreshedNewest)

	got := model.detailTranscript.page().Entries
	want := []clientui.TranscriptCommittedRow{
		detailTestUserRow("older"),
		detailTestAssistantRow("newer"),
	}
	if !detailTestRowsEqual(got, want) {
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
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(40),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(80),
		HasMoreBelow: false,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("newer\nline two\nline three\nline four"),
		},
	}
	older := clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(20),
		HasMoreAbove: false,
		NewerCursor:  appInt64Ptr(40),
		HasMoreBelow: true,
		Entries:      []clientui.TranscriptCommittedRow{detailTestUserRow("older")},
	}
	refreshedNewest := newest
	refreshedNewest.Entries = []clientui.TranscriptCommittedRow{
		detailTestAssistantRow("newer refreshed\nline two\nline three\nline four"),
	}

	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, newest)
	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{Cursor: appInt64Ptr(40)}, older)
	model.view = mustUpdateTUIModel(t, model.view, tea.KeyMsg{Type: tea.KeyEnter})
	if got := model.view.DetailSelectionAction(); got != tui.DetailSelectionActionCollapse {
		t.Fatalf("detail selection action after expand = %v, want collapse", got)
	}

	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, refreshedNewest)

	if got := model.view.DetailSelectionAction(); got != tui.DetailSelectionActionCollapse {
		t.Fatalf("detail selection action after default refresh = %v, want UI-local expansion preserved", got)
	}
	got := model.detailTranscript.page().Entries
	want := []clientui.TranscriptCommittedRow{
		detailTestUserRow("older"),
		detailTestAssistantRow("newer\nline two\nline three\nline four"),
	}
	if !detailTestRowsEqual(got, want) {
		t.Fatalf("detail entries after default refresh = %#v, want stale resident window %#v", got, want)
	}
}

func TestDetailTranscriptWindowTrimsFarResidentSegmentsAfterAppend(t *testing.T) {
	var window uiDetailTranscriptWindow
	window.replace(detailTestPage(detailTestSessionID, 0, 599, appInt64Ptr(600), nil))
	window.appendCursorPage(detailTestPage(detailTestSessionID, 600, 1199, appInt64Ptr(1200), appInt64Ptr(600)))
	window.appendCursorPage(detailTestPage(detailTestSessionID, 1200, 1799, nil, appInt64Ptr(1200)))

	if len(window.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segment count = %d, want %d", len(window.segments), uiDetailTranscriptMinResidentSegments)
	}
	if len(window.entries) != 1200 {
		t.Fatalf("resident entry count = %d, want two retained 600-entry segments", len(window.entries))
	}
	if got := window.entries[0].Assistant.Text; got != "entry-600" {
		t.Fatalf("first retained entry = %q, want appended window to unload oldest segment", got)
	}
}

func TestDetailTranscriptWindowTrimsFarResidentSegmentsAfterPrepend(t *testing.T) {
	var window uiDetailTranscriptWindow
	window.replace(detailTestPage(detailTestSessionID, 1200, 1799, nil, appInt64Ptr(1200)))
	window.prependCursorPage(detailTestPage(detailTestSessionID, 600, 1199, appInt64Ptr(600), appInt64Ptr(1200)))
	window.prependCursorPage(detailTestPage(detailTestSessionID, 0, 599, appInt64Ptr(1), appInt64Ptr(600)))

	if len(window.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segment count = %d, want %d", len(window.segments), uiDetailTranscriptMinResidentSegments)
	}
	if len(window.entries) != 1200 {
		t.Fatalf("resident entry count = %d, want two retained 600-entry segments", len(window.entries))
	}
	if got := window.entries[len(window.entries)-1].Assistant.Text; got != "entry-1199" {
		t.Fatalf("last retained entry = %q, want prepended window to unload newest far segment", got)
	}
}

// Spec: the resident detail window is bounded to two pages max (current + one
// adjacent), evicting the far page on cross. This must hold regardless of total
// entry count — there is no entry-count ceiling. These tests use small pages
// (well under any legacy 1000-entry gate) to prove eviction is segment-driven.
func TestDetailTranscriptWindowTrimsFarSegmentByCountAfterAppend(t *testing.T) {
	var window uiDetailTranscriptWindow
	window.replace(detailTestPage(detailTestSessionID, 0, 2, appInt64Ptr(3), nil))
	window.appendCursorPage(detailTestPage(detailTestSessionID, 3, 5, appInt64Ptr(6), appInt64Ptr(3)))
	window.appendCursorPage(detailTestPage(detailTestSessionID, 6, 8, nil, appInt64Ptr(6)))

	if len(window.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segment count = %d, want %d (segment-driven, not entry-count)", len(window.segments), uiDetailTranscriptMinResidentSegments)
	}
	if len(window.entries) != 6 {
		t.Fatalf("resident entry count = %d, want two retained 3-entry segments", len(window.entries))
	}
	if got := window.entries[0].Assistant.Text; got != "entry-3" {
		t.Fatalf("first retained entry = %q, want appended window to unload oldest segment", got)
	}
}

func TestDetailTranscriptWindowTrimsFarSegmentByCountAfterPrepend(t *testing.T) {
	var window uiDetailTranscriptWindow
	window.replace(detailTestPage(detailTestSessionID, 6, 8, nil, appInt64Ptr(6)))
	window.prependCursorPage(detailTestPage(detailTestSessionID, 3, 5, appInt64Ptr(3), appInt64Ptr(6)))
	window.prependCursorPage(detailTestPage(detailTestSessionID, 0, 2, appInt64Ptr(0), appInt64Ptr(3)))

	if len(window.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segment count = %d, want %d (segment-driven, not entry-count)", len(window.segments), uiDetailTranscriptMinResidentSegments)
	}
	if len(window.entries) != 6 {
		t.Fatalf("resident entry count = %d, want two retained 3-entry segments", len(window.entries))
	}
	if got := window.entries[len(window.entries)-1].Assistant.Text; got != "entry-5" {
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
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestToolRow(clientui.TranscriptToolRow{
				ToolCallID: "3f14b1c8-5fee-4c0e-a72e-f38bb6c3c389",
				ToolName:   "apply_patch",
				ToolPresentation: &clientui.ToolCallMeta{
					ToolName:       "apply_patch",
					Presentation:   clientui.ToolPresentationDefault,
					RenderBehavior: clientui.ToolCallRenderBehaviorDefault,
					PatchRender:    sourcePatch,
				},
			}),
		},
	}

	model.applyDetailTranscriptLoad("", clientui.TranscriptPageRequest{}, page)
	sourcePatch.Files[0].Diff[0] = "source changed"
	firstRead := model.detailTranscript.page()
	if got := firstRead.Entries[0].Tool.ToolPresentation.PatchRender.Files[0].Diff[0]; got != "old" {
		t.Fatalf("stored patch diff = %q, want source-isolated old diff", got)
	}

	firstRead.Entries[0].Tool.ToolPresentation.PatchRender.Files[0].Diff[0] = "read changed"
	secondRead := model.detailTranscript.page()
	if got := secondRead.Entries[0].Tool.ToolPresentation.PatchRender.Files[0].Diff[0]; got != "old" {
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

func detailTestEntries(first int, last int) []clientui.TranscriptCommittedRow {
	if last < first {
		return nil
	}
	entries := make([]clientui.TranscriptCommittedRow, 0, last-first+1)
	for idx := first; idx <= last; idx++ {
		entries = append(entries, detailTestAssistantRow("entry-"+strconv.Itoa(idx)))
	}
	return entries
}

func TestDetailTranscriptLoadIgnoresStaleSessionResponse(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID(detailTestSessionID))
	model.applyDetailTranscriptLoad(
		detailTestSessionID,
		clientui.TranscriptPageRequest{},
		clientui.TranscriptPage{
			SessionID: detailTestSessionID,
			Entries:   []clientui.TranscriptCommittedRow{detailTestAssistantRow("current")},
		},
	)

	model.applyDetailTranscriptLoad(
		detailTestStaleSessionID,
		clientui.TranscriptPageRequest{},
		clientui.TranscriptPage{
			SessionID: detailTestStaleSessionID,
			Entries:   []clientui.TranscriptCommittedRow{detailTestAssistantRow("stale")},
		},
	)

	got := model.detailTranscript.page().Entries
	want := []clientui.TranscriptCommittedRow{detailTestAssistantRow("current")}
	if !detailTestRowsEqual(got, want) {
		t.Fatalf("detail entries after stale response = %#v, want %#v", got, want)
	}
}

func TestDetailTranscriptIgnoresRuntimeCommittedChangesUntilPageIsRequested(t *testing.T) {
	model := newProjectedClosedUIModel(&runtimeControlFakeClient{}, WithUISessionID(detailTestSessionID))
	model.applyDetailTranscriptLoad(
		detailTestSessionID,
		clientui.TranscriptPageRequest{},
		clientui.TranscriptPage{
			SessionID: detailTestSessionID,
			Entries:   []clientui.TranscriptCommittedRow{detailTestAssistantRow("loaded page")},
		},
	)

	got := model.detailTranscript.page().Entries
	want := []clientui.TranscriptCommittedRow{detailTestAssistantRow("loaded page")}
	if !detailTestRowsEqual(got, want) {
		t.Fatalf("detail entries after committed runtime event = %#v, want stale loaded page %#v", got, want)
	}
}

func TestDetailEdgeRequestMessageLoadsAdjacentPage(t *testing.T) {
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries:   []clientui.TranscriptCommittedRow{detailTestUserRow("older page")},
	}}
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: detailTestSessionID}},
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.detailTranscript.replace(clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(25),
		HasMoreAbove: true,
		Entries:      []clientui.TranscriptCommittedRow{detailTestAssistantRow("current page")},
	})

	next, cmd := model.Update(tui.RequestDetailTranscriptPageMsg{Direction: tui.DetailTranscriptPageOlder})
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		if load, ok := msg.(detailTranscriptLoadMsg); ok {
			model = updateUIModel(t, model, load)
		}
	}

	got := model.detailTranscript.page().Entries
	want := []clientui.TranscriptCommittedRow{
		detailTestUserRow("older page"),
		detailTestAssistantRow("current page"),
	}
	if !detailTestRowsEqual(got, want) {
		t.Fatalf("loaded adjacent entries = %#v, want %#v", got, want)
	}
}

var _ tea.Msg = detailTranscriptLoadMsg{}

func appInt64Ptr(value int64) *int64 {
	return &value
}
