package app

import (
	"context"
	"core/cli/app/internal/runtimestate"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestApplyRuntimeEventReductionInvalidLifecycleKeepsSideEffectsAndReturnsStatusCommand(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.conversationFreshness = clientui.ConversationFreshnessFresh
	reviewer, err := clientui.NewReviewerLifecycle(true, true)
	if err != nil {
		t.Fatalf("reviewer lifecycle: %v", err)
	}

	cmd := m.runtimeAdapter().applyRuntimeEventReduction(runtimestate.RuntimeEventReduction{
		RunState: runtimestate.RuntimeRunStateReduction{
			State: runtimestate.RuntimeRunState{
				Run:        m.runtimeLifecycle.Run,
				Compaction: clientui.NewCompactionLifecycle(true),
				Reviewer:   reviewer,
			},
			Err: errors.New("bad lifecycle"),
		},
		Conversation: runtimestate.RuntimeConversationReduction{State: runtimestate.RuntimeConversationState{Freshness: clientui.ConversationFreshnessEstablished}},
		PendingInput: runtimestate.RuntimePendingInputReduction{
			State: runtimestate.PendingInputState{
				Input:      "draft",
				Submission: runtimestate.InputSubmissionLocked,
			},
		},
		Reasoning: runtimestate.RuntimeReasoningReduction{State: runtimestate.RuntimeReasoningState{StatusHeader: "thinking"}},
	})

	if cmd == nil {
		t.Fatal("expected invalid lifecycle to return transient status timer command")
	}
	if msgs := collectCmdMessages(t, cmd); len(msgs) == 0 {
		t.Fatal("expected transient status command to produce a timer message")
	}
	if m.activity != uiActivityError {
		t.Fatalf("activity = %v, want error", m.activity)
	}
	if !strings.Contains(m.transientStatus, "invalid runtime lifecycle") {
		t.Fatalf("transient status = %q, want invalid lifecycle error", m.transientStatus)
	}
	if !m.isCompacting() || !m.isReviewerRunning() || !m.isReviewerBlocking() {
		t.Fatalf("expected non-run lifecycle side effects to apply, compacting=%t reviewer=%t blocking=%t", m.isCompacting(), m.isReviewerRunning(), m.isReviewerBlocking())
	}
	if m.conversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", m.conversationFreshness)
	}
	if !m.isInputSubmitLocked() {
		t.Fatal("expected pending input submission side effect to apply")
	}
	if m.reasoningStatusHeader != "thinking" {
		t.Fatalf("reasoning header = %q, want thinking", m.reasoningStatusHeader)
	}
}

type runtimeAdapterFakeClient struct {
	responses []llm.Response
	index     int
}

type refreshingRuntimeClient struct {
	runtimeControlFakeClient
	views       []clientui.RuntimeMainView
	transcripts []clientui.TranscriptPage
	errs        []error
	calls       int
}

type startupTranscriptRuntimeClient struct {
	runtimeControlFakeClient
	transcriptCalls int
	loadRequests    []clientui.TranscriptPageRequest
	view            clientui.RuntimeMainView
	page            clientui.TranscriptPage
	loadPage        clientui.TranscriptPage
}

func (c *startupTranscriptRuntimeClient) MainView() clientui.RuntimeMainView {
	if c.view.Session.SessionID == "" {
		c.view.Session.SessionID = "session-1"
	}
	return c.view
}

func (c *startupTranscriptRuntimeClient) Transcript() clientui.TranscriptPage {
	c.transcriptCalls++
	if c.page.SessionID == "" {
		c.page.SessionID = "session-1"
	}
	return c.page
}

func (c *startupTranscriptRuntimeClient) RefreshTranscript() (clientui.TranscriptPage, error) {
	page := c.page
	if page.SessionID == "" {
		page.SessionID = "session-1"
	}
	return page, nil
}

func (c *startupTranscriptRuntimeClient) LoadTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	c.loadRequests = append(c.loadRequests, req)
	page := c.page
	if c.loadPage.SessionID != "" || len(c.loadPage.Entries) > 0 {
		page = c.loadPage
	}
	if page.SessionID == "" {
		page.SessionID = "session-1"
	}
	return page, nil
}

func (c *startupTranscriptRuntimeClient) RefreshTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return c.LoadTranscriptPage(req)
}

