package app

import (
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncConversationFromEngineUsesBundledSessionViewMetadata(t *testing.T) {
	store := createAppRuntimeSession(t)
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello user"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "hello", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, store, statusLineFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.sessionName = "stale"
	m.sessionID = "stale"

	msg, ok := startupCmdMessage[runtimeTranscriptRefreshedMsg](m.startupCmds)
	if !ok {
		t.Fatalf("expected startup sync command, got %d command(s)", len(m.startupCmds))
	}
	m.startupCmds = nil
	if msg.syncCause != runtimeTranscriptSyncCauseBootstrap {
		t.Fatalf("startup sync cause = %q, want %q", msg.syncCause, runtimeTranscriptSyncCauseBootstrap)
	}
	next, followUp := m.Update(msg)
	_ = followUp
	m = next.(*uiModel)
	if m.sessionName != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", m.sessionName)
	}
	if m.sessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want %q", m.sessionID, store.Meta().SessionID)
	}
	if m.conversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", m.conversationFreshness)
	}
	if got := m.view.OngoingSnapshot(); !strings.Contains(got, "hello") {
		t.Fatalf("expected synced conversation in view, got %q", got)
	}
}

func TestSyncConversationFromEngineRetriesAfterRefreshError(t *testing.T) {
	oldDelay := uiRuntimeHydrationRetryDelay
	uiRuntimeHydrationRetryDelay = 0
	defer func() { uiRuntimeHydrationRetryDelay = oldDelay }()

	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{
			{SessionID: "session-1"},
			{SessionID: "session-1", SessionName: "incident triage", Entries: []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}}, CommittedEntryCount: 1},
		},
		errs: []error{errors.New("temporary refresh failure"), nil},
	}
	m := newProjectedClosedUIModel(client)

	firstMsg, ok := startupCmdMessage[runtimeTranscriptRefreshedMsg](m.startupCmds)
	if !ok {
		t.Fatalf("expected startup sync command, got %d command(s)", len(m.startupCmds))
	}
	m.startupCmds = nil
	if firstMsg.syncCause != runtimeTranscriptSyncCauseBootstrap {
		t.Fatalf("startup sync cause = %q, want %q", firstMsg.syncCause, runtimeTranscriptSyncCauseBootstrap)
	}
	next, retryCmd := m.Update(firstMsg)
	if retryCmd == nil {
		t.Fatal("expected retry command after refresh error")
	}
	retryMsg, ok := retryCmd().(runtimeTranscriptRetryMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRetryMsg, got %T", retryCmd())
	}
	if retryMsg.syncCause != runtimeTranscriptSyncCauseBootstrap {
		t.Fatalf("retry sync cause = %q, want %q", retryMsg.syncCause, runtimeTranscriptSyncCauseBootstrap)
	}
	next, secondCmd := next.(*uiModel).Update(retryMsg)
	if secondCmd == nil {
		t.Fatal("expected second sync command after retry tick")
	}
	secondMsg, ok := secondCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", secondCmd())
	}
	if secondMsg.syncCause != runtimeTranscriptSyncCauseBootstrap {
		t.Fatalf("second sync cause = %q, want %q", secondMsg.syncCause, runtimeTranscriptSyncCauseBootstrap)
	}
	next, followUp := next.(*uiModel).Update(secondMsg)
	_ = followUp
	updated := next.(*uiModel)
	if updated.sessionName != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", updated.sessionName)
	}
	if got := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); !strings.Contains(got, "final answer") {
		t.Fatalf("expected retried sync to hydrate transcript, got %q", got)
	}
	if client.calls != 2 {
		t.Fatalf("refresh call count = %d, want 2", client.calls)
	}
}

func TestApplyProjectedTranscriptPageReplacesRecentTailWindow(t *testing.T) {
	m := newProjectedStaticUIModel()
	seed := []tui.TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "tool_call", Text: "pwd"},
		{Role: "assistant", Text: "**done**"},
	}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), seed...)
	m.forwardToView(tui.SetConversationMsg{Entries: seed})

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID:           "session-1",
		CommittedEntryCount: 3,
		StartEntryCount:     2,
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "**done**",
		}},
	}, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		_ = cmd()
	}

	plain := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if strings.Contains(plain, "prompt") || strings.Contains(plain, "pwd") {
		t.Fatalf("expected bounded tail window to replace stale earlier entries, got %q", plain)
	}
	if !strings.Contains(plain, "done") {
		t.Fatalf("expected merged transcript to keep tail entry, got %q", plain)
	}
}

