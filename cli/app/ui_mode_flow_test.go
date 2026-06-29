package app

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

type recordingTranscriptRuntimeClient struct {
	runtimeControlFakeClient
	loadRequests []clientui.TranscriptPageRequest
	loadPage     clientui.TranscriptPage
}

func (c *recordingTranscriptRuntimeClient) LoadTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	c.loadRequests = append(c.loadRequests, req)
	if c.loadPage.SessionID != "" || len(c.loadPage.Entries) > 0 {
		return c.loadPage, nil
	}
	return c.Transcript(), nil
}

func (c *recordingTranscriptRuntimeClient) RefreshTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return c.LoadTranscriptPage(req)
}

func TestScenarioDetailWhileAgentWorksReturnsToLatestRecentTail(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 18)
	m.input = "/"
	m.refreshSlashCommandFilterFromInputWithAuth(true)
	m.layout().syncViewport()

	for i := 1; i <= 20; i++ {
		m = updateUIModel(t, m, tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %02d", i)})
	}

	detail := updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode, got %q", detail.view.Mode())
	}

	for i := 21; i <= 30; i++ {
		detail = updateUIModel(t, detail, tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %02d", i)})
	}
	detail = updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyPgUp})

	ongoing := updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyShiftTab})
	if ongoing.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected ongoing mode, got %q", ongoing.view.Mode())
	}
	ongoing = sizedTestUIModel(ongoing, 100, 18)
	ongoing.layout().syncViewport()

	view := stripANSIAndTrimRight(ongoing.view.OngoingSnapshot())
	if !containsAny(view, "line 30", "line 29", "line 28") {
		t.Fatalf("expected latest content visible after returning from detail, got %q", view)
	}
	compact := stripANSIAndTrimRight(ongoing.View())
	if !strings.Contains(compact, "/new") {
		t.Fatalf("expected slash picker to remain visible, got %q", compact)
	}
}

func TestCtrlTTogglesTranscriptModeLikeShiftTab(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 16)
	m.layout().syncViewport()

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected ctrl+t to enter detail mode, got %q", m.view.Mode())
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected ctrl+t to return to ongoing mode, got %q", m.view.Mode())
	}
}

func TestCtrlTShowsLatestDetailTailWithoutResolvingMetricsEagerly(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 12)
	m.layout().syncViewport()

	for i := 0; i < 24; i++ {
		m = updateUIModel(t, m, tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %02d", i)})
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	_ = cmd
	detail := next.(*uiModel)
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected ctrl+t to enter detail mode, got %q", detail.view.Mode())
	}
	if detail.view.DetailMetricsResolved() {
		t.Fatal("expected detail entry to stay lazily bottom-anchored until first navigation")
	}
	view := stripANSIAndTrimRight(detail.view.View())
	if !containsAny(view, "line 23", "line 22", "line 21") {
		t.Fatalf("expected newest transcript tail immediately visible in detail, got %q", view)
	}
	if strings.Contains(view, "line 00") {
		t.Fatalf("did not expect eager full-history detail rendering on ctrl+t, got %q", view)
	}
	if pageCmd := detail.maybeRequestDetailTranscriptPage(); pageCmd != nil {
		t.Fatal("did not expect edge transcript paging before detail metrics are resolved")
	}
}

func TestCtrlTPrimesDetailFromCurrentTailWhenPreviousDetailPageIsStale(t *testing.T) {
	stalePage := clientui.TranscriptPage{SessionID: "session-1"}
	stalePage.Entries = append(stalePage.Entries, clientui.ChatEntry{Role: "assistant", Text: "stale detail page"})
	currentPage := clientui.TranscriptPage{SessionID: "session-1"}
	currentPage.Entries = append(currentPage.Entries, clientui.ChatEntry{Role: "assistant", Text: "fresh ongoing tail"})
	client := &recordingTranscriptRuntimeClient{loadPage: currentPage}
	m := setTestUITerminalSize(newProjectedClosedUIModel(client), 100, 12)
	m.sessionID = "session-1"
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptEntries = transcriptEntriesFromPage(currentPage)
	m.detailTranscript.replace(stalePage)
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.transcriptBaseOffset,
		TotalEntries: m.transcriptTotalEntries,
		Entries:      transcriptEntriesFromPage(currentPage),
	})
	m.layout().syncViewport()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	_ = cmd
	detail := next.(*uiModel)
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after ctrl+t, got %q", detail.view.Mode())
	}
	view := stripANSIAndTrimRight(detail.view.View())
	if !strings.Contains(view, "fresh ongoing tail") {
		t.Fatalf("expected detail entry to render current tail immediately, got %q", view)
	}
	if strings.Contains(view, "stale detail page") {
		t.Fatalf("expected stale detail cache to be replaced on entry, got %q", view)
	}
}

