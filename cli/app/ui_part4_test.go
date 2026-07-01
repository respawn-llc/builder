package app

import (
	"context"
	"core/cli/app/internal/runtimeattach"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/transcript"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMainInputViewportTracksCursorLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 20
	m.termHeight = 6
	m.windowSizeKnown = true
	m.input = "first\nsecond\nthird\nfourth"
	m.inputCursor = 1
	m.layout().syncViewport()

	plain := stripANSIAndTrimRight(strings.Join(m.layout().renderInputLines(20, uiThemeStyles("dark")), "\n"))
	if !strings.Contains(plain, "› first") || !strings.Contains(plain, "second") {
		t.Fatalf("expected viewport to keep cursor line visible, got %q", plain)
	}
	if strings.Contains(plain, "fourth") {
		t.Fatalf("expected viewport not to pin to tail while cursor is near top, got %q", plain)
	}
}

func TestArrowNavigationDoesNotMutateInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "abcdef"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyLeft})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyHome})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnd})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	updated = next.(*uiModel)

	if updated.input != "abcdef" {
		t.Fatalf("expected navigation keys not to mutate input, got %q", updated.input)
	}
}

func TestBusyEnterQueuesSteeringUntilFlushed(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "please continue with tests"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect input submit lock after enter while busy")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after queueing steering, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 1 {
		t.Fatalf("expected one pending injected message, got %d", len(updated.pendingInjected))
	}

	next, _ = updated.Update(projectedRuntimeEventMsg(runtime.Event{
		Kind:                         runtime.EventUserMessageFlushed,
		UserMessage:                  "please continue with tests",
		UserMessageBatch:             []string{"please continue with tests"},
		UserMessageBatchQueueItemIDs: queuedUserMessageIDsForTest(updated.pendingInjected),
	}))
	updated = next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect input lock after flush")
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after flush, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected queued steering cleared after flush, got %+v", updated.pendingInjected)
	}
}

func TestBusyEnterCanQueueMultipleSteeringMessages(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "first steering message"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated.input = "second steering message"

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 2 {
		t.Fatalf("expected two queued steering messages, got %+v", updated.pendingInjected)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after queueing multiple steering messages, got %q", updated.input)
	}

	next, _ = updated.Update(projectedRuntimeEventMsg(runtime.Event{
		Kind:                         runtime.EventUserMessageFlushed,
		UserMessage:                  "first steering message\n\nsecond steering message",
		UserMessageBatch:             []string{"first steering message", "second steering message"},
		UserMessageBatchQueueItemIDs: queuedUserMessageIDsForTest(updated.pendingInjected),
	}))
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected queued steering cleared after batched flush, got %+v", updated.pendingInjected)
	}
}

func TestBusyEnterQueuesInjectedInputWithoutRuntimeCreateDuringUpdate(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-queue-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.input = "please continue with tests"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected async queue create command")
	}
	if client.queueUserMessageCalls != 0 {
		t.Fatalf("QueueUserMessage called during Update: %d", client.queueUserMessageCalls)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].ID == "server-queue-1" {
		t.Fatalf("expected provisional pending injected item before command, got %+v", updated.pendingInjected)
	}

	msgs := collectCmdMessages(t, cmd)
	if client.queueUserMessageCalls != 1 || client.queuedText != "please continue with tests" {
		t.Fatalf("expected one command-time queue create, calls=%d text=%q", client.queueUserMessageCalls, client.queuedText)
	}
	var createDone injectedQueueCreateDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
		}
	}
	if createDone.item.ID != "server-queue-1" {
		t.Fatalf("expected server queue id in completion, got %+v", createDone)
	}

	next, _ = updated.Update(createDone)
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].ID != "server-queue-1" {
		t.Fatalf("expected pending injected item to adopt server id, got %+v", updated.pendingInjected)
	}
	if updated.injectedQueueBlocksDrain() {
		t.Fatalf("did not expect enqueued item to block drain, state=%+v", updated.injectedQueue)
	}
}

func TestQueuedInjectedInputEarlyFailureStatusClearsProvisionalQueue(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-queue-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.input = "please continue with tests"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected async queue create command")
	}
	if len(updated.pendingInjected) != 1 {
		t.Fatalf("expected one provisional pending item, got %+v", updated.pendingInjected)
	}
	clientRequestID := updated.pendingInjected[0].ClientRequestID
	if clientRequestID == "" {
		t.Fatalf("expected provisional pending item to have client request id, got %+v", updated.pendingInjected[0])
	}

	msgs := collectCmdMessages(t, cmd)
	var createDone injectedQueueCreateDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
		}
	}
	if createDone.item.ID != "server-queue-1" || createDone.item.ClientRequestID != clientRequestID {
		t.Fatalf("expected queue response with server id and request id, got %+v request=%q", createDone.item, clientRequestID)
	}

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
			QueueItemID:     "server-queue-1",
			ClientRequestID: clientRequestID,
			Status:          clientui.QueuedUserMessageFailed,
			RestoreText:     "please continue with tests",
			FailureReason:   clientui.QueuedUserMessageFailureClosing,
		},
	}})
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("expected early status to clear provisional queue, pending=%+v injected=%+v", updated.pendingInjected, updated.injectedQueue)
	}
	if updated.input != "please continue with tests" {
		t.Fatalf("expected failed queued text restored, got %q", updated.input)
	}

	next, _ = updated.Update(createDone)
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("expected delayed create completion ignored after failure, pending=%+v injected=%+v", updated.pendingInjected, updated.injectedQueue)
	}
}

func TestQueuedRuntimeWorkCheckDoesNotSubmitWhenRuntimeBecameBusy(t *testing.T) {
	client := &runtimeControlFakeClient{hasQueuedUserWork: true}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())

	cmd := m.inputController().queuedRuntimeWorkCheckCmd()
	if cmd == nil {
		t.Fatal("expected queued runtime work check command")
	}
	m.setBusy(true)
	raw := cmd()
	msg, ok := raw.(queuedRuntimeWorkCheckDoneMsg)
	if !ok {
		t.Fatalf("unexpected queue check message %T", raw)
	}
	next, followCmd := m.Update(msg)
	updated := next.(*uiModel)
	if followCmd != nil {
		t.Fatal("did not expect queued submit command while runtime is busy")
	}
	if client.submitQueuedCalls != 0 {
		t.Fatalf("queued submit started while runtime busy: %d", client.submitQueuedCalls)
	}
	if !updated.isBusy() {
		t.Fatal("expected busy state preserved")
	}
}