func (f *refreshingRuntimeClient) MainView() clientui.RuntimeMainView {
	if f.calls == 0 {
		return clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}}
	}
	idx := f.calls - 1
	if idx >= len(f.views) {
		idx = len(f.views) - 1
	}
	if idx < 0 {
		return clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}}
	}
	return f.views[idx]
}

func (f *refreshingRuntimeClient) RefreshMainView() (clientui.RuntimeMainView, error) {
	idx := f.calls
	view := clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}}
	if idx < len(f.views) {
		view = f.views[idx]
	} else if len(f.views) > 0 {
		view = f.views[len(f.views)-1]
	}
	return view, nil
}

func (f *refreshingRuntimeClient) Transcript() clientui.TranscriptPage {
	if f.calls == 0 {
		return clientui.TranscriptPage{SessionID: "session-1"}
	}
	idx := f.calls - 1
	if idx >= len(f.transcripts) {
		idx = len(f.transcripts) - 1
	}
	if idx < 0 {
		return clientui.TranscriptPage{SessionID: "session-1"}
	}
	return f.transcripts[idx]
}

func (f *refreshingRuntimeClient) RefreshTranscript() (clientui.TranscriptPage, error) {
	idx := f.calls
	f.calls++
	page := clientui.TranscriptPage{SessionID: "session-1"}
	if idx < len(f.transcripts) {
		page = f.transcripts[idx]
	} else if len(f.transcripts) > 0 {
		page = f.transcripts[len(f.transcripts)-1]
	}
	if idx < len(f.errs) && f.errs[idx] != nil {
		return page, f.errs[idx]
	}
	return page, nil
}

func (f *refreshingRuntimeClient) LoadTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	_ = req
	return f.RefreshTranscript()
}

func (f *refreshingRuntimeClient) RefreshTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	return f.LoadTranscriptPage(req)
}

func (f *runtimeAdapterFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	if f.index >= len(f.responses) {
		return llm.Response{}, errors.New("no fake response configured")
	}
	resp := f.responses[f.index]
	f.index++
	return resp, nil
}

func (f *runtimeAdapterFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}, nil
}

func TestApplyChatSnapshotSetsOngoingFromSnapshot(t *testing.T) {
	m := newProjectedStaticUIModel()

	_ = m.runtimeAdapter().applyChatSnapshot(runtime.ChatSnapshot{Streaming: "hello"})

	if got := m.view.OngoingStreamingText(); got != "hello" {
		t.Fatalf("expected snapshot ongoing text, got %q", got)
	}
}

func TestDeveloperErrorFeedbackLocalEntryAppearsInOngoing(t *testing.T) {
	m := newProjectedStaticUIModel()

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind: clientui.EventLocalEntryAdded,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "developer_error_feedback",
			Text:       "Goal loop stopped: provider down",
			Visibility: clientui.EntryVisibilityAll,
		}},
	}).
		cmd

	ongoing := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(ongoing, "Goal loop stopped: provider down") {
		t.Fatalf("expected developer error feedback in ongoing scrollback, got %q", ongoing)
	}
}

func TestProjectRuntimeEventKeepsReviewerCompletedAsStatusOnlyEvent(t *testing.T) {
	evt := projectRuntimeEvent(runtime.Event{
		Kind: runtime.EventReviewerCompleted,
		Reviewer: &runtime.ReviewerStatus{
			Outcome:          "applied",
			SuggestionsCount: 2,
		},
	})

	if len(evt.TranscriptEntries) != 0 {
		t.Fatalf("expected reviewer_completed to avoid transcript entries, got %+v", evt.TranscriptEntries)
	}
}

func TestProjectRuntimeEventIncludesBackgroundSystemTranscriptEntry(t *testing.T) {
	evt := projectRuntimeEvent(runtime.Event{
		Kind: runtime.EventBackgroundUpdated,
		Background: &runtime.BackgroundShellEvent{
			Type:        "completed",
			ID:          "1000",
			State:       "completed",
			NoticeText:  "Background shell 1000 completed.\nOutput:\nhello",
			CompactText: "Background shell 1000 completed",
		},
	})

	if len(evt.TranscriptEntries) != 1 {
		t.Fatalf("expected one transcript entry, got %d", len(evt.TranscriptEntries))
	}
	entry := evt.TranscriptEntries[0]
	if entry.Role != "system" {
		t.Fatalf("background transcript role = %q, want system", entry.Role)
	}
	if !strings.Contains(entry.Text, "Background shell 1000 completed") {
		t.Fatalf("background transcript text = %q", entry.Text)
	}
	if entry.CondensedText != "Background shell 1000 completed" {
		t.Fatalf("background transcript ongoing = %q", entry.CondensedText)
	}
}