func TestCtrlTPrimesDetailFromCurrentTailAfterOngoingAdvancedSincePreviousDetail(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 12)
	m.layout().syncViewport()
	m = updateUIModel(t, m, tui.AppendTranscriptMsg{Role: "assistant", Text: "old detail tail", Committed: true})

	detail := updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected first ctrl+t to enter detail mode, got %q", detail.view.Mode())
	}
	ongoing := updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyCtrlT})
	if ongoing.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected second ctrl+t to return to ongoing mode, got %q", ongoing.view.Mode())
	}
	ongoing = updateUIModel(t, ongoing, tui.AppendTranscriptMsg{Role: "assistant", Text: "fresh ongoing tail", Committed: true})

	next, cmd := ongoing.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	_ = cmd
	detail = next.(*uiModel)
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected third ctrl+t to enter detail mode, got %q", detail.view.Mode())
	}
	view := stripANSIAndTrimRight(detail.view.View())
	if !strings.Contains(view, "fresh ongoing tail") {
		t.Fatalf("expected detail entry to include tail appended while ongoing, got %q", view)
	}
}

func TestCtrlTPreservesLoadedDetailWindowWhenNoLocalTailIsKnown(t *testing.T) {
	stalePage := clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true, HasMoreBelow: true}
	for i := 0; i < 250; i++ {
		stalePage.Entries = append(stalePage.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("history %03d", 100+i)})
	}
	client := &recordingTranscriptRuntimeClient{loadPage: stalePage}
	m := setTestUITerminalSize(newProjectedClosedUIModel(client), 100, 12)
	m.sessionID = "session-1"
	m.detailTranscript.setKnownBounds(100, 500)
	m.detailTranscript.replace(stalePage)
	m.layout().syncViewport()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	_ = cmd
	detail := next.(*uiModel)
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after ctrl+t, got %q", detail.view.Mode())
	}
	if detail.view.TranscriptBaseOffset() != 100 {
		t.Fatalf("expected loaded detail window offset preserved, got %d want %d", detail.view.TranscriptBaseOffset(), 100)
	}
	view := stripANSIAndTrimRight(detail.view.View())
	if !strings.Contains(view, "history 349") {
		t.Fatalf("expected loaded detail window content preserved, got %q", view)
	}
}

func TestCtrlTPrimesDetailTailWithAbsoluteBounds(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 100, 12)
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.transcriptBaseOffset = 380
	m.transcriptTotalEntries = 400
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleAssistant, Text: "tail 380", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "tail 381", Committed: true},
	}

	m.primeDetailTranscriptFromCurrentTail()

	if got := m.detailTranscript.offset; got != 380 {
		t.Fatalf("detail offset = %d, want live tail base offset 380", got)
	}
	if got := m.detailTranscript.totalEntries; got != 400 {
		t.Fatalf("detail total entries = %d, want 400", got)
	}
	if !m.detailTranscript.hasMoreAbove {
		t.Fatal("detail tail should preserve that older transcript segments exist")
	}
	if got := len(m.detailTranscript.entries); got != 2 {
		t.Fatalf("detail entries = %d, want current committed tail", got)
	}
}

