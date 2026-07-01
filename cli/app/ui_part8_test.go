package app

import (
	"context"
	"core/cli/app/commands"
	"core/server/llm"
	"core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBusySlashSupervisorOnAppliesToInFlightRunCompletion(t *testing.T) {
	mainClient := &busyToggleFakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_patch_1", Name: string(toolspec.ToolPatch), Custom: true, CustomInput: "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch"}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &busyToggleFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	_, eng := newAppRuntimeEngine(t, mainClient, runtime.Config{
		Model: "gpt-5",
		Reviewer: runtime.ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	}, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: busyTogglePatchTool{delay: 80 * time.Millisecond}})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.activity = uiActivityRunning

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "edit file")
		submitDone <- submitErr
	}()
	time.Sleep(10 * time.Millisecond)

	m.input = "/supervisor on"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if !updated.reviewerEnabled || updated.reviewerMode != "edits" {
		t.Fatalf("expected ui reviewer enabled in edits mode after /supervisor on, got enabled=%v mode=%q", updated.reviewerEnabled, updated.reviewerMode)
	}
	if got := eng.ReviewerFrequency(); got != "edits" {
		t.Fatalf("expected runtime reviewer mode edits, got %q", got)
	}

	if err := <-submitDone; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if got := reviewerClient.CallCount(); got != 1 {
		t.Fatalf("expected reviewer call for in-flight run after /supervisor on, got %d", got)
	}
}

func TestSlashSupervisorWithEngineTogglesRuntimeReviewer(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{
		Model: "gpt-5",
		Reviewer: runtime.ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        statusLineFakeClient{},
		},
	})
	m := newProjectedEngineUIModel(eng)
	m.input = "/supervisor on"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected runtime control command")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if got := eng.ReviewerFrequency(); got != "edits" {
		t.Fatalf("expected runtime reviewer mode edits, got %q", got)
	}
	if !updated.reviewerEnabled || updated.reviewerMode != "edits" {
		t.Fatalf("expected ui reviewer enabled in edits mode, got enabled=%v mode=%q", updated.reviewerEnabled, updated.reviewerMode)
	}

	updated.input = "/supervisor off"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if got := eng.ReviewerFrequency(); got != "off" {
		t.Fatalf("expected runtime reviewer mode off, got %q", got)
	}
	if updated.reviewerEnabled || updated.reviewerMode != "off" {
		t.Fatalf("expected ui reviewer disabled in off mode, got enabled=%v mode=%q", updated.reviewerEnabled, updated.reviewerMode)
	}
}

func TestSlashAutoCompactionTogglesAndShowsStatus(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 24
	m.windowSizeKnown = true
	m.layout().syncViewport()
	m.input = "/autocompaction"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if updated.autoCompactionEnabled {
		t.Fatal("expected auto-compaction disabled after toggle")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /autocompaction, got %q", updated.input)
	}
	if !strings.Contains(updated.transientStatus, "Auto-compaction disabled") {
		t.Fatalf("expected transient status for /autocompaction toggle, got %q", updated.transientStatus)
	}
	plain := stripANSIAndTrimRight(updated.View())
	if !strings.Contains(plain, "Auto-compaction disabled") {
		t.Fatalf("expected transcript notice for /autocompaction toggle, got %q", plain)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}

	updated.input = "/autocompaction on"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if !updated.autoCompactionEnabled {
		t.Fatal("expected auto-compaction enabled")
	}
	if !strings.Contains(updated.transientStatus, "Auto-compaction enabled") {
		t.Fatalf("expected enable transient status, got %q", updated.transientStatus)
	}
}

func TestBusySlashAutoCompactionExecutesImmediatelyWithoutQueueing(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/autocompaction off"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if !updated.isBusy() {
		t.Fatal("expected busy state unchanged while command executes")
	}
	if updated.autoCompactionEnabled {
		t.Fatal("expected auto-compaction disabled")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %d", len(updated.pendingInjected))
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after /autocompaction, got %q", updated.input)
	}
}