func TestRuntimeAdapterRunStartAppliesPendingInputBeforeActivityEffect(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.activity = uiActivityIdle

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)},
	}).
		cmd

	if m.activity != uiActivityRunning {
		t.Fatalf("activity = %v, want running", m.activity)
	}
	if !m.isBusy() {
		t.Fatal("expected busy state set")
	}
}

func TestRuntimeAdapterUserMessageFlushClearsDraft(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.conversationFreshness = clientui.ConversationFreshnessFresh
	m.input = "steered message"
	m.pendingInjected = queuedUserMessagesForTest("steered message", "follow-up")
	m.lockedInjectText = "steered message"
	m.lockedInjectID = "queue-test-0"
	m.setInputSubmitLocked(true)

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                         clientui.EventUserMessageFlushed,
		UserMessage:                  "steered message",
		UserMessageBatchQueueItemIDs: []string{"queue-test-0"},
	}).
		cmd

	if m.input != "" {
		t.Fatalf("input = %q, want cleared", m.input)
	}
	if m.isInputSubmitLocked() {
		t.Fatal("expected input submit lock cleared")
	}
	if m.lockedInjectText != "" {
		t.Fatalf("locked inject text = %q, want cleared", m.lockedInjectText)
	}
	if len(m.pendingInjected) != 1 || m.pendingInjected[0].Text != "follow-up" {
		t.Fatalf("pending injected = %+v, want follow-up only", m.pendingInjected)
	}
	if len(m.promptHistory) != 0 {
		t.Fatalf("prompt history = %+v, want no queued flush append", m.promptHistory)
	}
	if m.conversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", m.conversationFreshness)
	}
}

func TestRuntimeAdapterBackgroundUpdateRefreshesOpenProcessListAndShowsNotice(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIProcessClient(fixedUIProcessClient{
		entries: []clientui.BackgroundProcess{{ID: "proc-1", State: "completed"}},
	}))
	m.processList.open = true

	cmd := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind: clientui.EventBackgroundUpdated,
		Background: &clientui.BackgroundShellEvent{
			Type:        "completed",
			ID:          "proc-1",
			State:       "completed",
			CompactText: "Background shell proc-1 completed",
		},
	}).
		cmd

	msgs := collectCmdMessages(t, cmd)
	var refresh processListRefreshDoneMsg
	foundRefresh := false
	for _, msg := range msgs {
		if typed, ok := msg.(processListRefreshDoneMsg); ok {
			refresh = typed
			foundRefresh = true
			break
		}
	}
	if !foundRefresh {
		t.Fatalf("expected background update to schedule process refresh completion, got %+v", msgs)
	}
	next, _ := m.Update(refresh)
	m = next.(*uiModel)

	if len(m.processList.entries) != 1 || m.processList.entries[0].ID != "proc-1" {
		t.Fatalf("process entries = %+v, want refreshed proc-1", m.processList.entries)
	}
	if m.transientStatus != "Background shell proc-1 completed" {
		t.Fatalf("transient status = %q, want background notice", m.transientStatus)
	}
	if m.transientStatusKind != uiStatusNoticeSuccess {
		t.Fatalf("transient status kind = %d, want success", m.transientStatusKind)
	}
	if cmd == nil {
		t.Fatal("expected notice clear command")
	}
}

func TestRuntimeAdapterBackgroundUpdateCachesProcessStatusWithoutListRead(t *testing.T) {
	processes := &countingProcessClient{}
	m := newProjectedStaticUIModel(WithUIProcessClient(processes))

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind: clientui.EventBackgroundUpdated,
		Background: &clientui.BackgroundShellEvent{
			Type:    "started",
			ID:      "proc-1",
			State:   "running",
			Command: "sleep 1",
		},
	}).
		cmd

	if processes.listCalls != 0 {
		t.Fatalf("expected background status cache update not to list processes, got %d calls", processes.listCalls)
	}
	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "ps 1") {
		t.Fatalf("expected cached process count in status line, got %q", status)
	}
}

