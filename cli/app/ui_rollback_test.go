package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionview"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/rollbacktarget"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDoubleEscLoadsNewestDetailPageAndSelectsNewestRollbackCandidate(t *testing.T) {
	olderTarget := rollbacktarget.EncodeUserMessageSeq(11)
	newestTarget := rollbacktarget.EncodeUserMessageSeq(22)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("older user message", olderTarget),
			detailTestAssistantRow("assistant answer"),
			detailTestRollbackUserRow("newest user message", newestTarget),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	if cmd == nil {
		t.Fatal("second Esc did not request the newest bounded detail page")
	}

	completion := rollbackDetailLoadCompletion(t, cmd)
	next, _ = model.Update(completion)
	model = next.(*uiModel)

	if !model.rollback.isSelecting() {
		t.Fatal("double Esc did not open rollback selection after detail hydration")
	}
	if model.surface() != uiSurfaceRollbackSelection || model.view.Mode() != tui.ModeDetail {
		t.Fatalf("rollback picker surface = %q mode = %q, want rollback detail overlay", model.surface(), model.view.Mode())
	}

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.rollback.isEditing() {
		t.Fatal("Enter did not begin editing the selected rollback candidate")
	}
	if model.input != "newest user message" {
		t.Fatalf("rollback edit input = %q, want newest candidate text", model.input)
	}
	if model.rollback.editingCandidate == nil || model.rollback.editingCandidate.RollbackTargetID != newestTarget {
		t.Fatalf("editing candidate = %#v, want newest rollback target %q", model.rollback.editingCandidate, newestTarget)
	}
}

func TestDoubleEscWithNoRollbackCandidatesHasNoVisibleEffect(t *testing.T) {
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("assistant-only transcript"),
			detailTestUserRow("user row without rollback target"),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	completion := rollbackDetailLoadCompletion(t, cmd)
	next, transitionCmd := model.Update(completion)
	model = next.(*uiModel)

	if model.rollback.isActive() || model.rollback.isAwaitingNewest() {
		t.Fatalf("rollback state = %#v, want inactive after a candidate-free page", model.rollback)
	}
	if model.surface() != uiSurfaceOngoingTranscript || model.view.Mode() != tui.ModeOngoing || model.altScreenActive {
		t.Fatalf(
			"candidate-free rollback changed visible surface: surface=%q mode=%q alt=%t",
			model.surface(),
			model.view.Mode(),
			model.altScreenActive,
		)
	}
	if transitionCmd != nil {
		t.Fatal("candidate-free rollback scheduled a visible surface transition")
	}
}

func TestDoubleEscWithNoCandidatesRestoresPriorDetailWindow(t *testing.T) {
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("candidate-free newest transcript"),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	priorDetailPage := clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("prior detail window"),
		},
	}
	model.detailTranscript.replace(priorDetailPage)
	model.forwardToView(tui.SetDetailTranscriptPageMsg{
		Page:   priorDetailPage,
		Anchor: tui.DetailTranscriptAnchorBottom,
	})
	model.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	model.activeSurface = uiSurfaceTranscriptDetail

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, loadCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	completion := rollbackDetailLoadCompletion(t, loadCmd)
	next, transitionCmd := model.Update(completion)
	model = next.(*uiModel)

	if model.rollback.isActive() || model.rollback.isAwaitingActivation() {
		t.Fatalf("candidate-free detail activation remained active: %#v", model.rollback)
	}
	if model.surface() != uiSurfaceTranscriptDetail || model.view.Mode() != tui.ModeDetail {
		t.Fatalf("candidate-free activation restored surface=%q mode=%q, want prior detail", model.surface(), model.view.Mode())
	}
	if !model.detailTranscript.matchesPage(priorDetailPage) {
		t.Fatalf("candidate-free activation replaced prior detail cache: %#v", model.detailTranscript.page())
	}
	if transitionCmd != nil {
		t.Fatal("candidate-free detail activation scheduled a visible surface transition")
	}
}

