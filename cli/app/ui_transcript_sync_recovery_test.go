package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSessionActivityGapRecoveryEventuallyHydratesCommittedTranscriptInBothModes(t *testing.T) {
	oldDelay := uiRuntimeHydrationRetryDelay
	uiRuntimeHydrationRetryDelay = 0
	defer func() { uiRuntimeHydrationRetryDelay = oldDelay }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{err: serverapi.ErrStreamGap}}}
	resubscribed := &stubSessionActivitySubscription{}
	remaining := []serverapi.SessionActivitySubscription{resubscribed}
	events, stop := startSessionActivityEvents(ctx, initial, func(context.Context, uint64) (serverapi.SessionActivitySubscription, error) {
		if len(remaining) == 0 {
			return nil, serverapi.ErrStreamGap
		}
		next := remaining[0]
		remaining = remaining[1:]
		return next, nil
	}, func() bool { return false }, nil)
	defer stop()

	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{
			{SessionID: "session-1"},
			{SessionID: "session-1", SessionName: "debug session", Entries: []clientui.ChatEntry{{Role: "assistant", Text: "final answer after retry"}}, CommittedEntryCount: 1},
		},
		errs: []error{errors.New("temporary refresh failure"), nil},
	}

	m := newProjectedTestUIModel(client, events, closedAskEvents())
	m.startupCmds = nil
	m.termWidth = 90
	m.termHeight = 16
	m.windowSizeKnown = true
	m.layout().syncViewport()

	evt := waitSessionActivityEvent(t, events)
	if evt.Kind != clientui.EventStreamGap {
		t.Fatalf("expected explicit stream-gap event after replay failure, got %+v", evt)
	}
	if evt.RecoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("expected stream-gap recovery cause, got %+v", evt)
	}

	firstCmd := m.runtimeAdapter().applyProjectedRuntimeEvent(evt).cmd
	if firstCmd == nil {
		t.Fatal("expected first authoritative refresh command")
	}
	firstRefresh, ok := firstCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", firstCmd())
	}
	if firstRefresh.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("first sync cause = %q, want %q", firstRefresh.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
	}
	next, retryCmd := m.Update(firstRefresh)
	if retryCmd == nil {
		t.Fatal("expected retry command after first refresh failure")
	}
	retryMsg, ok := retryCmd().(runtimeTranscriptRetryMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRetryMsg, got %T", retryCmd())
	}
	if retryMsg.recoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("expected retry to preserve stream-gap recovery cause, got %+v", retryMsg)
	}
	if retryMsg.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("retry sync cause = %q, want %q", retryMsg.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
	}

	next, secondCmd := next.(*uiModel).Update(retryMsg)
	if secondCmd == nil {
		t.Fatal("expected second authoritative refresh command after retry tick")
	}
	secondRefresh, ok := secondCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", secondCmd())
	}
	if secondRefresh.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("second sync cause = %q, want %q", secondRefresh.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
	}
	next, followUp := next.(*uiModel).Update(secondRefresh)
	m = next.(*uiModel)
	m = applyRuntimeEventBatchMessagesFromCommand(t, m, followUp)

	ongoing := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(ongoing, "final answer after retry") {
		t.Fatalf("expected ongoing mode to converge after refresh retry, got %q", ongoing)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = next.(*uiModel)
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", m.view.Mode())
	}
	detail := stripANSIAndTrimRight(m.view.View())
	if !strings.Contains(detail, "final answer after retry") {
		t.Fatalf("expected detail mode to converge after refresh retry, got %q", detail)
	}

	if client.calls != 2 {
		t.Fatalf("refresh call count = %d, want 2", client.calls)
	}
}

func TestSupervisorTerminalEventsReachOngoingBeforeDetail(t *testing.T) {
	m, ongoing := newSupervisorTerminalFenceRepro(t)

	if !strings.Contains(ongoing, "updated final after supervisor") || !strings.Contains(ongoing, "Supervisor ran: 1 suggestion, applied.") {
		t.Fatalf("expected ongoing to receive final assistant and supervisor rows before detail, got %q", ongoing)
	}
	if m.waitRuntimeEventAfterHydration {
		t.Fatal("expected hydration fence to clear after stale authoritative response")
	}
	if got := len(m.runtimeEvents); got != 0 {
		t.Fatalf("expected terminal row events consumed before detail, got %d queued events", got)
	}
}

func TestDetailHydrationStillReadsSupervisorTerminalRowsFromSSOT(t *testing.T) {
	m, ongoing := newSupervisorTerminalFenceRepro(t)
	if !strings.Contains(ongoing, "updated final after supervisor") || !strings.Contains(ongoing, "Supervisor ran: 1 suggestion, applied.") {
		t.Fatalf("expected ongoing delivered before detail, got %q", ongoing)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = next.(*uiModel)
	_ = collectCmdMessages(t, cmd)
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode = %q, want detail", m.view.Mode())
	}

	// The real workaround is an authoritative detail hydration. The live-dirty
	// flag avoids the duplicate-page short-circuit because this repro
	// intentionally starts from an already-primed detail tail.
	m.transcriptLiveDirty = true
	next, cmd = m.Update(detailTranscriptLoadMsg{})
	m = next.(*uiModel)
	msgs := collectCmdMessages(t, cmd)
	var detailRefresh runtimeTranscriptRefreshedMsg
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			detailRefresh = typed
		}
	}
	if detailRefresh.token == 0 {
		t.Fatalf("expected detail hydration after ongoing miss, got %+v", msgs)
	}

	next, cmd = m.Update(detailRefresh)
	m = next.(*uiModel)
	_ = collectCmdMessages(t, cmd)
	detailAfterHydration := stripANSIAndTrimRight(m.view.View())
	if !strings.Contains(detailAfterHydration, "updated final after supervisor") || !strings.Contains(detailAfterHydration, "Supervisor ran: 1 suggestion, applied.") {
		t.Fatalf("expected detail hydration to repair missing terminal rows, got %q", detailAfterHydration)
	}
}

