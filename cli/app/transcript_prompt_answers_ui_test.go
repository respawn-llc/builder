package app

import (
	"context"
	"errors"
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

type deadlineThenSuccessApprovalControl struct {
	singlePromptOnlyControl
	mu               sync.Mutex
	approvalRequests []serverapi.PromptAnswerBatchRequest
	firstStarted     chan struct{}
	firstRelease     chan struct{}
}

func newDeadlineThenSuccessApprovalControl() *deadlineThenSuccessApprovalControl {
	return &deadlineThenSuccessApprovalControl{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
}

func (c *deadlineThenSuccessApprovalControl) AnswerPromptBatch(
	ctx context.Context,
	request serverapi.PromptAnswerBatchRequest,
) (serverapi.PromptAnswerBatchResponse, error) {
	c.mu.Lock()
	call := len(c.approvalRequests)
	c.approvalRequests = append(c.approvalRequests, request)
	c.mu.Unlock()
	if call != 0 {
		return resolvedPromptBatchResponse(request), nil
	}
	close(c.firstStarted)
	select {
	case <-ctx.Done():
		return serverapi.PromptAnswerBatchResponse{}, ctx.Err()
	case <-c.firstRelease:
		return serverapi.PromptAnswerBatchResponse{}, context.DeadlineExceeded
	}
}

func (c *deadlineThenSuccessApprovalControl) requests() []serverapi.PromptAnswerBatchRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.PromptAnswerBatchRequest(nil), c.approvalRequests...)
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
	if active := testActiveAsk(model); active == nil || active.prompt.PromptID != successor.PromptID {
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
		model.ask.current.prompt.PromptID != "ask-new-session" ||
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

func TestDenyCommentaryDeadlineKeepsEditedDraftActionableWithoutQueuedCopy(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newDeadlineThenSuccessApprovalControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testApprovalPrompt(
			"deny-commentary-deadline",
			"Allow access?",
			clientui.ApprovalDecisionAllowOnce,
			clientui.ApprovalDecisionDeny,
		),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("original denial")})

	next, firstDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if firstDelivery == nil || len(model.injectedQueue) != 0 {
		t.Fatalf("deny submission = command %v queued %+v, want direct delivery without queued commentary", firstDelivery, model.injectedQueue)
	}
	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstDelivery()
	}()
	<-control.firstStarted

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edited")})
	if testAskInput(model) != "original denial edited" {
		t.Fatalf("retry commentary = %q, want edited draft", testAskInput(model))
	}
	close(control.firstRelease)
	model = updateUIModel(t, model, <-firstResult)
	if testPromptAnswerDeliveryActive(model) || testActiveAsk(model) == nil {
		t.Fatal("denial deadline did not restore the unresolved prompt")
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("denial deadline notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}

	next, secondDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runPromptDeliveryCommand(t, next.(*uiModel), secondDelivery)
	requests := control.requests()
	if len(requests) != 2 {
		t.Fatalf("denial requests = %d, want deadline plus user resubmission", len(requests))
	}
	firstAnswer := requireApprovalAnswerEntry(t, requests[0]).ApprovalAnswer
	secondAnswer := requireApprovalAnswerEntry(t, requests[1]).ApprovalAnswer
	if firstAnswer.Decision != clientui.ApprovalDecisionDeny ||
		secondAnswer.Decision != clientui.ApprovalDecisionDeny {
		t.Fatalf("denial answers = %+v then %+v, want deny", firstAnswer, secondAnswer)
	}
	if approvalCommentary(firstAnswer) != "original denial" || approvalCommentary(secondAnswer) != "original denial edited" {
		t.Fatalf("denial commentary = %q then %q, want immutable submission then edited retry", approvalCommentary(firstAnswer), approvalCommentary(secondAnswer))
	}
	if len(model.injectedQueue) != 0 {
		t.Fatalf("denial commentary created a queued copy: %+v", model.injectedQueue)
	}
	if testPromptAnswerDeliveryActive(model) || testActiveAsk(model) != nil {
		t.Fatal("successful denial delivery did not immediately finish the prompt")
	}
}

func TestAllowCommentaryQueueUnlocksBeforeCancelableApprovalDelivery(t *testing.T) {
	control := newDeadlineThenSuccessApprovalControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeClient := &runtimeControlFakeClient{submitQueuedID: "allow-commentary-queue"}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model.setRuntimeActivityBusyForTest(true)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testApprovalPrompt(
			"allow-commentary-cancel",
			"Allow access?",
			clientui.ApprovalDecisionAllowOnce,
			clientui.ApprovalDecisionDeny,
		),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("safe operation")})

	next, queueCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	if queueCommand == nil || !model.ask.answerPending {
		t.Fatal("allow commentary did not enter the locked queue stage")
	}
	next, deliveryCommand := model.Update(queueCommand())
	model = next.(*uiModel)
	if deliveryCommand == nil || model.ask.answerPending {
		t.Fatal("completed queue stage did not unlock the prompt before approval delivery")
	}

	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- deliveryCommand()
	}()
	<-control.firstStarted

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edited")})
	if testAskInput(model) != "safe operation edited" {
		t.Fatalf("approval retry draft = %q, want responsive editing during delivery", testAskInput(model))
	}
	next, cancelDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = next.(*uiModel)
	if cancelDelivery == nil || !testPromptAnswerDeliveryActive(model) {
		t.Fatal("Esc did not replace the active approval delivery with a cancellation delivery")
	}
	model = updateUIModel(t, model, <-firstResult)
	model = runPromptDeliveryCommand(t, model, cancelDelivery)

	requests := control.requests()
	if len(requests) != 2 {
		t.Fatalf("approval requests = %d, want original allow plus cancellation", len(requests))
	}
	firstAnswer := requireApprovalAnswerEntry(t, requests[0]).ApprovalAnswer
	if firstAnswer.Decision != clientui.ApprovalDecisionAllowOnce || approvalCommentary(firstAnswer) != "safe operation" {
		t.Fatalf("original immutable approval request = %+v", requests[0])
	}
	if len(requests[1].Entries) != 1 || requests[1].Entries[0].Declined == nil {
		t.Fatalf("replacement approval request = %+v, want Declined", requests[1])
	}
	if testActiveAsk(model) != nil {
		t.Fatal("successful Declined batch did not immediately remove the Approval")
	}
}

