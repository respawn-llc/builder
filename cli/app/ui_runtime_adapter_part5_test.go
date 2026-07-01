package app

import (
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func TestApplyRuntimeTranscriptPageAcceptsNewerRevisionReasoningClear(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "user", Text: "u"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "Plan summary"}})

	fresh := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "u"},
			{Role: "assistant", Text: "done", Phase: string(llm.MessagePhaseFinal)},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, fresh, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	if detail := stripANSIAndTrimRight(m.view.View()); strings.Contains(detail, "Plan summary") {
		t.Fatalf("expected newer authoritative page to clear live reasoning, got %q", detail)
	}
}

func TestReasoningDeltaPreservesStreamingWhitespaceAcrossUpdates(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "Analyzing chat snapshot commentary insertion"}})

	if detail := stripANSIAndTrimRight(m.view.View()); !strings.Contains(detail, "Analyzing chat snapshot commentary insertion") {
		t.Fatalf("expected reasoning whitespace preserved, got %q", detail)
	}
}

func TestReasoningDeltaBoldOnlyUpdatesStatusLineHeader(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventRunStateChanged, RunState: &runtime.RunState{Lifecycle: runtime.RunningRunLifecycle(runtime.RunModeTurn)}})
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "**Summarizing fix and investigation**"}})

	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Summarizing fix and investigation") {
		t.Fatalf("expected bold-only reasoning summary in status line, got %q", status)
	}
	if strings.Contains(status, "**Summarizing fix and investigation**") {
		t.Fatalf("expected status line header without markdown markers, got %q", status)
	}
}

func TestReasoningDeltaMixedContentUsesFirstBoldSpanForStatusLineHeader(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventRunStateChanged, RunState: &runtime.RunState{Lifecycle: runtime.RunningRunLifecycle(runtime.RunModeTurn)}})
	text := "**Summarizing fix and investigation**\n\nregular reasoning details"
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: text}})

	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Summarizing fix and investigation") {
		t.Fatalf("expected first bold span in status line, got %q", status)
	}
	if detail := stripANSIAndTrimRight(m.view.View()); !strings.Contains(detail, "regular reasoning details") {
		t.Fatalf("expected mixed reasoning content to remain in detail view, got %q", detail)
	}
}

func TestReasoningDeltaRegularSummaryDoesNotReplaceStatusLineHeader(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "u"})
	m.forwardToView(tui.ToggleModeMsg{})

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventRunStateChanged, RunState: &runtime.RunState{Lifecycle: runtime.RunningRunLifecycle(runtime.RunModeTurn)}})
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "**Preparing patch**"}})
	text := "I am exploring ways to define atomic, low-level collection methods in NavResultStore that support reified filtering without reflection."
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: text}})

	if detail := stripANSIAndTrimRight(m.view.View()); !strings.Contains(detail, "I am exploring ways to define atomic, low-level collection methods") {
		t.Fatalf("expected plain reasoning summary in detail view, got %q", detail)
	}
	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Preparing patch") {
		t.Fatalf("expected prior bold-only header to persist, got %q", status)
	}
	if strings.Contains(status, "I am exploring ways to define atomic") {
		t.Fatalf("did not expect regular reasoning summary in status line, got %q", status)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventReasoningDelta, ReasoningDelta: &llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "**Running checks**"}})
	status = stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Running checks") || strings.Contains(status, "Preparing patch") {
		t.Fatalf("expected latest bold-only header to replace prior value, got %q", status)
	}
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventRunStateChanged, RunState: &runtime.RunState{Lifecycle: runtime.IdleRunLifecycle()}})
	status = stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if strings.Contains(status, "Running checks") {
		t.Fatalf("expected status line header cleared when run stops, got %q", status)
	}
}