func TestDetailEdgePagingWaitsForFirstNavigationToResolveMetrics(t *testing.T) {
	client := &recordingTranscriptRuntimeClient{
		loadPage: clientui.TranscriptPage{SessionID: "session-1"},
	}
	m := setTestUITerminalSize(newProjectedClosedUIModel(client), 100, 12)
	m.layout().syncViewport()

	page := clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true, HasMoreBelow: true}
	for i := 0; i < 250; i++ {
		page.Entries = append(page.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("line %03d", 100+i)})
	}
	page.NewerCursor = 5000
	page.HasMoreBelow = true
	entries := transcriptEntriesFromPage(page)
	m.detailTranscript.setKnownBounds(100, 500)
	m.detailTranscript.replace(page)
	m.detailTranscript.lastRequest = clientui.TranscriptPageRequest{Cursor: 4096}
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 100, TotalEntries: 500, Entries: entries})

	next, enterCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	_ = enterCmd
	detail := next.(*uiModel)
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after ctrl+t, got %q", detail.view.Mode())
	}
	if detail.view.DetailMetricsResolved() {
		t.Fatal("expected detail entry to defer metric resolution until navigation")
	}
	if len(client.loadRequests) != 0 {
		t.Fatalf("expected no transcript page loads on detail entry, got %d", len(client.loadRequests))
	}

	next, navCmd := detail.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := next.(*uiModel)
	if updated.view.DetailMetricsResolved() {
		t.Fatal("expected first detail navigation to stay lazy")
	}
	if navCmd == nil {
		t.Fatal("expected edge-triggered transcript paging after first navigation at the bottom edge")
	}
	msg, ok := navCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", navCmd())
	}
	if got, want := len(client.loadRequests), 1; got != want {
		t.Fatalf("load request count = %d, want %d", got, want)
	}
	expectedReq := clientui.TranscriptPageRequest{NewerCursor: 5000}
	if msg.req != expectedReq {
		t.Fatalf("paging request = %+v, want %+v", msg.req, expectedReq)
	}
	if client.loadRequests[0] != expectedReq {
		t.Fatalf("client paging request = %+v, want %+v", client.loadRequests[0], expectedReq)
	}
}

func TestCtrlTDeferredDetailLoadSkipsDuplicateSeededPageRequest(t *testing.T) {
	seed := clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true}
	for i := 0; i < 200; i++ {
		seed.Entries = append(seed.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("line %03d", 300+i)})
	}
	client := &recordingTranscriptRuntimeClient{loadPage: seed}
	m := setTestUITerminalSize(newProjectedClosedUIModel(client), 100, 12)
	m.transcriptTotalEntries = 500
	_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, seed, clientui.TranscriptRecoveryCauseNone)
	m.layout().syncViewport()

	next, enterCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	detail := next.(*uiModel)
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after ctrl+t, got %q", detail.view.Mode())
	}
	if got := len(client.loadRequests); got != 0 {
		t.Fatalf("expected no transcript load before deferred detail tick, got %d", got)
	}

	msgs := collectCmdMessages(t, enterCmd)
	foundDeferredLoad := false
	for _, msg := range msgs {
		if _, ok := msg.(detailTranscriptLoadMsg); ok {
			foundDeferredLoad = true
			break
		}
	}
	if !foundDeferredLoad {
		t.Fatalf("expected deferred detail transcript load message, got %#v", msgs)
	}

	next, refreshCmd := detail.Update(detailTranscriptLoadMsg{})
	updated := next.(*uiModel)
	if refreshCmd != nil {
		t.Fatalf("expected duplicate seeded detail load to be skipped, got %T", refreshCmd())
	}
	if got := len(client.loadRequests); got != 0 {
		t.Fatalf("load request count = %d, want 0", got)
	}
	if updated.runtimeTranscriptBusy {
		t.Fatal("expected duplicate seeded detail load to leave transcript sync idle")
	}
}

func TestCtrlTDeferredDetailLoadSkippedKeepsDetailMetricsLazyEndToEnd(t *testing.T) {
	seed := clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true}
	for i := 0; i < 200; i++ {
		seed.Entries = append(seed.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("line %03d", 300+i)})
	}
	client := &recordingTranscriptRuntimeClient{loadPage: seed}
	m := setTestUITerminalSize(newProjectedClosedUIModel(client), 100, 12)
	m.transcriptTotalEntries = 500
	_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, seed, clientui.TranscriptRecoveryCauseNone)
	m.layout().syncViewport()

	next, enterCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	detail := next.(*uiModel)
	_ = collectCmdMessages(t, enterCmd)
	beforeMetricsResolved := detail.view.DetailMetricsResolved()
	if beforeMetricsResolved {
		t.Fatal("expected duplicate-seeded detail entry to start with lazy metrics")
	}

	next, refreshCmd := detail.Update(detailTranscriptLoadMsg{})
	updated := next.(*uiModel)
	if refreshCmd != nil {
		t.Fatalf("expected duplicate seeded detail load to be skipped, got %T", refreshCmd())
	}
	afterMetricsResolved := updated.view.DetailMetricsResolved()
	if afterMetricsResolved {
		t.Fatal("expected duplicate deferred detail refresh to keep detail metrics lazy")
	}
	if updated.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode to remain active, got %q", updated.view.Mode())
	}
	if updated.runtimeTranscriptBusy {
		t.Fatal("expected transcript sync to clear busy state after duplicate refresh result")
	}
}

