package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func approvalCommentary(answer *serverapi.PromptApprovalAnswer) string {
	if answer == nil || answer.Commentary == nil {
		return ""
	}
	return *answer.Commentary
}

type deadlineThenSuccessPromptControl struct {
	singlePromptOnlyControl
	mu           sync.Mutex
	askRequests  []serverapi.PromptAnswerBatchRequest
	firstStarted chan struct{}
	firstRelease chan struct{}
}

type scriptedAskPromptControl struct {
	singlePromptOnlyControl
	mu          sync.Mutex
	results     []error
	askRequests []serverapi.PromptAnswerBatchRequest
}

func (c *scriptedAskPromptControl) AnswerPromptBatch(
	_ context.Context,
	request serverapi.PromptAnswerBatchRequest,
) (serverapi.PromptAnswerBatchResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := len(c.askRequests)
	c.askRequests = append(c.askRequests, request)
	if call < len(c.results) && c.results[call] != nil {
		return serverapi.PromptAnswerBatchResponse{}, c.results[call]
	}
	return resolvedPromptBatchResponse(request), nil
}

func (c *scriptedAskPromptControl) requests() []serverapi.PromptAnswerBatchRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.PromptAnswerBatchRequest(nil), c.askRequests...)
}

func newDeadlineThenSuccessPromptControl() *deadlineThenSuccessPromptControl {
	return &deadlineThenSuccessPromptControl{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
}

func (c *deadlineThenSuccessPromptControl) AnswerPromptBatch(
	ctx context.Context,
	request serverapi.PromptAnswerBatchRequest,
) (serverapi.PromptAnswerBatchResponse, error) {
	c.mu.Lock()
	call := len(c.askRequests)
	c.askRequests = append(c.askRequests, request)
	c.mu.Unlock()
	if call != 0 {
		response := resolvedPromptBatchResponse(request)
		response.Results[0].Outcome = serverapi.PromptAnswerBatchOutcomeSkipped
		return response, nil
	}
	close(c.firstStarted)
	select {
	case <-ctx.Done():
		return serverapi.PromptAnswerBatchResponse{}, ctx.Err()
	case <-c.firstRelease:
		return serverapi.PromptAnswerBatchResponse{}, context.DeadlineExceeded
	}
}

func (c *deadlineThenSuccessPromptControl) requests() []serverapi.PromptAnswerBatchRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.PromptAnswerBatchRequest(nil), c.askRequests...)
}

func TestAskDeadlineKeepsEditedRetryDraftActionableUntilCanonicalResolution(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newDeadlineThenSuccessPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model.setRuntimeActivityBusyForTest(true)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-deadline", "How should I proceed?", "Use the draft", "Stop"),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("original")})

	next, firstDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if firstDelivery == nil {
		t.Fatal("submitting an answer did not return a stateless delivery command")
	}
	if !testPromptAnswerDeliveryActive(model) {
		t.Fatal("answer delivery is not active after submission")
	}

	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstDelivery()
	}()
	<-control.firstStarted

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edited")})
	if got := testAskInput(model); got != "original edited" {
		t.Fatalf("retry draft = %q, want edited draft while delivery is outstanding", got)
	}
	if got := testAskCursor(model); got != 0 {
		t.Fatalf("selection = %d, want original selection retained", got)
	}

	close(control.firstRelease)
	model = updateUIModel(t, model, <-firstResult)
	if testPromptAnswerDeliveryActive(model) {
		t.Fatal("deadline left answer delivery active")
	}
	if testActiveAsk(model) == nil {
		t.Fatal("deadline locally resolved the prompt")
	}
	if model.activity != uiActivityQuestion {
		t.Fatalf("deadline activity = %d, want question", model.activity)
	}
	if got := testAskInput(model); got != "original edited" {
		t.Fatalf("retry draft = %q after deadline, want edited draft preserved", got)
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("deadline notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}

	next, secondDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if secondDelivery == nil {
		t.Fatal("resubmitting the edited draft did not return a delivery command")
	}
	model = updateUIModel(t, model, secondDelivery())
	if testPromptAnswerDeliveryActive(model) || testActiveAsk(model) != nil {
		t.Fatal("Skipped delivery did not immediately finish the prompt")
	}
	if model.activity != uiActivityRunning || model.inputMode() != uiInputModeMain {
		t.Fatalf("pre-successor completion = activity %d input %q", model.activity, model.inputMode())
	}
	successor := testQuestionPrompt("ask-successor", "Next?", "Continue", "Stop")
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(successor)})
	if active := testActiveAsk(model); active == nil || active.prompt.ToolCallID != successor.ToolCallID {
		t.Fatalf("later authoritative successor = %+v", active)
	}

	requests := control.requests()
	if len(requests) != 2 {
		t.Fatalf("ask requests = %d, want deadline attempt plus user resubmission", len(requests))
	}
	if freeform := requireQuestionAnswerEntry(t, requests[0]).QuestionAnswer.Freeform; freeform == nil || *freeform != "original" {
		t.Fatalf("first immutable request = %+v, want original draft", requests[0])
	}
	if freeform := requireQuestionAnswerEntry(t, requests[1]).QuestionAnswer.Freeform; freeform == nil || *freeform != "original edited" {
		t.Fatalf("resubmitted request = %+v, want edited retry draft", requests[1])
	}
}