func TestDoubleEscFindsNewestRollbackCandidateAcrossCompactionBoundary(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(17)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(100),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestUserRow("synthetic compacted user row"),
			detailTestAssistantRow("post-compaction answer"),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, newestLoadCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	newestCompletion := rollbackDetailLoadCompletion(t, newestLoadCmd)
	next, olderLoadCmd := model.Update(newestCompletion)
	model = next.(*uiModel)
	if olderLoadCmd == nil {
		t.Fatal("candidate-free newest segment did not request the immediately older bounded segment")
	}
	if model.rollback.isActive() {
		t.Fatal("rollback picker became visible before a candidate was available")
	}
	if model.transientStatus != "" {
		t.Fatalf("bounded candidate probe became visible through status notice %q", model.transientStatus)
	}

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		NewerCursor:  appInt64Ptr(100),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("original pre-compaction prompt", targetID),
		},
	}
	model = applyRollbackDetailLoad(t, model, olderLoadCmd)

	if !model.rollback.isSelecting() {
		t.Fatal("rollback picker did not open after bounded compaction-boundary paging")
	}
	assertRollbackSelection(t, model, targetID)
	if len(model.detailTranscript.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident segments = %d, want bounded two-segment activation window", len(model.detailTranscript.segments))
	}
}

func TestDoubleEscUsesLatestRollbackCandidateLocatorAcrossMultipleCandidateFreeSegments(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(17)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(300),
		HasMoreAbove: true,
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq:       17,
			CandidatePageEndByte: 100,
		},
		Entries: []clientui.TranscriptCommittedRow{
			detailTestUserRow("newest synthetic compacted user row"),
			detailTestAssistantRow("newest post-compaction answer"),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	priorDetailPage := clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("previous detail one"),
			detailTestAssistantRow("previous detail two"),
			detailTestAssistantRow("previous detail three\n" + strings.Repeat("expanded line\n", 30)),
		},
	}
	model.forwardToView(tui.SetViewportSizeMsg{Lines: 2, Width: 80})
	model.detailTranscript.replace(priorDetailPage)
	model.forwardToView(tui.SetDetailTranscriptPageMsg{
		Page:   priorDetailPage,
		Anchor: tui.DetailTranscriptAnchorBottom,
	})
	model.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	model.activeSurface = uiSurfaceTranscriptDetail
	model.forwardToView(tea.KeyMsg{Type: tea.KeyEnter})
	model.forwardToView(tea.KeyMsg{Type: tea.KeyDown})

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	priorDetailRender := model.view.View()
	priorDetailAction := model.view.DetailSelectionAction()
	next, newestLoadCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	newestCompletion := rollbackDetailLoadCompletion(t, newestLoadCmd)
	next, candidateLoadCmd := model.Update(newestCompletion)
	model = next.(*uiModel)
	if candidateLoadCmd == nil {
		t.Fatal("candidate-free newest segment did not request the advertised rollback candidate page")
	}

	sessionViews.page = clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq:       17,
			CandidatePageEndByte: 100,
		},
		NewerCursor:  appInt64Ptr(100),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("original prompt behind multiple compactions", targetID),
		},
	}
	candidateCompletion := rollbackDetailLoadCompletion(t, candidateLoadCmd)
	if sessionViews.lastPageReq.Cursor == nil || *sessionViews.lastPageReq.Cursor != 100 {
		t.Fatalf(
			"rollback locator request cursor = %v, want direct candidate page cursor 100 instead of adjacent cursor 300",
			sessionViews.lastPageReq.Cursor,
		)
	}
	next, _ = model.Update(candidateCompletion)
	model = next.(*uiModel)

	if !model.rollback.isSelecting() {
		t.Fatal("rollback picker did not open from the directly located candidate page")
	}
	assertRollbackSelection(t, model, targetID)
	if got := sessionViews.pageCount.Load(); got != 2 {
		t.Fatalf("transcript page requests = %d, want newest page plus one direct locator request", got)
	}
	if len(model.detailTranscript.segments) != 1 {
		t.Fatalf("picker detail segments = %d, want isolated directly located candidate page", len(model.detailTranscript.segments))
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*uiModel)
	if cmd != nil {
		t.Fatal("Down on the server-authoritative newest rollback candidate requested a candidate-free newer page")
	}
	assertRollbackSelection(t, model, targetID)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.surface() != uiSurfaceTranscriptDetail || model.view.Mode() != tui.ModeDetail {
		t.Fatalf("picker exit restored surface=%q mode=%q, want prior detail transcript", model.surface(), model.view.Mode())
	}
	if !model.detailTranscript.matchesPage(priorDetailPage) {
		t.Fatalf("picker exit left a gapped detail cache: %#v", model.detailTranscript.page())
	}
	if model.view.View() != priorDetailRender || model.view.DetailSelectionAction() != priorDetailAction {
		t.Fatalf(
			"picker exit did not restore prior detail presentation: render_equal=%t action=%v want_action=%v",
			model.view.View() == priorDetailRender,
			model.view.DetailSelectionAction(),
			priorDetailAction,
		)
	}
	if got := sessionViews.pageCount.Load(); got != 2 {
		t.Fatalf("picker exit issued %d transcript reads, want only newest plus direct locator", got)
	}
}

