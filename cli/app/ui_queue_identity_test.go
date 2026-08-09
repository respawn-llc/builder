package app

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func queuedUserMessagesForTest(texts ...string) []clientui.QueuedUserMessage {
	messages := make([]clientui.QueuedUserMessage, 0, len(texts))
	for index, text := range texts {
		messages = append(messages, clientui.QueuedUserMessage{ID: fmt.Sprintf("queue-test-%d", index), Text: text})
	}
	return messages
}

func queuedInputsForTest(texts ...string) []queuedInputItem {
	items := make([]queuedInputItem, 0, len(texts))
	for index, text := range texts {
		items = append(items, queuedInputItem{ID: fmt.Sprintf("input-queue-test-%d", index), Text: text})
	}
	return items
}

func applyFirstInjectedQueueCreateDoneForTest(t *testing.T, m *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			next, _ := m.Update(typed)
			updated, ok := next.(*uiModel)
			if !ok {
				t.Fatalf("updated model = %T, want *uiModel", next)
			}
			return updated
		}
	}
	return m
}

func TestBusyEnterUsesAuthoritativeSubmitPath(t *testing.T) {
	client := &runtimeControlFakeClient{submitQueuedID: "busy-submit-queue"}
	model := newProjectedTestUIModel(client)
	model.setRuntimeActivityBusyForTest(true)
	testSetMainInput(model, "steer while thinking")

	next, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if command == nil {
		t.Fatal("busy Enter produced no runtime command")
	}
	for _, message := range collectCmdMessages(t, command) {
		model = updateUIModel(t, model, message)
	}

	if client.submitText != "steer while thinking" {
		t.Fatalf("submitted text = %q, want authoritative submit", client.submitText)
	}
	if client.submitCalls != 1 {
		t.Fatalf("authoritative submit calls = %d, want 1", client.submitCalls)
	}
}

func TestInjectedQueueCreateConnectionFailureRestoresDraftWithoutTranscriptEntry(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{submitErr: io.EOF}
	model := newProjectedTestUIModel(client)
	model.setRuntimeActivityBusyForTest(true)
	beforeActivity := model.activity

	submitCmd := model.queueInjectedInput("  failed steering  ")
	testSetMainInput(model, "newer draft")
	if len(model.pendingInjected) != 1 || len(model.injectedQueue) != 1 {
		t.Fatalf("queued input state = pending %d, queue %d; want one matching item", len(model.pendingInjected), len(model.injectedQueue))
	}

	var createDone injectedQueueCreateDoneMsg
	foundCreateDone := false
	for _, msg := range collectCmdMessages(t, submitCmd) {
		if typed, ok := msg.(injectedQueueCreateDoneMsg); ok {
			createDone = typed
			foundCreateDone = true
			break
		}
	}
	if !foundCreateDone {
		t.Fatal("queue submission produced no create completion")
	}

	next, returnedCmd := model.Update(createDone)
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, returnedCmd) {
		updated = updateUIModel(t, updated, msg)
	}

	if got, want := testMainInput(updated), "newer draft\n\nfailed steering"; got != want {
		t.Fatalf("restored composer text = %q, want %q", got, want)
	}
	wantInput := "newer draft\n\nfailed steering"
	if got, want := testMainInputRuneCursor(updated), len([]rune(wantInput)); got != want {
		t.Fatalf("composer cursor = %d, want %d", got, want)
	}
	if len(updated.pendingInjected) != 0 || len(updated.injectedQueue) != 1 ||
		updated.injectedQueue[0].State != injectedRuntimeQueueCreateFailed ||
		!updated.injectedQueue[0].RecoveryOwned {
		t.Fatalf("failed queue recovery = pending %+v queue %+v, want one active recovery owner", updated.pendingInjected, updated.injectedQueue)
	}
	if updated.activity != beforeActivity {
		t.Fatalf("activity = %v, want pre-failure activity %v", updated.activity, beforeActivity)
	}
	if client.appendCalls != 0 {
		t.Fatalf("committed-entry calls = %d, want 0", client.appendCalls)
	}
	if client.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", client.submitCalls)
	}

	activeNotice := ansi.Strip(updated.layout().renderStatusNotice(statusLineUnboundedWidth))
	if want := runtimeattach.FormatSubmissionError(io.EOF); activeNotice != want {
		t.Fatalf("active status notice = %q, want %q", activeNotice, want)
	}
	activeToken := updated.transientStatusToken
	updated = updateUIModel(t, updated, clearTransientStatusMsg{token: activeToken})
	if got, want := ansi.Strip(updated.layout().renderStatusNotice(statusLineUnboundedWidth)), runtimeDisconnectedStatusMessage; got != want {
		t.Fatalf("cleared status notice = %q, want %q", got, want)
	}
}