func TestAskRepeatedEnterWhileDeliveryActiveDoesNotSubmitAgain(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newDeadlineThenSuccessPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-duplicate", "Proceed?", "Yes", "No"),
	)})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	result := make(chan tea.Msg, 1)
	go func() {
		result <- delivery()
	}()
	<-control.firstStarted

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(control.requests()) != 1 {
		t.Fatalf("ask requests = %d, want one while delivery is active", len(control.requests()))
	}
	if !testPromptAnswerDeliveryActive(model) {
		t.Fatal("repeated Enter cleared the active delivery")
	}
	if model.transientStatusKind != uiStatusNoticeInfo || model.transientStatus == "" {
		t.Fatalf("sending notice = kind %d text %q, want nonblocking info", model.transientStatusKind, model.transientStatus)
	}

	close(control.firstRelease)
	model = updateUIModel(t, model, <-result)
	if testPromptAnswerDeliveryActive(model) {
		t.Fatal("deadline result left the delivery active")
	}
}

func TestAskCtrlCFinishesPromptCancellationBeforeRuntimeInterrupt(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newRecordingPromptControl()
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model.sessionID = ongoingTestSessionID().String()
	model.setRuntimeActivityBusyForTest(true)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-interrupt-order", "Proceed?", "Yes", "No"),
	)})

	next, cancellationCommand := model.askController().handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = next.(*uiModel)
	activeCancellation := model.ask.activeDelivery
	if cancellationCommand == nil || activeCancellation == nil {
		t.Fatal("Ctrl+C did not start prompt cancellation delivery")
	}
	if runtimeClient.interruptCalls != 0 {
		t.Fatal("Ctrl+C interrupted runtime before prompt cancellation completed")
	}
	if model.transientStatus != "" {
		t.Fatal("Ctrl+C reported interruption before prompt cancellation completed")
	}

	next, repeatedCommand := model.askController().handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = next.(*uiModel)
	if repeatedCommand != nil {
		t.Fatal("repeated Ctrl+C started another cancellation sequence")
	}
	if model.ask.activeDelivery != activeCancellation {
		t.Fatal("repeated Ctrl+C replaced the in-flight prompt cancellation")
	}

	rawResult := cancellationCommand()
	result, ok := rawResult.(promptAnswerDeliveryResultMsg)
	if !ok {
		t.Fatalf("prompt cancellation result = %T, want promptAnswerDeliveryResultMsg", rawResult)
	}
	if result.err != nil {
		t.Fatalf("prompt cancellation failed: %v", result.err)
	}
	if runtimeClient.interruptCalls != 0 {
		t.Fatal("runtime interrupted concurrently with prompt cancellation")
	}

	continuation := model.askController().applyDeliveryResult(result)
	if continuation == nil || testActiveAsk(model) != nil {
		t.Fatal("successful prompt cancellation did not finish the prompt and schedule global Ctrl+C")
	}
	next, interruptCommand := model.Update(continuation())
	model = next.(*uiModel)
	if interruptCommand == nil || runtimeClient.interruptCalls != 0 {
		t.Fatal("global Ctrl+C did not defer runtime interruption to its command")
	}
	if model.transientStatus == "" || model.transientStatusKind != uiStatusNoticeError {
		t.Fatal("successful prompt cancellation did not report interruption when routing global Ctrl+C")
	}
	model = updateUIModel(t, model, interruptCommand())
	if runtimeClient.interruptCalls != 1 {
		t.Fatalf("runtime interrupt calls = %d, want one after prompt cancellation", runtimeClient.interruptCalls)
	}

	request := requirePromptAnswerBatchRequest(t, control)
	entry := request.Entries[0]
	if entry.Declined == nil {
		t.Fatalf("prompt cancellation entry = %+v, want declined", entry)
	}
}