func TestRollbackPickerDoesNotOpenAfterComposerChangesDuringHydration(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(27)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("stale activation candidate", targetID),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, loadCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	completion := rollbackDetailLoadCompletion(t, loadCmd)
	next, transitionCmd := model.Update(completion)
	model = next.(*uiModel)

	if model.rollback.isActive() || model.rollback.isAwaitingActivation() {
		t.Fatalf("stale rollback activation survived composer edit: %#v", model.rollback)
	}
	if model.input != "x" || model.surface() != uiSurfaceOngoingTranscript || model.view.Mode() != tui.ModeOngoing {
		t.Fatalf("stale rollback activation changed composer/surface: input=%q surface=%q mode=%q", model.input, model.surface(), model.view.Mode())
	}
	if transitionCmd != nil {
		t.Fatal("stale rollback activation scheduled a surface transition")
	}
}

func TestRollbackSelectionPagesAcrossBoundedDetailWindowsAtCandidateEdges(t *testing.T) {
	oldestTarget := rollbacktarget.EncodeUserMessageSeq(10)
	middleTarget := rollbacktarget.EncodeUserMessageSeq(20)
	newestTarget := rollbacktarget.EncodeUserMessageSeq(30)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(200),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("newest message", newestTarget),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(100),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(200),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("middle message", middleTarget),
		},
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(*uiModel)
	model = applyRollbackDetailLoad(t, model, cmd)
	assertRollbackSelection(t, model, middleTarget)

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		NewerCursor:  appInt64Ptr(100),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("oldest message", oldestTarget),
		},
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(*uiModel)
	model = applyRollbackDetailLoad(t, model, cmd)
	assertRollbackSelection(t, model, oldestTarget)
	if len(model.detailTranscript.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident detail segments = %d, want bounded window of %d", len(model.detailTranscript.segments), uiDetailTranscriptMinResidentSegments)
	}

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*uiModel)
	if cmd != nil {
		t.Fatal("Down within the resident candidate window unexpectedly requested a page")
	}
	assertRollbackSelection(t, model, middleTarget)

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(200),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("newest message", newestTarget),
		},
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*uiModel)
	model = applyRollbackDetailLoad(t, model, cmd)
	assertRollbackSelection(t, model, newestTarget)
	if len(model.detailTranscript.segments) != uiDetailTranscriptMinResidentSegments {
		t.Fatalf("resident detail segments after paging newer = %d, want %d", len(model.detailTranscript.segments), uiDetailTranscriptMinResidentSegments)
	}

	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*uiModel)
	if cmd != nil {
		t.Fatal("Down at the newest transcript edge unexpectedly requested another page")
	}
	assertRollbackSelection(t, model, newestTarget)
}