func TestAllowCommentaryQueueCreateConnectionFailureAnswersIndependently(t *testing.T) {
	client := &runtimeControlFakeClient{submitErr: io.EOF}
	model := newProjectedTestUIModel(client)
	control := newRecordingPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control).withConnectionOutcomeSink(func(err error) {
		enqueueRuntimeConnectionStateChange(model.runtimeConnectionEvents, err)
	})
	if model.runtimeConnectionEvents == nil {
		model.runtimeConnectionEvents = make(chan runtimeConnectionStateChangedMsg, 1)
	}
	model.setRuntimeActivityBusyForTest(true)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testApprovalPrompt(
			"allow-commentary-create-failure",
			"Allow access?",
			clientui.ApprovalDecisionAllowOnce,
			clientui.ApprovalDecisionDeny,
		),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("failed commentary")})

	next, queueCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if queueCmd == nil || !model.ask.answerPending {
		t.Fatal("allow commentary did not enter the queue stage")
	}
	queueResult := queueCmd()
	queueMsg, ok := queueResult.(injectedQueueCreateDoneMsg)
	if !ok {
		t.Fatalf("queue completion = %T, want injectedQueueCreateDoneMsg", queueResult)
	}

	expiryStarted := make(chan struct{})
	releaseExpiry := make(chan struct{})
	var expiryOnce sync.Once
	previousSchedule := scheduleTransientStatusClear
	scheduleTransientStatusClear = func(_ time.Duration, token uint64) tea.Cmd {
		return func() tea.Msg {
			expiryOnce.Do(func() { close(expiryStarted) })
			<-releaseExpiry
			return clearTransientStatusMsg{token: token}
		}
	}
	t.Cleanup(func() {
		scheduleTransientStatusClear = previousSchedule
	})

	next, returnedCmd := model.Update(queueMsg)
	model = next.(*uiModel)
	if got, want := testMainInput(model), "failed commentary"; got != want {
		t.Fatalf("restored commentary = %q, want %q", got, want)
	}
	if len(model.pendingInjected) != 0 || len(model.injectedQueue) != 1 ||
		model.injectedQueue[0].State != injectedRuntimeQueueCreateFailed ||
		!model.injectedQueue[0].RecoveryOwned {
		t.Fatalf("failed queue recovery = pending %+v queue %+v, want one active recovery owner", model.pendingInjected, model.injectedQueue)
	}
	if model.activity == uiActivityError {
		t.Fatalf("activity = %v, want no failure-owned error label", model.activity)
	}
	if got, want := ansi.Strip(model.layout().renderStatusNotice(statusLineUnboundedWidth)), runtimeattach.FormatSubmissionError(io.EOF); got != want {
		t.Fatalf("active status notice = %q, want %q", got, want)
	}

	if returnedCmd == nil {
		t.Fatal("queue failure returned no independent effects")
	}
	effect := returnedCmd()
	batch, ok := effect.(tea.BatchMsg)
	if !ok {
		t.Fatalf("queue failure effect = %T, want exported tea.BatchMsg", effect)
	}
	results := make(chan tea.Msg, len(batch))
	for _, child := range batch {
		if child == nil {
			continue
		}
		go func(command tea.Cmd) {
			results <- command()
		}(child)
	}
	select {
	case <-expiryStarted:
	case <-time.After(time.Second):
		t.Fatal("transient expiry did not start")
	}

	var approvalRequest serverapi.ApprovalAnswerRequest
	select {
	case approvalRequest = <-control.approvalRequests:
	case <-time.After(time.Second):
		t.Fatal("approval answer did not start while transient expiry was blocked")
	}
	if approvalCommentary(approvalRequest) != "failed commentary" || approvalRequest.Decision != clientui.ApprovalDecisionAllowOnce {
		t.Fatalf("approval request = %+v, want Allow once with failed commentary", approvalRequest)
	}

	remainingResults := len(batch)
	deliveryObserved := false
	for remainingResults > 0 && !deliveryObserved {
		result := <-results
		remainingResults--
		model = updateUIModel(t, model, result)
		_, deliveryObserved = result.(promptAnswerDeliveryResultMsg)
	}
	if !deliveryObserved {
		t.Fatal("approval delivery produced no result")
	}
	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	if model.runtimeDisconnectStatusVisible() {
		t.Fatal("successful approval delivery did not clear the disconnect state")
	}
	if got, want := ansi.Strip(model.layout().renderStatusNotice(statusLineUnboundedWidth)), runtimeattach.FormatSubmissionError(io.EOF); got != want {
		t.Fatalf("status after approval delivery = %q, want transient %q", got, want)
	}

	close(releaseExpiry)
	for remainingResults > 0 {
		model = updateUIModel(t, model, <-results)
		remainingResults--
	}
	if got := ansi.Strip(model.layout().renderStatusNotice(statusLineUnboundedWidth)); got != "" {
		t.Fatalf("status after transient expiry = %q, want empty", got)
	}
	if client.appendCalls != 0 {
		t.Fatalf("committed-entry calls = %d, want 0", client.appendCalls)
	}
	if client.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", client.submitCalls)
	}
}