func TestConversationSnapshotCommitClearsSawAssistantDelta(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.setBusy(true)
	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "partial"})
	if !m.sawAssistantDelta {
		t.Fatal("expected sawAssistantDelta true after assistant delta")
	}

	_ = m.runtimeAdapter().applyChatSnapshot(runtime.ChatSnapshot{Entries: []runtime.ChatEntry{{Role: "assistant", Text: "partial"}}, Streaming: ""})
	m.setBusy(false)
	m.layout().syncViewport()

	if m.sawAssistantDelta {
		t.Fatal("expected sawAssistantDelta cleared after commit snapshot")
	}
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected committed snapshot to clear streaming text, got %q", got)
	}
}

func TestApplyChatSnapshotShowsMixedParallelPendingStatesInLiveView(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.spinnerFrame = 0

	cmd := m.runtimeAdapter().applyChatSnapshot(runtime.ChatSnapshot{Entries: []runtime.ChatEntry{
		{Role: "assistant", Text: "working"},
		{Role: "tool_call", Text: "echo a", ToolCallID: "call_a", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "echo a"}},
		{Role: "tool_call", Text: "echo b", ToolCallID: "call_b", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "echo b"}},
		{Role: "tool_result_ok", Text: "out-b", ToolCallID: "call_b"},
	}})
	if cmd != nil {
		_ = cmd()
	}
	m.layout().syncViewport()

	view := stripANSIPreserve(m.View())
	pending := tui.PendingToolEntries(m.transcriptEntries)
	if len(pending) != 3 || pending[0].ToolCallID != "call_a" || pending[1].ToolCallID != "call_b" || pending[2].ToolCallID != "call_b" {
		t.Fatalf("expected unresolved call plus completed sibling/result in pending live group, got %+v", pending)
	}
	if !strings.Contains(view, "echo a") {
		t.Fatalf("expected unresolved tool to stay in live view, got %q", view)
	}
	if !strings.Contains(view, "$  echo b") {
		t.Fatalf("expected completed live shell command to align with two spaces after symbol, got %q", view)
	}
	if strings.Contains(view, "waiting") {
		t.Fatalf("did not expect waiting annotation in live view, got %q", view)
	}
}

func TestApplyChatSnapshotKeepsParallelPendingToolsInLiveView(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.spinnerFrame = 0

	cmd := m.runtimeAdapter().applyChatSnapshot(runtime.ChatSnapshot{Entries: []runtime.ChatEntry{
		{Role: "tool_call", Text: "echo alpha", ToolCallID: "call_alpha", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "echo alpha"}},
		{Role: "tool_call", Text: "echo beta", ToolCallID: "call_beta", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "echo beta"}},
	}})
	if cmd != nil {
		_ = cmd()
	}
	m.layout().syncViewport()

	pending := tui.PendingToolEntries(m.transcriptEntries)
	if len(pending) != 2 || pending[0].ToolCallID != "call_alpha" || pending[1].ToolCallID != "call_beta" {
		t.Fatalf("expected both parallel calls to remain pending, got %+v", pending)
	}
	view := stripANSIPreserve(m.View())
	if !strings.Contains(view, "echo alpha") {
		t.Fatalf("expected alpha pending tool in live view, got %q", view)
	}
	if !strings.Contains(view, "echo beta") {
		t.Fatalf("expected beta pending tool in live view, got %q", view)
	}
}

type gatedRefreshRuntimeClient struct {
	runtimeControlFakeClient
	page           clientui.TranscriptPage
	refreshStarted chan struct{}
	releaseRefresh chan struct{}
}

func (c *gatedRefreshRuntimeClient) RefreshTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	_ = req
	c.refreshTranscriptCalls++
	if c.refreshStarted != nil {
		select {
		case <-c.refreshStarted:
		default:
			close(c.refreshStarted)
		}
	}
	if c.releaseRefresh != nil {
		<-c.releaseRefresh
	}
	return c.page, c.err
}