func TestApplyProjectedTranscriptPageReplacesTranscriptWhenPageIsComplete(t *testing.T) {
	m := newProjectedStaticUIModel()
	seed := []tui.TranscriptEntry{{Role: "assistant", Text: "old"}}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), seed...)
	m.forwardToView(tui.SetConversationMsg{Entries: seed})

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID:           "session-1",
		CommittedEntryCount: 1,
		StartEntryCount:     0,
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "new",
		}},
	}, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		_ = cmd()
	}

	plain := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if strings.Contains(plain, "old") {
		t.Fatalf("expected complete page to replace stale transcript, got %q", plain)
	}
	if !strings.Contains(plain, "new") {
		t.Fatalf("expected complete page to render new transcript, got %q", plain)
	}
}

func TestApplyRuntimeTranscriptPageSkipsDuplicateDetailRefresh(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 12
	m.windowSizeKnown = true
	page := clientui.TranscriptPage{SessionID: "session-1", StartEntryCount: 300, CommittedEntryCount: 500}
	for i := 0; i < 200; i++ {
		page.Entries = append(page.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("line %03d", 300+i)})
	}
	entries := transcriptEntriesFromPage(page)
	m.detailTranscript.replace(page)
	m.forwardToView(tui.SetConversationMsg{BaseOffset: page.StartEntryCount, TotalEntries: page.CommittedEntryCount, Entries: entries})
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.layout().syncViewport()

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected duplicate detail page refresh to be skipped, got %T", msg)
		}
	}
	if m.view.TranscriptBaseOffset() != page.StartEntryCount || m.view.TranscriptTotalEntries() != page.CommittedEntryCount {
		t.Fatalf("detail transcript metadata changed unexpectedly: base=%d total=%d", m.view.TranscriptBaseOffset(), m.view.TranscriptTotalEntries())
	}
}

func TestApplyRuntimeTranscriptPageInDetailModeAdvancesRevisionEvenWhenPageMatches(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	page := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 2,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed-0"},
			{Role: "assistant", Text: "seed-1"},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.layout().syncViewport()

	updated := page
	updated.Revision = 11
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, updated, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected matching detail page refresh to be skipped, got %T", msg)
		}
	}
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision after matching detail refresh = %d, want 11", got)
	}
}

func TestApplyRuntimeTranscriptPageAcceptsSameRevisionEmptyOngoingWhenCommittedTranscriptAlreadyMatches(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "done"}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "done"})
	m.sawAssistantDelta = true

	page := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "done", Phase: string(llm.MessagePhaseFinal)}},
		Streaming:           "",
	}
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone)
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected same-revision authoritative page to clear stale live ongoing, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected same-revision authoritative page to clear assistant delta flag")
	}
	_ = cmd
}

func TestApplyRuntimeTranscriptPageAcceptsSameRevisionEmptyOngoingWhenPageCommitsLiveAssistantBeforeCommittedSuffix(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "user", Text: "prompt"}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 3
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "done"})
	m.sawAssistantDelta = true

	page := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 3,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "assistant", Text: "done", Phase: string(llm.MessagePhaseFinal)},
			{Role: "reviewer_status", Text: "Supervisor ran: no changes."},
		},
		Streaming: "",
	}
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone)
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected same-revision authoritative page to clear stale live ongoing when assistant is committed before suffix, got %q", got)
	}
	if m.sawAssistantDelta {
		t.Fatal("expected same-revision authoritative page to clear assistant delta flag when assistant is committed before suffix")
	}
	if got, want := len(m.transcriptEntries), 3; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if got := m.transcriptEntries[1].Text; got != "done" {
		t.Fatalf("assistant text = %q, want done", got)
	}
	if got := m.transcriptEntries[2].Role; got != "reviewer_status" {
		t.Fatalf("suffix role = %q, want reviewer_status", got)
	}
	_ = cmd
}

func TestApplyRuntimeTranscriptPageRejectsSameRevisionEmptyOngoingWhenOnlyOlderAssistantMatchesLiveText(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "done"}, {Role: "assistant", Text: "different"}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 2
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "done"})
	m.sawAssistantDelta = true

	page := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 2,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "done", Phase: string(llm.MessagePhaseFinal)},
			{Role: "assistant", Text: "different", Phase: string(llm.MessagePhaseFinal)},
		},
		Streaming: "",
	}
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, page, clientui.TranscriptRecoveryCauseNone)
	if got := m.view.OngoingStreamingText(); got != "done" {
		t.Fatalf("expected same-revision authoritative page to preserve live ongoing when only older assistant matches, got %q", got)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected stale page rejection to preserve assistant delta flag")
	}
	if cmd != nil {
		t.Fatalf("expected no native sync command on stale ongoing clear rejection, got %T", cmd)
	}
}