func TestIdleRuntimeResumesInjectedQueueThatWasEnqueuedWhileBusy(t *testing.T) {
	client := &runtimeControlFakeClient{hasQueuedUserWork: true, submitQueuedResult: "done"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.injectedQueue = []injectedRuntimeQueueItem{{LocalID: "local-1", ServerID: "server-1", Text: "follow up", State: injectedRuntimeQueueEnqueued}}
	m.pendingInjected = []clientui.QueuedUserMessage{{ID: "server-1", Text: "follow up"}}

	cmd := m.inputController().resumeQueuedInputsAfterIdleRuntime()
	if cmd == nil {
		t.Fatal("expected idle runtime to resume enqueued injected work")
	}
	raw := cmd()
	msg, ok := raw.(queuedRuntimeWorkCheckDoneMsg)
	if !ok {
		t.Fatalf("unexpected queue check message %T", raw)
	}
	next, submitCmd := m.Update(msg)
	updated := next.(*uiModel)
	if submitCmd == nil {
		t.Fatal("expected queued submit command after idle injected resume")
	}
	if !updated.isBusy() {
		t.Fatal("expected queued injected resume to mark UI busy")
	}
	_ = collectCmdMessages(t, submitCmd)
	if client.submitQueuedCalls != 1 {
		t.Fatalf("expected one queued submit call, got %d", client.submitQueuedCalls)
	}
}

func TestPendingInjectedCreateCanceledBeforeCompletionDiscardsLateServerItem(t *testing.T) {
	client := &runtimeControlFakeClient{queueUserMessageID: "server-queue-1", discardQueuedResult: true}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.input = "restore this steering"

	next, createCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if createCmd == nil {
		t.Fatal("expected async queue create command")
	}
	next, _ = updated.Update(submitDoneMsg{err: errors.New("network failure")})
	updated = next.(*uiModel)
	if updated.input != "restore this steering" {
		t.Fatalf("expected pending injected text restored, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending projection cleared before create completion, got %+v", updated.pendingInjected)
	}
	if client.discardQueuedCalls != 0 {
		t.Fatalf("discard called before server id existed: %d", client.discardQueuedCalls)
	}

	msgs := collectCmdMessages(t, createCmd)
	var createDone injectedQueueCreateDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
		}
	}
	next, discardCmd := updated.Update(createDone)
	updated = next.(*uiModel)
	if discardCmd == nil {
		t.Fatal("expected late create success to schedule discard")
	}
	if client.discardQueuedCalls != 0 {
		t.Fatalf("discard called during create completion Update: %d", client.discardQueuedCalls)
	}
	discardMsgs := collectCmdMessages(t, discardCmd)
	if client.discardQueuedCalls != 1 || client.discardQueuedID != "server-queue-1" {
		t.Fatalf("expected command-time discard of late queue item, calls=%d id=%q", client.discardQueuedCalls, client.discardQueuedID)
	}
	var discardDone injectedQueueDiscardDoneMsg
	for _, msg := range discardMsgs {
		if typed, ok := msg.(injectedQueueDiscardDoneMsg); ok {
			discardDone = typed
		}
	}
	next, _ = updated.Update(discardDone)
	updated = next.(*uiModel)
	if len(updated.injectedQueue) != 0 {
		t.Fatalf("expected late-created canceled item removed after discard, got %+v", updated.injectedQueue)
	}
}

func TestDiscardFailedInjectedQueueBlocksRuntimeQueuedDrain(t *testing.T) {
	client := &runtimeControlFakeClient{hasQueuedUserWork: true, queueUserMessageID: "server-queue-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.pendingInjected = []clientui.QueuedUserMessage{{ID: "server-queue-1", Text: "blocked steering"}}
	m.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:  "local-queue-1",
		ServerID: "server-queue-1",
		Text:     "blocked steering",
		State:    injectedRuntimeQueueEnqueued,
	}}

	restoreCmd := m.inputController().restorePendingInjectedIntoInput()
	if restoreCmd == nil {
		t.Fatal("expected restore to schedule discard")
	}
	msgs := collectCmdMessages(t, restoreCmd)
	var discardDone injectedQueueDiscardDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueDiscardDoneMsg); ok {
			discardDone = typed
		}
	}
	next, _ := m.Update(discardDone)
	updated := next.(*uiModel)
	if !updated.injectedQueueBlocksDrain() {
		t.Fatalf("expected discard failure to block drain, state=%+v", updated.injectedQueue)
	}
	if cmd := updated.inputController().startQueuedInjectionSubmission(); cmd != nil {
		t.Fatal("did not expect queued runtime work check while discard failure blocks drain")
	}
	if client.hasQueuedUserWorkCalls != 0 || client.submitQueuedCalls != 0 {
		t.Fatalf("expected no runtime queued-work calls while blocked, check=%d submit=%d", client.hasQueuedUserWorkCalls, client.submitQueuedCalls)
	}
	next, submitCmd := updated.inputController().queueOrStartSubmission(updated.input)
	updated = next.(*uiModel)
	if submitCmd == nil {
		t.Fatal("expected blocked normal submit to surface an error")
	}
	_ = collectCmdMessages(t, submitCmd)
	if updated.isBusy() {
		t.Fatal("did not expect normal submit while queued runtime message is still server-side")
	}
	if client.submitText != "" {
		t.Fatalf("did not expect duplicate normal submit while discard failed, got %q", client.submitText)
	}
}

