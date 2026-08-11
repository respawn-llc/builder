package app

import (
	"errors"
	"strings"
	"testing"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type compactionTurnQueueHook struct {
	aborted             int
	compactionCompleted int
}

func (h *compactionTurnQueueHook) OnTranscriptMessage(clientui.TranscriptMessage) {}
func (h *compactionTurnQueueHook) OnTurnQueueDrained()                            {}
func (h *compactionTurnQueueHook) OnTurnQueueAborted()                            { h.aborted++ }
func (h *compactionTurnQueueHook) OnUserCompactionCompleted(bool) {
	h.compactionCompleted++
}

func TestDirectCompactWithoutClientRestoresExactSubmittedText(t *testing.T) {
	model := newProjectedStaticUIModel()
	submitted := "  /compact  preserve\n these instructions  "
	testSetMainInput(model, submitted)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	done := compactDoneMessageFromCommand(t, cmd)
	if !errors.Is(done.err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("compact error = %v, want runtime command not accepted", done.err)
	}

	model = updateUIModel(t, model, done)
	if got := testMainInput(model); got != submitted {
		t.Fatalf("restored composer = %q, want exact %q", got, submitted)
	}
}

func TestCompactNotAcceptedUsesOnlyLocalCreationFailurePresentation(t *testing.T) {
	disableTransientStatusClearForTest(t)
	rejection := serverapi.NewRuntimeCommandNotAcceptedError(serverapi.ErrRuntimeUnavailable)
	client := &runtimeControlFakeClient{compactErr: rejection}
	hook := &compactionTurnQueueHook{}
	model := newProjectedTestUIModel(client, WithUITurnQueueHook(hook))
	model.activity = uiActivityRunning
	model.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:  "local-steer",
		ServerID: "server-steer",
		Text:     "existing accepted steer",
		State:    injectedRuntimeQueueEnqueued,
	}}
	model.queueInput("unrelated queued input")
	submitted := "/compact rejected"

	done := compactDoneMessageFromCommand(
		t,
		model.inputController().startCompactionWithOrigin(submitted, "rejected", uiCompactionOriginManual),
	)
	next, _ := model.Update(done)
	model = next.(*uiModel)

	if model.activity != uiActivityRunning {
		t.Fatalf("activity = %v, want unchanged running activity", model.activity)
	}
	if model.transientStatus != runtimeattach.FormatSubmissionError(rejection) ||
		model.transientStatusKind != uiStatusNoticeError {
		t.Fatalf(
			"transient status = %q kind %v, want ordinary compact rejection detail",
			model.transientStatus,
			model.transientStatusKind,
		)
	}
	if got := testMainInput(model); got != submitted {
		t.Fatalf("restored composer = %q, want %q", got, submitted)
	}
	if len(model.injectedQueue) != 1 || model.injectedQueue[0].State != injectedRuntimeQueueEnqueued {
		t.Fatalf("existing accepted steer was mutated: %+v", model.injectedQueue)
	}
	if len(model.queued) != 1 || model.queued[0].Text != "unrelated queued input" {
		t.Fatalf("unrelated queued input was mutated: %+v", model.queued)
	}
	if client.compactCalls != 1 || client.appendCalls != 0 || client.discardQueuedCalls != 0 {
		t.Fatalf(
			"runtime calls = compact %d append %d discard %d, want 1/0/0",
			client.compactCalls,
			client.appendCalls,
			client.discardQueuedCalls,
		)
	}
	if hook.aborted != 0 || hook.compactionCompleted != 0 {
		t.Fatalf("queue hooks = aborted %d completed %d, want 0/0", hook.aborted, hook.compactionCompleted)
	}
}

func compactDoneMessageFromCommand(t *testing.T, cmd tea.Cmd) compactDoneMsg {
	t.Helper()
	for _, msg := range collectCmdMessages(t, cmd) {
		if done, ok := msg.(compactDoneMsg); ok {
			return done
		}
	}
	t.Fatal("compaction command returned no completion")
	return compactDoneMsg{}
}

func TestQueuedCompactWithoutClientRestoresExactTextAndAbortsDrain(t *testing.T) {
	hook := &compactionTurnQueueHook{}
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hook))
	compactText := "  /compact  queued\n guidance  "
	laterText := "later queued input"
	model.queueInput(compactText)
	model.queueInput(laterText)

	_, cmd := model.inputController().flushQueuedInputs(queueDrainAuto)
	done := compactDoneMessageFromCommand(t, cmd)
	if !errors.Is(done.err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("compact error = %v, want runtime command not accepted", done.err)
	}

	model = updateUIModel(t, model, done)
	want := strings.Join([]string{compactText, laterText}, "\n\n")
	if got := testMainInput(model); got != want {
		t.Fatalf("restored composer = %q, want %q", got, want)
	}
	if len(model.queued) != 0 {
		t.Fatalf("queued inputs remained after abort: %+v", model.queued)
	}
	if hook.aborted != 1 || hook.compactionCompleted != 0 {
		t.Fatalf("queue hooks = aborted %d completed %d, want 1/0", hook.aborted, hook.compactionCompleted)
	}
}

