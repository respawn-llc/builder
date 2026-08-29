package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func queuedInputsForTest(texts ...string) []queuedInputItem {
	items := make([]queuedInputItem, 0, len(texts))
	for index, text := range texts {
		items = append(items, queuedInputItem{
			ID:              fmt.Sprintf("input-queue-test-%d", index),
			Text:            text,
			submissionOrder: inputSubmissionOrder{sequence: uint64(index + 1)},
		})
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
	submittedText := "  steer while\nthinking  "
	testSetMainInput(model, submittedText)

	next, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if command == nil {
		t.Fatal("busy Enter produced no runtime command")
	}
	for _, message := range collectCmdMessages(t, command) {
		model = updateUIModel(t, model, message)
	}

	if client.submitText != submittedText {
		t.Fatalf("submitted text = %q, want verbatim %q", client.submitText, submittedText)
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
	if len(model.injectedQueue) != 1 {
		t.Fatalf("queued input state = %+v, want one item", model.injectedQueue)
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

	if got, want := testMainInput(updated), "newer draft\n\n  failed steering  "; got != want {
		t.Fatalf("restored composer text = %q, want %q", got, want)
	}
	wantInput := "newer draft\n\n  failed steering  "
	if got, want := testMainInputRuneCursor(updated), len([]rune(wantInput)); got != want {
		t.Fatalf("composer cursor = %d, want %d", got, want)
	}
	if len(updated.injectedQueue) != 0 {
		t.Fatalf("failed queue state = %+v, want no items", updated.injectedQueue)
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
	if len(model.injectedQueue) != 0 {
		t.Fatalf("failed queue state = %+v, want no items", model.injectedQueue)
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

	var batchRequest serverapi.PromptAnswerBatchRequest
	select {
	case batchRequest = <-control.batchRequests:
	case <-time.After(time.Second):
		t.Fatal("approval answer did not start while transient expiry was blocked")
	}
	entry := requireApprovalAnswerEntry(t, batchRequest)
	if entry.ApprovalAnswer.Commentary == nil ||
		*entry.ApprovalAnswer.Commentary != "failed commentary" ||
		entry.ApprovalAnswer.Decision != clientui.ApprovalDecisionAllowOnce {
		t.Fatalf("approval request = %+v, want Allow once with failed commentary", batchRequest)
	}

	deliveryResult := <-results
	model = updateUIModel(t, model, deliveryResult)
	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	if model.runtimeDisconnectStatusVisible() {
		t.Fatal("successful approval delivery did not clear the disconnect state")
	}
	if got, want := ansi.Strip(model.layout().renderStatusNotice(statusLineUnboundedWidth)), runtimeattach.FormatSubmissionError(io.EOF); got != want {
		t.Fatalf("status after approval delivery = %q, want transient %q", got, want)
	}

	close(releaseExpiry)
	model = updateUIModel(t, model, <-results)
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

func TestTranscriptQueuedStateOnlyMutatesMatchingLocalRestorationOwnership(t *testing.T) {
	disableTransientStatusClearForTest(t)
	const localText = "  local queued text  "
	serverText := "server queued text"
	for _, test := range []struct {
		status       clientui.QueuedUserMessageStatus
		wantQueued   int
		wantComposer string
	}{
		{status: clientui.QueuedUserMessageAccepted, wantQueued: 1, wantComposer: "existing draft"},
		{status: clientui.QueuedUserMessageSubmitted, wantComposer: "existing draft"},
		{status: clientui.QueuedUserMessageFailed, wantComposer: "existing draft\n\n  local queued text  "},
		{status: clientui.QueuedUserMessageDiscarded, wantComposer: "existing draft"},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			client := &runtimeControlFakeClient{}
			model := newProjectedTestUIModel(client)
			testSetMainInput(model, "existing draft")
			queueID := ongoingTestQueueItemID()
			model.injectedQueue = []injectedRuntimeQueueItem{{
				LocalID:         "local-queue-item",
				ServerID:        queueID.String(),
				Text:            localText,
				State:           injectedRuntimeQueueEnqueued,
				submissionOrder: inputSubmissionOrder{sequence: 1},
			}}
			model.injectedQueue[0].ApprovalCommentaryAnswer = &clientui.PromptAnswer{PromptID: "approval"}
			model.registerSteeredQueuedUserMessage(clientui.QueuedUserMessage{
				ID: queueID.String(), Text: serverText})
			model.pendingWorkRefresh.collection = runtimeinput.PendingWork{}
			if len(model.injectedQueue) != 1 || model.injectedQueue[0].ApprovalCommentaryAnswer == nil {
				t.Fatal("membership replacement settled local queue lifecycle")
			}
			if test.status == clientui.QueuedUserMessageSubmitted {
				model.queued = []queuedInputItem{{ID: "local-draft", Text: "local draft"}}
				if cmd := model.inputController().resumeQueuedInputsAfterIdleRuntime(); cmd != nil || len(model.queued) != 1 {
					t.Fatal("local draft advanced before accepted server work reached a terminal state")
				}
			}

			cmd := model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
				QueueItemID: queueID,
				Status:      test.status,
				Text:        &serverText,
			})
			if test.status == clientui.QueuedUserMessageSubmitted {
				cmd = tea.Batch(cmd, model.inputController().resumeQueuedInputsAfterIdleRuntime())
			}
			for _, msg := range collectCmdMessages(t, cmd) {
				model = updateUIModel(t, model, msg)
			}

			if len(model.injectedQueue) != test.wantQueued {
				t.Fatalf("queued state = %+v, want %d", model.injectedQueue, test.wantQueued)
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

func TestTranscriptQueueStateWaitsForMatchingRPCOwnership(t *testing.T) {
	model := newProjectedTestUIModel(&runtimeControlFakeClient{})
	secondID := runtimeids.NewQueueItemID()
	model.injectedQueue = []injectedRuntimeQueueItem{
		{
			LocalID:         "first-local",
			Text:            "first",
			State:           injectedRuntimeQueuePendingCreate,
			CreateToken:     1,
			submissionOrder: inputSubmissionOrder{sequence: 1},
		},
		{
			LocalID:         "second-local",
			Text:            "second",
			State:           injectedRuntimeQueuePendingCreate,
			CreateToken:     2,
			submissionOrder: inputSubmissionOrder{sequence: 2},
		},
	}

	model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
		QueueItemID: secondID,
		Status:      clientui.QueuedUserMessageAccepted,
	})
	model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
		QueueItemID: secondID,
		Status:      clientui.QueuedUserMessageSubmitted,
	})
	if model.injectedQueue[0].ServerID != "" || model.injectedQueue[1].ServerID != "" {
		t.Fatalf("transcript event guessed pending ownership: %+v", model.injectedQueue)
	}

	next, cmd := model.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{
		token:   2,
		localID: "second-local",
		item:    clientui.QueuedUserMessage{ID: secondID.String(), Text: "second"},
	})
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		model = updateUIModel(t, model, msg)
	}
	if len(model.injectedQueue) != 1 ||
		model.injectedQueue[0].LocalID != "first-local" ||
		model.injectedQueue[0].ServerID != "" ||
		model.injectedQueue[0].State != injectedRuntimeQueuePendingCreate {
		t.Fatalf("terminal second submission mutated first ownership: %+v", model.injectedQueue)
	}
	if len(model.unownedQueuedTerminalStates) != 0 {
		t.Fatalf("terminal state remained after matching RPC ownership: %+v", model.unownedQueuedTerminalStates)
	}
}

func TestInterruptedHumanInputWaitsForMatchingRPCOwnership(t *testing.T) {
	model := newProjectedTestUIModel(&runtimeControlFakeClient{})
	queueID := runtimeids.NewQueueItemID()
	model.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:         "local",
		Text:            "interrupted",
		State:           injectedRuntimeQueuePendingCreate,
		CreateToken:     1,
		submissionOrder: inputSubmissionOrder{sequence: 1},
	}}

	model.applyTranscriptHumanInputInterrupted(clientui.TranscriptHumanInputInterrupted{
		Items: []clientui.TranscriptInterruptedHumanInputItem{{
			QueueItemID: queueID,
			Text:        "interrupted",
		}},
	})
	if len(model.injectedQueue) != 1 || len(model.unownedQueuedTerminalStates) != 1 {
		t.Fatalf("interruption discarded unresolved ownership: queue=%+v terminal=%+v", model.injectedQueue, model.unownedQueuedTerminalStates)
	}

	next, cmd := model.inputController().handleInjectedQueueCreateDone(injectedQueueCreateDoneMsg{
		token:   1,
		localID: "local",
		item:    clientui.QueuedUserMessage{ID: queueID.String(), Text: "interrupted"},
	})
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		model = updateUIModel(t, model, msg)
	}
	if len(model.injectedQueue) != 0 || len(model.unownedQueuedTerminalStates) != 0 {
		t.Fatalf("interrupted ownership left queued ghost: queue=%+v terminal=%+v", model.injectedQueue, model.unownedQueuedTerminalStates)
	}
	if got := testMainInput(model); got != "interrupted" {
		t.Fatalf("restored composer = %q, want interrupted text exactly once", got)
	}
}