func TestPendingInjectedCreateFailureRestoresInputAndSurfacesError(t *testing.T) {
	boom := errors.New("queue create failed")
	client := &runtimeControlFakeClient{queueUserMessageErr: boom}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.input = "restore failed create"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected async queue create command")
	}
	msgs := collectCmdMessages(t, cmd)
	var createDone injectedQueueCreateDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
		}
	}
	next, persistCmd := updated.Update(createDone)
	updated = next.(*uiModel)
	if updated.input != "restore failed create" {
		t.Fatalf("expected failed queue text restored, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 0 {
		t.Fatalf("expected failed queue removed, pending=%+v state=%+v", updated.pendingInjected, updated.injectedQueue)
	}
	if updated.activity != uiActivityError {
		t.Fatalf("expected error activity, got %v", updated.activity)
	}
	_ = collectCmdMessages(t, persistCmd)
	if client.appendedRole != string(transcript.EntryRoleDeveloperErrorFeedback) || !strings.Contains(client.appendedText, "queue create failed") {
		t.Fatalf("expected runtime-persisted queue create error, role=%q text=%q", client.appendedRole, client.appendedText)
	}
}

func TestCompactionCompletionWaitsForPendingInjectedCreateBeforeResumingQueuedRuntimeWork(t *testing.T) {
	client := &runtimeControlFakeClient{hasQueuedUserWork: true, queueUserMessageID: "server-queue-1", submitQueuedResult: "done"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.input = "late steering"

	next, createCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	next, compactCmd := updated.Update(compactDoneMsg{})
	updated = next.(*uiModel)
	if compactCmd != nil {
		t.Fatalf("did not expect queued-work check while create is pending, got %T", compactCmd())
	}
	if client.hasQueuedUserWorkCalls != 0 || client.submitQueuedCalls != 0 {
		t.Fatalf("runtime queue checked/submitted before create completion: check=%d submit=%d", client.hasQueuedUserWorkCalls, client.submitQueuedCalls)
	}

	msgs := collectCmdMessages(t, createCmd)
	var createDone injectedQueueCreateDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
		}
	}
	next, checkCmd := updated.Update(createDone)
	updated = next.(*uiModel)
	if checkCmd == nil {
		t.Fatal("expected create completion to schedule queued-work check")
	}
	updated, submitCmd := applyQueuedRuntimeWorkCheckForTest(t, updated, checkCmd)
	if client.hasQueuedUserWorkCalls != 1 {
		t.Fatalf("HasQueuedUserWork calls = %d, want 1", client.hasQueuedUserWorkCalls)
	}
	if submitCmd == nil || !updated.isBusy() {
		t.Fatalf("expected queued runtime work to resume after safe create, busy=%t cmd=%v", updated.isBusy(), submitCmd)
	}
	_ = collectCmdMessages(t, submitCmd)
	if client.submitQueuedCalls != 1 {
		t.Fatalf("SubmitQueuedUserMessages calls = %d, want 1", client.submitQueuedCalls)
	}
}

func TestLateCreateDiscardFailureBlocksDrainUntilRetrySucceeds(t *testing.T) {
	client := &runtimeControlFakeClient{hasQueuedUserWork: true, queueUserMessageID: "server-queue-1"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.input = "restore late create"

	next, createCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	next, _ = updated.Update(submitDoneMsg{err: errors.New("network failure")})
	updated = next.(*uiModel)
	msgs := collectCmdMessages(t, createCmd)
	var createDone injectedQueueCreateDoneMsg
	for _, msg := range msgs {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
		}
	}
	next, discardCmd := updated.Update(createDone)
	updated = next.(*uiModel)
	discardMsgs := collectCmdMessages(t, discardCmd)
	var discardDone injectedQueueDiscardDoneMsg
	for _, msg := range discardMsgs {
		if typed, ok := msg.(injectedQueueDiscardDoneMsg); ok {
			discardDone = typed
		}
	}
	next, _ = updated.Update(discardDone)
	updated = next.(*uiModel)
	if !updated.injectedQueueBlocksDrain() {
		t.Fatalf("expected late-create discard failure to block drain, state=%+v", updated.injectedQueue)
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].ID != "server-queue-1" {
		t.Fatalf("expected failed discard to re-adopt server item visibly, got %+v", updated.pendingInjected)
	}

	client.discardQueuedResult = true
	retryCmd := updated.inputController().restorePendingInjectedIntoInput()
	if retryCmd == nil {
		t.Fatal("expected discard retry command")
	}
	retryMsgs := collectCmdMessages(t, retryCmd)
	var retryDone injectedQueueDiscardDoneMsg
	for _, msg := range retryMsgs {
		if typed, ok := msg.(injectedQueueDiscardDoneMsg); ok {
			retryDone = typed
		}
	}
	next, _ = updated.Update(retryDone)
	updated = next.(*uiModel)
	if updated.injectedQueueBlocksDrain() || len(updated.injectedQueue) != 0 {
		t.Fatalf("expected successful retry to unblock drain, state=%+v", updated.injectedQueue)
	}
}

func TestBusySteeringBatchFlushPreservesPostTurnQueueOrder(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "first steering message"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated.input = "second steering message"

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	updated.input = "queued after turn"

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 2 {
		t.Fatalf("expected two queued steering messages, got %+v", updated.pendingInjected)
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "queued after turn" {
		t.Fatalf("expected normal queued input preserved, got %+v", updated.queued)
	}

	next, _ = updated.Update(projectedRuntimeEventMsg(runtime.Event{
		Kind:                         runtime.EventUserMessageFlushed,
		UserMessage:                  "first steering message\n\nsecond steering message",
		UserMessageBatch:             []string{"first steering message", "second steering message"},
		UserMessageBatchQueueItemIDs: queuedUserMessageIDsForTest(updated.pendingInjected),
	}))
	updated = next.(*uiModel)
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected steering queue cleared after batched flush, got %+v", updated.pendingInjected)
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "queued after turn" {
		t.Fatalf("expected post-turn queue preserved until turn completion, got %+v", updated.queued)
	}

	next, cmd := updated.Update(submitDoneMsg{})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected post-turn queue to start draining after turn completion")
	}
	if !updated.isBusy() {
		t.Fatal("expected queued post-turn input to begin submission after steering flush")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected post-turn queue drained in order, got %+v", updated.queued)
	}
}

