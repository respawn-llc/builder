package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRuntimeSyncPolicyDefersRoutineCommittedUpdateWhileStreaming(t *testing.T) {
	client := &runtimeControlFakeClient{
		transcript: clientui.TranscriptPage{
			SessionID:           "session-1",
			Revision:            3,
			CommittedEntryCount: 2,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "seed"},
				{Role: "assistant", Text: "authoritative"},
			},
		},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.sawAssistantDelta = true
	m.forwardToView(tui.StreamAssistantMsg{Delta: "live assistant"})

	next, cmd := m.Update(runtimeEventBatchMsg{events: []clientui.Event{{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         4,
		CommittedEntryCount:        3,
	}}})
	updated := next.(*uiModel)
	msgs := collectCmdMessages(t, cmd)

	assertNoRuntimeTranscriptRefreshMsg(t, msgs)
	if client.refreshTranscriptCalls != 0 || client.loadTranscriptCalls != 0 {
		t.Fatalf("transcript page calls = refresh:%d load:%d, want none", client.refreshTranscriptCalls, client.loadTranscriptCalls)
	}
	if !updated.runtimeTranscriptPendingSet {
		t.Fatal("expected routine committed update to be deferred")
	}
	if updated.waitRuntimeEventAfterHydration {
		t.Fatal("deferred routine sync must not arm hydration fence")
	}
	if got := updated.view.OngoingStreamingText(); got != "live assistant" {
		t.Fatalf("live assistant text = %q, want preserved", got)
	}
}

func TestRuntimeSyncPolicyReleasesDeferredTranscriptWhenProcessOverlayCloses(t *testing.T) {
	client := &runtimeControlFakeClient{
		transcript: clientui.TranscriptPage{
			SessionID:           "session-1",
			Revision:            3,
			CommittedEntryCount: 2,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "seed"},
				{Role: "assistant", Text: "authoritative"},
			},
		},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.openProcessList()

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         4,
		CommittedEntryCount:        3,
	}).
		cmd
	assertNoRuntimeTranscriptRefreshMsg(t, collectCmdMessages(t, cmd))
	if !m.runtimeTranscriptPendingSet {
		t.Fatal("expected routine transcript sync deferred while /ps is open")
	}

	closeCmd := (uiInputController{model: m}).stopProcessListFlowCmd()
	msgs := collectCmdMessages(t, closeCmd)
	if _, ok := findRuntimeTranscriptRefreshMsg(msgs); !ok {
		t.Fatalf("expected deferred transcript sync to run after /ps close, got %+v", msgs)
	}
	if client.refreshTranscriptCalls != 1 {
		t.Fatalf("refresh transcript calls = %d, want 1", client.refreshTranscriptCalls)
	}
}

func TestRuntimeSyncPolicyAllowsRecoveryWhileStreaming(t *testing.T) {
	client := &runtimeControlFakeClient{
		transcript: clientui.TranscriptPage{
			SessionID:           "session-1",
			Revision:            2,
			CommittedEntryCount: 1,
			Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "recovered"}},
		},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.sawAssistantDelta = true
	m.forwardToView(tui.StreamAssistantMsg{Delta: "live assistant"})

	next, cmd := m.Update(runtimeEventBatchMsg{events: []clientui.Event{{
		Kind:          clientui.EventStreamGap,
		RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap,
	}}})
	updated := next.(*uiModel)
	msgs := collectCmdMessages(t, cmd)
	refresh, ok := findRuntimeTranscriptRefreshMsg(msgs)
	if !ok {
		t.Fatalf("expected recovery hydrate while streaming, got %+v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("sync cause = %q, want continuity recovery", refresh.syncCause)
	}
	if !updated.waitRuntimeEventAfterHydration {
		t.Fatal("allowed recovery hydrate should own runtime event hydration fence")
	}
}