func TestUserMessageFlushedSyncsConversationForNativeReplay(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	_ = m.runtimeAdapter().handleRuntimeEvent(runtime.Event{Kind: runtime.EventUserMessageFlushed, UserMessage: "steered message"})
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected immediate transcript append, got %d entries", got)
	}
	if got := m.transcriptEntries[0].Text; got != "steered message" {
		t.Fatalf("transcript entry text = %q, want steered message", got)
	}
	if !strings.Contains(stripANSIPreserve(m.view.OngoingSnapshot()), "steered message") {
		t.Fatalf("expected flushed message in ongoing view, got %q", m.view.OngoingSnapshot())
	}
}

func TestUserMessageFlushedAlreadyCoveredByAuthoritativeTailDoesNotDuplicateNativeReplay(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "user", Text: "steered message"}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "steered message",
		TranscriptRevision:         10,
		CommittedEntryCount:        1,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steered message",
		}},
	}).cmd
	if len(m.transcriptEntries) != 1 {
		t.Fatalf("expected stale flushed user message to be skipped, got %+v", m.transcriptEntries)
	}
	_ = cmd
}

func TestWorktreeReminderBeforeUserFlushRendersOnceInOngoing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 120
	m.termHeight = 24
	m.windowSizeKnown = true

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         10,
		TranscriptEntries: []clientui.ChatEntry{{
			Visibility:    transcript.EntryVisibilityAll,
			Role:          string(transcript.EntryRoleDeveloperContext),
			Text:          "The user has moved this conversation into a git worktree.",
			CondensedText: "Switched worktree to fixes-1.2-part-3: /tmp/fixes-1.2-part-3",
			MessageType:   string(llm.MessageTypeWorktreeMode),
		}},
	}).
		cmd
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "typed after switch",
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "typed after switch",
		}},
	}).
		cmd

	if len(m.transcriptEntries) != 2 {
		t.Fatalf("transcript entries = %+v, want worktree reminder then user", m.transcriptEntries)
	}
	if got := strings.TrimSpace(m.transcriptEntries[0].CondensedText); got != "Switched worktree to fixes-1.2-part-3: /tmp/fixes-1.2-part-3" {
		t.Fatalf("worktree ongoing text = %q", got)
	}
	if got := m.transcriptEntries[1].Text; got != "typed after switch" {
		t.Fatalf("user text = %q", got)
	}
	plain := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if count := strings.Count(plain, "Switched worktree to fixes-1.2-part-3"); count != 1 {
		t.Fatalf("worktree reminder count = %d, view=%q", count, plain)
	}
	if count := strings.Count(plain, "typed after switch"); count != 1 {
		t.Fatalf("user message count = %d, view=%q", count, plain)
	}
}

func TestQueuedUserMessageFailedStatusRestoresPendingInjectedInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.pendingInjected = []clientui.QueuedUserMessage{{ID: "queue-1", Text: "steered message"}}
	m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local-1", ServerID: "queue-1", Text: "steered message", State: injectedRuntimeQueueEnqueued}}

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind: clientui.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
			QueueItemID:   "queue-1",
			Status:        clientui.QueuedUserMessageFailed,
			RestoreText:   "steered message",
			FailureReason: clientui.QueuedUserMessageFailureClosing,
		},
	}).
		cmd

	if len(m.pendingInjected) != 0 || len(m.injectedQueue) != 0 {
		t.Fatalf("expected failed queued item removed, pending=%+v queue=%+v", m.pendingInjected, m.injectedQueue)
	}
	if strings.TrimSpace(m.input) != "steered message" {
		t.Fatalf("input = %q, want restored queued text", m.input)
	}
	if m.transientStatus == "" || m.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected transient queue failure status, got %q kind=%d", m.transientStatus, m.transientStatusKind)
	}
	if cmd == nil {
		t.Fatal("expected transient status clear command")
	}
}