func TestQueuedCompactOwnsExactComposerTextBeforeNormalization(t *testing.T) {
	disableTransientStatusClearForTest(t)
	model := busyCommandTestModel()
	submitted := "  /compact  queued\n guidance  "
	testSetMainInput(model, submitted)

	next, queueCmd := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = next.(*uiModel)
	if queueCmd != nil {
		t.Fatal("busy Queue submission unexpectedly dispatched immediately")
	}
	if len(model.queued) != 1 || model.queued[0].Text != submitted {
		t.Fatalf("queued compact text = %+v, want exact composer text %q", model.queued, submitted)
	}

	next, drainCmd := model.Update(submitDoneMsg{message: "turn complete"})
	model = next.(*uiModel)
	done := compactDoneMessageFromCommand(t, drainCmd)
	if done.submittedText != submitted {
		t.Fatalf("request-owned compact text = %q, want exact %q", done.submittedText, submitted)
	}

	next, _ = model.Update(done)
	model = next.(*uiModel)
	if got := testMainInput(model); got != submitted {
		t.Fatalf("restored composer = %q, want exact %q", got, submitted)
	}
}

func TestIdleQueueKeyCompactRejectionRestoresExactComposerText(t *testing.T) {
	disableTransientStatusClearForTest(t)
	tests := []struct {
		name string
		key  tea.Msg
	}{
		{name: "Tab", key: tea.KeyMsg{Type: tea.KeyTab}},
		{name: "CtrlEnter", key: customKeyMsg{Kind: customKeyCtrlEnter}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newProjectedStaticUIModel()
			submitted := "  /compact  idle\n guidance  "
			testSetMainInput(model, submitted)

			next, cmd := model.Update(test.key)
			model = next.(*uiModel)
			if cmd == nil {
				t.Fatalf(
					"idle Queue-key compact did not dispatch: input=%q queued=%+v blockers=%d busy=%t",
					testMainInput(model),
					model.queued,
					model.postTurnCompactionsInFlight,
					model.blocksRuntimeInput(),
				)
			}
			done := compactDoneMessageFromCommand(t, cmd)
			if !errors.Is(done.err, serverapi.ErrRuntimeCommandNotAccepted) {
				t.Fatalf("compact error = %v, want runtime command not accepted", done.err)
			}
			if done.submittedText != submitted {
				t.Fatalf("request-owned compact text = %q, want exact %q", done.submittedText, submitted)
			}

			next, _ = model.Update(done)
			model = next.(*uiModel)
			if got := testMainInput(model); got != submitted {
				t.Fatalf("restored composer = %q, want exact %q", got, submitted)
			}
		})
	}
}

func TestCompactCommandInvokesCapturedClientAndReducesItsResult(t *testing.T) {
	capturedErr := serverapi.NewRuntimeCommandNotAcceptedError(errors.New("captured client rejected request"))
	captured := &runtimeControlFakeClient{compactErr: capturedErr}
	replacement := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(captured)
	submitted := "/compact captured guidance"

	cmd := model.inputController().startCompactionWithOrigin(submitted, "captured guidance", uiCompactionOriginManual)
	model.engine = replacement
	done := compactDoneMessageFromCommand(t, cmd)

	if captured.compactCalls != 1 || captured.compactArgs != "captured guidance" {
		t.Fatalf("captured client calls = %d args %q, want 1 and parsed guidance", captured.compactCalls, captured.compactArgs)
	}
	if replacement.compactCalls != 0 {
		t.Fatalf("replacement client calls = %d, want 0", replacement.compactCalls)
	}
	if !errors.Is(done.err, capturedErr) {
		t.Fatalf("completion error = %v, want captured result %v", done.err, capturedErr)
	}
	if done.submittedText != submitted || done.guidance != "captured guidance" ||
		done.origin != uiCompactionOriginManual || !done.invoked {
		t.Fatalf("request-owned completion = %+v", done)
	}

	model = updateUIModel(t, model, done)
	if got := testMainInput(model); got != submitted {
		t.Fatalf("restored composer = %q, want request-owned text %q", got, submitted)
	}
}

