package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/registry"
	"core/server/runtime"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func TestReviewerActivityPublishesInvocationAndTerminalStateToTUI(t *testing.T) {
	tests := []struct {
		name     string
		terminal string
	}{
		{name: "success", terminal: "success"},
		{name: "failure", terminal: "failure"},
		{name: "cancellation", terminal: "cancellation"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reviewer := newBlockingReviewerActivityClient()
			if testCase.terminal == "failure" {
				reviewer.resultErr = errors.Join(llm.ErrInvalidRequest, errors.New("reviewer unavailable"))
			}
			activity := registry.NewRuntimeRegistry()
			var engine *runtime.Engine
			store, engine := newAppRuntimeEngine(t, reviewerActivityMainClient{}, runtime.Config{
				Model:         "gpt-5",
				ThinkingLevel: "medium",
				OnEvent: func(event runtime.Event) {
					if event.Kind == runtime.EventRuntimeActivityChanged {
						_ = activity.PublishRuntimeEventToAll(event)
					}
				},
				Reviewer: runtime.ReviewerConfig{
					Frequency: "all",
					Model:     "gpt-5",
					Client:    reviewer,
				},
			})
			sessionID, err := runtimeids.ParseSessionID(engine.SessionID())
			if err != nil {
				t.Fatalf("parse Session ID: %v", err)
			}
			resource, err := runtimeids.NewSessionResourceRef(sessionID, 1)
			if err != nil {
				t.Fatalf("create Runtime resource: %v", err)
			}
			if err := activity.ResourceReady(
				context.Background(),
				sessionruntime.AgentResourceDescriptor{Ref: resource, State: sessionruntime.AgentResourceReady},
				engine,
				func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil },
			); err != nil {
				t.Fatalf("register Runtime: %v", err)
			}
			var closeOnce sync.Once
			closeEngine := func() {
				closeOnce.Do(func() {
					if err := engine.Close(); err != nil && !errors.Is(err, context.Canceled) {
						t.Errorf("close Runtime: %v", err)
					}
				})
			}
			t.Cleanup(func() {
				closeEngine()
				_ = activity.ResourceDraining(
					context.Background(),
					sessionruntime.AgentResourceDescriptor{Ref: resource, State: sessionruntime.AgentResourceReady},
				)
			})

			subscription, err := activity.SubscribeSessionTranscript(
				context.Background(),
				serverapi.TranscriptSubscribeRequest{SessionID: engine.SessionID()},
			)
			if err != nil {
				t.Fatalf("subscribe transcript: %v", err)
			}
			defer func() { _ = subscription.Close() }()

			runtimeClient := newTestSessionRuntimeClient(
				&countingSessionViewClient{},
				newUnavailableRuntimeControlService(),
			)
			model := newProjectedTestUIModel(runtimeClient)
			controller := newOngoingTranscriptController(
				&ongoingSurfaceSpy{},
				model.ongoingFrameInput,
				runtimeClient.admitTranscriptMessageState,
				model.applyAdmittedTranscriptMessageState,
			)
			applyReviewerActivityMessage(
				t,
				controller,
				nextReviewerActivityMessage(t, subscription, clientui.ReviewerActivityInactive),
			)

			submitDone := make(chan error, 1)
			go func() {
				answer, submitErr := engine.SubmitUserMessage(context.Background(), "review this")
				if submitErr == nil && (answer.Content == nil || *answer.Content != "main answer") {
					submitErr = errors.New("main answer was not returned")
				}
				submitDone <- submitErr
			}()
			select {
			case <-reviewer.started:
			case <-time.After(3 * time.Second):
				t.Fatal("Reviewer did not start")
			}
			applyReviewerActivityMessage(
				t,
				controller,
				nextReviewerActivityMessage(t, subscription, clientui.ReviewerActivityInvoking),
			)
			if !model.isReviewerActive() {
				t.Fatalf("TUI Reviewer projection = %+v, want active", model.runtimeActivityProjection)
			}
			select {
			case submitErr := <-submitDone:
				if submitErr != nil {
					t.Fatalf("main answer: %v", submitErr)
				}
			case <-time.After(time.Second):
				t.Fatal("main answer waited for blocked Reviewer")
			}
			if testCase.terminal == "success" {
				if _, err := engine.SubmitUserMessage(context.Background(), "ordinary work while reviewing"); err != nil {
					t.Fatalf("ordinary work while Reviewer runs: %v", err)
				}
				if calls := reviewer.calls.Load(); calls != 1 {
					t.Fatalf("Reviewer calls while already active = %d, want one", calls)
				}
			}

			if testCase.terminal == "cancellation" {
				closeEngine()
			} else {
				close(reviewer.release)
			}
			applyReviewerActivityMessage(
				t,
				controller,
				nextReviewerActivityMessage(t, subscription, clientui.ReviewerActivityInactive),
			)
			if model.isReviewerActive() {
				t.Fatalf("TUI Reviewer projection = %+v, want inactive", model.runtimeActivityProjection)
			}
			if testCase.terminal == "failure" {
				closeEngine()
				reopened := newAppRuntimeEngineWithStore(
					t,
					store,
					reviewerActivityMainClient{},
					runtime.Config{Model: "gpt-5", ThinkingLevel: "medium"},
				)
				if err := reopened.Close(); err != nil {
					t.Fatalf("close reopened Runtime: %v", err)
				}
			}
		})
	}
}