func TestDeferredDetailLoadRefreshesWhenTranscriptDirty(t *testing.T) {
	seed := clientui.TranscriptPage{SessionID: "session-1", HasMoreAbove: true}
	for i := 0; i < 200; i++ {
		seed.Entries = append(seed.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("line %03d", 300+i)})
	}
	client := &recordingTranscriptRuntimeClient{loadPage: seed}
	m := setTestUITerminalSize(newProjectedClosedUIModel(client), 100, 12)
	m.transcriptTotalEntries = 500
	_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, seed, clientui.TranscriptRecoveryCauseNone)
	m.layout().syncViewport()

	next, enterCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	detail := next.(*uiModel)
	_ = collectCmdMessages(t, enterCmd)
	detail.transcriptLiveDirty = true

	next, refreshCmd := detail.Update(detailTranscriptLoadMsg{})
	updated := next.(*uiModel)
	if refreshCmd == nil {
		t.Fatal("expected dirty detail transcript to refresh even when request matches seeded page")
	}
	refreshed, ok := refreshCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", refreshCmd())
	}
	if refreshed.syncCause != runtimeTranscriptSyncCauseManualTranscriptRefresh {
		t.Fatalf("detail refresh sync cause = %q, want %q", refreshed.syncCause, runtimeTranscriptSyncCauseManualTranscriptRefresh)
	}
	expectedReq := clientui.TranscriptPageRequest{}
	if refreshed.req != expectedReq {
		t.Fatalf("dirty deferred detail request = %+v, want %+v", refreshed.req, expectedReq)
	}
	if got := len(client.loadRequests); got != 1 {
		t.Fatalf("load request count = %d, want 1", got)
	}
	if client.loadRequests[0] != expectedReq {
		t.Fatalf("client load request = %+v, want %+v", client.loadRequests[0], expectedReq)
	}
	if !updated.runtimeTranscriptBusy {
		t.Fatal("expected transcript sync to remain busy until dirty refresh result is applied")
	}
}

func TestScenarioHarnessRestartAndSessionResumeKeepsTranscriptVisible(t *testing.T) {
	workspace := t.TempDir()
	store := createAppRuntimeSessionAt(t, workspace, "ws", workspace)
	appendTranscriptMessage(t, store, llm.RoleUser, "u1")
	appendTranscriptMessage(t, store, llm.RoleAssistant, "a1")
	appendTranscriptMessage(t, store, llm.RoleUser, "u2")
	appendTranscriptMessage(t, store, llm.RoleAssistant, "a2 tail")

	eng := newAppRuntimeEngineWithStore(t, store, statusLineFakeClient{}, runtime.Config{})
	m := setTestUITerminalSize(newProjectedEngineUIModel(eng), 90, 16)
	m.layout().syncViewport()

	first := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(first, "a2 tail") {
		t.Fatalf("expected resumed tail in ongoing mode, got %q", first)
	}

	eng.AppendCommittedEntry("assistant", "post-resume live update")
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventLocalEntryAdded, LocalEntry: &runtime.ChatEntry{Role: "assistant", Text: "post-resume live update"}})
	live := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(live, "post-resume live update") {
		t.Fatalf("expected live update after local entry event, got %q", live)
	}

	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	eng2 := newAppRuntimeEngineWithStore(t, reopened, statusLineFakeClient{}, runtime.Config{})
	m2 := setTestUITerminalSize(newProjectedEngineUIModel(eng2), 90, 16)
	m2.layout().syncViewport()

	afterRestart := stripANSIAndTrimRight(m2.view.OngoingSnapshot())
	if !strings.Contains(afterRestart, "a2 tail") {
		t.Fatalf("expected resumed transcript after harness restart, got %q", afterRestart)
	}
	if !strings.Contains(afterRestart, "post-resume live update") {
		t.Fatalf("expected committed local update to survive restart, got %q", afterRestart)
	}

	m2 = updateUIModel(t, m2, tea.KeyMsg{Type: tea.KeyShiftTab})
	m2 = updateUIModel(t, m2, tea.KeyMsg{Type: tea.KeyShiftTab})
	backToOngoing := stripANSIAndTrimRight(m2.view.OngoingSnapshot())
	if !strings.Contains(backToOngoing, "a2 tail") {
		t.Fatalf("expected transcript preserved across detail roundtrip after restart, got %q", backToOngoing)
	}
}