func TestSlashAutoCompactionWithEngineTogglesRuntime(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{})
	m := newProjectedEngineUIModel(eng)
	m.input = "/autocompaction off"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected runtime control command")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if got := eng.AutoCompactionEnabled(); got {
		t.Fatalf("expected runtime auto-compaction disabled, got %v", got)
	}
	if updated.autoCompactionEnabled {
		t.Fatal("expected ui auto-compaction disabled")
	}

	updated.input = "/autocompaction on"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if got := eng.AutoCompactionEnabled(); !got {
		t.Fatalf("expected runtime auto-compaction enabled, got %v", got)
	}
	if !updated.autoCompactionEnabled {
		t.Fatal("expected ui auto-compaction enabled")
	}
}

func TestSlashAutoCompactionKeepsPriorStateWhenRuntimeToggleFails(t *testing.T) {
	client := &runtimeControlFakeClient{err: errors.New("daemon stalled")}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.autoCompactionEnabled = true
	m.input = "/autocompaction off"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected runtime control command")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if !updated.autoCompactionEnabled {
		t.Fatal("expected prior auto-compaction state preserved on toggle failure")
	}
	if !strings.Contains(updated.transientStatus, "daemon stalled") {
		t.Fatalf("expected transport error in transient status, got %q", updated.transientStatus)
	}
}

func TestSlashAutoCompactionShowsCompactionModeNoneNote(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{CompactionMode: "none"})
	m := newProjectedEngineUIModel(eng)
	m.input = "/autocompaction on"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if !strings.Contains(updated.transientStatus, "compaction_mode=none") {
		t.Fatalf("expected compaction_mode=none note in status, got %q", updated.transientStatus)
	}
}

func TestWorkflowSessionAutoCompactionOffBlockedBeforeRuntimeCall(t *testing.T) {
	client := &runtimeControlFakeClient{
		status: clientui.RuntimeStatus{
			AutoCompactionEnabled: true,
			WorkflowSession:       &clientui.WorkflowSessionStatus{RunID: "run-1"},
		},
	}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.input = "/autocompaction off"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)

	if client.setAutoCompactCalls != 0 {
		t.Fatal("did not expect runtime auto-compaction disable call")
	}
	if !strings.Contains(updated.transientStatus, "Auto-compaction cannot be disabled") {
		t.Fatalf("transient status = %q, want workflow auto-compaction block", updated.transientStatus)
	}
	if cmd == nil {
		t.Fatal("expected transient status clear command")
	}
}

func TestBusyUnsupportedSlashCommandShowsTransientErrorAndDoesNotQueue(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/compact keep details"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected transient status clear timer cmd")
	}
	if updated.transientStatus == "" {
		t.Fatal("expected transient status message for unsupported busy command")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages, got %d", len(updated.queued))
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected no pending injected messages, got %d", len(updated.pendingInjected))
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect input submit lock for blocked slash command")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared for blocked slash command, got %q", updated.input)
	}
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "cannot run /compact while model is working") {
		t.Fatalf("expected transient status in status line, got %q", status)
	}

	next, _ = updated.Update(clearTransientStatusMsg{token: updated.transientStatusToken})
	cleared := next.(*uiModel)
	if cleared.transientStatus != "" {
		t.Fatalf("expected transient status to clear, got %q", cleared.transientStatus)
	}
}

func TestSlashCommandSetsExitAction(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/exit"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected quit cmd for /exit")
	}
	updated := next.(*uiModel)
	if updated.exitAction != UIActionExit {
		t.Fatalf("expected UIActionExit, got %q", updated.exitAction)
	}
}

func TestSlashCommandSetsResumeAction(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIHasOtherSessions(true, true))
	m.input = "/resume"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected quit cmd for /resume")
	}
	updated := next.(*uiModel)
	if updated.exitAction != UIActionResume {
		t.Fatalf("expected UIActionResume, got %q", updated.exitAction)
	}
}

func TestInitialTranscriptVisibleImmediately(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIInitialTranscript([]UITranscriptEntry{
			{Role: "user", Text: "hello"},
			{Role: "assistant", Text: "world"},
		}),
	)
	m.termWidth = 80
	m.termHeight = 20

	ongoing := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(ongoing, "world") {
		t.Fatalf("expected resumed content in ongoing mode, got %q", ongoing)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	detail := stripANSIAndTrimRight(next.(*uiModel).View())
	if !containsInOrder(detail, "❯", "hello", "❮", "world") {
		t.Fatalf("expected resumed transcript in detail mode, got %q", detail)
	}
}

