package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestEnginePublishesLiveRunTerminalFactsThroughSubmitSeam(t *testing.T) {
	t.Run("completed final answer", func(t *testing.T) {
		store := mustCreateTestSession(t)
		events := &liveRunEventCollector{}
		eng := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{finalTextResponse("done")}}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: events.accept,
		})

		if _, err := eng.SubmitUserMessage(t.Context(), "finish"); err != nil {
			t.Fatalf("SubmitUserMessage: %v", err)
		}
		result := events.single(t)
		if result.Status != RunStatusCompleted ||
			result.ResultKind != LiveRunResultAssistantFinalAnswer ||
			messageContent(result.AssistantMessage) != "done" {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("runtime failure", func(t *testing.T) {
		store := mustCreateTestSession(t)
		withGenerateRetryDelays(t, nil)
		failure := errors.New("provider failed")
		events := &liveRunEventCollector{}
		eng := mustNewTestEngine(t, store, &fakeClient{errors: []error{failure}}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: events.accept,
		})

		if _, err := eng.SubmitUserMessage(t.Context(), "fail"); !errors.Is(err, failure) {
			t.Fatalf("SubmitUserMessage error = %v, want %v", err, failure)
		}
		result := events.single(t)
		if result.Status != RunStatusFailed ||
			!errors.Is(result.Error, failure) {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("completed blank final", func(t *testing.T) {
		store := mustCreateTestSession(t)
		events := &liveRunEventCollector{}
		eng := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value(""),
			},
			Usage: llm.Usage{WindowTokens: 200_000},
		}}}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: events.accept,
		})

		message, err := eng.SubmitUserMessage(t.Context(), "finish silently")
		if err != nil {
			t.Fatalf("SubmitUserMessage: %v", err)
		}
		if message.Content != nil {
			t.Fatalf("blank final content = %#v, want absent", message.Content)
		}
		result := events.single(t)
		if result.Status != RunStatusCompleted ||
			result.ResultKind != LiveRunResultNoFinalAnswer ||
			result.NoFinalReason != LiveRunNoFinalAnswerReasonUnknown {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("interruption is excluded", func(t *testing.T) {
		store := mustCreateTestSession(t)
		started := make(chan struct{})
		client := &interruptibleLiveRunClient{started: started}
		events := &liveRunEventCollector{}
		eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: events.accept,
		})
		done := make(chan error, 1)
		go func() {
			_, err := eng.SubmitUserMessage(context.Background(), "interrupt")
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for active run")
		}
		stopped, err := eng.TryInterruptActiveRun()
		if err != nil || !stopped {
			t.Fatalf("TryInterruptActiveRun stopped=%t err=%v", stopped, err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitUserMessage error = %v, want context canceled", err)
		}
		if got := events.count(); got != 0 {
			t.Fatalf("interruption published live-run terminal facts: %+v", events.snapshot())
		}
	})

	t.Run("non-error no-final shell run is excluded", func(t *testing.T) {
		store := mustCreateTestSession(t)
		events := &liveRunEventCollector{}
		eng := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
			Model:   "gpt-5",
			OnEvent: events.accept,
		})
		if _, err := eng.SubmitUserShellCommand(t.Context(), "pwd"); err != nil {
			t.Fatalf("SubmitUserShellCommand: %v", err)
		}
		if got := events.count(); got != 0 {
			t.Fatalf("shell run published %d live-run terminal facts", got)
		}
	})

	t.Run("terminal callback does not hold waiters or queued successor scheduling", func(t *testing.T) {
		store := mustCreateTestSession(t)
		client := &fakeClient{responses: []llm.Response{
			finalTextResponse("first"),
			finalTextResponse("second"),
		}}
		stepLifecycle := newBlockingStepLifecycleSink()
		callbackStarted := make(chan struct{})
		releaseCallback := make(chan struct{})
		var terminalCallbacks int
		var callbackMu sync.Mutex
		eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
			Model:         "gpt-5",
			StepLifecycle: stepLifecycle,
			OnEvent: func(event Event) {
				if event.Kind != EventLiveRunFinished {
					return
				}
				callbackMu.Lock()
				terminalCallbacks++
				index := terminalCallbacks
				callbackMu.Unlock()
				if index == 1 {
					close(callbackStarted)
					<-releaseCallback
				}
			},
		})

		firstDone := make(chan error, 1)
		go func() {
			_, err := eng.SubmitUserMessage(context.Background(), "first")
			firstDone <- err
		}()
		select {
		case <-stepLifecycle.endedStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for step terminal publication")
		}
		eng.QueueUserMessage("queued successor")
		close(stepLifecycle.releaseEnded)
		select {
		case <-callbackStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for terminal callback")
		}

		deadline := time.Now().Add(3 * time.Second)
		for {
			calls := fakeClientCallCount(client)
			if calls >= 2 || !time.Now().Before(deadline) {
				if calls < 2 {
					t.Fatal("queued successor scheduling waited for prior terminal callback")
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(releaseCallback)
		if err := <-firstDone; err != nil {
			t.Fatalf("first SubmitUserMessage: %v", err)
		}
		waitEngineLifecycleTasks(t, eng)
	})
}

type liveRunEventCollector struct {
	mu      sync.Mutex
	results []LiveRunResult
}

func (c *liveRunEventCollector) accept(event Event) {
	if event.Kind != EventLiveRunFinished || event.LiveRunResult == nil {
		return
	}
	c.mu.Lock()
	c.results = append(c.results, *event.LiveRunResult)
	c.mu.Unlock()
}

func (c *liveRunEventCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.results)
}

func (c *liveRunEventCollector) snapshot() []LiveRunResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LiveRunResult(nil), c.results...)
}

func (c *liveRunEventCollector) single(t *testing.T) LiveRunResult {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.results) != 1 {
		t.Fatalf("live-run terminal facts = %d, want 1", len(c.results))
	}
	return c.results[0]
}

type interruptibleLiveRunClient struct {
	started chan<- struct{}
}

func (c *interruptibleLiveRunClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	close(c.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func (*interruptibleLiveRunClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}