func TestQueuedSubmitKeepsActiveQueuedMessageAheadOfLaterQueueItems(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.queued = queuedInputsForTest("first queued", "second queued", "third queued")

	next, cmd := m.inputController().flushQueuedInputs(queueDrainAuto)
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected first queued message to start submission")
	}
	msgs := collectCmdMessages(t, cmd)
	var done submitDoneMsg
	foundDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			done = typed
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected submit completion for original queued message, got %+v", msgs)
	}
	if done.submittedText != "first queued" || client.submitText != "first queued" {
		t.Fatalf("submitted text = (%q, %q), want first queued before later queued items", done.submittedText, client.submitText)
	}
	next, _ = updated.Update(done)
	updated = next.(*uiModel)
	if len(updated.queued) != 2 || updated.queued[0].Text != "second queued" || updated.queued[1].Text != "third queued" {
		t.Fatalf("expected later queued messages preserved in order, got %+v", updated.queued)
	}
}

func TestDirectSubmitQueuesBehindExistingVisibleMessages(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.queued = queuedInputsForTest("first queued")
	m.input = "direct submit"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected existing queued message to start before direct submit")
	}
	msgs := collectCmdMessages(t, cmd)
	foundSubmit := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			foundSubmit = true
			if typed.submittedText != "first queued" {
				t.Fatalf("submitted text = %q, want first queued", typed.submittedText)
			}
		}
	}
	if !foundSubmit {
		t.Fatalf("expected submit for existing queued message, got %+v", msgs)
	}
	if updated.activeSubmit.text != "first queued" {
		t.Fatalf("active submit text = %q, want first queued", updated.activeSubmit.text)
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "direct submit" {
		t.Fatalf("expected direct submit to remain queued behind active first message, got %+v", updated.queued)
	}
}

func TestRuntimeClientDirectSubmitDoesNotRemainQueued(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.input = "direct submit"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	_ = collectCmdMessages(t, cmd)
	if updated.activeSubmit.text != "direct submit" {
		t.Fatalf("active submit text = %q, want direct submit", updated.activeSubmit.text)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("direct submit should not also remain queued, got %+v", updated.queued)
	}
	if client.submitText != "direct submit" {
		t.Fatalf("submitted text = %q, want direct submit", client.submitText)
	}
}

func TestRuntimeClientDirectSubmitInterruptRestoresInputWithoutQueueing(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.input = "direct submit"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.activeSubmit.text != "direct submit" {
		t.Fatalf("active submit text = %q, want direct submit", updated.activeSubmit.text)
	}
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated = next.(*uiModel)
	updated = applyInterruptedRunStateForTest(t, updated)

	if updated.input != "direct submit" {
		t.Fatalf("expected interrupted direct submit restored into input, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("interrupted direct submit should not enter visible queue, got %+v", updated.queued)
	}
	if client.interruptCalls != 0 {
		t.Fatalf("interrupt calls during Update = %d, want 0", client.interruptCalls)
	}
	_ = collectCmdMessages(t, cmd)
	if client.interruptCalls != 1 {
		t.Fatalf("interrupt calls after command = %d, want 1", client.interruptCalls)
	}
}

func TestRuntimeIdleEventResumesVisibleQueuedMessagesWithoutBlankEnter(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("first queued", "second queued")
	client.transcript = clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  4,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "done"}},
	}

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()},
	}})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected runtime idle event to start queued drain hydration")
	}
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued drain armed after runtime idle event")
	}
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	foundRefresh := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			foundRefresh = true
		}
	}
	if !foundRefresh {
		t.Fatalf("expected queued drain transcript refresh, got %+v", msgs)
	}

	next, cmd = updated.Update(refresh)
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued message to submit after hydration")
	}
	_ = collectCmdMessages(t, cmd)
	if updated.activeSubmit.text != "first queued" {
		t.Fatalf("active submit text = %q, want first queued without blank Enter", updated.activeSubmit.text)
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "second queued" {
		t.Fatalf("expected active first queued item removed from visible queue while preserving later items, got %+v", updated.queued)
	}
}

func TestRuntimeIdleEventDoesNotDuplicatePendingQueuedDrainHydration(t *testing.T) {
	client := &runtimeControlFakeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("first queued", "second queued")
	m.pendingQueuedDrainAfterHydration = true
	m.queuedDrainReadyAfterHydration = false

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()},
	}})
	updated := next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected existing queued drain hydration to stay armed")
	}
	if len(updated.queued) != 2 || updated.queued[0].Text != "first queued" || updated.queued[1].Text != "second queued" {
		t.Fatalf("expected queued messages preserved without duplicate drain, got %+v", updated.queued)
	}
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect duplicate queued drain transcript refresh, got %+v", msgs)
		}
	}
}

type countingTurnQueueHook struct {
	projected int
	drained   int
	aborted   int
}

func (h *countingTurnQueueHook) OnProjectedRuntimeEvent(clientui.Event) { h.projected++ }
func (h *countingTurnQueueHook) OnTurnQueueDrained()                    { h.drained++ }
func (h *countingTurnQueueHook) OnTurnQueueAborted()                    { h.aborted++ }
func (h *countingTurnQueueHook) OnUserCompactionCompleted(bool)         {}