func TestRuntimeSyncPolicyDropsRoutineTranscriptResponseWhenBlockerAppears(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	client.transcript = clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            2,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "authoritative"}},
	}
	cmd := m.requestRuntimeCommittedConversationSync()
	if cmd == nil {
		t.Fatal("expected unblocked routine transcript request to start")
	}
	msg := cmd().(runtimeTranscriptRefreshedMsg)

	m.setBusy(true)
	if !m.shouldDeferRuntimeTranscriptSync(m.runtimeTranscriptActiveRequest) {
		t.Fatalf("test fixture did not block active transcript request: busy=%t class=%d req=%+v", m.isBusy(), m.runtimeTranscriptActiveRequest.class, m.runtimeTranscriptActiveRequest)
	}
	if !m.shouldDeferRuntimeTranscriptSync(msg.syncRequest) {
		t.Fatalf("test fixture did not block response transcript request: busy=%t class=%d req=%+v", m.isBusy(), msg.syncRequest.class, msg.syncRequest)
	}
	m.waitRuntimeEventAfterHydration = true
	next, followCmd := m.Update(msg)
	updated := next.(*uiModel)
	_ = collectCmdMessages(t, followCmd)

	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("dropped routine response should not apply transcript entries, got %+v", updated.transcriptEntries)
	}
	if !updated.runtimeTranscriptPendingSet {
		t.Fatal("expected dropped routine response to become pending")
	}
	if updated.waitRuntimeEventAfterHydration {
		t.Fatal("blocked routine drop should release hydration fence when no allowed hydrate owns it")
	}
}

func TestRuntimeSyncPolicyRunsWorktreeMutationRefreshWhileBusy(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	client.mainView = clientui.RuntimeMainView{
		Session:         clientui.RuntimeSessionView{SessionID: "session-1", SessionName: "server-name"},
		ExternalRuntime: &clientui.ExternalRuntimeStatus{State: clientui.ExternalRuntimeStateRegisteredIdle, QueueAccepting: true},
	}
	m.setBusy(true)

	cmd := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd
	if cmd == nil {
		t.Fatal("expected worktree-mutation main-view refresh to run authoritatively while busy")
	}
	refresh := cmd().(runtimeMainViewRefreshedMsg)
	next, _ := m.Update(refresh)
	updated := next.(*uiModel)
	if updated.isBusy() {
		t.Fatal("expected authoritative worktree-mutation refresh to reconcile busy to idle server truth")
	}
	if updated.sessionName != "server-name" {
		t.Fatalf("session name = %q, want server-name", updated.sessionName)
	}
}

func TestRuntimeSyncPolicyDefersAndReleasesMainViewRefresh(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	client.mainView = clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1", SessionName: "server-name"},
	}
	m.setBusy(true)

	if cmd := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseManual)).cmd; cmd != nil {
		t.Fatalf("expected main-view refresh deferred while streaming/busy, got %T", cmd)
	}
	if client.refreshMainViewCalls != 0 {
		t.Fatalf("RefreshMainView calls = %d, want none while blocked", client.refreshMainViewCalls)
	}
	if !m.runtimeMainViewPendingSet {
		t.Fatal("expected pending main-view refresh")
	}

	m.setBusy(false)
	msgs := collectCmdMessages(t, m.releaseDeferredRuntimeSyncs())
	refresh, ok := findRuntimeMainViewRefreshMsg(msgs)
	if !ok {
		t.Fatalf("expected deferred main-view refresh to run, got %+v", msgs)
	}
	next, _ := m.Update(refresh)
	updated := next.(*uiModel)
	if client.refreshMainViewCalls != 1 {
		t.Fatalf("RefreshMainView calls = %d, want 1", client.refreshMainViewCalls)
	}
	if updated.sessionName != "server-name" {
		t.Fatalf("session name = %q, want server-name", updated.sessionName)
	}
}

func TestRuntimeSyncPolicyDropsRoutineMainViewResponseWhenBlockerAppears(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	client.mainView = clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1", SessionName: "server-name"},
	}
	cmd := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseManual)).cmd
	if cmd == nil {
		t.Fatal("expected unblocked main-view refresh to start")
	}
	msg := cmd().(runtimeMainViewRefreshedMsg)

	m.setBusy(true)
	if !m.shouldDeferRuntimeMainViewRefresh(m.runtimeMainViewActiveRequest) {
		t.Fatalf("test fixture did not block active main-view request: busy=%t class=%d req=%+v", m.isBusy(), m.runtimeMainViewActiveRequest.class, m.runtimeMainViewActiveRequest)
	}
	next, followCmd := m.Update(msg)
	updated := next.(*uiModel)
	_ = collectCmdMessages(t, followCmd)

	if updated.sessionName == "server-name" {
		t.Fatal("blocked routine main-view response should not apply session metadata")
	}
	if !updated.runtimeMainViewPendingSet {
		t.Fatal("expected dropped main-view response to become pending")
	}
}