func TestSubmitDoneNoopFinalStaysInvisibleWithoutRuntimeClient(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)

	next, _ := m.Update(newSubmitDoneMsg(0, uiNoopFinalToken, "", nil))
	updated := next.(*uiModel)
	if updated.isBusy() {
		t.Fatal("expected UI idle after NO_OP final")
	}
	if updated.activity != uiActivityIdle {
		t.Fatalf("activity = %v, want idle", updated.activity)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if strings.Contains(plain, uiNoopFinalToken) {
		t.Fatalf("expected NO_OP to stay invisible in local flow, got %q", plain)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("expected no transcript entries after NO_OP final, got %+v", updated.transcriptEntries)
	}
}

func TestRuntimeSubmitNoopFinalStaysSilent(t *testing.T) {
	ringer := &countRinger{}
	bells := newBellHooks(ringer, nil)
	client := &runtimeControlFakeClient{submitResult: uiNoopFinalToken}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUITurnQueueHook(bells))
	m.startupCmds = nil
	m.input = "hello"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected submit command batch")
	}
	msgs := collectCmdMessages(t, cmd)
	var done submitDoneMsg
	doneFound := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			done = typed
			doneFound = true
		}
	}
	if !doneFound {
		t.Fatalf("expected submitDone message, got %+v", msgs)
	}
	if !done.silentFinal {
		t.Fatalf("expected runtime NO_OP submit result to be marked silent, got %+v", done)
	}

	next, _ = updated.Update(done)
	updated = next.(*uiModel)
	if updated.isBusy() {
		t.Fatal("expected UI idle after runtime NO_OP final")
	}
	if updated.activity != uiActivityIdle {
		t.Fatalf("activity = %v, want idle", updated.activity)
	}
	if client.submitText != "hello" {
		t.Fatalf("submit text = %q, want hello", client.submitText)
	}
	if got := ringer.Count(); got != 0 {
		t.Fatalf("ring count = %d after runtime NO_OP final, want 0", got)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if strings.Contains(plain, uiNoopFinalToken) {
		t.Fatalf("expected runtime NO_OP to stay invisible, got %q", plain)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("expected no transcript entries after runtime NO_OP final, got %+v", updated.transcriptEntries)
	}
}

func TestInitAutoSubmitsStartupPrompt(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIStartupSubmit("run review"),
	)
	m.termWidth = 80
	m.termHeight = 20

	_ = m.Init()

	if !m.isBusy() {
		t.Fatal("expected startup prompt to start submission immediately")
	}
	plain := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(plain, "run review") {
		t.Fatalf("expected startup prompt in transcript, got %q", plain)
	}
}

func TestInitialInputSeedsDraftWithoutAutoSubmit(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIInitialInput("draft reply"),
	)

	if m.input != "draft reply" {
		t.Fatalf("expected initial input draft, got %q", m.input)
	}
	if m.inputCursor != -1 {
		t.Fatalf("expected initial input cursor at tail, got %d", m.inputCursor)
	}
	if m.isBusy() {
		t.Fatal("did not expect initial input to auto-submit")
	}
}