func TestRuntimeIdleQueuedDrainNotifiesTurnQueueHookOnce(t *testing.T) {
	client := &runtimeControlFakeClient{}
	hook := &countingTurnQueueHook{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUITurnQueueHook(hook))
	m.startupCmds = nil
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("follow up")
	client.transcript = clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  4,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "done"}},
	}

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()},
	}})
	updated := next.(*uiModel)
	msgs := collectCmdMessages(t, cmd)
	var refresh runtimeTranscriptRefreshedMsg
	foundRefresh := false
	for _, msg := range msgs {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
			foundRefresh = true
		}
	}
	if !foundRefresh {
		t.Fatalf("expected queued drain transcript refresh, got %+v", msgs)
	}
	if hook.drained != 0 || hook.aborted != 0 {
		t.Fatalf("hook counts before queued turn starts: drained=%d aborted=%d", hook.drained, hook.aborted)
	}

	next, cmd = updated.Update(refresh)
	updated = next.(*uiModel)
	msgs = collectCmdMessages(t, cmd)
	var done submitDoneMsg
	foundDone := false
	for _, msg := range msgs {
		if typed, ok := msg.(submitDoneMsg); ok {
			done = typed
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected queued follow-up submit, got %+v", msgs)
	}
	if hook.drained != 0 || hook.aborted != 0 {
		t.Fatalf("hook counts while queued turn is running: drained=%d aborted=%d", hook.drained, hook.aborted)
	}

	next, _ = updated.Update(done)
	updated = next.(*uiModel)
	if hook.drained != 1 || hook.aborted != 0 {
		t.Fatalf("hook counts after queued drain: drained=%d aborted=%d", hook.drained, hook.aborted)
	}
	if updated.isBusy() || len(updated.queued) != 0 {
		t.Fatalf("expected queue drained and idle, busy=%t queued=%+v", updated.isBusy(), updated.queued)
	}
}

func TestBusyEnterWithUserShellPrefixQueuesInsteadOfInjecting(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "$ pwd"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit lock for queued user shell command")
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("did not expect pending injected messages, got %d", len(updated.pendingInjected))
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "$ pwd" {
		t.Fatalf("expected queued raw user shell input, got %+v", updated.queued)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after queueing user shell command, got %q", updated.input)
	}
}

func TestSubmitErrorRestoresQueuedSteeringInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "please continue with tests"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect input submit lock after enter while busy")
	}
	if len(updated.pendingInjected) != 1 {
		t.Fatalf("expected one pending injected message, got %d", len(updated.pendingInjected))
	}

	updated.queued = append(updated.queued, queuedInputsForTest("follow-up")...)
	next, cmd := updated.Update(submitDoneMsg{err: errors.New("network failure")})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected status clear timer command")
	}
	if updated.isBusy() {
		t.Fatal("did not expect busy after submission error")
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit lock after submission error")
	}
	if updated.input != "please continue with tests\n\nfollow-up" {
		t.Fatalf("expected queued steering and queued drafts restored into input, got %q", updated.input)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("submit error without runtime must not create transcript entries: %+v", updated.transcriptEntries)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injection queue cleared after restore, got %d", len(updated.pendingInjected))
	}
}

func TestSubmitErrorRestoresQueuedSteeringAndDiscardsEngineQueue(t *testing.T) {
	client := &requestCaptureFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	_, eng := newAppRuntimeEngine(t, client, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.input = "restored steering"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	next, _ = updated.Update(submitDoneMsg{err: errors.New("network failure")})
	updated = next.(*uiModel)

	if updated.input != "restored steering" {
		t.Fatalf("expected steering restored into input, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected UI pending steering cleared after restore, got %+v", updated.pendingInjected)
	}

	msg, err := eng.SubmitUserMessage(context.Background(), "fresh prompt")
	if err != nil {
		t.Fatalf("submit fresh prompt: %v", err)
	}
	if msg.Content != "ok" {
		t.Fatalf("assistant content = %q, want ok", msg.Content)
	}
	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected one model request without stale runtime steering, got %d", len(requests))
	}
	for _, message := range llm.MessagesFromItems(requests[0].Items) {
		if message.Role == llm.RoleUser && message.Content == "restored steering" {
			t.Fatalf("did not expect restored steering to remain queued in runtime request: %+v", llm.MessagesFromItems(requests[0].Items))
		}
	}
}

func TestBusyTabQueuesPostTurnSubmissionAndKeepsInputUnlocked(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "queue this"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 {
		t.Fatalf("expected one queued post-turn message, got %d", len(updated.queued))
	}
	if updated.queued[0].Text != "queue this" {
		t.Fatalf("unexpected queued message: %q", updated.queued[0].Text)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("did not expect injected steering message, got %d", len(updated.pendingInjected))
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after tab while busy, got %q", updated.input)
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit lock for tab queue")
	}
}

func TestQueueInjectedInputIgnoresBlankTextWithoutClearingInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "keep this draft"
	m.activity = uiActivityIdle

	m.queueInjectedInput("   \n\t  ")

	if m.input != "keep this draft" {
		t.Fatalf("expected blank injected input to leave draft untouched, got %q", m.input)
	}
	if len(m.pendingInjected) != 0 {
		t.Fatalf("expected no queued injected messages, got %+v", m.pendingInjected)
	}
	if m.activity != uiActivityIdle {
		t.Fatalf("expected blank injected input to leave activity unchanged, got %q", m.activity)
	}
}

func TestCtrlCWhileBusyRestoresQueuedMessagesIntoInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.queued = queuedInputsForTest("first queued", "second queued", "third queued")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to mark pending interrupt")
	}
	updated = applyInterruptedRunStateForTest(t, updated)

	if updated.isBusy() {
		t.Fatal("expected busy=false after ctrl+c interrupt")
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued list to be restored into input and cleared, got %d", len(updated.queued))
	}
	if updated.input != "first queued\n\nsecond queued\n\nthird queued" {
		t.Fatalf("unexpected restored input text: %q", updated.input)
	}
	if updated.inputCursor != -1 {
		t.Fatalf("expected cursor moved to tail after restore, got %d", updated.inputCursor)
	}
}

func TestCtrlCWhileBusyRestoresQueuedSlashCommandsIntoInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.queued = queuedInputsForTest("/name queued title")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to mark pending interrupt")
	}
	updated = applyInterruptedRunStateForTest(t, updated)

	if updated.isBusy() {
		t.Fatal("expected busy=false after ctrl+c interrupt")
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued slash command restored into input and cleared, got %+v", updated.queued)
	}
	if updated.input != "/name queued title" {
		t.Fatalf("expected queued slash command restored into input, got %q", updated.input)
	}
}