func TestRuntimeSyncPolicyQueuedDrainSurvivesRecoveryCoalescing(t *testing.T) {
	client := &runtimeControlFakeClient{
		transcript: clientui.TranscriptPage{
			SessionID:           "session-1",
			Revision:            2,
			CommittedEntryCount: 1,
			Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "recovered"}},
		},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7
	m.pendingQueuedDrainAfterHydration = true

	if cmd := m.requestRuntimeQueuedDrainTranscriptSync(); cmd != nil {
		t.Fatalf("expected queued-drain request to wait behind busy hydration, got %T", cmd)
	}
	if got := m.runtimeTranscriptPending.syncCause; got != runtimeTranscriptSyncCauseQueuedDrain {
		t.Fatalf("pending sync after queued drain = %q, want queued_drain", got)
	}
	if cmd := m.requestRuntimeTranscriptSyncForContinuityLoss(clientui.TranscriptRecoveryCauseStreamGap); cmd != nil {
		t.Fatalf("expected recovery request to wait behind busy hydration, got %T", cmd)
	}
	if got := m.runtimeTranscriptPending.syncCause; got != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("pending sync after recovery = %q, want continuity_recovery", got)
	}

	next, followCmd := m.Update(runtimeTranscriptRefreshedMsg{
		token:      7,
		transcript: clientui.TranscriptPage{SessionID: "session-1", Revision: 1},
	})
	m = next.(*uiModel)
	msgs := collectCmdMessages(t, followCmd)
	recoveryMsg, ok := findRuntimeTranscriptRefreshMsg(msgs)
	if !ok {
		t.Fatalf("expected coalesced recovery hydrate to start, got %+v", msgs)
	}
	if recoveryMsg.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("coalesced sync cause = %q, want continuity recovery", recoveryMsg.syncCause)
	}
	if !m.pendingQueuedDrainAfterHydration || m.queuedDrainReadyAfterHydration {
		t.Fatalf("queued drain should remain armed but not ready before coalesced recovery completes: pending=%t ready=%t", m.pendingQueuedDrainAfterHydration, m.queuedDrainReadyAfterHydration)
	}

	next, finalCmd := m.Update(recoveryMsg)
	m = next.(*uiModel)
	_ = collectCmdMessages(t, finalCmd)
	if m.pendingQueuedDrainAfterHydration || m.queuedDrainReadyAfterHydration {
		t.Fatalf("expected successful coalesced recovery to release queued-drain state, pending=%t ready=%t", m.pendingQueuedDrainAfterHydration, m.queuedDrainReadyAfterHydration)
	}
}

func TestStartupUpdateNoticeUsesMainViewPolicyWhenUnchecked(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	client.mainView = clientui.RuntimeMainView{
		Status: clientui.RuntimeStatus{
			Update: clientui.UpdateStatus{Available: true, LatestVersion: "v9.9.9"},
		},
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
	}

	cmd := m.startupUpdateNoticeCmd(clientui.UpdateStatus{})
	msgs := collectCmdMessages(t, cmd)
	refresh, ok := findRuntimeMainViewRefreshMsg(msgs)
	if !ok {
		t.Fatalf("expected unchecked startup update status to refresh through main-view policy, got %+v", msgs)
	}
	if client.refreshMainViewCalls != 1 {
		t.Fatalf("RefreshMainView calls = %d, want 1", client.refreshMainViewCalls)
	}
	next, followCmd := m.Update(refresh)
	updated := next.(*uiModel)
	notice, ok := findStartupUpdateNoticeMsg(collectCmdMessages(t, followCmd))
	if !ok {
		t.Fatal("expected startup update notice after policy refresh applies")
	}
	if notice.version != "v9.9.9" {
		t.Fatalf("startup update notice version = %q, want v9.9.9", notice.version)
	}
	if updated.runtimeMainViewPendingSet {
		t.Fatal("did not expect pending main-view refresh after unblocked startup update refresh")
	}
}