func TestAskCtrlCCancellationConnectionFailureDoesNotReportInterrupted(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := &scriptedAskPromptControl{results: []error{io.EOF}}
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model.sessionID = ongoingTestSessionID().String()
	model.setRuntimeActivityBusyForTest(true)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-interrupt-connection-failure", "Proceed?", "Yes", "No"),
	)})

	next, cancellationCommand := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = next.(*uiModel)
	if model.transientStatus != "" {
		t.Fatal("Ctrl+C reported interruption before prompt cancellation completed")
	}
	model = updateUIModel(t, model, cancellationCommand())
	if model.transientStatus != "" {
		t.Fatal("failed prompt cancellation left an interruption notice")
	}
	if testActiveAsk(model) == nil || testPromptAnswerDeliveryActive(model) {
		t.Fatal("failed prompt cancellation did not restore the actionable prompt")
	}
	if runtimeClient.interruptCalls != 0 {
		t.Fatalf("runtime interrupt calls = %d, want zero after cancellation failure", runtimeClient.interruptCalls)
	}
}

func TestAskCtrlCCanonicalResolutionPreservesRuntimeContinuation(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newRecordingPromptControl()
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model.sessionID = ongoingTestSessionID().String()
	model.setRuntimeActivityBusyForTest(true)
	prompt := testQuestionPrompt("ask-canonical-interrupt-order", "Proceed?", "Yes", "No")
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(prompt)})

	next, cancellationCommand := model.askController().handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = next.(*uiModel)
	rawResult := cancellationCommand()
	result, ok := rawResult.(promptAnswerDeliveryResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("prompt cancellation result = %#v, want successful delivery result", rawResult)
	}

	resolved := cloneTranscriptPromptForAsk(prompt)
	resolved.Status = clientui.TranscriptPromptStatusResolved
	message := clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(resolved))
	continuation := model.applyAdmittedTranscriptMessageState(message, runtimeTupleMergeResult{})
	if continuation == nil {
		t.Fatal("canonical prompt resolution dropped the pending global Ctrl+C continuation")
	}
	if testActiveAsk(model) != nil || testPromptAnswerDeliveryActive(model) {
		t.Fatal("canonical prompt resolution retained prompt delivery ownership")
	}
	if staleCommand := model.askController().applyDeliveryResult(result); staleCommand != nil {
		t.Fatal("stale delivery result scheduled a duplicate global Ctrl+C continuation")
	}

	next, interruptCommand := model.Update(continuation())
	model = next.(*uiModel)
	if interruptCommand == nil || runtimeClient.interruptCalls != 0 {
		t.Fatal("canonical resolution did not defer runtime interruption to its command")
	}
	model = updateUIModel(t, model, interruptCommand())
	if runtimeClient.interruptCalls != 1 {
		t.Fatalf("runtime interrupt calls = %d, want one after canonical prompt resolution", runtimeClient.interruptCalls)
	}
}