func TestOngoingReviewerEntriesAfterCommittedFinalKeepFinalVisibleWithoutHydration(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{SessionID: "session-1"}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 100})

	finalText := "final answer"
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: finalText,
	}).
		cmd
	if got := stripANSIPreserve(m.view.OngoingSnapshot()); !strings.Contains(got, finalText) {
		t.Fatalf("expected streaming final answer visible before commit, got %q", got)
	}

	events := []clientui.Event{
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         1,
			CommittedEntryStart:        0,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  finalText,
				Phase: string(llm.MessagePhaseFinal),
			}},
		},
		{
			Kind:                       clientui.EventLocalEntryAdded,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         2,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:          "reviewer_suggestions",
				Text:          "Supervisor suggested:\n1. Check final answer.",
				CondensedText: "Supervisor suggested:\n1. Check final answer.",
			}},
		},
		{
			Kind:                       clientui.EventLocalEntryAdded,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         3,
			CommittedEntryStart:        2,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "reviewer_status",
				Text: "Supervisor ran: 1 suggestion, no changes applied.",
			}},
		},
		{
			Kind:   clientui.EventReviewerCompleted,
			StepID: "step-1",
		},
	}

	for _, evt := range events {
		msgs := collectCmdMessages(t, m.runtimeAdapter().applyProjectedRuntimeEvent(evt).cmd)
		for _, msg := range msgs {
			if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
				t.Fatalf("did not expect reviewer sequence to trigger transcript hydration, event=%s msg=%+v", evt.Kind, msg)
			}
		}
		if m.runtimeTranscriptBusy {
			t.Fatalf("did not expect reviewer sequence to start transcript sync after event=%s", evt.Kind)
		}
	}

	view := stripANSIPreserve(m.view.OngoingSnapshot())
	if !containsInOrder(view, finalText, "Supervisor suggested:", "Supervisor ran: 1 suggestion") {
		t.Fatalf("expected final answer and reviewer entries visible immediately in ongoing mode, got %q", view)
	}
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("expected committed final to clear streaming text, got %q", got)
	}
}

func TestHandleProjectedRuntimeEventAppendsTranscriptEntriesImmediately(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:        runtime.EventUserMessageFlushed,
		StepID:      "step-1",
		UserMessage: "say hi",
	})).
		cmd

	callMeta := transcript.ToolCallMeta{ToolName: "shell", Command: "pwd", CompactText: "pwd", IsShell: true}
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: "step-1",
		ToolCall: &llm.ToolCall{
			ID:           "call-1",
			Name:         string(toolspec.ToolExecCommand),
			Presentation: transcript.EncodeToolCallMeta(callMeta),
		},
	})).
		cmd

	if pending := tui.PendingToolEntries(m.transcriptEntries); len(pending) != 1 {
		t.Fatalf("expected pending tool call visible immediately, got %d pending entries", len(pending))
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventToolCallCompleted,
		StepID: "step-1",
		ToolResult: &tools.Result{
			CallID: "call-1",
			Name:   toolspec.ToolExecCommand,
			Output: []byte("/tmp"),
		},
	})).
		cmd

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventAssistantMessage,
		StepID: "step-1",
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "**done**",
			Phase:   llm.MessagePhaseFinal,
		},
	})).
		cmd

	if len(m.transcriptEntries) != 4 {
		t.Fatalf("expected four transcript entries, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[0].Role; got != "user" {
		t.Fatalf("entry[0].Role = %q, want user", got)
	}
	if got := m.transcriptEntries[1].Role; got != "tool_call" {
		t.Fatalf("entry[1].Role = %q, want tool_call", got)
	}
	if got := m.transcriptEntries[1].Text; got != "pwd" {
		t.Fatalf("entry[1].Text = %q, want pwd", got)
	}
	if got := m.transcriptEntries[2].Role; got != "tool_result_ok" {
		t.Fatalf("entry[2].Role = %q, want tool_result_ok", got)
	}
	if got := m.transcriptEntries[2].Text; !strings.Contains(got, "/tmp") {
		t.Fatalf("entry[2].Text = %q, want tool output", got)
	}
	if got := m.transcriptEntries[3].Role; got != "assistant" {
		t.Fatalf("entry[3].Role = %q, want assistant", got)
	}
	if got := m.transcriptEntries[3].Text; got != "**done**" {
		t.Fatalf("entry[3].Text = %q, want final assistant text", got)
	}
	if pending := tui.PendingToolEntries(m.transcriptEntries); len(pending) != 0 {
		t.Fatalf("expected pending tool call cleared after result, got %d pending entries", len(pending))
	}
	if loaded := m.view.LoadedTranscriptEntries(); len(loaded) != 4 {
		t.Fatalf("view loaded transcript length = %d, want 4", len(loaded))
	}
}