func TestConcurrentCompactRequestsRemainIndependentWhenCompletionsArriveOutOfOrder(t *testing.T) {
	firstErr := serverapi.NewRuntimeCommandNotAcceptedError(errors.New("first request was not accepted"))
	secondErr := errors.New("second request failed after acceptance")
	firstClient := &runtimeControlFakeClient{compactErr: firstErr}
	secondClient := &runtimeControlFakeClient{compactErr: secondErr}
	model := newProjectedTestUIModel(firstClient)

	firstText := " /compact first guidance "
	firstCmd := model.inputController().startCompactionWithOrigin(firstText, "first guidance", uiCompactionOriginManual)
	model.engine = secondClient
	secondText := "/compact second guidance"
	secondCmd := model.inputController().startCompactionWithOrigin(secondText, "second guidance", uiCompactionOriginManual)

	if firstCmd == nil || secondCmd == nil {
		t.Fatalf("compact commands = first %v second %v, want both dispatched", firstCmd, secondCmd)
	}
	if model.isCompacting() {
		t.Fatal("compact dispatch mutated client-local compaction lifecycle")
	}

	firstDone := compactDoneMessageFromCommand(t, firstCmd)
	secondDone := compactDoneMessageFromCommand(t, secondCmd)
	if firstClient.compactCalls != 1 || firstClient.compactArgs != "first guidance" {
		t.Fatalf("first client = calls %d args %q", firstClient.compactCalls, firstClient.compactArgs)
	}
	if secondClient.compactCalls != 1 || secondClient.compactArgs != "second guidance" {
		t.Fatalf("second client = calls %d args %q", secondClient.compactCalls, secondClient.compactArgs)
	}

	model = updateUIModel(t, model, secondDone)
	if got := testMainInput(model); got != "" {
		t.Fatalf("accepted error restored composer = %q, want empty", got)
	}
	model = updateUIModel(t, model, firstDone)
	if got := testMainInput(model); got != firstText {
		t.Fatalf("not-accepted request restored %q, want only %q", got, firstText)
	}
}

func TestAcceptedQueuedCompactErrorConsumesCommandAndAdvancesQueue(t *testing.T) {
	acceptedErr := errors.New("compaction committed before finalization failed")
	client := &runtimeControlFakeClient{compactErr: acceptedErr}
	model := newProjectedTestUIModel(client)
	compactText := "/compact queued guidance"
	laterText := "later queued input"
	model.queueInput(laterText)

	cmd := model.inputController().startCompactionWithOrigin(compactText, "queued guidance", uiCompactionOriginQueued)
	done := compactDoneMessageFromCommand(t, cmd)
	next, completionCmd := model.Update(done)
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, completionCmd) {
		model = updateUIModel(t, model, msg)
	}

	if got := testMainInput(model); got != "" {
		t.Fatalf("accepted compact error restored composer = %q, want empty", got)
	}
	if client.submitCalls != 1 || client.submitText != laterText {
		t.Fatalf("later Queue dispatch = calls %d text %q, want 1 and %q", client.submitCalls, client.submitText, laterText)
	}
}

func TestQueuedCompactBlocksOnlyLaterPostTurnDispatchUntilCompletion(t *testing.T) {
	client := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(client)
	compactText := "/compact queued guidance"
	laterText := "later queued input"
	model.queueInput(laterText)

	cmd := model.inputController().startCompactionWithOrigin(compactText, "queued guidance", uiCompactionOriginQueued)
	done := compactDoneMessageFromCommand(t, cmd)
	_, blockedCmd := model.inputController().flushQueuedInputs(queueDrainAuto)
	if blockedCmd != nil {
		t.Fatal("later post-turn Queue input dispatched before compact completion")
	}
	if client.submitCalls != 0 || len(model.queued) != 1 || model.queued[0].Text != laterText {
		t.Fatalf("pre-completion Queue state = submit calls %d queued %+v", client.submitCalls, model.queued)
	}
	if model.postTurnCompactionsInFlight != 1 {
		t.Fatalf("post-turn compact blockers = %d, want 1", model.postTurnCompactionsInFlight)
	}
	if model.isCompacting() {
		t.Fatal("queued compact dispatch mutated client-local compaction lifecycle")
	}

	next, completionCmd := model.Update(done)
	model = next.(*uiModel)
	for _, msg := range collectCmdMessages(t, completionCmd) {
		model = updateUIModel(t, model, msg)
	}

	if model.postTurnCompactionsInFlight != 0 {
		t.Fatalf("post-turn compact blockers = %d, want 0 after completion", model.postTurnCompactionsInFlight)
	}
	if client.submitCalls != 1 || client.submitText != laterText {
		t.Fatalf("later Queue dispatch = calls %d text %q, want 1 and %q", client.submitCalls, client.submitText, laterText)
	}
}