func TestPromptCtrlCContinuationDoesNotAffectReplacementExecution(t *testing.T) {
	replacementRunID, err := runtimeids.ParseRunID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if err != nil {
		t.Fatalf("parse replacement run id: %v", err)
	}
	replacementStepID, err := runtimeids.ParseStepID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if err != nil {
		t.Fatalf("parse replacement step id: %v", err)
	}
	continuation := promptCtrlCContinuationMsg{key: transcriptPromptKey{
		sessionID:  ongoingTestSessionID(),
		stepID:     ongoingTestStepID(),
		toolCallID: "ask-replaced",
	}}
	tests := []struct {
		name      string
		sessionID string
		activity  clientui.RuntimeActivity
	}{
		{
			name:      "session",
			sessionID: "ffffffff-ffff-4fff-8fff-ffffffffffff",
			activity:  runtimeTupleTestRunningActivity(),
		},
		{
			name:      "step",
			sessionID: ongoingTestSessionID().String(),
			activity: clientui.RuntimeActivity{
				State:    clientui.RuntimeActivityRunning,
				Reviewer: clientui.ReviewerActivityInactive,
				ActiveStep: &clientui.RuntimeActiveStep{
					RunID:      replacementRunID,
					StepID:     replacementStepID,
					ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
				},
			},
		},
		{
			name:      "starting",
			sessionID: ongoingTestSessionID().String(),
			activity: clientui.RuntimeActivity{
				State:    clientui.RuntimeActivityStarting,
				Reviewer: clientui.ReviewerActivityInactive,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &runtimeControlFakeClient{}
			model := newProjectedTestUIModel(runtimeClient)
			model.sessionID = test.sessionID
			if err := model.applyRuntimeActivityProjection(test.activity); err != nil {
				t.Fatalf("apply replacement runtime activity: %v", err)
			}

			next, command := model.Update(continuation)
			model = next.(*uiModel)
			if command != nil || runtimeClient.interruptCalls != 0 || model.exitAction == UIActionExit {
				t.Fatal("stale prompt Ctrl+C continuation affected a replacement execution")
			}
		})
	}
}

func TestPromptCtrlCContinuationUsesGlobalExitWhenOriginIsIdle(t *testing.T) {
	model := newProjectedTestUIModel(&runtimeControlFakeClient{})
	model.sessionID = ongoingTestSessionID().String()
	model.setRuntimeActivityBusyForTest(false)

	next, command := model.Update(promptCtrlCContinuationMsg{key: transcriptPromptKey{
		sessionID:  ongoingTestSessionID(),
		stepID:     ongoingTestStepID(),
		toolCallID: "ask-idle",
	}})
	model = next.(*uiModel)
	if command == nil || model.exitAction != UIActionExit {
		t.Fatal("idle prompt Ctrl+C continuation did not use global exit handling")
	}
}

func TestPromptCtrlCContinuationWaitsForPromptActivityToClear(t *testing.T) {
	disableTransientStatusClearForTest(t)
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.sessionID = ongoingTestSessionID().String()
	awaitingPrompt := runtimeTupleTestRunningActivity()
	awaitingPrompt.State = clientui.RuntimeActivityAwaitingPrompt
	if err := model.applyRuntimeActivityProjection(awaitingPrompt); err != nil {
		t.Fatalf("apply awaiting-prompt runtime activity: %v", err)
	}
	continuation := promptCtrlCContinuationMsg{key: transcriptPromptKey{
		sessionID:  ongoingTestSessionID(),
		stepID:     ongoingTestStepID(),
		toolCallID: "ask-awaiting-runtime",
	}}

	next, command := model.Update(continuation)
	model = next.(*uiModel)
	if command != nil || runtimeClient.interruptCalls != 0 || model.exitAction == UIActionExit {
		t.Fatal("awaiting-prompt continuation routed global Ctrl+C before prompt wait cleared")
	}

	command = model.applyTranscriptRuntimeReadModelUpdate(runtimeTupleMergeResult{
		decision: runtimeTupleApply,
		project:  true,
		view: clientui.RuntimeMainView{
			Activity: runtimeTupleTestRunningActivity(),
		},
	})
	if command == nil {
		t.Fatal("running activity did not release the pending prompt Ctrl+C continuation")
	}
	next, interruptCommand := model.Update(command())
	model = next.(*uiModel)
	if interruptCommand == nil || runtimeClient.interruptCalls != 0 {
		t.Fatal("released prompt Ctrl+C continuation did not defer interruption to its command")
	}
	model = updateUIModel(t, model, interruptCommand())
	if runtimeClient.interruptCalls != 1 {
		t.Fatalf("runtime interrupt calls = %d, want one after prompt wait cleared", runtimeClient.interruptCalls)
	}
}

