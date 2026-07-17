package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type deadlineThenSuccessPromptControl struct {
	mu           sync.Mutex
	askRequests  []serverapi.AskAnswerRequest
	firstStarted chan struct{}
	firstRelease chan struct{}
}

type scriptedAskPromptControl struct {
	mu          sync.Mutex
	results     []error
	askRequests []serverapi.AskAnswerRequest
}

type deadlineThenSuccessApprovalControl struct {
	mu               sync.Mutex
	approvalRequests []serverapi.ApprovalAnswerRequest
	firstStarted     chan struct{}
	firstRelease     chan struct{}
}

func newDeadlineThenSuccessApprovalControl() *deadlineThenSuccessApprovalControl {
	return &deadlineThenSuccessApprovalControl{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
}

func (c *deadlineThenSuccessApprovalControl) AnswerAsk(context.Context, serverapi.AskAnswerRequest) error {
	return errors.New("unexpected ask answer")
}

func (c *deadlineThenSuccessApprovalControl) AnswerApproval(ctx context.Context, request serverapi.ApprovalAnswerRequest) error {
	c.mu.Lock()
	call := len(c.approvalRequests)
	c.approvalRequests = append(c.approvalRequests, request)
	c.mu.Unlock()
	if call != 0 {
		return nil
	}
	close(c.firstStarted)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.firstRelease:
		return context.DeadlineExceeded
	}
}

func (c *deadlineThenSuccessApprovalControl) requests() []serverapi.ApprovalAnswerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.ApprovalAnswerRequest(nil), c.approvalRequests...)
}

func (c *scriptedAskPromptControl) AnswerAsk(_ context.Context, request serverapi.AskAnswerRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := len(c.askRequests)
	c.askRequests = append(c.askRequests, request)
	if call < len(c.results) {
		return c.results[call]
	}
	return nil
}

func (c *scriptedAskPromptControl) AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error {
	return errors.New("unexpected approval answer")
}

func (c *scriptedAskPromptControl) requests() []serverapi.AskAnswerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.AskAnswerRequest(nil), c.askRequests...)
}

func newDeadlineThenSuccessPromptControl() *deadlineThenSuccessPromptControl {
	return &deadlineThenSuccessPromptControl{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
}

func (c *deadlineThenSuccessPromptControl) AnswerAsk(ctx context.Context, request serverapi.AskAnswerRequest) error {
	c.mu.Lock()
	call := len(c.askRequests)
	c.askRequests = append(c.askRequests, request)
	c.mu.Unlock()
	if call != 0 {
		return nil
	}
	close(c.firstStarted)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.firstRelease:
		return context.DeadlineExceeded
	}
}

func (c *deadlineThenSuccessPromptControl) AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error {
	return errors.New("unexpected approval answer")
}

func (c *deadlineThenSuccessPromptControl) requests() []serverapi.AskAnswerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.AskAnswerRequest(nil), c.askRequests...)
}