func TestScenarioSessionResumeNormalizesLegacyReviewerEntriesInOngoingMode(t *testing.T) {
	workspace := t.TempDir()
	store := createAppRuntimeSessionAt(t, workspace, "ws", workspace)
	if _, _, err := store.AppendEvent("legacy-step", "local_entry", map[string]any{
		"role":           "reviewer_suggestions",
		"text":           "Supervisor suggested:\n1. Add final verification notes.",
		"condensed_text": "Supervisor made 1 suggestion.",
	}); err != nil {
		t.Fatalf("append legacy reviewer_suggestions: %v", err)
	}
	if _, _, err := store.AppendEvent("legacy-step", "local_entry", map[string]any{
		"role": "reviewer_status",
		"text": "Supervisor ran, applied 1 suggestion:\n1. Add final verification notes.",
	}); err != nil {
		t.Fatalf("append legacy reviewer_status: %v", err)
	}

	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, reopened, statusLineFakeClient{}, runtime.Config{})
	m := setTestUITerminalSize(newProjectedEngineUIModel(eng), 90, 16)
	m.layout().syncViewport()

	ongoing := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !containsInOrder(ongoing, "Supervisor made 1 suggestion.", "Supervisor ran, applied 1 suggestion:") {
		t.Fatalf("expected stored reviewer entries after session resume, got %q", ongoing)
	}
	if strings.Contains(ongoing, "Supervisor suggested") {
		t.Fatalf("did not expect legacy reviewer suggestion header after resume, got %q", ongoing)
	}
	if strings.Contains(ongoing, "Supervisor ran: 1 suggestion, applied.") {
		t.Fatalf("did not expect legacy reviewer status summary after resume, got %q", ongoing)
	}
}

func TestScenarioTeleportBetweenSessionsResetsVisibleConversation(t *testing.T) {
	workspace := t.TempDir()
	storeA := createAppRuntimeSessionAt(t, workspace, "ws", workspace)
	appendTranscriptMessage(t, storeA, llm.RoleUser, "session-a-user")
	appendTranscriptMessage(t, storeA, llm.RoleAssistant, "session-a-tail")

	storeB := createAppRuntimeSessionAt(t, workspace, "ws", workspace)
	appendTranscriptMessage(t, storeB, llm.RoleUser, "session-b-user")
	appendTranscriptMessage(t, storeB, llm.RoleAssistant, "session-b-tail")

	engA := newAppRuntimeEngineWithStore(t, storeA, statusLineFakeClient{}, runtime.Config{})
	modelA := setTestUITerminalSize(newProjectedEngineUIModel(engA), 80, 14)
	modelA.layout().syncViewport()
	viewA := stripANSIAndTrimRight(modelA.view.OngoingSnapshot())
	if !strings.Contains(viewA, "session-a-tail") {
		t.Fatalf("expected session A tail, got %q", viewA)
	}

	engB := newAppRuntimeEngineWithStore(t, storeB, statusLineFakeClient{}, runtime.Config{})
	modelB := setTestUITerminalSize(newProjectedEngineUIModel(engB), 80, 14)
	modelB.layout().syncViewport()
	viewB := stripANSIAndTrimRight(modelB.view.OngoingSnapshot())
	if !strings.Contains(viewB, "session-b-tail") || strings.Contains(viewB, "session-a-tail") {
		t.Fatalf("expected teleported session B view only, got %q", viewB)
	}

	reopenA, err := session.Open(storeA.Dir())
	if err != nil {
		t.Fatalf("reopen A: %v", err)
	}
	engA2 := newAppRuntimeEngineWithStore(t, reopenA, statusLineFakeClient{}, runtime.Config{})
	modelA2 := setTestUITerminalSize(newProjectedEngineUIModel(engA2), 80, 14)
	modelA2.layout().syncViewport()
	viewA2 := stripANSIAndTrimRight(modelA2.view.OngoingSnapshot())
	if !strings.Contains(viewA2, "session-a-tail") || strings.Contains(viewA2, "session-b-tail") {
		t.Fatalf("expected teleported-back session A view only, got %q", viewA2)
	}
}

