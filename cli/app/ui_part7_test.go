package app

import (
	"context"
	"core/cli/app/commands"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHydrationCompletionKeepsDeferredQueuedDrainArmedUntilUnrelatedBusyStateClears(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID:    "session-1",
			TotalEntries: 1,
			Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
		}},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("follow up")
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7

	next, _ := m.Update(submitDoneMsg{message: "ignored by runtime-backed flow"})
	updated := next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued drain deferred until hydration completes")
	}

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}})
	updated = next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected unrelated runtime activity to mark UI busy before hydration completes")
	}

	next, refreshCmd := updated.Update(runtimeTranscriptRefreshedMsg{token: 7, transcript: client.transcripts[0]})
	updated = next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued drain to remain armed while dirty follow-up hydration is still required")
	}
	if updated.queuedDrainReadyAfterHydration {
		t.Fatal("did not expect queued drain marked ready before the final hydration completes")
	}
	if refreshCmd == nil {
		t.Fatal("expected dirty follow-up hydration after in-flight refresh completes")
	}
	followRefresh, ok := refreshCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected follow-up hydration message, got %T", refreshCmd())
	}

	next, _ = updated.Update(followRefresh)
	updated = next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued drain to remain armed when hydration completes during unrelated busy state")
	}
	if updated.queuedDrainReadyAfterHydration == false {
		t.Fatal("expected hydration completion to mark queued drain ready even when unrelated busy state blocks auto-drain")
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "follow up" {
		t.Fatalf("expected queued follow-up preserved while unrelated busy state blocks auto-drain, got %+v", updated.queued)
	}

	next, idleCmd := updated.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()}}})
	updated = next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected queued follow-up to start once unrelated busy state clears")
	}
	if updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected deferred queued drain cleared once unrelated busy state clears and auto-drain starts")
	}
	if updated.activeSubmit.text != "follow up" {
		t.Fatalf("expected queued follow-up to submit after unrelated busy state clears, got %q", updated.activeSubmit.text)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected active queued follow-up removed from visible queue after submit starts, got %+v", updated.queued)
	}
	if idleCmd == nil {
		t.Fatal("expected queued follow-up command sequence after unrelated busy state clears")
	}
	_ = collectCmdMessages(t, idleCmd)
	if client.submitText != "follow up" {
		t.Fatalf("expected queued follow-up submit after unrelated busy state clears, got %q", client.submitText)
	}
}

func TestBusyQueuedUnknownSlashDrainsAsPromptSubmission(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/nope queued"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/nope queued" {
		t.Fatalf("expected unknown slash text queued verbatim, got %+v", updated.queued)
	}
	if updated.sessionName != "" {
		t.Fatalf("did not expect unknown slash queue-submit to execute as command, got session name %q", updated.sessionName)
	}

	next, cmd := updated.Update(submitDoneMsg{})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued unknown slash to start submission after turn completion")
	}
	if !updated.isBusy() {
		t.Fatal("expected queued unknown slash to start prompt submission during drain")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued unknown slash drained, got %+v", updated.queued)
	}
	if updated.sessionName != "" {
		t.Fatalf("did not expect unknown slash drain to route through slash command execution, got session name %q", updated.sessionName)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if !strings.Contains(plain, "/nope queued") {
		t.Fatalf("expected queued unknown slash in transcript as prompt text, got %q", plain)
	}
}