func TestTranscriptQueuedStateOwnsRestorationAndLocalQueueAdmission(t *testing.T) {
	disableTransientStatusClearForTest(t)
	text := "server queued text"
	for _, test := range []struct {
		status       clientui.QueuedUserMessageStatus
		wantQueued   int
		wantComposer string
	}{
		{status: clientui.QueuedUserMessageAccepted, wantQueued: 1, wantComposer: "existing draft"},
		{status: clientui.QueuedUserMessageSubmitted, wantComposer: "existing draft"},
		{status: clientui.QueuedUserMessageFailed, wantComposer: "existing draft\n\nserver queued text"},
		{status: clientui.QueuedUserMessageDiscarded, wantComposer: "existing draft"},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			client := &runtimeControlFakeClient{}
			model := newProjectedTestUIModel(client)
			testSetMainInput(model, "existing draft")
			requestID := ongoingTestClientRequestID()
			queueID := ongoingTestQueueItemID()
			model.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
				ID: queueID.String(), Text: text, ClientRequestID: requestID.String(),
			})
			if test.status == clientui.QueuedUserMessageSubmitted {
				model.queued = []queuedInputItem{{ID: "local-draft", Text: "local draft"}}
				if cmd := model.inputController().resumeQueuedInputsAfterIdleRuntime(); cmd != nil || len(model.queued) != 1 {
					t.Fatal("local draft advanced before accepted server work reached a terminal state")
				}
			}

			cmd := model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
				ClientRequestID: requestID,
				QueueItemID:     queueID,
				Status:          test.status,
				Text:            &text,
			})
			if test.status == clientui.QueuedUserMessageSubmitted {
				cmd = tea.Batch(cmd, model.inputController().resumeQueuedInputsAfterIdleRuntime())
			}
			for _, msg := range collectCmdMessages(t, cmd) {
				model = updateUIModel(t, model, msg)
			}

			if len(model.pendingInjected) != test.wantQueued || len(model.injectedQueue) != test.wantQueued {
				t.Fatalf("queued state = pending:%d injected:%d, want %d", len(model.pendingInjected), len(model.injectedQueue), test.wantQueued)
			}
			if got := testMainInput(model); got != test.wantComposer {
				t.Fatalf("composer = %q, want %q", got, test.wantComposer)
			}
			if test.status == clientui.QueuedUserMessageSubmitted && client.submitCalls != 1 {
				t.Fatalf("local submit calls = %d, want 1", client.submitCalls)
			}
		})
	}
}

func TestDisconnectedQueuedFlushRestoresTextWithTransientStatus(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(client)
	model.setRuntimeActivityBusyForTest(true)
	model.setRuntimeDisconnected(true)
	model.queued = queuedInputsForTest("queued message")
	model.pendingInjected = []clientui.QueuedUserMessage{{ID: "steer-1", Text: "accepted steer"}}
	model.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:  "steer-1",
		ServerID: "steer-1",
		Text:     "accepted steer",
		State:    injectedRuntimeQueueEnqueued,
	}}
	beforeActivity := model.activity

	next, cmd := model.inputController().flushQueuedInputs(queueDrainOne)
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		updated = updateUIModel(t, updated, msg)
	}

	if got, want := testMainInput(updated), "queued message"; got != want {
		t.Fatalf("restored queued text = %q, want %q", got, want)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("queued items = %d, want 0", len(updated.queued))
	}
	if len(updated.pendingInjected) != 1 || len(updated.injectedQueue) != 1 {
		t.Fatalf("accepted steer queue state = pending %d, queue %d; want one item", len(updated.pendingInjected), len(updated.injectedQueue))
	}
	if updated.activity != beforeActivity {
		t.Fatalf("activity = %v, want pre-failure activity %v", updated.activity, beforeActivity)
	}
	if client.appendCalls != 0 {
		t.Fatalf("committed-entry calls = %d, want 0", client.appendCalls)
	}
	if got, want := updated.transientStatus, runtimeDisconnectedStatusMessage; got != want {
		t.Fatalf("transient status = %q, want %q", got, want)
	}
}