func TestDrainedQueueRuntimeCommandNotAcceptedRestoresVerbatimExactlyOnce(t *testing.T) {
	client := &runtimeControlFakeClient{
		submitErr: serverapi.NewRuntimeCommandNotAcceptedError(errors.New("turn was not accepted")),
	}
	model := newProjectedTestUIModel(client)
	text := "  queued\n\tmessage  "
	model.queueInput(text)

	_, cmd := model.inputController().flushQueuedInputs(queueDrainOne)
	var done submitDoneMsg
	found := false
	for _, msg := range collectCmdMessages(t, cmd) {
		if typed, ok := msg.(submitDoneMsg); ok {
			done = typed
			found = true
			break
		}
	}
	if !found {
		t.Fatal("drained Queue submission returned no completion")
	}

	model = updateUIModel(t, model, done)
	if got := testMainInput(model); got != text {
		t.Fatalf("restored composer = %q, want verbatim %q", got, text)
	}
	if len(model.queued) != 0 || len(model.injectedQueue) != 0 {
		t.Fatalf("rejected Queue item remained pending: queued=%+v injected=%+v", model.queued, model.injectedQueue)
	}

	model = updateUIModel(t, model, done)
	if got := testMainInput(model); got != text {
		t.Fatalf("duplicate completion restored Queue item twice: %q", got)
	}
}

func TestDirectRuntimeCommandNotAcceptedRestoresVerbatim(t *testing.T) {
	client := &runtimeControlFakeClient{
		submitErr: serverapi.NewRuntimeCommandNotAcceptedError(errors.New("turn was not accepted")),
	}
	model := newProjectedTestUIModel(client)
	text := "  direct\n\tmessage  "
	testSetMainInput(model, text)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		model = updateUIModel(t, model, msg)
	}

	if got := testMainInput(model); got != text {
		t.Fatalf("restored composer = %q, want verbatim %q", got, text)
	}
}

func TestDisconnectedQueuedFlushRestoresTextWithTransientStatus(t *testing.T) {
	disableTransientStatusClearForTest(t)

	client := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(client)
	model.setRuntimeActivityBusyForTest(true)
	model.setRuntimeDisconnected(true)
	model.queued = queuedInputsForTest("queued message")
	model.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:         "steer-1",
		ServerID:        "steer-1",
		Text:            "accepted steer",
		State:           injectedRuntimeQueueEnqueued,
		submissionOrder: inputSubmissionOrder{sequence: 1},
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
	if len(updated.injectedQueue) != 1 {
		t.Fatalf("accepted steer queue state = %+v, want one item", updated.injectedQueue)
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