func TestReviewerStatusEndToEnd_VerboseSuggestionsIssuedAndStatusConcise(t *testing.T) {
	_, eng := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{})
	eng.AppendCommittedEntryWithCondensedText("reviewer_suggestions", "Supervisor suggested:\n1. First detailed suggestion text\n2. Second detailed suggestion text", "Supervisor suggested:\n1. First detailed suggestion text\n2. Second detailed suggestion text")
	eng.AppendCommittedEntry("reviewer_status", "Supervisor ran: 2 suggestions, no changes applied.")

	m := newProjectedEngineUIModel(eng, WithUITheme("dark"))
	m.termWidth = 100
	m.termHeight = 24

	ongoing := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !containsInOrder(ongoing, "Supervisor suggested:", "1. First detailed suggestion text", "2. Second detailed suggestion text") {
		t.Fatalf("expected verbose reviewer suggestions in ongoing mode, got %q", ongoing)
	}
	if !strings.Contains(ongoing, "Supervisor ran: 2 suggestions, no changes applied.") {
		t.Fatalf("expected short reviewer status in ongoing mode, got %q", ongoing)
	}
	if strings.Count(ongoing, "Supervisor suggested:") != 1 {
		t.Fatalf("expected reviewer suggestions details only at issuance time in ongoing mode, got %q", ongoing)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	detail := stripANSIAndTrimRight(next.(*uiModel).View())
	if !containsInOrder(detail, "Supervisor suggested:", "1. First detailed suggestion text", "2. Second detailed suggestion text", "Supervisor ran: 2 suggestions, no changes applied.") {
		t.Fatalf("expected full reviewer suggestions in detail mode, got %q", detail)
	}
}

func TestDisconnectedEnterKeepsInputAndDoesNotStartSubmission(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)
	m.input = "continue with tests"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.isBusy() {
		t.Fatal("did not expect busy state while disconnected")
	}
	if updated.input != "continue with tests" {
		t.Fatalf("expected input preserved while disconnected, got %q", updated.input)
	}
	if client.submitText != "" {
		t.Fatalf("did not expect runtime submit attempt, got %q", client.submitText)
	}
	if updated.activity != uiActivityError {
		t.Fatalf("expected error activity while disconnected, got %v", updated.activity)
	}
}

func TestDisconnectedEnterShowsStatusOnlyWhenRuntimeAppendFails(t *testing.T) {
	client := &runtimeControlFakeClient{appendErr: errors.New("append failed")}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)
	m.input = "continue with tests"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)

	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("did not expect immediate transcript entry, got %+v", updated.transcriptEntries)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("runtime append failure must not create local transcript entries: %+v", updated.transcriptEntries)
	}
	if committed := committedTranscriptEntriesForApp(updated.transcriptEntries); len(committed) != 0 {
		t.Fatalf("runtime append failure advanced committed transcript entries: %+v", committed)
	}
	if updated.transientStatus != "append failed" || updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected append failure status, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
	if client.submitText != "" {
		t.Fatalf("did not expect runtime submit attempt, got %q", client.submitText)
	}
}

func TestBlockDisconnectedSubmissionPersistsFeedbackWhenRuntimeAppendSucceeds(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)
	m.pendingInjected = queuedUserMessagesForTest("hidden steering")

	blocked, cmd := m.inputController().blockDisconnectedSubmission(true, "generated prompt")

	if !blocked {
		t.Fatal("expected disconnected submission to block")
	}
	if cmd != nil {
		if client.appendedRole != "" || client.appendedText != "" {
			t.Fatalf("did not expect runtime append during disconnected submission block, got role=%q text=%q", client.appendedRole, client.appendedText)
		}
		_ = collectCmdMessages(t, cmd)
	}
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("did not expect local fallback after successful runtime append, got %+v", m.transcriptEntries)
	}
	if m.activity != uiActivityError {
		t.Fatalf("expected error activity while disconnected, got %v", m.activity)
	}
	if m.input != "hidden steering\n\ngenerated prompt" {
		t.Fatalf("expected hidden drafts restored into input, got %q", m.input)
	}
	if client.appendedRole != string(transcript.EntryRoleDeveloperErrorFeedback) || client.appendedText != runtimeDisconnectedStatusMessage {
		t.Fatalf("unexpected runtime committed entry attempt: role=%q text=%q", client.appendedRole, client.appendedText)
	}
}

func TestDisconnectedQueuedFlushRestoresHiddenQueuedDrafts(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)
	m.queued = queuedInputsForTest("first queued", "second queued")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)

	if cmd != nil {
		if client.appendedRole != "" || client.appendedText != "" {
			t.Fatalf("did not expect runtime append during disconnected queued flush, got role=%q text=%q", client.appendedRole, client.appendedText)
		}
		_ = collectCmdMessages(t, cmd)
	}
	if updated.input != "first queued\n\nsecond queued" {
		t.Fatalf("expected hidden queued drafts restored into input, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued drafts restored and cleared, got %+v", updated.queued)
	}
}

