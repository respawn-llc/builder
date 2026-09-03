package workflowrunner

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	agentruntime "core/server/runtime"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type retainedBackgroundProgressClient struct {
	*retainedProgressClient
	backgroundCalls      int
	backgroundFirst      chan struct{}
	backgroundSecond     chan struct{}
	backgroundRelease    <-chan struct{}
	backgroundFirstGate  sync.Once
	backgroundSecondGate sync.Once
}

func (c *retainedBackgroundProgressClient) Generate(
	ctx context.Context, request llm.Request, callbacks llm.StreamCallbacks,
) (llm.Response, error) {
	background := !retainedRequestEndsWithMessage(request, "continue") &&
		c.backgroundFirst != nil && retainedRequestContainsBackgroundNotice(request)
	if !background {
		return c.retainedProgressClient.Generate(ctx, request, callbacks)
	}
	c.recordRequest(request)
	c.mu.Lock()
	c.backgroundCalls++
	backgroundCall := c.backgroundCalls
	c.mu.Unlock()
	switch backgroundCall {
	case 1:
		c.backgroundFirstGate.Do(func() { close(c.backgroundFirst) })
		return retainedResponse("background progress", llm.ToolCall{
			ID: "background-shell", Name: string(toolspec.ToolExecCommand),
			Input: []byte(`{"cmd":"true"}`),
		}), nil
	case 2:
		c.backgroundSecondGate.Do(func() { close(c.backgroundSecond) })
		<-c.backgroundRelease
		return retainedResponse("background progress", llm.ToolCall{
			ID: "background-shell-again", Name: string(toolspec.ToolExecCommand),
			Input: []byte(`{"cmd":"true"}`),
		}), nil
	default:
		return llm.Response{}, &llm.ProviderAPIError{
			ProviderID: "test", StatusCode: 400,
			Code: llm.UnifiedErrorCodeProviderContract, Err: errors.New("stop background"),
		}
	}
}

func retainedRequestEndsWithMessage(request llm.Request, content string) bool {
	messages := llm.MessagesFromItems(request.Items)
	return len(messages) > 0 && messages[len(messages)-1].Content != nil &&
		*messages[len(messages)-1].Content == content
}

func retainedRequestContainsBackgroundNotice(request llm.Request) bool {
	return slices.ContainsFunc(llm.MessagesFromItems(request.Items), func(message llm.Message) bool {
		return message.MessageType != nil && *message.MessageType == llm.MessageTypeBackgroundNotice
	})
}

func startRetainedSameResourceBackground(
	t *testing.T,
	f *currentNodeRunnerFixture,
	sessionID runtimeids.SessionID,
	client *retainedBackgroundProgressClient,
) (chan error, func()) {
	t.Helper()
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client.backgroundFirst, client.backgroundSecond, client.backgroundRelease = first, second, release
	var releaseOnce sync.Once
	releaseBackground := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBackground)
	done := make(chan error, 1)
	go func() {
		err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(
			ctx context.Context, engine *agentruntime.Engine,
		) error {
			return engine.RunBackgroundShellContinuation(ctx, agentruntime.BackgroundShellEvent{
				Type:       agentruntime.BackgroundShellEventCompleted,
				ID:         "background",
				ActivityID: uuid.New(),
				NoticeText: "background notice",
			})
		})
		done <- err
	}()
	select {
	case <-first:
	case <-time.After(currentNodeRunnerWait):
		for index, request := range client.Requests() {
			t.Logf("request %d: background_notice=%t selected_tail=%t", index,
				retainedRequestContainsBackgroundNotice(request),
				retainedRequestEndsWithMessage(request, "continue"))
		}
		t.Fatalf("background did not reach first provider request")
	case err := <-done:
		var snapshot *agentruntime.RunSnapshot
		_ = f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
			snapshot = engine.ActiveStepSnapshot()
			return nil
		})
		t.Fatalf("background ended before first provider request: %v; active=%+v; requests=%d", err, snapshot, len(client.Requests()))
	}
	return done, releaseBackground
}

func runRetainedSameResourceBackgroundProgress(t *testing.T) {
	client := &retainedBackgroundProgressClient{
		retainedProgressClient: &retainedProgressClient{selectedCompletes: true},
	}
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeTool
	f.starter.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeTool
	sessionID, reference, _ := prepareRetainedSessionForWorkflow(
		t, f, createCurrentNodeAgentWorkflow(t, f.store), client,
	)
	client.transitionByAssignment = map[string]string{
		workflowruntime.CurrentNodePromptIdentity(reference): "done",
	}
	backgroundDone, releaseBackground := startRetainedSameResourceBackground(t, f, sessionID, client)
	var progress []serverapi.RunPromptProgress
	selectedDone := make(chan error, 1)
	go func() {
		_, err := runPromptClientForCurrentNodeFixture(f, f.controller).RunPrompt(
			context.Background(),
			serverapi.RunPromptRequest{
				Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
				Prompt: "continue",
			},
			serverapi.RunPromptProgressFunc(func(event serverapi.RunPromptProgress) {
				progress = append(progress, event)
			}),
		)
		selectedDone <- err
	}()
	retainedWait(t, client.backgroundSecond, "background did not reach second provider request")
	deadline := time.Now().Add(currentNodeRunnerWait)
	for {
		if _, active := f.authority.SessionExecution(sessionID); active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selected retained execution did not become live")
		}
		time.Sleep(time.Millisecond)
	}
	releaseBackground()
	if err := <-backgroundDone; err == nil {
		t.Fatal("background unexpectedly succeeded")
	}
	client.backgroundFirst, client.backgroundSecond, client.backgroundRelease = nil, nil, nil
	if err := <-selectedDone; err != nil {
		t.Fatalf("selected RunPrompt: %v", err)
	}
	if client.backgroundCalls < 3 {
		t.Fatalf("background provider requests = %d, want real background Step requests", client.backgroundCalls)
	}
	if !hasAssistantProgress(progress, "selected progress") ||
		hasAssistantProgress(progress, "background progress") {
		t.Fatalf("RunPrompt progress = %+v, want selected progress without background progress", progress)
	}
	if !slices.ContainsFunc(client.Requests(), func(request llm.Request) bool {
		return retainedRequestEndsWithMessage(request, "continue")
	}) {
		t.Fatalf("selected provider request did not contain continuation input")
	}
}
