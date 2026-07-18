package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOngoingTranscriptPromptHydrationIsIdempotentWhileAnswerBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	control := newBlockingPromptControl()
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	surface := &ongoingSurfaceSpy{}
	model := sizedTestUIModel(newProjectedStaticUIModel(WithUITurnQueueHook(hooks)), 80, 24)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model.promptAttention = hooks
	model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, surface)

	first := testQuestionPrompt("ask-1", "Choose once", "first", "second")
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRunning,
		QueueAccepting: true,
		ActiveStep: &clientui.RuntimeActiveStep{
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			RunID:      ongoingTestRunID(),
			StepID:     ongoingTestStepID(),
		},
	}
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{first}
	if err := hydration.Validate(); err != nil {
		t.Fatalf("validate initial hydration: %v", err)
	}
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         hydration,
	})
	if got := ringer.total(); got != 1 {
		t.Fatalf("initial prompt notification count = %d, want 1", got)
	}
	model, firstDelivery := startPromptAnswerCommand(t, model)

	firstRequest := control.nextRequest(t)
	if got, want := firstRequest.AskID, string(first.PromptID); got != want {
		t.Fatalf("first answered prompt = %q, want %q", got, want)
	}
	if firstRequest.SelectedOptionNumber == nil || *firstRequest.SelectedOptionNumber != 1 {
		t.Fatalf("first selected option = %v, want 1", firstRequest.SelectedOptionNumber)
	}

	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventLoss,
		SourceSessionID: ongoingTestSessionID(),
		Err:             errors.New("test scratch hydration"),
	})
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         hydration,
	})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	control.assertNoRequest(t)
	if got := ringer.total(); got != 1 {
		t.Fatalf("prompt notification count = %d, want 1", got)
	}
	if got := len(surface.lastFrameSectionLines(ongoing.FrameSectionPendingPrompt)); got != 1 {
		t.Fatalf("pending prompt section cardinality = %d, want 1 before canonical resolution", got)
	}

	control.release()
	control.waitForCompletion(t)
	model = updateUIModel(t, model, awaitPromptAnswerCommand(t, firstDelivery))

	resolved := first
	resolved.State = clientui.TranscriptPromptStateResolved
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 2,
			Kind:     clientui.TranscriptMessagePromptResolved,
			Payload:  clientui.TranscriptPayload{PromptResolved: &resolved},
		},
	})

	second := testQuestionPrompt("ask-2", "Choose next", "next")
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 3,
			Kind:     clientui.TranscriptMessagePromptPending,
			Payload:  clientui.TranscriptPayload{PromptPending: &second},
		},
	})
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	secondRequest := control.nextRequest(t)
	if got, want := secondRequest.AskID, string(second.PromptID); got != want {
		t.Fatalf("second answered prompt = %q, want %q", got, want)
	}
	control.assertNoRequest(t)
}

func TestOngoingTranscriptPromptHydrationCancelsOmittedWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	control := newCancellationObservingPromptControl()
	model := sizedTestUIModel(newProjectedStaticUIModel(), 80, 24)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, &ongoingSurfaceSpy{})

	omitted := testQuestionPrompt("omitted-worker", "Choose", "one")
	model = deliverPromptHydration(t, model, omitted)
	model, omittedDelivery := startPromptAnswerCommand(t, model)
	if got := control.nextRequest(t).AskID; got != string(omitted.PromptID) {
		t.Fatalf("blocked request prompt = %q, want %q", got, omitted.PromptID)
	}

	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventLoss,
		SourceSessionID: ongoingTestSessionID(),
		Err:             errors.New("scratch"),
	})
	emptyHydration := ongoingHydrationMessage(1)
	emptyHydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         emptyHydration,
	})
	if got := control.nextCancellation(t); got != string(omitted.PromptID) {
		t.Fatalf("canceled prompt = %q, want %q", got, omitted.PromptID)
	}
	model = updateUIModel(t, model, awaitPromptAnswerCommand(t, omittedDelivery))
	control.assertNoRequest(t)

	nextPrompt := testQuestionPrompt("after-omission", "Next?", "yes")
	nextPrompt.CreatedAt = omitted.CreatedAt.Add(time.Second)
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 2,
			Kind:     clientui.TranscriptMessagePromptPending,
			Payload:  clientui.TranscriptPayload{PromptPending: &nextPrompt},
		},
	})
	model, nextDelivery := startPromptAnswerCommand(t, model)
	if got := control.nextRequest(t).AskID; got != string(nextPrompt.PromptID) {
		t.Fatalf("next request prompt = %q, want %q", got, nextPrompt.PromptID)
	}
	cancel()
	_ = awaitPromptAnswerCommand(t, nextDelivery)
}