type reviewerActivityMainClient struct{}

func (reviewerActivityMainClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	return reviewerActivityMainResponse(), nil
}

func (reviewerActivityMainClient) GenerateStream(
	_ context.Context,
	_ llm.Request,
	onDelta func(string),
) (llm.Response, error) {
	if onDelta != nil {
		onDelta("main answer")
	}
	return reviewerActivityMainResponse(), nil
}

func reviewerActivityMainResponse() llm.Response {
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("main answer"),
		},
		Usage: llm.Usage{WindowTokens: 200_000},
	}
}

func (reviewerActivityMainClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type blockingReviewerActivityClient struct {
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
	resultErr error
	calls     atomic.Int32
}

func newBlockingReviewerActivityClient() *blockingReviewerActivityClient {
	return &blockingReviewerActivityClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *blockingReviewerActivityClient) Generate(ctx context.Context, _ llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	c.calls.Add(1)
	c.startOnce.Do(func() { close(c.started) })
	select {
	case <-c.release:
		if c.resultErr != nil {
			return llm.Response{}, c.resultErr
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value(`{"suggestions":[]}`),
			},
			Usage: llm.Usage{WindowTokens: 200_000},
		}, nil
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
}

func (*blockingReviewerActivityClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func nextReviewerActivityMessage(
	t *testing.T,
	subscription serverapi.TranscriptSubscription,
	want clientui.ReviewerActivity,
) clientui.TranscriptMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		message, err := subscription.Next(ctx)
		if err != nil {
			t.Fatalf("read Reviewer activity: %v", err)
		}
		switch message.Kind() {
		case clientui.TranscriptMessageHydration:
			if payload := message.Payload().(clientui.TranscriptHydration); payload.RuntimeReadModelUpdate.Activity.Reviewer == want {
				return message
			}
		case clientui.TranscriptMessageRuntimeReadModelUpdate:
			if payload := message.Payload().(clientui.RuntimeReadModelUpdate); payload.Activity.Reviewer == want {
				return message
			}
		}
	}
}

func applyReviewerActivityMessage(
	t *testing.T,
	controller *ongoingTranscriptController,
	message clientui.TranscriptMessage,
) {
	t.Helper()
	if _, _, err := controller.Accept(message); err != nil {
		t.Fatalf("apply Reviewer activity message: %v", err)
	}
}