func TestRollbackNavigationTraversesConsecutiveCandidateFreePages(t *testing.T) {
	newestTarget := rollbacktarget.EncodeUserMessageSeq(30)
	olderTarget := rollbacktarget.EncodeUserMessageSeq(10)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(300),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("newest candidate", newestTarget),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)
	originalWindow := model.detailTranscript.page()

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(200),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(300),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("candidate-free segment one"),
		},
	}
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(*uiModel)
	model, cmd = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	if cmd == nil {
		t.Fatal("first candidate-free page did not continue bounded rollback navigation")
	}
	assertRollbackSelection(t, model, newestTarget)
	if !model.detailTranscript.matchesPage(originalWindow) {
		t.Fatal("candidate-free traversal replaced the visible candidate window")
	}

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(100),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(200),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("candidate-free segment two"),
		},
	}
	model, cmd = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	if cmd == nil {
		t.Fatal("second candidate-free page did not continue bounded rollback navigation")
	}
	assertRollbackSelection(t, model, newestTarget)
	if !model.detailTranscript.matchesPage(originalWindow) {
		t.Fatal("second candidate-free traversal replaced the visible candidate window")
	}

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		NewerCursor:  appInt64Ptr(100),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("older candidate", olderTarget),
		},
	}
	model, _ = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	assertRollbackSelection(t, model, olderTarget)
	if len(model.detailTranscript.segments) != 1 {
		t.Fatalf(
			"non-adjacent candidate result retained %d segments, want one isolated bounded page",
			len(model.detailTranscript.segments),
		)
	}
	olderWindow := model.detailTranscript.page()

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(100),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(200),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("candidate-free segment two"),
		},
	}
	next, cmd = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*uiModel)
	model, cmd = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	if cmd == nil {
		t.Fatal("newer traversal stopped at the first candidate-free page")
	}
	assertRollbackSelection(t, model, olderTarget)
	if !model.detailTranscript.matchesPage(olderWindow) {
		t.Fatal("newer candidate-free traversal replaced the visible candidate window")
	}

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(200),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(300),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("candidate-free segment one"),
		},
	}
	model, cmd = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	if cmd == nil {
		t.Fatal("newer traversal stopped at the second candidate-free page")
	}
	assertRollbackSelection(t, model, olderTarget)
	if !model.detailTranscript.matchesPage(olderWindow) {
		t.Fatal("second newer candidate-free traversal replaced the visible candidate window")
	}

	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(300),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("newest candidate", newestTarget),
		},
	}
	model, _ = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	assertRollbackSelection(t, model, newestTarget)
	if len(model.detailTranscript.segments) != 1 {
		t.Fatalf(
			"newer non-adjacent candidate result retained %d segments, want one isolated bounded page",
			len(model.detailTranscript.segments),
		)
	}
	if got := sessionViews.pageCount.Load(); got != 7 {
		t.Fatalf("transcript page reads = %d, want newest plus six bounded navigation reads", got)
	}
}

func TestRollbackNavigationTimeoutKeepsCurrentCandidateAndStopsPaging(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(30)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(300),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("current candidate", targetID),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)
	originalWindow := model.detailTranscript.page()
	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(200),
		HasMoreAbove: true,
		NewerCursor:  appInt64Ptr(300),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestAssistantRow("candidate-free timeout page"),
		},
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(*uiModel)
	completion := rollbackDetailLoadCompletion(t, cmd)
	model.rollback.pendingNavigation.deadline = time.Now().Add(-time.Second)
	next, _ = model.Update(completion)
	model = next.(*uiModel)

	if model.rollback.pendingNavigation != nil || model.pendingDetailTranscript != nil {
		t.Fatalf(
			"timed-out rollback navigation remained pending: navigation=%#v request=%#v",
			model.rollback.pendingNavigation,
			model.pendingDetailTranscript,
		)
	}
	assertRollbackSelection(t, model, targetID)
	if !model.detailTranscript.matchesPage(originalWindow) {
		t.Fatal("timed-out rollback navigation replaced the visible candidate window")
	}
	if model.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("rollback timeout notice kind = %q, want error", model.transientStatusKind)
	}
	if got := sessionViews.pageCount.Load(); got != 2 {
		t.Fatalf("transcript page reads after timeout = %d, want newest plus one navigation read", got)
	}
}

func TestRollbackPageKeysKeepVisibleSelectionAndForkTargetInSync(t *testing.T) {
	oldestTarget := rollbacktarget.EncodeUserMessageSeq(1)
	middleTarget := rollbacktarget.EncodeUserMessageSeq(2)
	newestTarget := rollbacktarget.EncodeUserMessageSeq(3)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("oldest", oldestTarget),
			detailTestRollbackUserRow("middle", middleTarget),
			detailTestRollbackUserRow("newest", newestTarget),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = next.(*uiModel)
	if cmd != nil {
		t.Fatal("PgUp within the resident candidate window unexpectedly requested a page")
	}
	assertRollbackSelection(t, model, oldestTarget)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.input != "oldest" || model.rollback.editingCandidate == nil ||
		model.rollback.editingCandidate.RollbackTargetID != oldestTarget {
		t.Fatalf("PgUp edit target drifted: input=%q candidate=%#v", model.input, model.rollback.editingCandidate)
	}
}