func TestStaleQueuedApprovalCommentaryDoesNotUnlockCurrentPrompt(t *testing.T) {
	model, _ := newProjectedPromptTestUIModel(t)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testApprovalPrompt(
			"approval-current",
			"Allow current operation?",
			clientui.ApprovalDecisionAllowOnce,
			clientui.ApprovalDecisionDeny,
		),
	)})
	model.ask.answerPending = true

	command := model.answerQueuedApprovalCommentary(clientui.PromptAnswer{
		PromptID: "approval-stale",
		Approval: &clientui.ApprovalPromptAnswer{
			Decision:   clientui.ApprovalDecisionAllowOnce,
			Commentary: "stale commentary",
		},
	})

	if command != nil {
		t.Fatal("stale queued approval commentary created a delivery command")
	}
	if !model.ask.answerPending {
		t.Fatal("stale queued approval commentary unlocked the current prompt queue stage")
	}
	if testPromptAnswerDeliveryActive(model) {
		t.Fatal("stale queued approval commentary created active delivery ownership")
	}
}

func TestInterruptRestorationDeliversQueuedApprovalCommentary(t *testing.T) {
	model, control := newProjectedPromptTestUIModel(t)
	model.engine = &runtimeControlFakeClient{}
	prompt := testApprovalPrompt(
		"approval-interrupted-commentary",
		"Allow operation?",
		clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionDeny,
	)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(prompt)})
	answer := clientui.PromptAnswer{
		PromptID: string(prompt.PromptID),
		Approval: &clientui.ApprovalPromptAnswer{
			Decision:   clientui.ApprovalDecisionAllowOnce,
			Commentary: "interrupted commentary",
		},
	}
	_ = model.enqueueInjectedInputWithApprovalAnswer("interrupted commentary", &answer)
	model.setPendingInterrupt(true)

	for _, msg := range collectCmdMessages(t, model.acknowledgePendingInterrupt()) {
		model = updateUIModel(t, model, msg)
	}

	request := requirePromptAnswerBatchRequest(t, control)
	approvalAnswer := requireApprovalAnswerEntry(t, request).ApprovalAnswer
	if approvalAnswer.Decision != clientui.ApprovalDecisionAllowOnce ||
		approvalCommentary(approvalAnswer) != "interrupted commentary" {
		t.Fatalf("approval request = %+v, want queued interrupted commentary", request)
	}
}

func TestAllowCommentaryAnswerDeadlineRestoresFreshQueueAndAnswerResubmission(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newDeadlineThenSuccessApprovalControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeClient := &runtimeControlFakeClient{submitQueuedID: "allow-commentary-queue"}
	model := newProjectedTestUIModel(runtimeClient)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model.setRuntimeActivityBusyForTest(true)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testApprovalPrompt(
			"allow-commentary-deadline",
			"Allow access?",
			clientui.ApprovalDecisionAllowOnce,
			clientui.ApprovalDecisionDeny,
		),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("original allow")})

	next, firstQueueCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	next, firstDelivery := model.Update(firstQueueCommand())
	model = next.(*uiModel)
	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstDelivery()
	}()
	<-control.firstStarted

	close(control.firstRelease)
	model = updateUIModel(t, model, <-firstResult)
	if testPromptAnswerDeliveryActive(model) || model.ask.answerPending || testActiveAsk(model) == nil {
		t.Fatal("allow answer deadline did not restore the unresolved prompt")
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("allow deadline notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" edited")})

	next, secondQueueCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	next, secondDelivery := model.Update(secondQueueCommand())
	model = runPromptDeliveryCommand(t, next.(*uiModel), secondDelivery)

	requests := control.requests()
	if len(requests) != 2 {
		t.Fatalf("allow requests = %d, want deadline plus user resubmission", len(requests))
	}
	firstAnswer := requireApprovalAnswerEntry(t, requests[0]).ApprovalAnswer
	secondAnswer := requireApprovalAnswerEntry(t, requests[1]).ApprovalAnswer
	if approvalCommentary(firstAnswer) != "original allow" || approvalCommentary(secondAnswer) != "original allow edited" {
		t.Fatalf("allow commentary = %q then %q, want original then edited resubmission", approvalCommentary(firstAnswer), approvalCommentary(secondAnswer))
	}
	if runtimeClient.submitCalls != 2 {
		t.Fatalf("allow commentary submit calls = %d, want one per user submission", runtimeClient.submitCalls)
	}
	if testPromptAnswerDeliveryActive(model) || model.ask.answerPending || testActiveAsk(model) != nil {
		t.Fatal("successful allow resubmission did not immediately finish the prompt")
	}
}

func testPromptAnswerDeliveryActive(model *uiModel) bool {
	return model != nil && model.ask.activeDelivery != nil
}