func TestPromptCtrlCContinuationCancelsSameStepSuccessorBeforeRuntimeInterrupt(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newRecordingPromptControl()
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model.sessionID = ongoingTestSessionID().String()
	awaitingPrompt := runtimeTupleTestRunningActivity()
	awaitingPrompt.State = clientui.RuntimeActivityAwaitingPrompt
	if err := model.applyRuntimeActivityProjection(awaitingPrompt); err != nil {
		t.Fatalf("apply awaiting-prompt runtime activity: %v", err)
	}
	origin := testQuestionPrompt("ask-origin", "Proceed?", "Yes", "No")
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(origin)})

	next, originCancellation := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = next.(*uiModel)
	originResult := originCancellation().(promptAnswerDeliveryResultMsg)
	resolved := cloneTranscriptPromptForAsk(origin)
	resolved.Status = clientui.TranscriptPromptStatusResolved
	continuation := model.applyAdmittedTranscriptMessageState(
		clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(resolved)),
		runtimeTupleMergeResult{},
	)
	if continuation == nil {
		t.Fatal("origin resolution did not preserve global Ctrl+C continuation")
	}
	if stale := model.askController().applyDeliveryResult(originResult); stale != nil {
		t.Fatal("origin delivery result scheduled a duplicate continuation")
	}
	model = updateUIModel(t, model, continuation())

	successor := testQuestionPrompt("ask-successor", "Anything else?", "Yes", "No")
	next, successorCommand := model.Update(askEventMsg{event: model.transcriptPromptEvent(successor)})
	model = next.(*uiModel)
	var successorContinuation promptCtrlCContinuationMsg
	foundSuccessorContinuation := false
	for _, message := range collectCmdMessages(t, successorCommand) {
		if candidate, ok := message.(promptCtrlCContinuationMsg); ok {
			successorContinuation = candidate
			foundSuccessorContinuation = true
		}
	}
	if !foundSuccessorContinuation {
		t.Fatal("same-Step successor did not release the pending Ctrl+C continuation")
	}
	next, successorCancellation := model.Update(successorContinuation)
	model = next.(*uiModel)
	if successorCancellation == nil {
		t.Fatal("pending Ctrl+C continuation did not cancel the same-Step successor")
	}
	successorResult := successorCancellation().(promptAnswerDeliveryResultMsg)
	next, successorResolvedContinuation := model.Update(successorResult)
	model = next.(*uiModel)
	if successorResolvedContinuation == nil {
		t.Fatal("successor cancellation did not retain global Ctrl+C continuation")
	}
	model = updateUIModel(t, model, successorResolvedContinuation())
	if runtimeClient.interruptCalls != 0 || model.exitAction == UIActionExit {
		t.Fatal("successor cancellation routed global Ctrl+C while prompt activity was still stale")
	}

	running := awaitingPrompt
	running.State = clientui.RuntimeActivityRunning
	command := model.applyTranscriptRuntimeReadModelUpdate(runtimeTupleMergeResult{
		decision: runtimeTupleApply,
		project:  true,
		view:     clientui.RuntimeMainView{Activity: running},
	})
	next, interruptCommand := model.Update(command())
	model = next.(*uiModel)
	model = updateUIModel(t, model, interruptCommand())
	if runtimeClient.interruptCalls != 1 {
		t.Fatalf("runtime interrupt calls = %d, want one after successor cancellation", runtimeClient.interruptCalls)
	}

	for index := 0; index < 2; index++ {
		request := requirePromptAnswerBatchRequest(t, control)
		if request.Entries[0].Declined == nil {
			t.Fatalf("prompt cancellation %d = %+v, want declined", index, request)
		}
	}
}