func TestRollbackSharedListKeysNavigateResidentCandidates(t *testing.T) {
	oldestTarget := rollbacktarget.EncodeUserMessageSeq(1)
	middleTarget := rollbacktarget.EncodeUserMessageSeq(2)
	newestTarget := rollbacktarget.EncodeUserMessageSeq(3)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("oldest", oldestTarget),
			detailTestRollbackUserRow("middle", middleTarget),
			detailTestRollbackUserRow("newest", newestTarget),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	assertRollbackSelection(t, model, middleTarget)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	assertRollbackSelection(t, model, newestTarget)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyHome})
	assertRollbackSelection(t, model, oldestTarget)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnd})
	assertRollbackSelection(t, model, newestTarget)
}

func TestRollbackPageKeyPropagatesAdjacentWindowRequest(t *testing.T) {
	newestTarget := rollbacktarget.EncodeUserMessageSeq(20)
	olderTarget := rollbacktarget.EncodeUserMessageSeq(10)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		OlderCursor:  appInt64Ptr(100),
		HasMoreAbove: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("newest", newestTarget),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)
	sessionViews.page = clientui.TranscriptPage{
		SessionID:    detailTestSessionID,
		NewerCursor:  appInt64Ptr(100),
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("older", olderTarget),
		},
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = next.(*uiModel)
	if cmd == nil {
		t.Fatal("PgUp at the resident edge dropped the adjacent-page request")
	}
	model = applyRollbackDetailLoad(t, model, cmd)
	assertRollbackSelection(t, model, olderTarget)
}

func TestRollbackEditingEscChainRestoresPriorTranscriptSurface(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(42)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("message to revise", targetID),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.rollback.isEditing() || model.input != "message to revise" {
		t.Fatalf("rollback edit state = %#v input = %q", model.rollback, model.input)
	}

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if !model.rollback.isSelecting() || model.input != "" {
		t.Fatalf("first Esc state = %#v input = %q, want selection with empty composer", model.rollback, model.input)
	}
	assertRollbackSelection(t, model, targetID)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	if model.rollback.isActive() || model.surface() != uiSurfaceOngoingTranscript ||
		model.view.Mode() != tui.ModeOngoing || model.inputMode() != uiInputModeMain {
		t.Fatalf(
			"second Esc did not restore ongoing transcript: rollback=%#v surface=%q mode=%q inputMode=%q",
			model.rollback,
			model.surface(),
			model.view.Mode(),
			model.inputMode(),
		)
	}
	if cmd == nil {
		t.Fatal("second Esc did not schedule the alt-screen exit")
	}
}

func TestRollbackCtrlCClosesPickerBeforeBusyRuntimeInterrupt(t *testing.T) {
	for _, editing := range []bool{false, true} {
		name := "selection"
		if editing {
			name = "editing"
		}
		t.Run(name, func(t *testing.T) {
			targetID := rollbacktarget.EncodeUserMessageSeq(44)
			sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
				SessionID: detailTestSessionID,
				Entries: []clientui.TranscriptCommittedRow{
					detailTestRollbackUserRow("interrupt target", targetID),
				},
			}}
			model := newSizedProjectedClosedUIModel(
				&runtimeControlFakeClient{},
				100,
				20,
				WithUISessionID(detailTestSessionID),
				WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
			)
			model = openRollbackPicker(t, model)
			if editing {
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
			}
			model.setRuntimeActivityBusyForTest(true)
			model.activity = uiActivityRunning

			next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			model = next.(*uiModel)

			if model.rollback.isActive() || model.surface() != uiSurfaceOngoingTranscript ||
				model.view.Mode() != tui.ModeOngoing || model.inputMode() != uiInputModeMain {
				t.Fatalf(
					"Ctrl+C left rollback overlay active: rollback=%#v surface=%q mode=%q inputMode=%q",
					model.rollback,
					model.surface(),
					model.view.Mode(),
					model.inputMode(),
				)
			}
			if !model.hasPendingInterrupt() || cmd == nil {
				t.Fatalf("Ctrl+C did not continue through global busy interrupt: pending=%t cmd=%v", model.hasPendingInterrupt(), cmd)
			}
		})
	}
}

func TestRollbackCtrlCClosesPickerBeforeIdleExit(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(45)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("idle exit target", targetID),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = next.(*uiModel)

	if model.rollback.isActive() || model.surface() != uiSurfaceOngoingTranscript ||
		model.view.Mode() != tui.ModeOngoing || model.inputMode() != uiInputModeMain {
		t.Fatalf(
			"idle Ctrl+C left rollback overlay active: rollback=%#v surface=%q mode=%q inputMode=%q",
			model.rollback,
			model.surface(),
			model.view.Mode(),
			model.inputMode(),
		)
	}
	if model.exitAction != UIActionExit || cmd == nil {
		t.Fatalf("idle Ctrl+C action = %q cmd=%v, want global exit after closing picker", model.exitAction, cmd)
	}
}