func TestAutoDrainStopsAfterQueuedPSInlineAppendsToInput(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'queued-inline\n'; sleep 1"},
		DisplayCommand: "queued-inline",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start queued-inline: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("/ps inline "+res.SessionID, "summarize this")

	next, cmd := m.Update(submitDoneMsg{})
	updated := next.(*uiModel)

	if cmd == nil {
		t.Fatal("expected command batch from queued /ps inline execution")
	}
	if updated.isBusy() {
		t.Fatal("did not expect queued /ps inline to auto-submit the follow-up prompt")
	}
	if !updated.processList.actionInFlight {
		t.Fatal("expected queued /ps inline action to remain in flight")
	}
	updated = applyProcessActionCommandForTest(t, updated, cmd)
	if !strings.Contains(updated.input, "Output of bg shell "+res.SessionID+":") {
		t.Fatalf("expected inline shell transcript pasted into input, got %q", updated.input)
	}
	if !strings.Contains(updated.input, "queued-inline") {
		t.Fatalf("expected pasted shell transcript content in input, got %q", updated.input)
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "summarize this" {
		t.Fatalf("expected follow-up prompt to remain queued after inline paste, got %+v", updated.queued)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if strings.Contains(plain, "summarize this") {
		t.Fatalf("did not expect follow-up prompt submitted without pasted transcript, got %q", plain)
	}
}

func TestAutoDrainResumesAfterQueuedPSNonMutatingActions(t *testing.T) {
	for _, action := range []string{"kill", "logs"} {
		t.Run(action, func(t *testing.T) {
			manager := newFastBackgroundTestManager(t)
			workdir := t.TempDir()
			res, err := manager.Start(context.Background(), shelltool.ExecRequest{
				Command:        []string{"sh", "-c", "printf 'queued-action\n'; sleep 1"},
				DisplayCommand: "queued-action",
				Workdir:        workdir,
				YieldTime:      fastBackgroundTestYield,
			})
			if err != nil {
				t.Fatalf("start queued-action: %v", err)
			}
			if !res.Backgrounded {
				t.Fatal("expected background process")
			}
			originalOpenDefault := openDefault
			openDefault = func(string) error { return nil }
			defer func() { openDefault = originalOpenDefault }()

			m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
			m.setBusy(true)
			m.activity = uiActivityRunning
			m.queued = queuedInputsForTest("/ps "+action+" "+res.SessionID, "summarize this")

			next, cmd := m.Update(submitDoneMsg{})
			updated := next.(*uiModel)
			if cmd == nil {
				t.Fatal("expected command batch from queued /ps action execution")
			}
			if !updated.processList.actionInFlight {
				t.Fatal("expected queued /ps action to remain in flight")
			}

			updated = applyProcessActionCommandForTest(t, updated, cmd)
			if !updated.isBusy() {
				t.Fatal("expected follow-up prompt to start after non-mutating /ps action")
			}
			if updated.activeSubmit.text != "summarize this" {
				t.Fatalf("expected follow-up prompt active, got %q", updated.activeSubmit.text)
			}
			if len(updated.queued) != 0 {
				t.Fatalf("expected follow-up queue drained, got %+v", updated.queued)
			}
		})
	}
}

type failingQueuedProcessClient struct {
	err error
}

func (c failingQueuedProcessClient) ListProcesses(context.Context) ([]clientui.BackgroundProcess, error) {
	return nil, c.err
}

func (c failingQueuedProcessClient) KillProcess(context.Context, string) error {
	return c.err
}

func (c failingQueuedProcessClient) InlineOutput(context.Context, string, int) (string, string, error) {
	return "", "", c.err
}

func TestAutoDrainResumesAfterQueuedPSNonMutatingActionError(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIProcessClient(failingQueuedProcessClient{err: errors.New("kill failed")}))
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("/ps kill proc-1", "summarize this")

	next, cmd := m.Update(submitDoneMsg{})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected command batch from queued /ps action execution")
	}
	if !updated.processList.actionInFlight {
		t.Fatal("expected queued /ps action to remain in flight")
	}

	updated = applyProcessActionCommandForTest(t, updated, cmd)
	if !strings.Contains(updated.transientStatus, "kill failed") {
		t.Fatalf("expected process error status, got %q", updated.transientStatus)
	}
	if !updated.isBusy() {
		t.Fatal("expected follow-up prompt to start after non-mutating /ps action error")
	}
	if updated.activeSubmit.text != "summarize this" {
		t.Fatalf("expected follow-up prompt active, got %q", updated.activeSubmit.text)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected follow-up queue drained, got %+v", updated.queued)
	}
}