func TestAskDeadlineKeepsEditedRetryDraftActionableUntilCanonicalResolution(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newDeadlineThenSuccessPromptControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
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
	if !testPromptAnswerDeliveryActive(model) {
		t.Fatal("successful delivery stopped awaiting canonical prompt resolution")
	}
	if testActiveAsk(model) == nil {
		t.Fatal("successful delivery locally resolved the prompt")
	}

	requests := control.requests()
	if len(requests) != 2 {
		t.Fatalf("ask requests = %d, want deadline attempt plus user resubmission", len(requests))
	}
	if requests[0].ClientRequestID == "" || requests[1].ClientRequestID == "" || requests[0].ClientRequestID == requests[1].ClientRequestID {
		t.Fatalf("request IDs = %q, %q; want distinct non-empty IDs", requests[0].ClientRequestID, requests[1].ClientRequestID)
	}
	if requests[0].Answer != "original" || requests[0].FreeformAnswer != "original" {
		t.Fatalf("first immutable request = %+v, want original draft", requests[0])
	}
	if requests[1].Answer != "original edited" || requests[1].FreeformAnswer != "original edited" {
		t.Fatalf("resubmitted request = %+v, want edited retry draft", requests[1])
	}

	resolveAnsweredTestAskThroughTranscript(t, model)
	if testActiveAsk(model) != nil {
		t.Fatal("prompt remained after canonical transcript resolution")
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

func TestAskRetryThenSuccessKeepsOneRequestIDUntilCanonicalResolution(t *testing.T) {
	control := &scriptedAskPromptControl{results: []error{
		errors.New("retryable one"),
		errors.New("retryable two"),
		nil,
	}}
	answerer := newTranscriptPromptAnswerer(context.Background(), control)
	answerer.retryWait = func(context.Context, time.Duration) error { return nil }

	model := newProjectedStaticUIModel()
	model.promptAnswers = answerer
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-retry-success", "Provide details"),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("answer")})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runPromptDeliveryCommand(t, next.(*uiModel), delivery)
	requests := control.requests()
	if len(requests) != 3 {
		t.Fatalf("ask requests = %d, want two retries then success", len(requests))
	}
	requestID := requests[0].ClientRequestID
	for index, request := range requests {
		if request.ClientRequestID != requestID {
			t.Fatalf("request %d ID = %q, want stable %q", index, request.ClientRequestID, requestID)
		}
	}
	if !testPromptAnswerDeliveryActive(model) || testActiveAsk(model) == nil {
		t.Fatal("successful retry resolved the prompt before canonical transcript resolution")
	}
	resolveAnsweredTestAskThroughTranscript(t, model)
	if testActiveAsk(model) != nil {
		t.Fatal("prompt remained after canonical transcript resolution")
	}
}

func TestAskPersistentRetryableFailureStopsAfterSixCallsAndRestoresActionability(t *testing.T) {
	disableTransientStatusClearForTest(t)
	persistent := errors.New("persistent retryable failure")
	control := &scriptedAskPromptControl{results: []error{
		persistent,
		persistent,
		persistent,
		persistent,
		persistent,
		persistent,
	}}
	answerer := newTranscriptPromptAnswerer(context.Background(), control)
	answerer.retryWait = func(context.Context, time.Duration) error { return nil }

	model := newProjectedStaticUIModel()
	model.promptAnswers = answerer
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-retry-exhausted", "Provide details"),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("retry draft")})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runPromptDeliveryCommand(t, next.(*uiModel), delivery)
	requests := control.requests()
	if len(requests) != 6 {
		t.Fatalf("ask requests = %d, want bounded six calls", len(requests))
	}
	requestID := requests[0].ClientRequestID
	for index, request := range requests {
		if request.ClientRequestID != requestID {
			t.Fatalf("request %d ID = %q, want stable %q", index, request.ClientRequestID, requestID)
		}
	}
	if testPromptAnswerDeliveryActive(model) {
		t.Fatal("exhausted retry left answer delivery active")
	}
	if testActiveAsk(model) == nil || testAskInput(model) != "retry draft" {
		t.Fatal("exhausted retry did not preserve the actionable prompt draft")
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("exhausted retry notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}
}

func TestAskRetryReportsDisconnectAndReachabilityBeforeFinalDelivery(t *testing.T) {
	control := &scriptedAskPromptControl{results: []error{io.EOF, nil}}
	waitStarted := make(chan struct{})
	releaseWait := make(chan struct{})
	answerer := newTranscriptPromptAnswerer(context.Background(), control)
	answerer.retryWait = func(waitCtx context.Context, _ time.Duration) error {
		close(waitStarted)
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-releaseWait:
			return nil
		}
	}

	_, engine := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{ContextWindowTokens: 400_000})
	model := newProjectedEngineUIModel(engine)
	if model.runtimeConnectionEvents == nil {
		t.Fatal("projected runtime model did not create the global connection event channel")
	}
	model.promptAnswers = answerer.withConnectionOutcomeSink(func(err error) {
		enqueueRuntimeConnectionStateChange(model.runtimeConnectionEvents, err)
	})
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-connection-retry", "Proceed?", "Yes", "No"),
	)})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	finalResult := make(chan tea.Msg, 1)
	go func() {
		finalResult <- delivery()
	}()
	<-waitStarted

	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	if !model.runtimeDisconnectStatusVisible() {
		t.Fatal("disconnect was not visible while prompt delivery remained in backoff")
	}
	if !testPromptAnswerDeliveryActive(model) {
		t.Fatal("connection failure prematurely cleared active delivery")
	}
	if model.transientStatus != "" {
		t.Fatalf("connection failure created prompt-local notice %q", model.transientStatus)
	}

	close(releaseWait)
	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	if model.runtimeDisconnectStatusVisible() {
		t.Fatal("reachable retry did not clear the global disconnect state")
	}
	model = updateUIModel(t, model, <-finalResult)
	if !testPromptAnswerDeliveryActive(model) || testActiveAsk(model) == nil {
		t.Fatal("successful delivery stopped awaiting canonical prompt resolution")
	}
	requests := control.requests()
	if len(requests) != 2 || requests[0].ClientRequestID != requests[1].ClientRequestID {
		t.Fatalf("retry requests = %+v, want two calls with one stable request ID", requests)
	}
	resolveAnsweredTestAskThroughTranscript(t, model)
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