func TestSessionReplacementDiscardsRollbackStateWithoutRestoringOldTranscript(t *testing.T) {
	for _, editing := range []bool{false, true} {
		name := "selection"
		if editing {
			name = "editing"
		}
		t.Run(name, func(t *testing.T) {
			targetID := rollbacktarget.EncodeUserMessageSeq(46)
			sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
				SessionID: detailTestSessionID,
				Entries: []clientui.TranscriptCommittedRow{
					detailTestRollbackUserRow("old session rollback target", targetID),
				},
			}}
			model := newSizedProjectedClosedUIModel(
				&runtimeControlFakeClient{},
				100,
				20,
				WithUISessionID(detailTestSessionID),
				WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
			)
			model = openRollbackPicker(t, model)
			if editing {
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
			}

			cmd := model.runtimeAdapter().applyProjectedSessionMetadata(clientui.RuntimeSessionView{
				SessionID: detailTestReplacementSessionID,
			})

			if model.rollback.isActive() || model.rollback.isAwaitingActivation() ||
				model.rollback.restoreDetailTranscript != nil {
				t.Fatalf("session replacement retained rollback state: %#v", model.rollback)
			}
			if model.surface() == uiSurfaceRollbackSelection || model.inputMode() != uiInputModeMain {
				t.Fatalf("session replacement retained rollback surface/input mode: surface=%q inputMode=%q", model.surface(), model.inputMode())
			}
			if model.detailTranscript.loaded {
				t.Fatalf("session replacement restored old transcript page: %#v", model.detailTranscript.page())
			}
			if model.input != "" {
				t.Fatalf("session replacement retained old rollback composer %q", model.input)
			}
			model.resetRollbackState()
			if model.detailTranscript.loaded {
				t.Fatal("inactive rollback reset resurrected the old session transcript")
			}
			if cmd == nil {
				t.Fatal("session replacement did not schedule new-session transcript hydration")
			}
		})
	}
}

func TestRollbackEditingSubmissionPreservesExactSelectedTarget(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(73)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("original prompt", targetID),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model = openRollbackPicker(t, model)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	editedPrompt := "edited rollback prompt"
	model.replaceMainInput(editedPrompt, len([]rune(editedPrompt)))

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)

	if model.exitAction != UIActionForkRollback {
		t.Fatalf("rollback submit action = %q, want %q", model.exitAction, UIActionForkRollback)
	}
	if model.nextForkRollbackTargetID != targetID {
		t.Fatalf("fork rollback target = %q, want exact selected target %q", model.nextForkRollbackTargetID, targetID)
	}
	if model.nextSessionInitialPrompt != "edited rollback prompt" {
		t.Fatalf("fork initial prompt = %q, want edited composer text", model.nextSessionInitialPrompt)
	}
	if cmd == nil {
		t.Fatal("rollback submit did not quit into session transition")
	}
}