func TestAutoDrainResumesAfterQueuedPSInlineActionError(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIProcessClient(failingQueuedProcessClient{err: errors.New("inline failed")}))
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("/ps inline proc-1", "summarize this")

	next, cmd := m.Update(submitDoneMsg{})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected command batch from queued /ps inline execution")
	}
	if !updated.processList.actionInFlight {
		t.Fatal("expected queued /ps inline action to remain in flight")
	}

	updated = applyProcessActionCommandForTest(t, updated, cmd)
	if !strings.Contains(updated.transientStatus, "inline failed") {
		t.Fatalf("expected process error status, got %q", updated.transientStatus)
	}
	if !updated.isBusy() {
		t.Fatal("expected follow-up prompt to start after inline action error")
	}
	if updated.activeSubmit.text != "summarize this" {
		t.Fatalf("expected follow-up prompt active, got %q", updated.activeSubmit.text)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected follow-up queue drained, got %+v", updated.queued)
	}
}

func TestAutoDrainResumesAfterDiscardedPSInlinePaste(t *testing.T) {
	client := &countingProcessActionClient{}
	m := newProjectedStaticUIModel(WithUIProcessClient(client))
	m.queued = queuedInputsForTest("summarize this")
	cmd := m.processActionCmd("inline", "proc-1", "", m.mainInputDraftToken)
	if cmd == nil {
		t.Fatal("expected inline action command")
	}
	if !m.processList.actionInFlight {
		t.Fatal("expected inline action in flight")
	}
	m.replaceMainInput("edited draft", -1)

	var done processActionDoneMsg
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(processActionDoneMsg); ok {
			done = typed
		}
	}
	next, _ := m.Update(done)
	updated := next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected queued prompt to start after stale inline paste is discarded")
	}
	if updated.activeSubmit.text != "summarize this" {
		t.Fatalf("expected queued prompt active, got %q", updated.activeSubmit.text)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queue drained after stale inline paste, got %+v", updated.queued)
	}
}

type countingProcessActionClient struct {
	killCalls int
}

func (c *countingProcessActionClient) ListProcesses(context.Context) ([]clientui.BackgroundProcess, error) {
	return []clientui.BackgroundProcess{{ID: "proc-1", LogPath: "/tmp/process.log"}}, nil
}

func (c *countingProcessActionClient) KillProcess(context.Context, string) error {
	c.killCalls++
	return nil
}

func (c *countingProcessActionClient) InlineOutput(context.Context, string, int) (string, string, error) {
	return "output", "", nil
}

func TestPSActionsAreSerializedWhileInFlight(t *testing.T) {
	client := &countingProcessActionClient{}
	m := newProjectedStaticUIModel(WithUIProcessClient(client))
	m.processList.entries = []clientui.BackgroundProcess{{ID: "proc-1", LogPath: "/tmp/process.log"}}

	_, firstCmd := m.inputController().runProcessAction("kill", "proc-1")
	if firstCmd == nil {
		t.Fatal("expected first process action command")
	}
	_, secondCmd := m.inputController().runProcessAction("kill", "proc-1")
	if secondCmd == nil {
		t.Fatal("expected second process action to produce status feedback")
	}
	_ = collectCmdMessages(t, secondCmd)
	if client.killCalls != 0 {
		t.Fatalf("second action should not start an RPC while first is in flight, got %d calls", client.killCalls)
	}
	_ = applyProcessActionCommandForTest(t, m, firstCmd)
	if client.killCalls != 1 {
		t.Fatalf("expected exactly one process action RPC, got %d", client.killCalls)
	}
}

func TestBusyQueuedReviewSlashCommandStartsFreshSessionAfterTurn(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/review cli/app"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/review cli/app" {
		t.Fatalf("expected queued /review command, got %+v", updated.queued)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected quit cmd for queued /review handoff")
	}
	if updated.exitAction != UIActionNewSession {
		t.Fatalf("expected UIActionNewSession, got %q", updated.exitAction)
	}
	if strings.TrimSpace(updated.nextSessionInitialPrompt) == "" {
		t.Fatal("expected queued /review to populate the next-session prompt")
	}
	if !strings.Contains(updated.nextSessionInitialPrompt, "cli/app") {
		t.Fatalf("expected queued /review args in handoff payload, got %q", updated.nextSessionInitialPrompt)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued /review drained, got %+v", updated.queued)
	}
}