func newSupervisorTerminalFenceRepro(t *testing.T) (*uiModel, string) {
	t.Helper()
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{
			{
				SessionID:           "session-1",
				Revision:            12,
				CommittedEntryCount: 2,
				Entries: []clientui.ChatEntry{
					{Role: "assistant", Text: "seed", Phase: string(llm.MessagePhaseFinal)},
					{Role: "user", Text: "supervisor feedback"},
				},
			},
			{
				SessionID:           "session-1",
				Revision:            13,
				CommittedEntryCount: 4,
				Entries: []clientui.ChatEntry{
					{Role: "assistant", Text: "seed", Phase: string(llm.MessagePhaseFinal)},
					{Role: "user", Text: "supervisor feedback"},
					{Role: "assistant", Text: "updated final after supervisor", Phase: string(llm.MessagePhaseFinal)},
					{Role: "reviewer_status", Text: "Supervisor ran: 1 suggestion, applied."},
				},
			},
		},
	}
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryCount:        4,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "updated final after supervisor",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryCount:        4,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "reviewer_status", Text: "Supervisor ran: 1 suggestion, applied."}},
	}
	close(runtimeEvents)

	m := newProjectedTestUIModel(client, runtimeEvents, closedAskEvents())
	m.startupCmds = nil
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.activity = uiActivityRunning
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 100})
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID:           "session-1",
		Revision:            11,
		CommittedEntryCount: 1,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed", Phase: string(llm.MessagePhaseFinal)}},
	}, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		m = applyRuntimeEventBatchMessagesFromCommand(t, m, cmd)
	}

	next, cmd := m.Update(runtimeEventBatchMsg{events: []clientui.Event{
		{
			Kind:                       clientui.EventUserMessageFlushed,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         12,
			CommittedEntryCount:        2,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			UserMessage:                "supervisor feedback",
			TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "supervisor feedback"}},
		},
		{
			Kind:                       clientui.EventConversationUpdated,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         13,
			CommittedEntryCount:        4,
		},
	}})
	m = next.(*uiModel)
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
		}
	}
	if refresh.token == 0 {
		t.Fatalf("expected committed-advance hydration command, got %+v", msgs)
	}

	next, cmd = m.Update(refresh)
	m = next.(*uiModel)
	m = applyRuntimeEventBatchMessagesFromCommand(t, m, cmd)
	return m, stripANSIAndTrimRight(m.view.OngoingSnapshot())
}

func applyRuntimeEventBatchMessagesFromCommand(t *testing.T, m *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	msgs := collectCmdMessages(t, cmd)
	for guard := 0; len(msgs) > 0 && guard < 20; guard++ {
		msg := msgs[0]
		msgs = msgs[1:]
		batch, ok := msg.(runtimeEventBatchMsg)
		if !ok {
			continue
		}
		next, nextCmd := m.Update(batch)
		m = next.(*uiModel)
		msgs = append(msgs, collectCmdMessages(t, nextCmd)...)
	}
	if len(msgs) > 0 {
		t.Fatalf("command drain stopped with %d unprocessed message(s): %+v", len(msgs), msgs)
	}
	return m
}

func TestDeferredContinuityRefreshPreservesRecoveryCauseAcrossBusyHydration(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{SessionID: "session-1", Entries: []clientui.ChatEntry{{Role: "assistant", Text: "authoritative after gap"}}, CommittedEntryCount: 1}},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7

	if cmd := m.requestRuntimeTranscriptSyncForContinuityLoss(clientui.TranscriptRecoveryCauseStreamGap); cmd != nil {
		t.Fatalf("expected no command while hydration is already in flight, got %T", cmd)
	}
	if !m.runtimeTranscriptPendingSet {
		t.Fatal("expected pending hydrate follow-up after deferred continuity refresh")
	}
	if got := m.runtimeTranscriptPending.recoveryCause; got != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("pending recovery cause = %q, want %q", got, clientui.TranscriptRecoveryCauseStreamGap)
	}

	next, followCmd := m.Update(runtimeTranscriptRefreshedMsg{token: 7, transcript: clientui.TranscriptPage{SessionID: "session-1"}})
	if followCmd == nil {
		t.Fatal("expected follow-up refresh after dirty hydrate completion")
	}
	followMsg, ok := followCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", followCmd())
	}
	if followMsg.recoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("follow-up recovery cause = %q, want %q", followMsg.recoveryCause, clientui.TranscriptRecoveryCauseStreamGap)
	}
	if followMsg.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
		t.Fatalf("follow-up sync cause = %q, want %q", followMsg.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
	}
	updated := next.(*uiModel)
	if updated.runtimeTranscriptPendingSet {
		t.Fatal("expected pending hydrate cleared once follow-up request starts")
	}
}