func TestStartupUpdateNoticeDefersMainViewRefreshWhenBlocked(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	client.mainView = clientui.RuntimeMainView{
		Status: clientui.RuntimeStatus{
			Update: clientui.UpdateStatus{Available: true, LatestVersion: "v9.9.9"},
		},
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
	}
	m.setBusy(true)

	if cmd := m.startupUpdateNoticeCmd(clientui.UpdateStatus{}); cmd != nil {
		t.Fatalf("expected blocked startup update refresh to defer, got %T", cmd)
	}
	if client.refreshMainViewCalls != 0 {
		t.Fatalf("RefreshMainView calls while blocked = %d, want none", client.refreshMainViewCalls)
	}
	if !m.runtimeMainViewPendingSet {
		t.Fatal("expected pending startup update main-view refresh while blocked")
	}

	m.setBusy(false)
	msgs := collectCmdMessages(t, m.releaseDeferredRuntimeSyncs())
	refresh, ok := findRuntimeMainViewRefreshMsg(msgs)
	if !ok {
		t.Fatalf("expected deferred startup update refresh after blocker clears, got %+v", msgs)
	}
	next, followCmd := m.Update(refresh)
	_ = next.(*uiModel)
	if _, ok := findStartupUpdateNoticeMsg(collectCmdMessages(t, followCmd)); !ok {
		t.Fatal("expected startup update notice after deferred policy refresh applies")
	}
}

func TestStartupUpdateNoticePendingRefreshSurvivesWorktreeRefreshCoalescing(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	client.mainView = clientui.RuntimeMainView{
		Status: clientui.RuntimeStatus{
			Update: clientui.UpdateStatus{Available: true, LatestVersion: "v9.9.9"},
		},
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
	}
	m.setBusy(true)

	if cmd := m.startupUpdateNoticeCmd(clientui.UpdateStatus{}); cmd != nil {
		t.Fatalf("expected blocked startup update refresh to defer, got %T", cmd)
	}
	if m.runtimeMainViewPending.cause != runtimeMainViewRefreshCauseStartupUpdate {
		t.Fatalf("pending main-view cause = %q, want startup_update", m.runtimeMainViewPending.cause)
	}
	if cmd := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseManual)).cmd; cmd != nil {
		t.Fatalf("expected blocked routine refresh to coalesce, got %T", cmd)
	}
	if m.runtimeMainViewPending.cause != runtimeMainViewRefreshCauseStartupUpdate {
		t.Fatalf("coalesced pending main-view cause = %q, want startup_update", m.runtimeMainViewPending.cause)
	}

	m.setBusy(false)
	msgs := collectCmdMessages(t, m.releaseDeferredRuntimeSyncs())
	refresh, ok := findRuntimeMainViewRefreshMsg(msgs)
	if !ok {
		t.Fatalf("expected coalesced startup update refresh after blocker clears, got %+v", msgs)
	}
	next, followCmd := m.Update(refresh)
	_ = next.(*uiModel)
	if _, ok := findStartupUpdateNoticeMsg(collectCmdMessages(t, followCmd)); !ok {
		t.Fatal("expected startup update notice after coalesced refresh applies")
	}
}

func findRuntimeTranscriptRefreshMsg(msgs []tea.Msg) (runtimeTranscriptRefreshedMsg, bool) {
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			return typed, true
		}
	}
	return runtimeTranscriptRefreshedMsg{}, false
}

func assertNoRuntimeTranscriptRefreshMsg(t *testing.T, msgs []tea.Msg) {
	t.Helper()
	if refresh, ok := findRuntimeTranscriptRefreshMsg(msgs); ok {
		t.Fatalf("did not expect transcript refresh msg, got %+v", refresh)
	}
}

func runtimeTranscriptRefreshOrReleaseDeferredForTest(t *testing.T, m *uiModel, cmd tea.Cmd) runtimeTranscriptRefreshedMsg {
	t.Helper()
	msgs := collectCmdMessages(t, cmd)
	if refresh, ok := findRuntimeTranscriptRefreshMsg(msgs); ok {
		return refresh
	}
	if !m.runtimeTranscriptPendingSet {
		t.Fatalf("expected transcript refresh or deferred pending sync, got msgs=%+v", msgs)
	}
	m.sawAssistantDelta = false
	refresh, ok := findRuntimeTranscriptRefreshMsg(collectCmdMessages(t, m.releaseDeferredRuntimeSyncs()))
	if !ok {
		t.Fatalf("expected deferred transcript refresh after blocker clears")
	}
	return refresh
}

func findRuntimeMainViewRefreshMsg(msgs []tea.Msg) (runtimeMainViewRefreshedMsg, bool) {
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeMainViewRefreshedMsg); ok {
			return typed, true
		}
	}
	return runtimeMainViewRefreshedMsg{}, false
}

func findStartupUpdateNoticeMsg(msgs []tea.Msg) (startupUpdateNoticeMsg, bool) {
	for _, msg := range msgs {
		if typed, ok := msg.(startupUpdateNoticeMsg); ok {
			return typed, true
		}
	}
	return startupUpdateNoticeMsg{}, false
}