func TestBusyQueuedReviewSlashCommandWaitsForHydrationBeforePromptSubmission(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID:    "session-1",
			TotalEntries: 1,
			Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
		}},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/review cli/app"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/review cli/app" {
		t.Fatalf("expected queued /review command, got %+v", updated.queued)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued /review drain deferred until transcript hydration completes")
	}
	if updated.isBusy() {
		t.Fatal("did not expect queued /review prompt submission before hydration settles")
	}
	if updated.activeSubmit.text != "" {
		t.Fatalf("did not expect queued /review to submit before hydration, got %q", updated.activeSubmit.text)
	}
	if client.submitText != "" {
		t.Fatalf("did not expect queued /review submit before hydration, got %q", client.submitText)
	}
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	refreshFound := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			refreshFound = true
		}
	}
	if !refreshFound {
		t.Fatalf("expected runtime transcript refresh before queued /review drains, got %+v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseQueuedDrain {
		t.Fatalf("queued /review sync cause = %q, want %q", refresh.syncCause, runtimeTranscriptSyncCauseQueuedDrain)
	}

	next, drainCmd := updated.Update(refresh)
	updated = next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected queued /review to start prompt submission after hydration completes")
	}
	if updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued /review hydration deferral cleared once drained")
	}
	if updated.activeSubmit.text == "" {
		t.Fatal("expected queued /review generated prompt to submit")
	}
	if strings.Contains(updated.activeSubmit.text, "/review cli/app") {
		t.Fatalf("expected queued /review to submit generated prompt content, got %q", updated.activeSubmit.text)
	}
	if got := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); !strings.Contains(got, "final answer") {
		t.Fatalf("expected hydration to commit final answer before queued /review drains, got %q", got)
	}
	if drainCmd == nil {
		t.Fatal("expected queued /review submit command after hydration")
	}
	_ = collectCmdMessages(t, drainCmd)
	if client.submitText == "" {
		t.Fatal("expected queued /review submit after hydration")
	}
}

func TestQueuedReviewUsesEngineConversationFreshnessWhenUIDidNotReceiveRuntimeUpdateYet(t *testing.T) {
	store, eng := newAppRuntimeEngine(t, &runtimeAdapterFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/review cli/app"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/review cli/app" {
		t.Fatalf("expected queued /review command, got %+v", updated.queued)
	}
	if updated.conversationFreshness != clientui.ConversationFreshnessFresh {
		t.Fatalf("expected UI freshness to remain fresh before runtime sync, got %v", updated.conversationFreshness)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "first prompt"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if got := eng.ConversationFreshness(); got != session.ConversationFreshnessEstablished {
		t.Fatalf("expected engine freshness established after first prompt, got %v", got)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected hydration command for queued /review handoff")
	}
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued /review handoff deferred until hydration completes")
	}
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	refreshFound := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			refreshFound = true
		}
	}
	if !refreshFound {
		t.Fatalf("expected transcript refresh before queued /review handoff, got %+v", msgs)
	}
	if refresh.syncCause != runtimeTranscriptSyncCauseQueuedDrain {
		t.Fatalf("queued /review handoff sync cause = %q, want %q", refresh.syncCause, runtimeTranscriptSyncCauseQueuedDrain)
	}

	next, followCmd := updated.Update(refresh)
	updated = next.(*uiModel)
	if followCmd == nil {
		t.Fatal("expected queued /review drain after hydration")
	}
	if updated.exitAction != UIActionNewSession {
		t.Fatalf("expected UIActionNewSession, got %q", updated.exitAction)
	}
	if strings.TrimSpace(updated.nextSessionInitialPrompt) == "" {
		t.Fatal("expected queued /review to populate the next-session prompt")
	}
	if updated.conversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("expected UI freshness synced from engine during drain, got %v", updated.conversationFreshness)
	}
}