func TestOngoingTranscriptPromptHydrationKeepsRetainedDeliveryWhenDroppingDuplicateOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	control := newCancellationObservingPromptControl()
	model := sizedTestUIModel(newProjectedStaticUIModel(), 80, 24)
	model.promptAnswers = newTranscriptPromptAnswerer(ctx, control)
	model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, &ongoingSurfaceSpy{})

	prompt := testQuestionPrompt("duplicate-owned-delivery", "Choose", "one")
	model = deliverPromptHydration(t, model, prompt)
	model, delivery := startPromptAnswerCommand(t, model)
	if got := control.nextRequest(t).AskID; got != string(prompt.PromptID) {
		t.Fatalf("blocked request prompt = %q, want %q", got, prompt.PromptID)
	}
	model.ask.queue = append(model.ask.queue, model.transcriptPromptEvent(prompt))

	model = scratchHydratePrompts(t, model, []clientui.TranscriptPrompt{prompt})
	control.assertNoCancellation(t)
	control.assertNoRequest(t)

	cancel()
	_ = awaitPromptAnswerCommand(t, delivery)
}

func startPromptAnswerCommand(t *testing.T, model *uiModel) (*uiModel, <-chan tea.Msg) {
	t.Helper()
	next, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", next)
	}
	if command == nil {
		t.Fatal("prompt answer did not return a delivery command")
	}
	completed := make(chan tea.Msg, 1)
	go func() {
		completed <- command()
	}()
	return updated, completed
}

func awaitPromptAnswerCommand(t *testing.T, completed <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case message := <-completed:
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt answer delivery command")
		return nil
	}
}

type blockingPromptControl struct {
	requests  chan serverapi.AskAnswerRequest
	released  chan struct{}
	completed chan struct{}
}

type cancellationObservingPromptControl struct {
	requests      chan serverapi.AskAnswerRequest
	cancellations chan string
}

func newCancellationObservingPromptControl() *cancellationObservingPromptControl {
	return &cancellationObservingPromptControl{
		requests:      make(chan serverapi.AskAnswerRequest, 4),
		cancellations: make(chan string, 4),
	}
}

func (c *cancellationObservingPromptControl) AnswerAsk(
	ctx context.Context,
	request serverapi.AskAnswerRequest,
) error {
	c.requests <- request
	<-ctx.Done()
	c.cancellations <- request.AskID
	return ctx.Err()
}

func (c *cancellationObservingPromptControl) AnswerApproval(
	context.Context,
	serverapi.ApprovalAnswerRequest,
) error {
	return errors.New("unexpected approval answer")
}

func (c *cancellationObservingPromptControl) nextRequest(t *testing.T) serverapi.AskAnswerRequest {
	t.Helper()
	select {
	case request := <-c.requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for prompt-control request")
		return serverapi.AskAnswerRequest{}
	}
}

func (c *cancellationObservingPromptControl) assertNoRequest(t *testing.T) {
	t.Helper()
	select {
	case request := <-c.requests:
		t.Fatalf("unexpected delayed prompt-control request: %+v", request)
	case <-time.After(100 * time.Millisecond):
	}
}

func (c *cancellationObservingPromptControl) nextCancellation(t *testing.T) string {
	t.Helper()
	select {
	case promptID := <-c.cancellations:
		return promptID
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for prompt-control cancellation")
		return ""
	}
}

func (c *cancellationObservingPromptControl) assertNoCancellation(t *testing.T) {
	t.Helper()
	select {
	case promptID := <-c.cancellations:
		t.Fatalf("unexpected prompt-control cancellation for %q", promptID)
	case <-time.After(100 * time.Millisecond):
	}
}

func newBlockingPromptControl() *blockingPromptControl {
	return &blockingPromptControl{
		requests:  make(chan serverapi.AskAnswerRequest, 4),
		released:  make(chan struct{}),
		completed: make(chan struct{}, 4),
	}
}

func (c *blockingPromptControl) AnswerAsk(ctx context.Context, request serverapi.AskAnswerRequest) error {
	select {
	case c.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.released:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.completed <- struct{}{}
	return nil
}

func (c *blockingPromptControl) AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error {
	return errors.New("unexpected approval answer")
}

func (c *blockingPromptControl) nextRequest(t *testing.T) serverapi.AskAnswerRequest {
	t.Helper()
	select {
	case request := <-c.requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt-control request")
		return serverapi.AskAnswerRequest{}
	}
}

func (c *blockingPromptControl) assertNoRequest(t *testing.T) {
	t.Helper()
	select {
	case request := <-c.requests:
		t.Fatalf("unexpected duplicate prompt-control request: %+v", request)
	case <-time.After(100 * time.Millisecond):
	}
}

func (c *blockingPromptControl) release() {
	close(c.released)
}

func (c *blockingPromptControl) waitForCompletion(t *testing.T) {
	t.Helper()
	select {
	case <-c.completed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt-control completion")
	}
}