func TestScenarioScrollAttemptsAcrossModesAfterLongDetailStay(t *testing.T) {
	m := setTestUITerminalSize(newProjectedStaticUIModel(), 80, 10)
	m.layout().syncViewport()

	for i := 1; i <= 40; i++ {
		m = updateUIModel(t, m, tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %02d", i)})
	}
	start := m.view.OngoingScroll()
	if start == 0 {
		t.Fatal("expected ongoing transcript to be scrollable")
	}

	updated := updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	if got := updated.view.OngoingScroll(); got != start {
		t.Fatalf("expected pgup not to mutate ongoing scroll, got %d from %d", got, start)
	}

	detail := updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyShiftTab})
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode, got %q", detail.view.Mode())
	}
	for i := 41; i <= 45; i++ {
		detail = updateUIModel(t, detail, tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %02d", i)})
	}
	detail = updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyPgUp})
	detail = updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyPgDown})

	ongoing := updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyShiftTab})
	plain := stripANSIAndTrimRight(ongoing.view.OngoingSnapshot())
	if !containsAny(plain, "line 45", "line 44", "line 43") {
		t.Fatalf("expected latest line visible after returning from detail, got %q", plain)
	}

	afterUp := ongoing.view.OngoingScroll()
	ongoing = updateUIModel(t, ongoing, tea.KeyMsg{Type: tea.KeyPgDown})
	if ongoing.view.OngoingScroll() != afterUp {
		t.Fatalf("expected pgdown not to mutate ongoing scroll, got %d from %d", ongoing.view.OngoingScroll(), afterUp)
	}
}

func TestMainUIStartsInNormalBuffer(t *testing.T) {
	m := newProjectedStaticUIModel()
	if m.altScreenActive {
		t.Fatal("expected main UI to start in normal buffer")
	}
}

func TestStartupHydrationKeepsCompactionSummaryVerbose(t *testing.T) {
	client := &startupTranscriptRuntimeClient{
		view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1", SessionName: "incident triage"}},
		page: clientui.TranscriptPage{
			SessionID: "session-1",
			Entries: []clientui.ChatEntry{
				{Role: "compaction_notice", Text: "context compacted for the 1st time"},
				{Role: "compaction_summary", Text: "summary line one\nsummary line two"},
			},
		},
	}
	m := newProjectedClosedUIModel(client)

	next, startupCmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	updated := next.(*uiModel)
	_ = startupCmd
	seededOngoing := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if !strings.Contains(seededOngoing, "context compacted for the 1st time") {
		t.Fatalf("expected compaction notice visible in ongoing startup seed, got %q", seededOngoing)
	}
	if strings.Contains(seededOngoing, "summary line one") || strings.Contains(seededOngoing, "summary line two") {
		t.Fatalf("expected compaction summary hidden in ongoing startup seed, got %q", seededOngoing)
	}
	hydrationMsg, ok := startupRuntimeTranscriptRefreshedMsg(updated.startupCmds)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg from startup hydration, got %d command(s)", len(updated.startupCmds))
	}
	hydrated := updateUIModel(t, updated, hydrationMsg)
	hydratedOngoing := stripANSIAndTrimRight(hydrated.view.OngoingSnapshot())
	if !strings.Contains(hydratedOngoing, "context compacted for the 1st time") {
		t.Fatalf("expected compaction notice visible after hydration, got %q", hydratedOngoing)
	}
	if strings.Contains(hydratedOngoing, "summary line one") || strings.Contains(hydratedOngoing, "summary line two") {
		t.Fatalf("expected compaction summary hidden after hydration, got %q", hydratedOngoing)
	}

	detail := updateUIModel(t, hydrated, tea.KeyMsg{Type: tea.KeyCtrlT})
	detail = updateUIModel(t, detail, tea.KeyMsg{Type: tea.KeyEnter})
	if detail.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode after toggle, got %q", detail.view.Mode())
	}
	detailView := stripANSIAndTrimRight(detail.view.View())
	if !containsInOrder(detailView, "context compacted for the 1st time", "summary line one", "summary line two") {
		t.Fatalf("expected compaction notice plus full summary in detail after hydration, got %q", detailView)
	}
	if got, want := len(client.loadRequests), 1; got != want {
		t.Fatalf("load request count = %d, want %d", got, want)
	}
	if client.loadRequests[0] != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("startup hydration request = %+v, want recent-tail (zero cursor)", client.loadRequests[0])
	}
}