func TestBackSlashCommandCopiesLatestAssistantOutputWhenAvailable(t *testing.T) {
	tests := []struct {
		name       string
		activity   uiActivity
		transcript []llm.Message
		localEntry string
		ongoing    string
		want       string
	}{
		{name: "idle committed final assistant reply", activity: uiActivityIdle, transcript: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}}, want: "test"},
		{name: "interrupted streaming reply ignored", activity: uiActivityInterrupted, ongoing: "review findings", want: ""},
		{name: "last committed entry must be assistant final", activity: uiActivityIdle, transcript: []llm.Message{{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal}, {Role: llm.RoleTool, Name: "shell", ToolCallID: "call-1", Content: `{"ok":true}`}}, want: ""},
		{name: "commentary assistant reply ignored", activity: uiActivityIdle, transcript: []llm.Message{{Role: llm.RoleAssistant, Content: "commentary", Phase: llm.MessagePhaseCommentary}}, want: ""},
		{name: "local entry after final assistant does not block copy", activity: uiActivityIdle, transcript: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}}, localEntry: "Supervisor ran: ok", want: "test"},
		{name: "no assistant response", activity: uiActivityIdle, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := createAppRuntimeSession(t)
			if err := store.SetParentSessionID("parent-1"); err != nil {
				t.Fatalf("set parent session id: %v", err)
			}
			for idx, message := range tt.transcript {
				if _, _, err := store.AppendEvent("step-1", "message", message); err != nil {
					t.Fatalf("append transcript message %d: %v", idx, err)
				}
			}
			eng := newAppRuntimeEngineWithStore(t, store, &runtimeAdapterFakeClient{}, runtime.Config{})
			if tt.localEntry != "" {
				eng.AppendCommittedEntry("reviewer_status", tt.localEntry)
			}

			m := newProjectedEngineUIModel(eng)
			m.activity = tt.activity
			m.forwardToView(tui.SetConversationMsg{Ongoing: tt.ongoing})
			m.input = "/back"

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if cmd == nil {
				t.Fatal("expected quit cmd for /back teleport")
			}
			if updated.exitAction != UIActionOpenSession {
				t.Fatalf("expected UIActionOpenSession, got %q", updated.exitAction)
			}
			if updated.nextSessionID != "parent-1" {
				t.Fatalf("expected parent target session, got %q", updated.nextSessionID)
			}
			if updated.nextSessionInitialInput != tt.want {
				t.Fatalf("expected copied input %q, got %q", tt.want, updated.nextSessionInitialInput)
			}
		})
	}
}

func TestBackSlashCommandIgnoresRestoredPromptHistoryDraftInChildSession(t *testing.T) {
	store := createAppRuntimeSession(t)
	if err := store.SetParentSessionID("parent-1"); err != nil {
		t.Fatalf("set parent session id: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "latest reply", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant reply: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, store, &runtimeAdapterFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.promptHistory = []string{"/back"}
	m.promptHistoryDraft = "parked child draft"
	m.promptHistoryDraftCursor = -1
	m.promptHistorySelection = 0
	m.input = "/back"
	m.inputCursor = -1

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected quit cmd for /back teleport")
	}
	if updated.exitAction != UIActionOpenSession {
		t.Fatalf("expected UIActionOpenSession, got %q", updated.exitAction)
	}
	if updated.input != "parked child draft" {
		t.Fatalf("expected parked child draft restored locally, got %q", updated.input)
	}
	if updated.nextSessionInitialInput != "latest reply" {
		t.Fatalf("expected /back to still copy assistant reply, got %q", updated.nextSessionInitialInput)
	}
}

func TestUnknownSlashCommandIsSubmittedAsPrompt(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/nope"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected submission to start for unknown slash command")
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if !strings.Contains(plain, "/nope") {
		t.Fatalf("expected unknown slash command in user transcript, got %q", plain)
	}
}

func TestFileSlashCommandSubmitsInjectedUserPrompt(t *testing.T) {
	r := commands.NewRegistry()
	r.RegisterWithOptions("prompt:review", "", commands.RegisterOptions{PreservePromptHistoryDraft: true}, func(string) commands.Result {
		return commands.Result{Handled: true, SubmitUser: true, User: "# review\nexact body\n"}
	})
	m := newProjectedStaticUIModel(
		WithUICommandRegistry(r),
	)
	m.input = "/prompt:review"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected submission to start for file slash command")
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if strings.Contains(plain, "/prompt:review") {
		t.Fatalf("expected command text to be replaced by file prompt content, got %q", plain)
	}
	if !strings.Contains(plain, "review") || !strings.Contains(plain, "exact body") {
		t.Fatalf("expected file prompt content in transcript, got %q", plain)
	}
}