func TestAskResolutionCancelsConnectionBackoffWithoutFabricatingReachability(t *testing.T) {
	control := &scriptedAskPromptControl{results: []error{io.EOF}}
	waitStarted := make(chan struct{})
	answerer := newTranscriptPromptAnswerer(context.Background(), control)
	answerer.retryWait = func(waitCtx context.Context, _ time.Duration) error {
		close(waitStarted)
		<-waitCtx.Done()
		return waitCtx.Err()
	}

	_, engine := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{ContextWindowTokens: 400_000})
	model := newProjectedEngineUIModel(engine)
	model.promptAnswers = answerer.withConnectionOutcomeSink(func(err error) {
		enqueueRuntimeConnectionStateChange(model.runtimeConnectionEvents, err)
	})
	first := testQuestionPrompt("ask-cancel-backoff", "First?", "Yes", "No")
	second := testQuestionPrompt("ask-after-cancel", "Second?", "Continue", "Stop")
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(first)})
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(second)})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	finalResult := make(chan tea.Msg, 1)
	go func() {
		finalResult <- delivery()
	}()
	<-waitStarted
	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	if !model.runtimeDisconnectStatusVisible() {
		t.Fatal("disconnect was not visible before cancellation")
	}

	resolveAnsweredTestAskThroughTranscript(t, model)
	if active := testActiveAsk(model); active == nil || active.prompt.PromptID != second.PromptID {
		t.Fatalf("authoritative resolution did not activate the next prompt: %+v", active)
	}
	model = updateUIModel(t, model, <-finalResult)
	if active := testActiveAsk(model); active == nil || active.prompt.PromptID != second.PromptID {
		t.Fatalf("stale canceled result changed the next prompt: %+v", active)
	}
	if len(control.requests()) != 1 {
		t.Fatalf("service calls = %d, want no retry after cancellation", len(control.requests()))
	}
	select {
	case outcome := <-model.runtimeConnectionEvents:
		t.Fatalf("cancellation fabricated connection outcome %+v", outcome)
	default:
	}
	if !model.runtimeDisconnectStatusVisible() {
		t.Fatal("cancellation incorrectly cleared the global disconnect state")
	}

	enqueueRuntimeConnectionStateChange(model.runtimeConnectionEvents, nil)
	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	if model.runtimeDisconnectStatusVisible() {
		t.Fatal("a later real reachable outcome did not clear disconnect state")
	}
}