func TestDisconnectedQueuedFlushWithoutQueuedWorkDoesNotAppendFeedback(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)

	next, cmd := m.inputController().flushQueuedInputs(queueDrainAuto)
	updated := next.(*uiModel)

	if cmd != nil {
		t.Fatal("did not expect command for no-op queued flush")
	}
	if updated.activity != uiActivityIdle {
		t.Fatalf("activity = %v, want idle", updated.activity)
	}
	if client.appendedRole != "" || client.appendedText != "" {
		t.Fatalf("did not expect disconnect feedback append, got role=%q text=%q", client.appendedRole, client.appendedText)
	}
}

func TestDisconnectedQueuedInjectionSubmissionRestoresHiddenInjectedDrafts(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)
	m.pendingInjected = queuedUserMessagesForTest("hidden steering")

	cmd := m.inputController().startQueuedInjectionSubmission()
	if cmd != nil {
		if client.appendedRole != "" || client.appendedText != "" {
			t.Fatalf("did not expect runtime append during disconnected queued injection submission, got role=%q text=%q", client.appendedRole, client.appendedText)
		}
		_ = collectCmdMessages(t, cmd)
	}
	if m.input != "hidden steering" {
		t.Fatalf("expected hidden injected draft restored into input, got %q", m.input)
	}
	if len(m.pendingInjected) != 0 {
		t.Fatalf("expected pending injected drafts restored and cleared, got %+v", m.pendingInjected)
	}
}

func TestDisconnectedCommandSubmitRestoresGeneratedPrompt(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)

	next, _ := m.inputController().applyCommandResultWithPreSubmitQueuePosition(commands.Result{Handled: true, SubmitUser: true, User: "generated prompt"}, preSubmitQueueBack)
	updated := next.(*uiModel)
	if updated.input != "generated prompt" {
		t.Fatalf("expected generated prompt restored into input, got %q", updated.input)
	}
	if updated.isBusy() {
		t.Fatal("did not expect busy state while disconnected")
	}
}

func TestDisconnectedCommandSubmitRestoresGeneratedPromptAlongsideHiddenSteering(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, nil, nil)
	m.setRuntimeDisconnected(true)
	m.pendingInjected = queuedUserMessagesForTest("hidden steering")

	next, _ := m.inputController().applyCommandResultWithPreSubmitQueuePosition(commands.Result{Handled: true, SubmitUser: true, User: "generated prompt"}, preSubmitQueueBack)
	updated := next.(*uiModel)
	if updated.input != "hidden steering\n\ngenerated prompt" {
		t.Fatalf("expected generated prompt restored after hidden steering, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injected drafts restored and cleared, got %+v", updated.pendingInjected)
	}
}

func TestApplyCommandResultBackWithoutParentShowsStatusOnly(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.windowSizeKnown = true
	m.termWidth = 120

	next, cmd := m.inputController().applyCommandResultWithPreSubmitQueuePosition(commands.Result{Handled: true, Action: commands.ActionBack}, preSubmitQueueBack)
	updated := next.(*uiModel)

	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("back command without runtime must not create transcript entries: %+v", updated.transcriptEntries)
	}
	if cmd == nil {
		t.Fatal("expected status clear timer command")
	}
	if updated.transientStatus != "No parent session available" {
		t.Fatalf("expected back command status, got %q", updated.transientStatus)
	}
}

func TestEnqueueRuntimeConnectionStateChangeDropsStaleWithoutBlocking(t *testing.T) {
	ch := make(chan runtimeConnectionStateChangedMsg, 1)
	enqueueRuntimeConnectionStateChange(ch, errors.New("stale"))

	done := make(chan struct{})
	go func() {
		enqueueRuntimeConnectionStateChange(ch, io.EOF)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for non-blocking connection-state enqueue")
	}

	select {
	case msg := <-ch:
		if !errors.Is(msg.err, io.EOF) {
			t.Fatalf("expected latest connection-state error preserved, got %v", msg.err)
		}
	default:
		t.Fatal("expected queued connection-state message")
	}
}