func TestBuiltInReviewSlashCommandSubmitsInjectedUserPrompt(t *testing.T) {
	r := commands.NewDefaultRegistry()
	m := newProjectedStaticUIModel(
		WithUICommandRegistry(r),
	)
	m.input = "/review cli/app"
	if got := r.Execute("/review cli/app"); !got.Handled || !got.SubmitUser {
		t.Fatalf("expected /review command to submit injected user prompt, got %+v", got)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected submission cmd for /review")
	}
	if updated.exitAction != UIActionNone {
		t.Fatalf("expected no session transition for empty-session /review, got %q", updated.exitAction)
	}
	if !updated.isBusy() {
		t.Fatal("expected /review to submit in place for an empty session")
	}
	if updated.nextSessionInitialPrompt != "" {
		t.Fatalf("expected no handoff payload for empty-session /review, got %q", updated.nextSessionInitialPrompt)
	}
}

func TestBuiltInInitSlashCommandSubmitsInjectedUserPrompt(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/init starter repo"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected submission cmd for /init")
	}
	if updated.exitAction != UIActionNone {
		t.Fatalf("expected no session transition for empty-session /init, got %q", updated.exitAction)
	}
	if !updated.isBusy() {
		t.Fatal("expected /init to submit in place for an empty session")
	}
	if updated.nextSessionInitialPrompt != "" {
		t.Fatalf("expected no handoff payload for empty-session /init, got %q", updated.nextSessionInitialPrompt)
	}
}

func TestBuiltInReviewSlashCommandStartsFreshSessionWhenCurrentSessionHasVisibleUserPrompt(t *testing.T) {
	r := commands.NewDefaultRegistry()
	m := newProjectedStaticUIModel(
		WithUICommandRegistry(r),
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	m.input = "/review cli/app"
	expected := r.Execute("/review cli/app")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected quit cmd for non-empty-session /review handoff")
	}
	if updated.exitAction != UIActionNewSession {
		t.Fatalf("expected UIActionNewSession, got %q", updated.exitAction)
	}
	if updated.nextSessionInitialPrompt != expected.User {
		t.Fatalf("expected handoff payload to match /review command output\nwant: %q\n got: %q", expected.User, updated.nextSessionInitialPrompt)
	}
	if !updated.nextSessionInitialPromptHistoryRecorded {
		t.Fatal("expected /review handoff to preserve typed-command prompt-history recording")
	}
}

func TestBuiltInInitSlashCommandStartsFreshSessionWhenCurrentSessionHasVisibleUserPrompt(t *testing.T) {
	r := commands.NewDefaultRegistry()
	m := newProjectedStaticUIModel(
		WithUICommandRegistry(r),
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	m.input = "/init starter repo"
	expected := r.Execute("/init starter repo")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected quit cmd for non-empty-session /init handoff")
	}
	if updated.exitAction != UIActionNewSession {
		t.Fatalf("expected UIActionNewSession, got %q", updated.exitAction)
	}
	if updated.nextSessionInitialPrompt != expected.User {
		t.Fatalf("expected handoff payload to match /init command output\nwant: %q\n got: %q", expected.User, updated.nextSessionInitialPrompt)
	}
	if !updated.nextSessionInitialPromptHistoryRecorded {
		t.Fatal("expected /init handoff to preserve typed-command prompt-history recording")
	}
}

func TestStartupSubmitUsesRecordedPromptHistoryFlag(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(),
		WithUIStartupSubmit("expanded hidden prompt"),
		WithUIStartupSubmitPromptHistoryRecorded(true),
	)

	_ = collectCmdMessages(t, m.startupSubmitCmd())

	if client.submitText == "" {
		t.Fatal("expected startup submit to submit initial prompt")
	}
	if client.recordedPromptHistory != "" {
		t.Fatalf("did not expect startup submit to record expanded prompt history, got %q", client.recordedPromptHistory)
	}
}