func TestHandleProjectedRuntimeEventAppendsCompactionCacheWarningTranscriptEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:   runtime.EventCacheWarning,
		StepID: "step-1",
		CacheWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonCompaction,
		},
	})).
		cmd

	if len(m.transcriptEntries) != 1 {
		t.Fatalf("expected one transcript entry, got %d", len(m.transcriptEntries))
	}
	entry := m.transcriptEntries[0]
	if entry.Role != "cache_warning" {
		t.Fatalf("entry.Role = %q, want cache_warning", entry.Role)
	}
	expectedText := transcript.CacheWarningText(transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonCompaction})
	if entry.Text != expectedText {
		t.Fatalf("entry.Text = %q, want compaction cache warning", entry.Text)
	}
	if loaded := m.view.LoadedTranscriptEntries(); len(loaded) != 1 {
		t.Fatalf("view loaded transcript length = %d, want 1", len(loaded))
	} else if loaded[0].Role != "cache_warning" || loaded[0].Text != expectedText {
		t.Fatalf("loaded[0] = %+v, want live compaction cache warning", loaded[0])
	}
}

func TestHandleProjectedRuntimeEventKeepsDefaultCacheWarningOutOfOngoingMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 80})

	warning := transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix}
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:                   runtime.EventCacheWarning,
		StepID:                 "step-1",
		CacheWarningVisibility: transcript.EntryVisibilityVerbose,
		CacheWarning:           &warning,
	})).
		cmd

	ongoing := stripANSIPreserve(m.view.OngoingSnapshot())
	if strings.Contains(ongoing, transcript.CacheWarningText(warning)) {
		t.Fatalf("expected default cache warning hidden in ongoing mode, got %q", ongoing)
	}

	detail := updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	detailView := stripANSIPreserve(detail.view.View())
	if !strings.Contains(detailView, transcript.CacheWarningText(warning)) {
		t.Fatalf("expected default cache warning visible in detail mode, got %q", detailView)
	}
}

func TestHandleProjectedRuntimeEventSkipsAlreadyHydratedAssistantEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "same", Phase: llm.MessagePhaseFinal}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(projectRuntimeEvent(runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         10,
		CommittedEntryCount:        1,
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: "same",
			Phase:   llm.MessagePhaseFinal,
		},
	})).
		cmd

	if len(m.transcriptEntries) != 1 {
		t.Fatalf("expected duplicate hydrated assistant entry to be skipped, got %+v", m.transcriptEntries)
	}
}

func TestHandleProjectedRuntimeEventHydratesCommittedRangeThatStartsBeforeCurrentWindow(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "visible-a", Phase: llm.MessagePhaseFinal},
		{Role: "reviewer_status", Text: "visible-b"},
	}
	m.transcriptBaseOffset = 5
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.forwardToView(tui.SetConversationMsg{BaseOffset: m.transcriptBaseOffset, TotalEntries: m.transcriptTotalEntries, Entries: m.transcriptEntries})

	cmd, mutated, needsHydration, _ := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         12,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "user", Text: "hidden-prefix"},
			{Role: "assistant", Text: "visible-a", Phase: string(llm.MessagePhaseFinal)},
			{Role: "reviewer_status", Text: "visible-b"},
		},
	})

	if cmd == nil {
		t.Fatal("expected hydration command for partial committed range")
	}
	if mutated {
		t.Fatalf("expected no transcript mutation, got %+v", m.transcriptEntries)
	}
	if !needsHydration {
		t.Fatal("expected partial committed range to require hydration")
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
}