func TestPromptCtrlCContinuationCancelsSuccessorInstalledByHydration(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newRecordingPromptControl()
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model.sessionID = ongoingTestSessionID().String()
	awaitingPrompt := runtimeTupleTestRunningActivity()
	awaitingPrompt.State = clientui.RuntimeActivityAwaitingPrompt
	if err := model.applyRuntimeActivityProjection(awaitingPrompt); err != nil {
		t.Fatalf("apply awaiting-prompt runtime activity: %v", err)
	}
	originKey := transcriptPromptKey{
		sessionID:  ongoingTestSessionID(),
		stepID:     ongoingTestStepID(),
		toolCallID: "ask-hydration-origin",
	}
	model = updateUIModel(t, model, promptCtrlCContinuationMsg{key: originKey})

	hydrationMessage := runtimeTupleTestHydration(2, awaitingPrompt)
	hydration := hydrationMessage.Payload().(clientui.TranscriptHydration)
	successor := testQuestionPrompt("ask-hydration-successor", "Anything else?", "Yes", "No")
	hydration.PendingPrompts = []clientui.TranscriptPrompt{successor}
	hydrationMessage = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydration))
	command := model.applyAdmittedTranscriptMessageState(hydrationMessage, runtimeTupleMergeResult{
		decision: runtimeTupleApply,
		project:  true,
		view:     clientui.RuntimeMainView{Activity: awaitingPrompt},
	})

	var successorContinuation promptCtrlCContinuationMsg
	foundSuccessorContinuation := false
	for _, message := range collectCmdMessages(t, command) {
		if candidate, ok := message.(promptCtrlCContinuationMsg); ok {
			successorContinuation = candidate
			foundSuccessorContinuation = true
		}
	}
	if !foundSuccessorContinuation {
		t.Fatal("hydrated same-Step successor did not release the pending Ctrl+C continuation")
	}
	next, successorCancellation := model.Update(successorContinuation)
	model = next.(*uiModel)
	if successorCancellation == nil {
		t.Fatal("pending Ctrl+C continuation did not cancel the hydrated successor")
	}
	result := successorCancellation().(promptAnswerDeliveryResultMsg)
	if result.key.toolCallID != successor.ToolCallID {
		t.Fatalf("canceled prompt = %q, want hydrated successor %q", result.key.toolCallID, successor.ToolCallID)
	}
}

func TestStalePromptCtrlCContinuationDoesNotClearNewerPendingContinuation(t *testing.T) {
	disableTransientStatusClearForTest(t)
	runtimeClient := &runtimeControlFakeClient{}
	model := newProjectedTestUIModel(runtimeClient)
	model.sessionID = ongoingTestSessionID().String()
	newerRunID, err := runtimeids.ParseRunID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if err != nil {
		t.Fatalf("parse newer run id: %v", err)
	}
	newerStepID, err := runtimeids.ParseStepID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if err != nil {
		t.Fatalf("parse newer step id: %v", err)
	}
	newerActivity := clientui.RuntimeActivity{
		State:    clientui.RuntimeActivityAwaitingPrompt,
		Reviewer: clientui.ReviewerActivityInactive,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID:      newerRunID,
			StepID:     newerStepID,
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		},
	}
	if err := model.applyRuntimeActivityProjection(newerActivity); err != nil {
		t.Fatalf("apply newer awaiting-prompt activity: %v", err)
	}
	newer := promptCtrlCContinuationMsg{key: transcriptPromptKey{
		sessionID:  ongoingTestSessionID(),
		stepID:     newerStepID,
		toolCallID: "ask-newer",
	}}
	stale := promptCtrlCContinuationMsg{key: transcriptPromptKey{
		sessionID:  ongoingTestSessionID(),
		stepID:     ongoingTestStepID(),
		toolCallID: "ask-stale",
	}}

	model = updateUIModel(t, model, newer)
	model = updateUIModel(t, model, stale)

	newerActivity.State = clientui.RuntimeActivityRunning
	command := model.applyTranscriptRuntimeReadModelUpdate(runtimeTupleMergeResult{
		decision: runtimeTupleApply,
		project:  true,
		view:     clientui.RuntimeMainView{Activity: newerActivity},
	})
	if command == nil {
		t.Fatal("stale continuation cleared the newer pending Ctrl+C continuation")
	}
	next, interruptCommand := model.Update(command())
	model = next.(*uiModel)
	if interruptCommand == nil {
		t.Fatal("newer pending continuation did not route after its prompt wait cleared")
	}
	model = updateUIModel(t, model, interruptCommand())
	if runtimeClient.interruptCalls != 1 {
		t.Fatalf("runtime interrupt calls = %d, want one for the newer continuation", runtimeClient.interruptCalls)
	}
}