func TestQueuedUserMessageSubmittedStatusRemovesPendingInjectedInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.pendingInjected = []clientui.QueuedUserMessage{{ID: "queue-1", Text: "steered message"}, {ID: "queue-2", Text: "follow-up"}}
	m.injectedQueue = []injectedRuntimeQueueItem{
		{LocalID: "local-1", ServerID: "queue-1", Text: "steered message", State: injectedRuntimeQueueEnqueued},
		{LocalID: "local-2", ServerID: "queue-2", Text: "follow-up", State: injectedRuntimeQueueEnqueued},
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind: clientui.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
			QueueItemID: "queue-1",
			Status:      clientui.QueuedUserMessageSubmitted,
		},
	}).
		cmd

	if len(m.pendingInjected) != 1 || m.pendingInjected[0].ID != "queue-2" {
		t.Fatalf("pending injected = %+v, want only queue-2", m.pendingInjected)
	}
	if len(m.injectedQueue) != 1 || m.injectedQueue[0].ServerID != "queue-2" {
		t.Fatalf("injected queue = %+v, want only queue-2", m.injectedQueue)
	}
	if m.input != "" {
		t.Fatalf("input = %q, want unchanged empty input", m.input)
	}
}

func TestProjectedUserMessageFlushedWithSameTextAndNewCommittedCountAppendsDistinctEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "user", Text: "steered message"}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "steered message",
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steered message",
		}},
	}).cmd
	if len(m.transcriptEntries) != 2 {
		t.Fatalf("expected repeated same-text user message to append distinctly, got %+v", m.transcriptEntries)
	}
	_ = cmd
}

func TestProjectedUserMessageFlushedDoesNotScheduleTranscriptRefresh(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "steered message",
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steered message",
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect transcript refresh after flushed user message, got %+v", msgs)
		}
	}
	if got := len(m.transcriptEntries); got != 1 {
		t.Fatalf("expected immediate transcript append, got %d entries", got)
	}
	if got := m.transcriptEntries[0].Text; got != "steered message" {
		t.Fatalf("transcript entry text = %q, want steered message", got)
	}
}

func TestProjectedUserMessageFlushedAdvancesQueuedInputWithoutTranscriptRefresh(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.pendingInjected = queuedUserMessagesForTest("steered message", "follow-up")
	m.input = "steered message"
	m.lockedInjectText = "steered message"
	m.lockedInjectID = "queue-test-0"
	m.setInputSubmitLocked(true)

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                         clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged:   true,
		UserMessage:                  "steered message",
		UserMessageBatchQueueItemIDs: []string{"queue-test-0"},
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steered message",
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect transcript refresh after flushed injected user message, got %+v", msgs)
		}
	}
	if client.recordedPromptHistory != "" {
		t.Fatalf("did not expect queued flush to persist prompt history again, got %q", client.recordedPromptHistory)
	}
	if len(m.pendingInjected) != 1 || m.pendingInjected[0].Text != "follow-up" {
		t.Fatalf("expected pending injected queue advanced, got %+v", m.pendingInjected)
	}
	if m.input != "" {
		t.Fatalf("expected locked input cleared, got %q", m.input)
	}
	if m.isInputSubmitLocked() {
		t.Fatal("expected input submit lock cleared")
	}
}

func TestProjectedUserMessageFlushedDoesNotClobberLaterAssistantDelta(t *testing.T) {
	client := &runtimeControlFakeClient{
		transcript: clientui.TranscriptPage{
			SessionID: "session-1",
			Entries: []clientui.ChatEntry{{
				Role: "user",
				Text: "steered message",
			}},
		},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "steered message",
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steered message",
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect transcript refresh after flushed user message, got %+v", msgs)
		}
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "working"}).cmd
	if got := m.view.OngoingStreamingText(); got != "working" {
		t.Fatalf("ongoing streaming text = %q, want working", got)
	}
	if !strings.Contains(stripANSIPreserve(m.View()), "working") {
		t.Fatalf("expected assistant delta visible in view, got %q", stripANSIPreserve(m.View()))
	}
}