func TestRollbackPickerWorksAfterInterruptedRuntimeAndTUIRestart(t *testing.T) {
	blockingClient := &rollbackInterruptBlockingClient{started: make(chan struct{})}
	store, firstEngine := newAppRuntimeEngine(t, blockingClient, runtime.Config{})
	submitDone := make(chan error, 1)
	go func() {
		_, err := firstEngine.SubmitUserMessage(context.Background(), "interrupted prompt survives restart")
		submitDone <- err
	}()

	select {
	case <-blockingClient.started:
	case <-time.After(time.Second):
		t.Fatal("runtime did not reach the interruptible model request")
	}
	if err := firstEngine.Interrupt(); err != nil {
		t.Fatalf("interrupt runtime: %v", err)
	}
	select {
	case err := <-submitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted submit error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted runtime did not become idle")
	}
	if err := firstEngine.Close(); err != nil {
		t.Fatalf("close interrupted runtime: %v", err)
	}

	reopenedStore, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen interrupted session: %v", err)
	}
	restartedEngine := newAppRuntimeEngineWithStore(t, reopenedStore, statusLineFakeClient{}, runtime.Config{})
	t.Cleanup(func() {
		if err := restartedEngine.Close(); err != nil {
			t.Errorf("close restarted runtime: %v", err)
		}
	})
	resolver := sessionview.NewStaticRuntimeResolver(restartedEngine)
	reads := client.NewLoopbackSessionViewClient(sessionview.NewService(nil, resolver, nil))
	model := newSizedProjectedClosedUIModel(
		newUIRuntimeClientFromEngine(restartedEngine),
		100,
		20,
		WithUISessionID(restartedEngine.SessionID()),
		WithUIStatusConfig(uiStatusConfig{SessionViews: reads}),
	)
	if model.blocksRuntimeInput() {
		t.Fatal("restarted runtime remained input-blocked")
	}

	model = openRollbackPicker(t, model)
	if !model.rollback.isSelecting() {
		t.Fatal("Esc-Esc did not open rollback selection after runtime/TUI restart")
	}
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.input != "interrupted prompt survives restart" {
		t.Fatalf("restored rollback edit input = %q", model.input)
	}
	if model.rollback.editingCandidate == nil {
		t.Fatal("restored rollback candidate lost its target identity")
	}
	if _, err := rollbacktarget.DecodeUserMessageSeq(model.rollback.editingCandidate.RollbackTargetID); err != nil {
		t.Fatalf("restored rollback target is invalid: %v", err)
	}
}

func TestRollbackPickerNeverEnablesAlternateScroll(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(88)
	sessionViews := &countingSessionViewClient{page: clientui.TranscriptPage{
		SessionID: detailTestSessionID,
		Entries: []clientui.TranscriptCommittedRow{
			detailTestRollbackUserRow("rollback candidate", targetID),
		},
	}}
	model := newSizedProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		100,
		20,
		WithUISessionID(detailTestSessionID),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	originalWrite := writeTerminalSequence
	var terminalSequences []string
	writeTerminalSequence = func(sequence string) error {
		terminalSequences = append(terminalSequences, sequence)
		return nil
	}
	t.Cleanup(func() { writeTerminalSequence = originalWrite })

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, loadCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	completion := rollbackDetailLoadCompletion(t, loadCmd)
	next, transitionCmd := model.Update(completion)
	model = next.(*uiModel)
	if !model.rollback.isSelecting() {
		t.Fatal("rollback picker did not open")
	}
	_ = collectCmdMessages(t, transitionCmd)

	for _, sequence := range terminalSequences {
		if strings.Contains(sequence, "?1007h") {
			t.Fatalf("rollback picker enabled alternate scroll with terminal sequence %q", sequence)
		}
	}
}

type rollbackInterruptBlockingClient struct {
	started chan struct{}
	once    sync.Once
}

func (c *rollbackInterruptBlockingClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func detailTestRollbackUserRow(text, rollbackTargetID string) clientui.TranscriptCommittedRow {
	target := rollbackTargetID
	row := detailTestUserRow(text)
	row.User.RollbackTargetID = &target
	return row
}

func openRollbackPicker(t *testing.T, model *uiModel) *uiModel {
	t.Helper()
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	return applyRollbackDetailLoad(t, model, cmd)
}

func applyRollbackDetailLoad(t *testing.T, model *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	model, _ = applyRollbackDetailLoadWithFollowUp(t, model, cmd)
	return model
}

func applyRollbackDetailLoadWithFollowUp(t *testing.T, model *uiModel, cmd tea.Cmd) (*uiModel, tea.Cmd) {
	t.Helper()
	completion := rollbackDetailLoadCompletion(t, cmd)
	next, followUp := model.Update(completion)
	return next.(*uiModel), followUp
}

func assertRollbackSelection(t *testing.T, model *uiModel, wantTargetID string) {
	t.Helper()
	selected, _, ok := model.selectedRollbackCandidate()
	if !ok {
		t.Fatal("rollback picker has no selected candidate")
	}
	if selected.RollbackTargetID != wantTargetID {
		t.Fatalf("selected rollback target = %q, want %q", selected.RollbackTargetID, wantTargetID)
	}
}

func rollbackDetailLoadCompletion(t *testing.T, cmd tea.Cmd) detailTranscriptLoadMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("rollback action did not schedule a detail transcript load")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		if loaded, ok := msg.(detailTranscriptLoadMsg); ok {
			return loaded
		}
	}
	t.Fatal("rollback action did not complete a detail transcript load")
	return detailTranscriptLoadMsg{}
}