func TestStartupHydrationKeepsDefaultCacheWarningVerbose(t *testing.T) {
	warningText := transcript.CacheWarningText(transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix})
	client := &startupTranscriptRuntimeClient{
		view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1", SessionName: "incident triage"}},
		page: clientui.TranscriptPage{
			SessionID: "session-1",
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "latest answer"},
				{Role: "cache_warning", Text: warningText, Visibility: clientui.EntryVisibilityVerbose},
			},
		},
	}
	m := newProjectedClosedUIModel(client)

	next, startupCmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	updated := next.(*uiModel)
	_ = startupCmd
	seededOngoing := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if !strings.Contains(seededOngoing, "latest answer") {
		t.Fatalf("expected assistant answer visible in ongoing startup seed, got %q", seededOngoing)
	}
	if strings.Contains(seededOngoing, warningText) {
		t.Fatalf("expected default cache warning hidden in ongoing startup seed, got %q", seededOngoing)
	}

	hydrationMsg, ok := startupRuntimeTranscriptRefreshedMsg(updated.startupCmds)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg from startup hydration, got %d command(s)", len(updated.startupCmds))
	}
	hydrated := updateUIModel(t, updated, hydrationMsg)
	hydratedOngoing := stripANSIAndTrimRight(hydrated.view.OngoingSnapshot())
	if !strings.Contains(hydratedOngoing, "latest answer") {
		t.Fatalf("expected assistant answer visible after hydration, got %q", hydratedOngoing)
	}
	if strings.Contains(hydratedOngoing, warningText) {
		t.Fatalf("expected default cache warning hidden after hydration, got %q", hydratedOngoing)
	}

	detail := updateUIModel(t, hydrated, tea.KeyMsg{Type: tea.KeyCtrlT})
	detailView := stripANSIAndTrimRight(detail.view.View())
	if !containsInOrder(detailView, "latest answer", warningText) {
		t.Fatalf("expected assistant answer plus cache warning in detail after hydration, got %q", detailView)
	}
}

func startupRuntimeTranscriptRefreshedMsg(cmds []tea.Cmd) (runtimeTranscriptRefreshedMsg, bool) {
	return startupCmdMessage[runtimeTranscriptRefreshedMsg](cmds)
}

func startupCmdMessage[T tea.Msg](cmds []tea.Cmd) (T, bool) {
	var zero T
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		msg, ok := cmd().(T)
		if ok {
			return msg, true
		}
	}
	return zero, false
}

func appendTranscriptMessage(t *testing.T, store *session.Store, role llm.Role, text string) {
	t.Helper()
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: role, Content: text}); err != nil {
		t.Fatalf("append %s message: %v", role, err)
	}
}

func updateUIModel(t *testing.T, m *uiModel, msg tea.Msg) *uiModel {
	t.Helper()
	next, _ := m.Update(msg)
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", next)
	}
	return updated
}

func collectCmdMessages(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	msgs := make([]tea.Msg, 0)
	var runMsg func(tea.Msg)
	var runCmd func(tea.Cmd)
	runCmd = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		runMsg(cmd())
	}
	runMsg = func(msg tea.Msg) {
		if msg == nil {
			return
		}
		switch typed := msg.(type) {
		case tea.BatchMsg:
			for _, child := range typed {
				runCmd(child)
			}
			return
		}
		value := reflect.ValueOf(msg)
		if value.IsValid() && value.Kind() == reflect.Slice {
			for i := 0; i < value.Len(); i++ {
				child, ok := value.Index(i).Interface().(tea.Cmd)
				if !ok {
					msgs = append(msgs, msg)
					return
				}
				runCmd(child)
			}
			return
		}
		msgs = append(msgs, msg)
	}
	runCmd(cmd)
	return msgs
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}