func TestProjectedCommittedToolAndFinalEventsDoNotScheduleTranscriptRefresh(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed"}}
	m.transcriptTotalEntries = len(m.transcriptEntries)
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{BaseOffset: 0, TotalEntries: len(m.transcriptEntries), Entries: m.transcriptEntries})
	m.layout().syncViewport()

	callMeta := transcript.ToolCallMeta{ToolName: "shell", Command: "pwd", CompactText: "pwd", IsShell: true}
	events := []clientui.Event{
		{
			Kind:                       clientui.EventUserMessageFlushed,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         11,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			UserMessage:                "say hi",
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "user",
				Text: "say hi",
			}},
		},
		{Kind: clientui.EventAssistantDelta, StepID: "step-1", AssistantDelta: "done"},
		{
			Kind:                       clientui.EventToolCallStarted,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         12,
			CommittedEntryStart:        2,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_call",
				Text:       "pwd",
				ToolCallID: "call-1",
				ToolCall:   transcriptToolCallMetaClient(&callMeta),
			}},
		},
		{
			Kind:                       clientui.EventToolCallCompleted,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         13,
			CommittedEntryStart:        3,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_result_ok",
				Text:       "$ pwd\n/tmp",
				ToolCallID: "call-1",
			}},
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			CommittedTranscriptChanged: true,
			StepID:                     "step-1",
			TranscriptRevision:         14,
			CommittedEntryStart:        4,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "done",
				Phase: string(llm.MessagePhaseFinal),
			}},
		},
	}

	for _, evt := range events {
		msgs := collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(evt).cmd)
		for _, msg := range msgs {
			if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
				t.Fatalf("did not expect committed runtime event to trigger transcript hydration, event=%s msgs=%+v", evt.Kind, msgs)
			}
		}
	}
	loaded := m.view.LoadedTranscriptEntries()
	if got, want := len(loaded), 5; got != want {
		t.Fatalf("loaded transcript entry count = %d, want %d (%+v)", got, want, loaded)
	}
	if got := loaded[0].Text; got != "seed" {
		t.Fatalf("loaded[0].Text = %q, want seed", got)
	}
	if got := loaded[1].Text; got != "say hi" {
		t.Fatalf("loaded[1].Text = %q, want say hi", got)
	}
	if got := loaded[2].Text; got != "pwd" {
		t.Fatalf("loaded[2].Text = %q, want pwd", got)
	}
	if got := loaded[4].Text; got != "done" {
		t.Fatalf("loaded[4].Text = %q, want done", got)
	}
}

func TestProjectedConversationUpdatedEntriesAdvanceCommittedTranscriptAndDetailView(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true

	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "seed",
		}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail, SkipDetailWarmup: true})
	m.layout().syncViewport()

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "committed after",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect conversation_updated committed delta to trigger transcript hydration, got %+v", msgs)
		}
	}

	if got, want := len(m.transcriptEntries), 2; got != want {
		t.Fatalf("transcript entry count = %d, want %d", got, want)
	}
	if m.transcriptEntries[1].Transient {
		t.Fatalf("expected conversation_updated entry to be committed, got %+v", m.transcriptEntries[1])
	}
	if got := m.transcriptEntries[1].Text; got != "committed after" {
		t.Fatalf("second transcript entry = %q, want committed after", got)
	}
	if got := m.transcriptRevision; got != 11 {
		t.Fatalf("transcript revision = %d, want 11", got)
	}
	if got := m.detailTranscript.totalEntries; got != 2 {
		t.Fatalf("detail transcript total entries = %d, want 2", got)
	}
	if got, want := len(m.detailTranscript.entries), 2; got != want {
		t.Fatalf("detail transcript entry count = %d, want %d", got, want)
	}
	if got := m.detailTranscript.entries[1].Text; got != "committed after" {
		t.Fatalf("detail transcript tail = %q, want committed after", got)
	}
	view := stripANSIAndTrimRight(m.View())
	if !strings.Contains(view, "seed") {
		t.Fatalf("expected detail view to retain selected committed seed row, got %q", view)
	}
}