func TestInvalidateTransientTranscriptStateClearsDeferredCommittedTail(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.deferredCommittedTail = []deferredProjectedTranscriptTail{{rangeStart: 1, rangeEnd: 2, revision: 7, entries: []clientui.ChatEntry{{Role: "user", Text: "queued"}}}}
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "done", Transient: true, Committed: true}}
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "done"})

	m.invalidateTransientTranscriptState()

	if got := len(m.deferredCommittedTail); got != 0 {
		t.Fatalf("expected deferred committed tail cleared during transient state invalidation, got %d", got)
	}
}

func TestApplyRuntimeTranscriptPageRejectsStaleAuthoritativePageWhileDeferredCommittedTailExists(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedClosedUIModel(client)
	m.sessionID = "session-1"
	m.setBusy(true)
	m.sawAssistantDelta = true
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 0, Entries: nil, Ongoing: "done"})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         7,
		CommittedEntryCount:        1,
		UserMessage:                "steered message",
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "steered message"}},
	}).
		cmd
	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("expected deferred committed user tail before stale hydrate, got %d", got)
	}

	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            6,
		StartEntryCount:     0,
		CommittedEntryCount: 0,
		Entries:             nil,
	}, clientui.TranscriptRecoveryCauseNone)
	if cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("expected stale authoritative page to preserve deferred committed tail, got %d", got)
	}

	cmd = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         8,
		CommittedEntryCount:        2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "done",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}).
		cmd
	for _, msg := range collectCmdMessages(t, cmd) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect assistant commit after stale hydrate rejection to require hydration, got %+v", msg)
		}
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected deferred user tail to merge with assistant commit after stale hydrate rejection, got %d entries", got)
	}
	if got := m.transcriptEntries[0].Text; got != "steered message" {
		t.Fatalf("first transcript entry = %q, want steered message", got)
	}
	if got := m.transcriptEntries[1].Text; got != "done" {
		t.Fatalf("second transcript entry = %q, want done", got)
	}
}

func TestApplyRuntimeTranscriptPageResetsDetailWindowOnSessionChange(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 12
	m.windowSizeKnown = true

	pageA := clientui.TranscriptPage{SessionID: "session-a", StartEntryCount: 100, CommittedEntryCount: 400}
	for i := 0; i < 250; i++ {
		pageA.Entries = append(pageA.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("a-%03d", 100+i)})
	}
	m.detailTranscript.replace(pageA)
	m.forwardToView(tui.SetConversationMsg{BaseOffset: pageA.StartEntryCount, TotalEntries: pageA.CommittedEntryCount, Entries: transcriptEntriesFromPage(pageA)})
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.sessionID = "session-a"

	pageB := clientui.TranscriptPage{
		SessionID:           "session-b",
		SessionName:         "Session B",
		StartEntryCount:     0,
		CommittedEntryCount: 2,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "b-000"}, {Role: "assistant", Text: "b-001"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, pageB, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if got := m.detailTranscript.sessionID; got != "session-b" {
		t.Fatalf("detail transcript session id = %q, want session-b", got)
	}
	if got := m.detailTranscript.offset; got != 0 {
		t.Fatalf("detail transcript offset = %d, want 0", got)
	}
	if got := m.detailTranscript.totalEntries; got != 2 {
		t.Fatalf("detail transcript total entries = %d, want 2", got)
	}
	if got := len(m.detailTranscript.entries); got != 2 {
		t.Fatalf("detail transcript entry count = %d, want 2", got)
	}
	if got := m.detailTranscript.entries[0].Text; got != "b-000" {
		t.Fatalf("first detail transcript entry = %q, want b-000", got)
	}
	if got := stripANSIAndTrimRight(m.View()); strings.Contains(got, "a-100") || !strings.Contains(got, "b-001") {
		t.Fatalf("detail view leaked prior session transcript, got %q", got)
	}
}

func TestApplyRuntimeTranscriptPageRejectsEqualRevisionTailReplacementAfterLiveAppend(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.transcriptRevision; got != 10 {
		t.Fatalf("transcript revision = %d, want 10", got)
	}

	if cmd, mutated, needsHydration := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{Kind: clientui.EventAssistantMessage, TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "live append"}}}); cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected live append without extra command, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}
	if !m.transcriptLiveDirty {
		t.Fatal("expected live append to mark transcript live-dirty")
	}

	stale := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, stale, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected stale equal-revision page to be ignored, got %T", msg)
		}
	}
	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if got := m.transcriptEntries[1].Text; got != "live append" {
		t.Fatalf("second transcript entry = %q, want live append", got)
	}
	if got := stripANSIAndTrimRight(m.view.OngoingSnapshot()); !strings.Contains(got, "live append") {
		t.Fatalf("expected view to preserve live append, got %q", got)
	}
}