func TestCtrlCWhileBusyRestoresMixedQueuedInputsIntoInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.queued = queuedInputsForTest("draft one", "draft two", "/name queued title", "later draft")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to mark pending interrupt")
	}
	updated = applyInterruptedRunStateForTest(t, updated)

	if updated.input != "draft one\n\ndraft two\n\n/name queued title\n\nlater draft" {
		t.Fatalf("expected all queued inputs restored into input, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queue cleared after restore, got %+v", updated.queued)
	}
}

func TestCtrlCWhileBusyUnlocksSubmitLockedInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.setInputSubmitLocked(true)
	m.lockedInjectText = "keep this message"
	m.lockedInjectID = "queue-test-0"
	m.pendingInjected = queuedUserMessagesForTest("keep this message", "another")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to mark pending interrupt")
	}
	updated = applyInterruptedRunStateForTest(t, updated)

	if updated.isInputSubmitLocked() {
		t.Fatal("expected ctrl+c to unlock input")
	}
	if updated.lockedInjectText != "" {
		t.Fatalf("expected lockedInjectText cleared, got %q", updated.lockedInjectText)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injected queue restored into input and cleared, got %+v", updated.pendingInjected)
	}
	if updated.input != "another" {
		t.Fatalf("expected remaining queued steering restored into input, got %q", updated.input)
	}
}

func TestCtrlCRestoresQueuedSteeringAndDiscardsEngineQueue(t *testing.T) {
	client := &requestCaptureFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	_, eng := newAppRuntimeEngine(t, client, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.input = "restored steering"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated = next.(*uiModel)
	if !updated.hasPendingInterrupt() {
		t.Fatal("expected ctrl+c to mark pending interrupt")
	}
	updated = applyInterruptedRunStateForTest(t, updated)

	if updated.input != "restored steering" {
		t.Fatalf("expected steering restored into input, got %q", updated.input)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected UI pending steering cleared after restore, got %+v", updated.pendingInjected)
	}

	msg, err := eng.SubmitUserMessage(context.Background(), "fresh prompt")
	if err != nil {
		t.Fatalf("submit fresh prompt: %v", err)
	}
	if msg.Content != "ok" {
		t.Fatalf("assistant content = %q, want ok", msg.Content)
	}
	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected one model request without stale runtime steering, got %d", len(requests))
	}
	for _, message := range llm.MessagesFromItems(requests[0].Items) {
		if message.Role == llm.RoleUser && message.Content == "restored steering" {
			t.Fatalf("did not expect restored steering to remain queued in runtime request: %+v", llm.MessagesFromItems(requests[0].Items))
		}
	}
}

func TestInterruptedSubmitDoneRestoresQueueIntoInputAndDoesNotAutoDrain(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.queued = queuedInputsForTest("first", "second")

	next, cmd := m.Update(submitDoneMsg{err: runtimeattach.ErrSubmissionInterrupted})
	updated := next.(*uiModel)

	if cmd != nil {
		t.Fatal("did not expect follow-up submission command after interruption")
	}
	if updated.isBusy() {
		t.Fatal("expected busy=false after interrupted submit completion")
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queue restored into input and cleared, got %d", len(updated.queued))
	}
	if updated.input != "first\n\nsecond" {
		t.Fatalf("unexpected restored input text: %q", updated.input)
	}
	plain := stripANSIAndTrimRight(updated.View())
	if count := strings.Count(strings.ToLower(plain), "interrupted"); count != 1 {
		t.Fatalf("expected interruption rendered only as status state, got count=%d view=%q", count, plain)
	}
}

func TestInterruptedSubmitDoneRunsQueuedRuntimeDiscardCleanup(t *testing.T) {
	client := &runtimeControlFakeClient{discardQueuedResult: true}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.setBusy(true)
	m.setInputSubmitLocked(true)
	m.lockedInjectID = "server-queue-1"
	m.pendingInjected = []clientui.QueuedUserMessage{{ID: "server-queue-1", Text: "restore me"}}

	next, cmd := m.Update(submitDoneMsg{err: runtimeattach.ErrSubmissionInterrupted})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued runtime discard cleanup command")
	}
	if updated.isInputSubmitLocked() {
		t.Fatal("expected submit lock released after interrupted completion")
	}
	if updated.input != "" {
		t.Fatalf("did not expect locked submitted input restored, got %q", updated.input)
	}
	_ = collectCmdMessages(t, cmd)
	if client.discardQueuedCalls != 1 || client.discardQueuedID != "server-queue-1" {
		t.Fatalf("expected runtime queued item discarded, calls=%d id=%q", client.discardQueuedCalls, client.discardQueuedID)
	}
}

func TestInterruptedSubmitDoneDoesNotRestoreFlushedSubmittedText(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.activeSubmit = activeSubmitState{token: 7, text: "already flushed"}

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		StepID:   "step-1",
		RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)},
	}})
	updated := next.(*uiModel)

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		UserMessage:                "already flushed",
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "already flushed"}},
	}})
	updated = next.(*uiModel)

	next, _ = updated.Update(submitDoneMsg{token: 7, submittedText: "already flushed", err: runtimeattach.ErrSubmissionInterrupted})
	updated = next.(*uiModel)

	if updated.input != "" {
		t.Fatalf("did not expect already-flushed submitted text restored into input, got %q", updated.input)
	}
	if got := len(updated.transcriptEntries); got != 1 {
		t.Fatalf("expected one committed transcript entry, got %d", got)
	}
	if updated.transcriptEntries[0].Text != "already flushed" {
		t.Fatalf("unexpected transcript entry: %+v", updated.transcriptEntries[0])
	}
}

func TestDelayedMatchingUserFlushFromOldStepDoesNotMarkActiveSubmitFlushed(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.activeSubmit = activeSubmitState{token: 11, stepID: "new-step", text: "repeat"}

	m.markActiveSubmitFlushed(clientui.Event{
		Kind:        clientui.EventUserMessageFlushed,
		StepID:      "old-step",
		UserMessage: "repeat",
	})

	if m.activeSubmit.flushed {
		t.Fatal("old-step flush with matching text marked new active submit flushed")
	}
}