func TestAskDeliverySetupFailureKeepsQuestionActivity(t *testing.T) {
	disableTransientStatusClearForTest(t)
	model, control := newProjectedPromptTestUIModel(t)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-empty-setup-failure", "Provide details"),
	)})

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if model.activity != uiActivityQuestion || testActiveAsk(model) == nil || testPromptAnswerDeliveryActive(model) {
		t.Fatalf(
			"setup failure state = activity %d prompt %v delivery %t, want actionable question",
			model.activity,
			testActiveAsk(model) != nil,
			testPromptAnswerDeliveryActive(model),
		)
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("setup failure notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}
	if len(control.batchRequests) != 0 {
		t.Fatalf("setup failure recorded %d batch requests, want zero", len(control.batchRequests))
	}
}

func TestAskSameKeyRefreshPreservesActiveDeliveryDraftAndSelection(t *testing.T) {
	control := newDeadlineThenSuccessPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	prompt := testQuestionPrompt("ask-refresh", "Original question", "One", "Two")
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(prompt)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("retry draft")})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	active := model.ask.activeDelivery
	result := make(chan tea.Msg, 1)
	go func() {
		result <- delivery()
	}()
	<-control.firstStarted

	refreshed := cloneTranscriptPromptForAsk(prompt)
	refreshed.Question = "Refreshed question"
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(refreshed)})
	if model.ask.activeDelivery != active {
		t.Fatal("same-key refresh replaced active delivery ownership")
	}
	if testAskInput(model) != "retry draft" || testAskCursor(model) != 1 || testAskFreeform(model) {
		t.Fatalf(
			"same-key refresh changed draft/selection: draft=%q cursor=%d freeform=%t",
			testAskInput(model),
			testAskCursor(model),
			testAskFreeform(model),
		)
	}
	if testActiveAsk(model).prompt.Question != "Refreshed question" {
		t.Fatalf("same-key refresh did not update prompt payload: %+v", testActiveAsk(model).prompt)
	}

	close(control.firstRelease)
	model = updateUIModel(t, model, <-result)
	if testPromptAnswerDeliveryActive(model) {
		t.Fatal("deadline result left refreshed delivery active")
	}
}

func TestAskTypedTerminalFailureRestoresDraftAndShowsError(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := &scriptedAskPromptControl{results: []error{serverapi.ErrPromptNotFound}}
	var outcome error
	answerer := newTranscriptPromptAnswerer(context.Background(), control).withConnectionOutcomeSink(func(err error) {
		outcome = err
	})

	model := newProjectedStaticUIModel()
	model.promptAnswers = answerer
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-terminal", "Provide details"),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("retry draft")})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runPromptDeliveryCommand(t, next.(*uiModel), delivery)
	if testPromptAnswerDeliveryActive(model) {
		t.Fatal("typed terminal failure left answer delivery active")
	}
	if testActiveAsk(model) == nil || testAskInput(model) != "retry draft" {
		t.Fatal("typed terminal failure did not preserve the actionable retry draft")
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("typed failure notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}
	if !errors.Is(outcome, serverapi.ErrPromptNotFound) || len(control.requests()) != 1 {
		t.Fatalf("connection outcome = %v requests = %d", outcome, len(control.requests()))
	}
}

func TestAskSessionReplacementCancelsDeliveryAndClearsOldPrompt(t *testing.T) {
	control := newDeadlineThenSuccessPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	prompt := testQuestionPrompt("ask-old-session", "Proceed?", "Yes", "No")
	model.sessionID = prompt.SessionID.String()
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(prompt)})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	result := make(chan tea.Msg, 1)
	go func() {
		result <- delivery()
	}()
	<-control.firstStarted

	model.applyTranscriptSessionIdentity(clientui.TranscriptSessionIdentity{
		SessionID:             runtimeids.NewSessionID(),
		ConversationFreshness: clientui.ConversationFreshnessFresh,
	})
	if testActiveAsk(model) != nil || testPromptAnswerDeliveryActive(model) {
		t.Fatal("session replacement retained the old prompt delivery")
	}
	model = updateUIModel(t, model, <-result)
	if testActiveAsk(model) != nil || testPromptAnswerDeliveryActive(model) {
		t.Fatal("stale canceled result restored the old-session prompt")
	}
}