func TestProjectedConversationUpdatedMatchingCommittedStateSkipsHydration(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}, {Role: "assistant", Text: "committed after", Phase: string(llm.MessagePhaseFinal)}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        2,
	}).
		cmd
	for _, msg := range collectCmdMessages(t, cmd) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect matching committed conversation_updated to trigger hydration, got %+v", msg)
		}
	}
}

func TestProjectedPlainConversationUpdatedNeverHydrates(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:   clientui.EventConversationUpdated,
		StepID: "step-1",
	}).
		cmd
	for _, msg := range collectCmdMessages(t, cmd) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect plain conversation_updated to trigger hydration, got %+v", msg)
		}
	}
	if m.runtimeTranscriptBusy {
		t.Fatal("did not expect runtime transcript sync to start for plain conversation_updated")
	}
}

func TestProjectedCommittedConversationUpdatedRequestsHydrationOnlyOnContinuityLoss(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "assistant", Text: "committed after", Phase: string(llm.MessagePhaseFinal)},
		},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	client.transcript = clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "assistant", Text: "committed after", Phase: string(llm.MessagePhaseFinal)},
			{Role: "reviewer_status", Text: "Supervisor ran: no changes."},
		},
	}

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	refreshFound := false
	for _, msg := range msgs {
		typed, ok := msg.(runtimeTranscriptRefreshedMsg)
		if !ok {
			continue
		}
		refresh = typed
		refreshFound = true
	}
	if !refreshFound {
		t.Fatalf("expected committed conversation_updated mismatch to request hydration, got %+v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseCommittedConversation {
		t.Fatalf("committed conversation sync cause = %q, want %q", refresh.syncCause, runtimeTranscriptSyncCauseCommittedConversation)
	}
	if refresh.req != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("request = %+v, want recent-tail (zero cursor)", refresh.req)
	}
}

func TestBootstrapRefreshRejectsStaleAuthoritativePageAfterLocalCommittedEvent(t *testing.T) {
	client := &gatedRefreshRuntimeClient{
		runtimeControlFakeClient: runtimeControlFakeClient{
			sessionView: clientui.RuntimeSessionView{SessionID: "session-1"},
		},
		page: clientui.TranscriptPage{
			SessionID: "session-1",
			Revision:  10,
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		},
		refreshStarted: make(chan struct{}),
		releaseRefresh: make(chan struct{}),
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, client.page, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	cmd := m.requestRuntimeBootstrapTranscriptSync()
	if cmd == nil {
		t.Fatal("expected bootstrap transcript sync command")
	}
	msgCh := make(chan tea.Msg, 1)
	go func() {
		msgCh <- cmd()
	}()
	<-client.refreshStarted

	commitCmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "live commit",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}).
		cmd
	for _, msg := range collectCmdMessages(t, commitCmd) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect local committed event during bootstrap refresh to trigger extra hydration, got %+v", msg)
		}
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected local committed event appended during bootstrap refresh, got %d entries", got)
	}

	close(client.releaseRefresh)
	refreshMsg := (<-msgCh).(runtimeTranscriptRefreshedMsg)
	next, followCmd := m.Update(refreshMsg)
	updated := next.(*uiModel)
	_ = collectCmdMessages(t, followCmd)

	if got := len(updated.transcriptEntries); got != 2 {
		t.Fatalf("expected stale bootstrap page rejected after local committed event, got %d entries", got)
	}
	if got := updated.transcriptEntries[0].Text; got != "seed" {
		t.Fatalf("first transcript entry = %q, want seed", got)
	}
	if got := updated.transcriptEntries[1].Text; got != "live commit" {
		t.Fatalf("second transcript entry = %q, want live commit", got)
	}
	if strings.Count(stripANSIAndTrimRight(committedOngoingProjectionForTest(updated.view).Render(tui.TranscriptDivider)), "live commit") != 1 {
		t.Fatalf("expected live commit exactly once after stale bootstrap refresh, got %q", stripANSIAndTrimRight(committedOngoingProjectionForTest(updated.view).Render(tui.TranscriptDivider)))
	}
}