func TestStaleSubmitDoneAfterInterruptDoesNotRestoreSubmittedText(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.activeSubmit = activeSubmitState{token: 9, text: "previous"}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated := next.(*uiModel)
	if updated.input != "" {
		t.Fatalf("did not expect ctrl+c to restore active submitted text, got %q", updated.input)
	}
	updated = applyInterruptedRunStateForTest(t, updated)

	next, _ = updated.Update(submitDoneMsg{token: 9, submittedText: "previous", err: runtimeattach.ErrSubmissionInterrupted})
	updated = next.(*uiModel)
	if updated.input != "" {
		t.Fatalf("stale submit completion restored submitted text, got %q", updated.input)
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
}

func TestVerboseReviewerSuggestionsStaySingleAfterInterruptAndNextSubmit(t *testing.T) {
	suggestions := "Supervisor suggested:\n1. Add final verification notes."
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.activeSubmit = activeSubmitState{token: 21, text: suggestions}

	events := []clientui.Event{
		{Kind: clientui.EventRunStateChanged, StepID: "step-1", RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}},
		{
			Kind:                       clientui.EventUserMessageFlushed,
			StepID:                     "step-1",
			CommittedTranscriptChanged: true,
			TranscriptRevision:         1,
			CommittedEntryStart:        0,
			CommittedEntryStartSet:     true,
			UserMessage:                suggestions,
			TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: suggestions}},
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
				Text:          suggestions,
				CondensedText: "Supervisor made 1 suggestion.",
			}},
		},
		{Kind: clientui.EventAssistantDelta, StepID: "step-1", AssistantDelta: "applying suggestion"},
	}
	var next tea.Model = m
	for _, evt := range events {
		next, _ = next.(*uiModel).Update(runtimeEventMsg{event: evt})
	}
	updated := next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	updated = next.(*uiModel)
	next, _ = updated.Update(submitDoneMsg{token: 21, submittedText: suggestions, err: runtimeattach.ErrSubmissionInterrupted})
	updated = next.(*uiModel)
	if strings.Contains(updated.input, suggestions) {
		t.Fatalf("stale reviewer suggestions were restored into input: %q", updated.input)
	}

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		StepID:   "step-2",
		RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)},
	}})
	updated = next.(*uiModel)
	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		StepID:                     "step-2",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		UserMessage:                "next request",
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "next request"}},
	}})
	updated = next.(*uiModel)

	count := 0
	for _, entry := range updated.transcriptEntries {
		if entry.Role == "reviewer_suggestions" && entry.Text == suggestions {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected reviewer suggestions once after interrupt and next submit, got %d entries=%+v", count, updated.transcriptEntries)
	}
}

func TestSubmitErrorRestoresPendingInjectedSubmittedAndQueuedInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.pendingInjected = queuedUserMessagesForTest("steer")
	m.queued = queuedInputsForTest("queued")

	next, cmd := m.Update(submitDoneMsg{submittedText: "sent", err: errors.New("transport down")})
	updated := next.(*uiModel)

	if cmd == nil {
		t.Fatal("expected status clear timer command")
	}
	if updated.isBusy() {
		t.Fatal("expected busy=false after failed submit")
	}
	if updated.input != "steer\n\nsent\n\nqueued" {
		t.Fatalf("expected full rollback into input, got %q", updated.input)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("submit error without runtime must not create transcript entries: %+v", updated.transcriptEntries)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injected cleared after rollback, got %+v", updated.pendingInjected)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued drafts restored into input, got %+v", updated.queued)
	}
	if updated.activity != uiActivityError {
		t.Fatalf("expected error activity, got %v", updated.activity)
	}
}

func TestSubmitErrorRestoresQueuedDraftsIntoInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.queued = queuedInputsForTest("submitted", "queued later")
	m.activeSubmit = activeSubmitState{token: 1, text: "submitted", queuedID: m.queued[0].ID}

	next, _ := m.Update(submitDoneMsg{token: 1, submittedText: "submitted", err: errors.New("submit failed")})
	updated := next.(*uiModel)

	if updated.input != "submitted\n\nqueued later" {
		t.Fatalf("expected submit rollback to restore current and queued drafts, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued drafts restored into input, got %+v", updated.queued)
	}
}

func TestSubmitCancellationRestoresQueuedDraftsWithoutErrorEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("submitted", "queued later")
	m.activeSubmit = activeSubmitState{token: 1, text: "submitted", queuedID: m.queued[0].ID}

	next, _ := m.Update(submitDoneMsg{token: 1, submittedText: "submitted", err: context.Canceled})
	updated := next.(*uiModel)

	if updated.input != "submitted\n\nqueued later" {
		t.Fatalf("expected submit cancellation to restore current and queued drafts, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued drafts restored into input, got %+v", updated.queued)
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("did not expect transcript error entry, got %+v", updated.transcriptEntries)
	}
}

func TestCompactFailureRestoresQueuedDraftsIntoInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.setCompacting(true)
	m.queued = queuedInputsForTest("queued later")

	next, _ := m.Update(compactDoneMsg{err: errors.New("compact failed")})
	updated := next.(*uiModel)

	if updated.input != "queued later" {
		t.Fatalf("expected compaction rollback to restore current and queued drafts, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued drafts restored into input, got %+v", updated.queued)
	}
}

func TestCompactCancellationRestoresQueuedDraftsWithoutErrorEntry(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("queued later")

	next, _ := m.Update(compactDoneMsg{err: context.Canceled})
	updated := next.(*uiModel)

	if updated.input != "queued later" {
		t.Fatalf("expected compaction cancellation to restore current and queued drafts, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued drafts restored into input, got %+v", updated.queued)
	}
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity, got %v", updated.activity)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("did not expect transcript error entry, got %+v", updated.transcriptEntries)
	}
}

func TestCompactDoneKeepsQueuedSteeringPending(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.input = "please continue with tests"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect input submit lock after enter while busy")
	}
	if len(updated.pendingInjected) != 1 {
		t.Fatalf("expected one pending injected message, got %d", len(updated.pendingInjected))
	}

	next, _ = updated.Update(compactDoneMsg{})
	updated = next.(*uiModel)
	if updated.isInputSubmitLocked() {
		t.Fatal("did not expect submit lock after compaction completion")
	}
	if len(updated.pendingInjected) != 1 || updated.pendingInjected[0].Text != "please continue with tests" {
		t.Fatalf("expected queued steering preserved across compaction completion, got %+v", updated.pendingInjected)
	}
}