func TestBusySlashNameExecutesImmediatelyWithoutQueueing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/name incident triage"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected window title update cmd from /name")
	}
	if !updated.isBusy() {
		t.Fatal("expected busy state unchanged while command executes")
	}
	if updated.sessionName != "incident triage" {
		t.Fatalf("expected session name update, got %q", updated.sessionName)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %d", len(updated.pendingInjected))
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /name, got %q", updated.input)
	}
}

func TestBusySlashThinkingExecutesImmediatelyWithoutQueueing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.thinkingLevel = "high"
	m.input = "/thinking low"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected status clear timer command")
	}
	if !updated.isBusy() {
		t.Fatal("expected busy state unchanged while command executes")
	}
	if updated.thinkingLevel != "low" {
		t.Fatalf("expected thinking level update, got %q", updated.thinkingLevel)
	}
	if updated.transientStatus != "Thinking level set to low" {
		t.Fatalf("expected thinking status, got %q", updated.transientStatus)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("/thinking without runtime must not create transcript entries: %+v", updated.transcriptEntries)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %d", len(updated.pendingInjected))
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /thinking, got %q", updated.input)
	}
}

func TestLocalSlashCommandsDiscardPromptHistoryDraftAfterExecution(t *testing.T) {
	for _, input := range []string{"/thinking low", "/fast on", "/supervisor on", "/autocompaction off"} {
		t.Run(input, func(t *testing.T) {
			m := newProjectedStaticUIModel(WithUIFastModeAvailable(true))
			m.promptHistoryDraft = "previous user prompt"
			m.promptHistoryDraftCursor = -1
			m.input = input

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if updated.input != "" {
				t.Fatalf("expected %s to clear input instead of restoring prompt history draft, got %q", input, updated.input)
			}
			if updated.promptHistoryDraft != "" || updated.promptHistoryDraftCursor != -1 {
				t.Fatalf("expected %s to clear prompt history draft, got text=%q cursor=%d", input, updated.promptHistoryDraft, updated.promptHistoryDraftCursor)
			}
		})
	}
}

func TestMirroredTransientStatusClearsOnlyForMatchingNoticeID(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transientStatus = "Fast mode enabled"
	m.transientStatusKind = uiStatusNoticeSuccess
	m.transientStatusNoticeID = "notice-1"

	m.clearMirroredTransientStatus([]tui.TranscriptEntry{{Role: "system", Text: "Fast mode enabled", NoticeID: "notice-2"}})
	if m.transientStatus != "Fast mode enabled" || m.transientStatusNoticeID != "notice-1" {
		t.Fatalf("expected unmatched notice id to preserve transient status, got status=%q notice=%q", m.transientStatus, m.transientStatusNoticeID)
	}

	m.clearMirroredTransientStatus([]tui.TranscriptEntry{{Role: "system", Text: "different text", NoticeID: "notice-1"}})
	if m.transientStatus != "" || m.transientStatusNoticeID != "" {
		t.Fatalf("expected matching notice id to clear transient status, got status=%q notice=%q", m.transientStatus, m.transientStatusNoticeID)
	}
}

func TestSlashFastTogglesAndShowsStatus(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIFastModeAvailable(true))
	m.termWidth = 100
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()
	m.input = "/fast"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if !updated.fastModeEnabled {
		t.Fatal("expected fast mode enabled after toggle")
	}
	if updated.transientStatus == "" {
		t.Fatal("expected a transient status notice for /fast toggle")
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("static /fast toggle must not create transcript entries: %+v", updated.transcriptEntries)
	}

	updated.input = "/fast off"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if updated.fastModeEnabled {
		t.Fatal("expected fast mode disabled")
	}
	if updated.transientStatus == "" {
		t.Fatal("expected a transient status notice for /fast off")
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("static /fast off must not create transcript entries: %+v", updated.transcriptEntries)
	}

	updated.input = "/fast status"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd for /fast status")
	}
	if updated.fastModeEnabled {
		t.Fatal("expected /fast status to leave fast mode disabled")
	}
	if plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); plain != "" {
		t.Fatalf("status-only /fast status notice leaked into ongoing transcript: %q", plain)
	}
	if updated.transientStatus == "" {
		t.Fatal("expected /fast status to surface status-only feedback")
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("static /fast status must not create transcript entries: %+v", updated.transcriptEntries)
	}
}