func TestHandleProjectedRuntimeEventHydratesCommittedSuffixRangeThatStartsBeforeCurrentWindow(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "visible-a", Phase: llm.MessagePhaseFinal},
		{Role: "reviewer_status", Text: "visible-b"},
	}
	m.transcriptBaseOffset = 5
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.forwardToView(tui.SetConversationMsg{BaseOffset: m.transcriptBaseOffset, TotalEntries: m.transcriptTotalEntries, Entries: m.transcriptEntries})

	cmd, mutated, needsHydration, _ := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         12,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "user", Text: "hidden-prefix"},
			{Role: "assistant", Text: "visible-a", Phase: string(llm.MessagePhaseFinal)},
			{Role: "reviewer_status", Text: "visible-b"},
			{Role: "cache_warning", Text: "new-visible-suffix"},
		},
	})

	if cmd == nil {
		t.Fatal("expected hydration command for partial committed suffix range")
	}
	if mutated {
		t.Fatalf("expected no transcript mutation, got %+v", m.transcriptEntries)
	}
	if !needsHydration {
		t.Fatal("expected partial committed suffix range to require hydration")
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
}

func TestApplyProjectedTranscriptEntriesForwardsCompactMetadataToLiveView(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.forwardToView(tui.SetConversationMsg{Entries: []tui.TranscriptEntry{{Role: "assistant", Text: "seed"}}})
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed"}}
	entry := clientui.ChatEntry{
		Role:              "warning",
		Text:              "long warning body",
		MessageType:       string(llm.MessageTypeCompactionSoonReminder),
		SourcePath:        "  testdata/compact-source.md  ",
		CompactLabel:      "Compaction reminder",
		ToolResultSummary: "summary text",
		ToolCallID:        " call-1 ",
	}

	cmd, mutated, needsHydration, _ := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{entry},
	})

	if cmd != nil || !mutated || needsHydration {
		t.Fatalf("expected direct metadata append, mutated=%t needsHydration=%t cmd=%v", mutated, needsHydration, cmd)
	}
	viewEntries := m.view.LoadedTranscriptEntries()
	if got, want := len(viewEntries), 2; got != want {
		t.Fatalf("view transcript entry count = %d, want %d", got, want)
	}
	got := viewEntries[1]
	if got.MessageType != llm.MessageTypeCompactionSoonReminder || got.SourcePath != "testdata/compact-source.md" || got.CompactLabel != "Compaction reminder" || got.ToolResultSummary != "summary text" || got.ToolCallID != "call-1" {
		t.Fatalf("expected live view append to preserve compact metadata, got %+v", got)
	}
}

func TestAppendTranscriptMsgFromEntryPreservesTransientCompactMetadata(t *testing.T) {
	entry := transcriptEntryFromProjectedChatEntry(clientui.ChatEntry{
		Visibility:        transcript.EntryVisibilityVerbose,
		Role:              "warning",
		Text:              "transient warning body",
		CondensedText:     "transient warning",
		Phase:             string(llm.MessagePhaseFinal),
		MessageType:       string(llm.MessageTypeCompactionSoonReminder),
		SourcePath:        "  testdata/compact-source.md  ",
		CompactLabel:      "Compaction reminder",
		ToolResultSummary: "summary text",
		ToolCallID:        " call-1 ",
	}, true, false)

	got := appendTranscriptMsgFromEntry(entry)
	if !got.Transient || got.Committed || got.Visibility != transcript.EntryVisibilityVerbose || got.Role != "warning" || got.CondensedText != "transient warning" || got.Phase != llm.MessagePhaseFinal {
		t.Fatalf("expected transient append state preserved, got %+v", got)
	}
	if got.MessageType != llm.MessageTypeCompactionSoonReminder || got.SourcePath != "testdata/compact-source.md" || got.CompactLabel != "Compaction reminder" || got.ToolResultSummary != "summary text" || got.ToolCallID != "call-1" {
		t.Fatalf("expected transient append to preserve compact metadata, got %+v", got)
	}
}