func TestProjectedCommittedGapRequestsExplicitCommittedGapHydration(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	if cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}
	client.transcript = clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "user", Text: "missing gap row"},
			{Role: "assistant", Text: "authoritative tail"},
		},
	}

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        3,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "authoritative tail",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	refreshFound := false
	for _, msg := range msgs {
		typed, ok := msg.(runtimeTranscriptRefreshedMsg)
		if !ok {
			continue
		}
		refresh = typed
		refreshFound = true
	}
	if !refreshFound {
		t.Fatalf("expected committed gap to request runtime transcript refresh, got %+v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseCommittedGap {
		t.Fatalf("committed gap sync cause = %q, want %q", refresh.syncCause, runtimeTranscriptSyncCauseCommittedGap)
	}
	if refresh.req != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("request = %+v, want recent-tail (zero cursor)", refresh.req)
	}
}

func TestProjectedUserMessageFlushedDefersCommittedGapWhileAssistantStreamIsLive(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.setBusy(true)
	m.pendingInjected = queuedUserMessagesForTest("steered message")
	m.input = "steered message"
	m.lockedInjectText = "steered message"
	m.lockedInjectID = "queue-test-0"
	m.setInputSubmitLocked(true)
	client.transcript = clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  7,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "foreground done"},
			{Role: "user", Text: "steered message"},
		},
	}
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "foreground done"}).cmd

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                         clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged:   true,
		TranscriptRevision:           7,
		UserMessage:                  "steered message",
		UserMessageBatchQueueItemIDs: []string{"queue-test-0"},
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steered message",
		}},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect committed gap hydration while assistant stream is live, got %+v", msgs)
		}
	}
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("expected live user append to wait behind assistant stream, got %+v", m.transcriptEntries)
	}
	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("expected queued user flush to use deferred committed tail, got %d", got)
	}
	if got := m.view.OngoingStreamingText(); got != "foreground done" {
		t.Fatalf("expected live assistant stream to remain visible, got %q", got)
	}
	if client.recordedPromptHistory != "" {
		t.Fatalf("did not expect queued flush to persist prompt history again, got %q", client.recordedPromptHistory)
	}
	if len(m.pendingInjected) != 0 {
		t.Fatalf("expected pending injected queue consumed even while hydrate is pending, got %+v", m.pendingInjected)
	}
	queuedPane := strings.TrimSpace(stripANSIAndTrimRight(strings.Join(m.layout().renderQueuedMessagesPane(80), "\n")))
	if !strings.Contains(queuedPane, "next: steered message") {
		t.Fatalf("expected flushed user message to stay visible as next pending item, got %q", queuedPane)
	}
	if m.isInputSubmitLocked() {
		t.Fatal("expected input submit lock cleared")
	}
	if m.input != "" {
		t.Fatalf("expected cleared input after deferred flushed user message, got %q", m.input)
	}
	if !m.sawAssistantDelta {
		t.Fatal("expected assistant delta flag to remain set while stream is live")
	}
}

func TestDeferredCommittedUserFlushRequestsTranscriptRefreshWhenRunEndsWithoutCatchUp(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.deferredCommittedTail = []deferredProjectedTranscriptTail{{
		rangeStart: 1,
		rangeEnd:   2,
		revision:   7,
		entries:    []clientui.ChatEntry{{Role: "user", Text: "steered message"}},
	}}

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()},
	}).
		cmd
	msgs := collectCmdMessages(t, cmd)
	refreshFound := false
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refreshFound = true
		}
	}
	if !refreshFound {
		t.Fatalf("expected deferred committed tail to request transcript refresh when run ends without catch-up, got %+v", msgs)
	}
	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("expected deferred committed tail retained until hydration applies, got %d", got)
	}
}