func TestSlashFastStatusNoticeReplacesWithoutWaitingForClear(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIFastModeAvailable(true))
	m.termWidth = 100
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()
	m.input = "/fast on"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if !updated.fastModeEnabled {
		t.Fatal("expected fast mode enabled")
	}
	enabledStatus := updated.transientStatus
	if enabledStatus == "" {
		t.Fatal("expected enable status to be set")
	}

	updated.input = "/fast off"
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if updated.fastModeEnabled {
		t.Fatal("expected fast mode disabled")
	}
	if updated.transientStatus == "" || updated.transientStatus == enabledStatus {
		t.Fatalf("expected disable status to immediately replace enable status, got %q", updated.transientStatus)
	}
}

func TestSlashFastUnavailableShowsError(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()
	m.input = "/fast on"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if updated.fastModeEnabled {
		t.Fatal("did not expect fast mode enabled")
	}
	if updated.transientStatus == "" {
		t.Fatal("expected availability error status to be surfaced")
	}
}

func TestSlashFastWithEngineTogglesRuntime(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFastClient{}, runtime.Config{Model: "gpt-5.3-codex"})
	m := newProjectedEngineUIModel(eng)
	m.input = "/fast on"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected runtime control command")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if !eng.FastModeEnabled() {
		t.Fatal("expected runtime fast mode enabled")
	}
	if !updated.fastModeEnabled {
		t.Fatal("expected ui fast mode enabled")
	}

	updated.input = "/fast off"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if eng.FastModeEnabled() {
		t.Fatal("expected runtime fast mode disabled")
	}
	if updated.fastModeEnabled {
		t.Fatal("expected ui fast mode disabled")
	}
}

func TestSlashSupervisorTogglesReviewerInvocationAndShowsStatus(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()
	m.input = "/supervisor"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if !updated.reviewerEnabled {
		t.Fatal("expected reviewer invocation enabled after toggle")
	}
	if updated.reviewerMode != "edits" {
		t.Fatalf("expected reviewer mode edits after toggle, got %q", updated.reviewerMode)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /supervisor, got %q", updated.input)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}

	updated.input = "/supervisor off"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if updated.reviewerEnabled {
		t.Fatal("expected reviewer invocation disabled")
	}
	if updated.reviewerMode != "off" {
		t.Fatalf("expected reviewer mode off after disable, got %q", updated.reviewerMode)
	}
}

func TestBusySlashSupervisorExecutesImmediatelyWithoutQueueing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/supervisor on"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if !updated.isBusy() {
		t.Fatal("expected busy state unchanged while command executes")
	}
	if !updated.reviewerEnabled || updated.reviewerMode != "edits" {
		t.Fatalf("expected reviewer enabled in edits mode, got enabled=%v mode=%q", updated.reviewerEnabled, updated.reviewerMode)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %d", len(updated.pendingInjected))
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /supervisor, got %q", updated.input)
	}
}

func TestBusySlashSupervisorOffAppliesToInFlightRunCompletion(t *testing.T) {
	mainClient := &busyToggleFakeClient{
		delay: 80 * time.Millisecond,
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		}},
	}
	reviewerClient := &busyToggleFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	_, eng := newAppRuntimeEngine(t, mainClient, runtime.Config{
		Model: "gpt-5",
		Reviewer: runtime.ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.activity = uiActivityRunning

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "hello")
		submitDone <- submitErr
	}()
	time.Sleep(10 * time.Millisecond)

	m.input = "/supervisor off"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if updated.reviewerEnabled || updated.reviewerMode != "off" {
		t.Fatalf("expected ui reviewer disabled after /supervisor off, got enabled=%v mode=%q", updated.reviewerEnabled, updated.reviewerMode)
	}
	if got := eng.ReviewerFrequency(); got != "off" {
		t.Fatalf("expected runtime reviewer mode off, got %q", got)
	}

	if err := <-submitDone; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if got := reviewerClient.CallCount(); got != 0 {
		t.Fatalf("expected no reviewer call for in-flight run after /supervisor off, got %d", got)
	}
}