func TestApplyRuntimeTranscriptPageRejectsOlderRevisionTailReplacement(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	current := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            11,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "newer"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, current, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	older := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "older"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, older, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected older-revision page to be ignored, got %T", msg)
		}
	}
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision = %d, want 11", got)
	}
	if got, want := len(m.transcriptEntries), 1; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if got := m.transcriptEntries[0].Text; got != "newer" {
		t.Fatalf("transcript entry = %q, want newer", got)
	}
}

func TestApplyRuntimeTranscriptPageRejectsEqualRevisionTailReplacementThatClearsLiveOngoing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "working"})
	if got := m.view.OngoingStreamingText(); got != "working" {
		t.Fatalf("ongoing streaming text = %q, want working", got)
	}

	stale := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, stale, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected stale equal-revision page to be ignored, got %T", msg)
		}
	}
	if got := m.view.OngoingStreamingText(); got != "working" {
		t.Fatalf("expected live ongoing stream preserved, got %q", got)
	}
}

func TestApplyRuntimeTranscriptPagePreservesLiveOngoingForEqualRevisionDetailPage(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "working"})
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: m.transcriptEntries, Ongoing: m.view.OngoingStreamingText(), OngoingError: "boom"})
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})

	staleDetail := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, staleDetail, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.view.OngoingStreamingText(); got != "working" {
		t.Fatalf("expected live ongoing stream preserved for detail page, got %q", got)
	}
	if got := m.view.OngoingErrorText(); got != "boom" {
		t.Fatalf("expected live ongoing error preserved for detail page, got %q", got)
	}
	if got := m.detailTranscript.ongoing; got != "working" {
		t.Fatalf("expected detail transcript to preserve live ongoing stream, got %q", got)
	}
}

func TestRuntimeTranscriptRefreshPreservesLiveOngoingForEqualRevisionDetailPage(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedClosedUIModel(client)
	m.startupCmds = nil
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "working"})
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: 1, Entries: m.transcriptEntries, Ongoing: m.view.OngoingStreamingText(), OngoingError: "boom"})
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7

	staleDetail := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	next, cmd := m.Update(runtimeTranscriptRefreshedMsg{
		token:      7,
		req:        clientui.TranscriptPageRequest{},
		transcript: staleDetail,
	})
	updated := next.(*uiModel)
	_ = collectCmdMessages(t, cmd)
	if got := updated.view.OngoingStreamingText(); got != "working" {
		t.Fatalf("expected hydrated detail page to preserve live ongoing stream, got %q mode=%q revision=%d detail_loaded=%t detail_ongoing=%q detail_error=%q", got, updated.view.Mode(), updated.transcriptRevision, updated.detailTranscript.loaded, updated.detailTranscript.ongoing, updated.detailTranscript.ongoingError)
	}
	if got := updated.view.OngoingErrorText(); got != "boom" {
		t.Fatalf("expected hydrated detail page to preserve live ongoing error, got %q", got)
	}
	if got := updated.detailTranscript.ongoing; got != "working" {
		t.Fatalf("expected hydrated detail transcript to preserve live ongoing stream, got %q", got)
	}
}

func TestApplyRuntimeTranscriptPageAcceptsNewerRevisionTailReplacementThatClearsLiveOngoing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "working"})

	fresh := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            11,
		StartEntryCount:     0,
		CommittedEntryCount: 2,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "assistant", Text: "done", Phase: string(llm.MessagePhaseFinal)},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, fresh, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected fresh authoritative page to clear live ongoing, got %q", got)
	}
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision = %d, want 11", got)
	}
}

func TestProjectedAssistantMessageAdvancesTranscriptRevisionForReplayDedupe(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed"}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	evt := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "live append",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(evt).cmd
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision after live append = %d, want 11", got)
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count after live append = %d, want 2", got)
	}

	if cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(evt).cmd; cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("expected replayed assistant message to be skipped, got %T", msg)
		}
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected replayed assistant message to stay deduped, got %d entries", got)
	}
}

func TestApplyRuntimeTranscriptPageAcceptsEqualRevisionTailReplacementWhenRuntimeOnlyEntryChanged(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.transcriptLiveDirty = true

	runtimeOnly := clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            10,
		StartEntryCount:     0,
		CommittedEntryCount: 2,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "error", Text: "background continuation failed: boom"},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, runtimeOnly, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
	if got := m.transcriptEntries[1].Text; got != "background continuation failed: boom" {
		t.Fatalf("runtime-only entry text = %q, want background continuation failed: boom", got)
	}
	if m.transcriptLiveDirty {
		t.Fatal("expected accepted equal-revision tail refresh to clear transcriptLiveDirty")
	}
}