func TestAskConnectionExhaustionUsesOnlyGlobalDisconnectNotice(t *testing.T) {
	control := &scriptedAskPromptControl{results: []error{io.EOF, io.EOF, io.EOF, io.EOF, io.EOF, io.EOF}}
	answerer := newTranscriptPromptAnswerer(context.Background(), control)
	answerer.retryWait = func(context.Context, time.Duration) error { return nil }

	_, engine := newAppRuntimeEngine(t, statusLineFakeClient{}, runtime.Config{ContextWindowTokens: 400_000})
	model := newProjectedEngineUIModel(engine)
	model.promptAnswers = answerer.withConnectionOutcomeSink(func(err error) {
		enqueueRuntimeConnectionStateChange(model.runtimeConnectionEvents, err)
	})
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testQuestionPrompt("ask-connection-exhausted", "Proceed?", "Yes", "No"),
	)})

	next, delivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	result := delivery()
	model = updateUIModel(t, model, <-model.runtimeConnectionEvents)
	model = updateUIModel(t, model, result)

	if !model.runtimeDisconnectStatusVisible() {
		t.Fatal("connection exhaustion did not leave the global disconnect visible")
	}
	if model.transientStatus != "" {
		t.Fatalf("connection exhaustion created prompt-local notice %q", model.transientStatus)
	}
	if testPromptAnswerDeliveryActive(model) || testActiveAsk(model) == nil {
		t.Fatal("connection exhaustion did not restore the unresolved prompt")
	}
	if len(control.requests()) != 6 {
		t.Fatalf("service calls = %d, want bounded six calls", len(control.requests()))
	}
}

func TestAskTypedTerminalFailureRestoresDraftAndShowsError(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := &scriptedAskPromptControl{results: []error{serverapi.ErrPromptNotFound}}
	answerer := newTranscriptPromptAnswerer(context.Background(), control)

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

func TestApprovalDeadlineKeepsEditedSelectionActionableUntilCanonicalResolution(t *testing.T) {
	disableTransientStatusClearForTest(t)
	control := newDeadlineThenSuccessApprovalControl()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := newProjectedStaticUIModel()
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model = updateUIModel(t, model, askEventMsg{event: model.transcriptPromptEvent(
		testApprovalPrompt(
			"approval-deadline",
			"Allow access?",
			clientui.ApprovalDecisionAllowOnce,
			clientui.ApprovalDecisionDeny,
		),
	)})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})

	next, firstDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstDelivery()
	}()
	<-control.firstStarted

	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyUp})
	if testAskCursor(model) != 0 {
		t.Fatalf("retry selection = %d, want edited allow selection", testAskCursor(model))
	}
	close(control.firstRelease)
	model = updateUIModel(t, model, <-firstResult)
	if testPromptAnswerDeliveryActive(model) || testActiveAsk(model) == nil {
		t.Fatal("approval deadline did not restore the unresolved prompt")
	}
	if model.transientStatusKind != uiStatusNoticeError || model.transientStatus == "" {
		t.Fatalf("approval deadline notice = kind %d text %q, want visible error", model.transientStatusKind, model.transientStatus)
	}

	next, secondDelivery := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = runPromptDeliveryCommand(t, next.(*uiModel), secondDelivery)
	requests := control.requests()
	if len(requests) != 2 {
		t.Fatalf("approval requests = %d, want deadline plus user resubmission", len(requests))
	}
	if requests[0].Decision != clientui.ApprovalDecisionDeny || requests[1].Decision != clientui.ApprovalDecisionAllowOnce {
		t.Fatalf("approval decisions = %q then %q, want immutable deny then edited allow", requests[0].Decision, requests[1].Decision)
	}
	if requests[0].ClientRequestID == requests[1].ClientRequestID {
		t.Fatalf("approval resubmission reused request ID %q", requests[0].ClientRequestID)
	}
	if !testPromptAnswerDeliveryActive(model) || testActiveAsk(model) == nil {
		t.Fatal("successful approval delivery stopped awaiting canonical resolution")
	}
	resolveAnsweredTestAskThroughTranscript(t, model)
}

func testPromptAnswerDeliveryActive(model *uiModel) bool {
	return model != nil && model.ask.activeDelivery != nil
}