func TestCompactDoneSurfacesQueuedRuntimeWorkProbeFailure(t *testing.T) {
	client := &runtimeControlFakeClient{err: errors.New("daemon stalled")}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning

	next, cmd := m.Update(compactDoneMsg{})
	updated := next.(*uiModel)
	updated, cmd = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if updated.activity != uiActivityError {
		t.Fatalf("expected error activity after queued runtime work probe failure, got %v", updated.activity)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("did not expect immediate local error entry with probe failure, got %+v", updated.transcriptEntries)
	}
	if client.appendedRole != "" || client.appendedText != "" {
		t.Fatalf("did not expect runtime append during Update, got role=%q text=%q", client.appendedRole, client.appendedText)
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if client.appendedRole != string(transcript.EntryRoleDeveloperErrorFeedback) || !strings.Contains(client.appendedText, "daemon stalled") {
		t.Fatalf("expected runtime error entry with probe failure, role=%q text=%q", client.appendedRole, client.appendedText)
	}
	if len(updated.transcriptEntries) != 0 {
		t.Fatalf("runtime append failure must not create local transcript entries: %+v", updated.transcriptEntries)
	}
	if committed := committedTranscriptEntriesForApp(updated.transcriptEntries); len(committed) != 0 {
		t.Fatalf("runtime append failure advanced committed transcript entries: %+v", committed)
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("expected runtime append failure status, got status=%q kind=%v", updated.transientStatus, updated.transientStatusKind)
	}
}

func TestIdleCompactDoneRefreshesCommittedSteeringOutput(t *testing.T) {
	client := &runtimeControlFakeClient{
		sessionView: clientui.RuntimeSessionView{SessionID: "session-1"},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.sessionID = "session-1"
	_, startupCmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	_ = collectCmdMessages(t, startupCmd)
	m.compactionOrigin = uiCompactionOriginManual
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning

	next, checkCmd := m.Update(compactDoneMsg{})
	updated := next.(*uiModel)
	client.transcript = clientui.TranscriptPage{
		SessionID:   "session-1",
		Revision:    1,
		NewerCursor: 1,
		Entries: []clientui.ChatEntry{
			{Role: "compaction_notice", Text: "context compacted for the 1st time"},
		},
	}
	updated, syncCmd := applyQueuedRuntimeWorkCheckForTest(t, updated, checkCmd)
	if syncCmd == nil {
		t.Fatal("expected idle compaction completion to request committed transcript sync")
	}
	for _, msg := range collectCmdMessages(t, syncCmd) {
		next, followCmd := updated.Update(msg)
		updated = next.(*uiModel)
		_ = collectCmdMessages(t, followCmd)
	}
	if client.hasQueuedUserWorkCalls != 1 {
		t.Fatalf("HasQueuedUserWork calls = %d, want 1", client.hasQueuedUserWorkCalls)
	}
	if client.refreshTranscriptCalls != 1 {
		t.Fatalf("RefreshTranscriptPage calls = %d, want 1", client.refreshTranscriptCalls)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if !strings.Contains(plain, "context compacted") {
		t.Fatalf("idle compaction did not render committed steering output, rendered=%q", plain)
	}
	if got := strings.Count(plain, "context compacted"); got != 1 {
		t.Fatalf("idle compaction rendered steering output %d times, want once", got)
	}
}

func TestCompactDoneSuppressesQueuedRuntimeWorkProbeCancellation(t *testing.T) {
	client := &runtimeControlFakeClient{hasQueuedUserWorkErr: context.Canceled}
	m := newProjectedStaticUIModel()
	m.engine = client
	m.setBusy(true)
	m.setCompacting(true)
	m.activity = uiActivityRunning

	next, cmd := m.Update(compactDoneMsg{})
	updated := next.(*uiModel)
	updated, _ = applyQueuedRuntimeWorkCheckForTest(t, updated, cmd)
	if updated.activity != uiActivityInterrupted {
		t.Fatalf("expected interrupted activity after queued runtime work probe cancellation, got %v", updated.activity)
	}
	if client.appendedRole != "" || client.appendedText != "" {
		t.Fatalf("did not expect appended runtime error entry, role=%q text=%q", client.appendedRole, client.appendedText)
	}
}

func TestCalcChatLinesShrinksWhenInputWraps(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 20
	m.termHeight = 12

	m.input = "short"
	chatShort := m.layout().calcChatLines()

	m.input = strings.Repeat("x", 120)
	chatLong := m.layout().calcChatLines()

	if chatLong >= chatShort {
		t.Fatalf("expected wrapped input to reduce chat lines: short=%d long=%d", chatShort, chatLong)
	}
}

func TestCalcChatLinesUsesFullHeightInDetailMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 20
	m.termHeight = 12
	m.input = strings.Repeat("x", 80)
	m.queued = queuedInputsForTest("one", "two", "three", "four", "five", "six")
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	base := m.layout().calcChatLines()
	if base >= m.termHeight-1 {
		t.Fatalf("expected ongoing chat lines to reserve non-chat panes, got %d", base)
	}

	m.forwardToView(tui.ToggleModeMsg{})
	detail := m.layout().calcChatLines()
	if detail != m.termHeight-1 {
		t.Fatalf("expected detail chat lines to use full height minus status line: got %d want %d", detail, m.termHeight-1)
	}
}

func TestCalcChatLinesRemainsViewportBasedDuringActiveWork(t *testing.T) {
	store := createAppRuntimeSession(t)
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, store, statusLineFakeClient{}, runtime.Config{})
	m := newProjectedEngineUIModel(eng)
	m.termWidth = 100
	m.termHeight = 24
	m.setBusy(true)
	m.sawAssistantDelta = true

	if got := m.layout().calcChatLines(); got <= 1 {
		t.Fatalf("expected viewport-based ongoing mode to keep multi-line chat area during active work, got %d", got)
	}
}