func TestSkippedCommittedEventBeforeCurrentWindowStillAdvancesRevisionAndCount(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "visible-a", Phase: llm.MessagePhaseFinal},
		{Role: "reviewer_status", Text: "visible-b"},
	}
	m.transcriptBaseOffset = 5
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.forwardToView(tui.SetConversationMsg{BaseOffset: m.transcriptBaseOffset, TotalEntries: m.transcriptTotalEntries, Entries: m.transcriptEntries})

	cmd, mutated, needsHydration, _ := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         13,
		CommittedEntryCount:        8,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "cache_warning",
			Text: "hidden-prefix-only",
		}},
	})

	if cmd != nil {
		t.Fatalf("expected no hydrate/append command, got %v", cmd)
	}
	if mutated {
		t.Fatalf("expected no transcript mutation, got %+v", m.transcriptEntries)
	}
	if needsHydration {
		t.Fatal("expected hidden committed event to avoid hydration")
	}
	if got := m.transcriptRevision; got != 13 {
		t.Fatalf("transcript revision = %d, want 13", got)
	}
	if got := m.transcriptTotalEntries; got != 8 {
		t.Fatalf("transcript total entries = %d, want 8", got)
	}
}

func TestSkippedCommittedEventBeforeCurrentWindowDoesNotTriggerFollowUpConversationHydrate(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{SessionID: "session-1"}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "visible-a", Phase: llm.MessagePhaseFinal},
		{Role: "reviewer_status", Text: "visible-b"},
	}
	m.transcriptBaseOffset = 5
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.forwardToView(tui.SetConversationMsg{BaseOffset: m.transcriptBaseOffset, TotalEntries: m.transcriptTotalEntries, Entries: m.transcriptEntries})

	cmd, mutated, needsHydration, _ := m.runtimeAdapter().applyProjectedTranscriptEntries(clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         13,
		CommittedEntryCount:        8,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "cache_warning",
			Text: "hidden-prefix-only",
		}},
	})

	if cmd != nil || mutated || needsHydration {
		t.Fatalf("hidden committed skip = (cmd=%v mutated=%t needsHydration=%t), want no-op", cmd, mutated, needsHydration)
	}

	followUp := m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryCount:        8,
	}).
		cmd
	for _, msg := range collectCmdMessages(t, followUp) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect matching committed conversation_updated after hidden skip to trigger hydration, got %+v", msg)
		}
	}
	if m.runtimeTranscriptBusy {
		t.Fatal("did not expect runtime transcript sync to start after hidden committed skip")
	}
}

func TestHandleProjectedRuntimeEventDoesNotRepairCoveredAssistantEntryLocally(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseCommentary},
		{Role: "assistant", Text: "stale", Phase: llm.MessagePhaseFinal},
	}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 2
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryCount:        2,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "fresh",
			Phase: string(llm.MessagePhaseFinal),
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected covered assistant range not to mutate local transcript, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].Text; got != "stale" {
		t.Fatalf("assistant entry text = %q, want stale before hydration", got)
	}
	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) != 2 || loaded[1].Text != "stale" {
		t.Fatalf("expected covered assistant unchanged in view, got %+v", loaded)
	}
}

func TestHandleProjectedRuntimeEventDoesNotRepairCoveredAssistantEntryOrAppendTrailingToolCall(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "assistant", Text: "stale", Phase: llm.MessagePhaseFinal},
	}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 2
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryCount:        3,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: "fresh", Phase: string(llm.MessagePhaseFinal)},
			{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
		},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected covered range not to mutate local transcript, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].Text; got != "stale" {
		t.Fatalf("assistant entry text = %q, want stale before hydration", got)
	}
	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) != 2 || loaded[1].Text != "stale" {
		t.Fatalf("expected covered range unchanged in view, got %+v", loaded)
	}
}

func TestHandleProjectedRuntimeEventDoesNotSuppressPendingToolCallStart(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed", Phase: llm.MessagePhaseCommentary}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		CommittedTranscriptChanged: true,
		StepID:                     "step-1",
		TranscriptRevision:         10,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-1",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
		}},
	}).
		cmd

	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("expected pending tool call appended immediately, got %+v", m.transcriptEntries)
	}
	if got := m.transcriptEntries[1].Role; got != "tool_call" {
		t.Fatalf("second transcript role = %q, want tool_call", got)
	}
}