func TestAskSessionReplacementRunsQueuedProjectionBeforeReplacementPrompt(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.sessionID = runtimeids.NewSessionID().String()
	next, firstProjection := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-old-current",
		"Old current?",
		"yes",
	)})
	model = next.(*uiModel)
	next, _ = model.Update(firstProjection())
	model = next.(*uiModel)
	next, _ = model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-old-queued",
		"Old queued?",
		"yes",
	)})
	model = next.(*uiModel)
	if model.ask.activeProjection == nil || model.ask.inFlightProjection != nil || len(model.ask.queue) != 1 {
		t.Fatal("test setup did not establish a visible current prompt and queued prompt")
	}

	replacementSessionID := runtimeids.NewSessionID()
	replacementCmd := model.applyTranscriptSessionIdentity(clientui.TranscriptSessionIdentity{
		SessionID:             replacementSessionID,
		ConversationFreshness: clientui.ConversationFreshnessFresh,
	})
	if replacementCmd == nil {
		t.Fatal("session replacement dropped prompt reconciliation projection work")
	}
	for _, msg := range collectCmdMessages(t, replacementCmd) {
		next, _ = model.Update(msg)
		model = next.(*uiModel)
	}
	if model.ask.inFlightProjection != nil {
		t.Fatal("session replacement left an orphaned in-flight projection")
	}

	next, projectionCmd := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-new-session",
		"New session?",
		"yes",
	)})
	model = next.(*uiModel)
	if projectionCmd == nil {
		t.Fatal("replacement-session prompt could not start projection")
	}
	next, _ = model.Update(projectionCmd())
	model = next.(*uiModel)
	if model.ask.current == nil ||
		model.ask.current.prompt.ToolCallID != "ask-new-session" ||
		!model.askReadyForInteraction() {
		t.Fatal("replacement-session prompt did not become visible")
	}
}

func TestAskResolutionBeforeDeliveryCommandRunsDoesNotCallPromptControl(t *testing.T) {
	control := &scriptedAskPromptControl{}
	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-resolved-before-command", "Proceed?", "Yes", "No"),
	)})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if delivery == nil || !testPromptAnswerDeliveryActive(model) {
		t.Fatal("submission did not create an active delivery command")
	}
	resolveAnsweredTestAskThroughTranscript(t, model)
	if testActiveAsk(model) != nil || testPromptAnswerDeliveryActive(model) {
		t.Fatal("canonical resolution did not cancel and remove the prompt")
	}

	result, ok := delivery().(promptAnswerDeliveryResultMsg)
	if !ok {
		t.Fatalf("delivery result = %T, want promptAnswerDeliveryResultMsg", result)
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("delivery error = %v, want context canceled", result.err)
	}
	if len(control.requests()) != 0 {
		t.Fatalf("prompt-control calls = %d, want zero after pre-execution resolution", len(control.requests()))
	}
	model = updateUIModel(t, model, result)
	if testActiveAsk(model) != nil || testPromptAnswerDeliveryActive(model) {
		t.Fatal("stale canceled result restored the resolved prompt")
	}
}

func TestApprovalCommentaryRetriesAreDeliberateAndKeepToolCallIdentity(t *testing.T) {
	for _, decision := range []clientui.ApprovalDecision{
		clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionDeny,
	} {
		t.Run(string(decision), func(t *testing.T) {
			control := &scriptedAskPromptControl{results: []error{context.DeadlineExceeded, io.ErrUnexpectedEOF}}
			model := newProjectedStaticUIModel()
			model.promptAnswers = newTranscriptPromptAnswerer(context.Background(), control)
			model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
				testApprovalPrompt("stable-tool-call", "Allow access?", clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionDeny),
			)})
			if decision == clientui.ApprovalDecisionDeny {
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
			}
			model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})

			wantCommentary := []string{"first", "first second", "first second third"}
			for _, appended := range []string{"first", " second", " third"} {
				model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(appended)})
				next, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
				model = runPromptDeliveryCommand(t, next.(*uiModel), command)
				if len(model.injectedQueue) != 0 {
					t.Fatalf("attempt created Approval Queue state: %+v", model.injectedQueue)
				}
			}

			requests := control.requests()
			if len(requests) != len(wantCommentary) || testActiveAsk(model) != nil {
				t.Fatalf("requests = %d active=%t, want three completed deliberate submissions", len(requests), testActiveAsk(model) != nil)
			}
			for i, request := range requests {
				entry := requireApprovalAnswerEntry(t, request)
				if entry.ToolCallID != "stable-tool-call" ||
					entry.ApprovalAnswer.Decision != decision ||
					approvalCommentary(entry.ApprovalAnswer) != wantCommentary[i] {
					t.Fatalf("attempt %d = %+v, want immutable %q commentary for %q", i+1, request, wantCommentary[i], decision)
				}
			}
		})
	}
}

func testPromptAnswerDeliveryActive(model *uiModel) bool {
	return model != nil && model.ask.activeDelivery != nil
}
