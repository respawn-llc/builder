package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type fakeWorkflowController struct {
	completed                           atomic.Int64
	completeErr                         error
	violations                          atomic.Int64
	protocolBudgetResets                atomic.Int64
	protocolBudgetResetErr              error
	maxHits                             atomic.Int64
	completionObservations              atomic.Int64
	completeExternallyAfterObservations int64
	completedExternally                 atomic.Bool
	mu                                  sync.Mutex
	requests                            []workflowruntime.CompletionRequest
}

func (c *fakeWorkflowController) CompleteCurrentNode(_ context.Context, req workflowruntime.CompletionRequest) (workflowruntime.CompletionResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	if c.completeErr != nil {
		return workflowruntime.CompletionResult{}, c.completeErr
	}
	c.completed.Add(1)
	return workflowruntime.CompletionResult{TransitionID: "transition-applied", State: "applied"}, nil
}

func (c *fakeWorkflowController) RecordProtocolViolation(_ context.Context, req workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	count := c.violations.Add(1)
	interrupted := count >= int64(req.MaxCount)
	if interrupted {
		c.maxHits.Add(1)
	}
	return workflowruntime.ViolationResult{Count: count, Interrupted: interrupted}, nil
}

func (c *fakeWorkflowController) ResetProtocolViolationBudget(_ context.Context, _ workflowruntime.ViolationResetRequest) error {
	if c.protocolBudgetResetErr != nil {
		return c.protocolBudgetResetErr
	}
	c.violations.Store(0)
	c.protocolBudgetResets.Add(1)
	return nil
}

func (c *fakeWorkflowController) ObserveCurrentNodeCompletion(_ context.Context, _ workflowruntime.CompletionObservationRequest) (workflowruntime.CompletionObservationResult, error) {
	count := c.completionObservations.Add(1)
	completed := c.completedExternally.Load()
	if c.completeExternallyAfterObservations > 0 && count >= c.completeExternallyAfterObservations {
		completed = true
	}
	return workflowruntime.CompletionObservationResult{Completed: completed}, nil
}

func (c *fakeWorkflowController) completionRequests() []workflowruntime.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]workflowruntime.CompletionRequest(nil), c.requests...)
}

type fakeTaskAwarenessSource struct {
	awareness workflowruntime.TaskAwareness
	err       error
	calls     atomic.Int64
}

func (c *fakeTaskAwarenessSource) TaskAwareness(context.Context, workflow.TaskID) (workflowruntime.TaskAwareness, error) {
	c.calls.Add(1)
	if c.err != nil {
		return workflowruntime.TaskAwareness{}, c.err
	}
	return c.awareness, nil
}

func TestSubmitWorkflowTurnWaitsForTerminalBackgroundNoticeContinuation(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	backgroundStarted := make(chan struct{})
	backgroundRelease := make(chan struct{})
	releaseBackground := sync.OnceFunc(func() { close(backgroundRelease) })
	t.Cleanup(releaseBackground)
	var firstRequest sync.Once
	client := &hookClient{
		response: commentaryResponse(
			"complete",
			completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
		),
		errors: []error{&llm.ProviderAPIError{
			ProviderID: "test",
			Code:       llm.UnifiedErrorCodeProviderContract,
		}},
		beforeReturn: func() error {
			firstRequest.Do(func() {
				close(backgroundStarted)
				<-backgroundRelease
			})
			return nil
		},
	}
	engine := mustNewWorkflowTestEngine(
		t,
		store,
		client,
		testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
		Config{},
	)

	engine.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:       BackgroundShellEventCompleted,
		ID:         "background-race",
		State:      "completed",
		NoticeText: "Background shell completed.",
	}, true)
	select {
	case <-backgroundStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal background notice continuation did not start")
	}

	workflowDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitWorkflowTurn(context.Background())
		workflowDone <- err
	}()
	select {
	case err := <-workflowDone:
		t.Fatalf("workflow turn returned before the background continuation finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseBackground()
	select {
	case err := <-workflowDone:
		if err != nil {
			t.Fatalf("workflow turn after background continuation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workflow turn did not start after the background continuation finished")
	}
	waitEngineLifecycleTasks(t, engine)
	client.mu.Lock()
	callCount := len(client.calls)
	client.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("model calls = %d, want background continuation then workflow turn", callCount)
	}
}

type workflowSteeringClient struct {
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	requests []llm.Request
}

func newWorkflowSteeringClient() *workflowSteeringClient {
	return &workflowSteeringClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *workflowSteeringClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	call := len(c.requests)
	c.mu.Unlock()
	if call == 1 {
		close(c.started)
		<-c.release
		return encryptedReasoningOnlyResponse("rs_workflow"), nil
	}
	return commentaryResponse("complete", completeNodeCall("call-complete", json.RawMessage(`{"commentary":"done","summary":"done"}`))), nil
}

func (c *workflowSteeringClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true}, nil
}

func (c *workflowSteeringClient) Requests() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.requests...)
}

type countingTool struct {
	name  toolspec.ID
	count atomic.Int64
}

func (t *countingTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	t.count.Add(1)
	return tools.Result{CallID: c.ID, Name: c.Name, Output: json.RawMessage(`{"ok":true}`)}, nil
}

type externalCompletionTool struct {
	controller *fakeWorkflowController
	count      atomic.Int64
}

func (t *externalCompletionTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	t.count.Add(1)
	if t.controller != nil {
		t.controller.completedExternally.Store(true)
	}
	return tools.Result{CallID: c.ID, Name: c.Name, Output: json.RawMessage(`{"completed":true}`)}, nil
}

func testWorkflowConfig(controller workflowruntime.Controller, mode config.WorkflowCompletionMode) *workflowruntime.CurrentNodeExecutionConfig {
	return &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Contract: workflowruntime.CompletionContract{
			Transitions: []workflowruntime.CompletionTransition{{
				ID:         "done",
				Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary of work."}},
			}},
		},
		CompletionMode:               workflowruntime.CompletionMode(mode),
		MaxInvalidCompletionAttempts: 2,
		Controller:                   controller,
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: workflow.CurrentNodeReference{
				TaskID: "task-1",
				NodeID: "node-1",
			},
			TaskShortID:      "BUI-1",
			TaskTitle:        "Workflow task",
			TaskBody:         "Task body.",
			WorkflowID:       testsetup.WorkflowIDValue("runtime-workflow"),
			WorkflowName:     "Release preparation",
			NodeKey:          "agent",
			NodeDisplayName:  "Agent",
			ContextMode:      "new_session",
			Transitions:      []workflowruntime.TransitionInstruction{{ID: "done", DisplayName: "Done"}},
			TransitionPrompt: "Do node work.",
		},
	}
}

func completeNodeCall(id string, input json.RawMessage) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: string(toolspec.ToolCompleteNode), Input: input}
}

func structuredFinalResponse(content string) llm.Response {
	return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(content), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 200000}}
}

func compatibleResponsesCapabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{
		ProviderID:           "openai-compatible",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   false,
	}
}

func compatiblePhaseAbsentResponse(content string) llm.Response {
	return llm.Response{
		Assistant:     llm.Message{Role: llm.RoleAssistant, Content: textutil.OptionalTrimmedString(content)},
		ProviderPhase: llm.AbsentProviderPhase(),
		Usage:         llm.Usage{WindowTokens: 200000},
	}
}

func compatibleFinalResponse(content string) llm.Response {
	return llm.Response{
		Assistant:     llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(content), Phase: textutil.Value(llm.MessagePhaseFinal)},
		ProviderPhase: llm.FinalProviderPhase(),
		Usage:         llm.Usage{WindowTokens: 200000},
	}
}

func compatibleCommentaryResponse(content string, toolCalls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Assistant:     llm.Message{Role: llm.RoleAssistant, Content: textutil.OptionalTrimmedString(content), Phase: textutil.Value(llm.MessagePhaseCommentary), ToolCalls: toolCalls},
		ProviderPhase: llm.CommentaryProviderPhase(),
		ToolCalls:     toolCalls,
		Usage:         llm.Usage{WindowTokens: 200000},
	}
}

func TestPhaseProtocolRejectsInconsistentProviderAndLegacyPhaseFacts(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		caps: compatibleResponsesCapabilities(),
		responses: []llm.Response{{
			Assistant:     llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ProviderPhase: llm.FinalProviderPhase(),
			Usage:         llm.Usage{WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{})

	_, err := eng.SubmitUserMessage(context.Background(), "run")
	if err == nil {
		t.Fatal("expected inconsistent provider and legacy phase facts to fail")
	}
}

func TestWorkflowToolModeExposesCompleteNodeDespiteEnabledTools(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, workflowCfg, Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	req, err := eng.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	toolsByName := map[string]llm.Tool{}
	for _, tool := range req.Tools {
		toolsByName[tool.Name] = tool
	}
	if _, ok := toolsByName[string(toolspec.ToolCompleteNode)]; !ok {
		t.Fatalf("complete_node not advertised, tools=%+v", req.Tools)
	}
	if _, ok := toolsByName[string(toolspec.ToolExecCommand)]; ok {
		t.Fatalf("exec_command should not be re-added from role tools, tools=%+v", req.Tools)
	}
	if req.ToolChoiceMode != llm.ToolChoiceModeRequired {
		t.Fatalf("tool choice mode = %q, want required", req.ToolChoiceMode)
	}
}

func TestCompleteNodeNotAdvertisedOutsideWorkflow(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolCompleteNode},
	})
	req, err := eng.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	for _, tool := range req.Tools {
		if tool.Name == string(toolspec.ToolCompleteNode) {
			t.Fatalf("complete_node advertised outside workflow: %+v", req.Tools)
		}
	}
	if req.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", req.ToolChoiceMode)
	}
}

func TestWorkflowGenerationToolChoiceMatchesEffectiveCompletionMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode config.WorkflowCompletionMode
		want llm.ToolChoiceMode
	}{
		{name: "structured output", mode: config.WorkflowCompletionModeStructuredOutput, want: llm.ToolChoiceModeAutomatic},
		{name: "tool", mode: config.WorkflowCompletionModeTool, want: llm.ToolChoiceModeRequired},
		{name: "shell command", mode: config.WorkflowCompletionModeShellCommand, want: llm.ToolChoiceModeRequired},
		{name: "unstructured output", mode: config.WorkflowCompletionModeUnstructured, want: llm.ToolChoiceModeAutomatic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, testWorkflowConfig(&fakeWorkflowController{}, tt.mode), Config{
				EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			})
			req, err := eng.buildRequest(context.Background(), "step", true)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			if req.ToolChoiceMode != tt.want {
				t.Fatalf("tool choice mode = %q, want %q", req.ToolChoiceMode, tt.want)
			}
		})
	}
}

func TestWorkflowRequiredToolChoiceIncludesLocalAndHostedTools(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolWebSearch, Handler: fakeTool{name: toolspec.ToolWebSearch}},
	), Config{
		Model:                "gpt-5",
		CurrentNodeExecution: testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand),
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
		WebSearchMode:        "native",
	})
	req, err := eng.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.ToolChoiceMode != llm.ToolChoiceModeRequired || !req.EnableNativeWebSearch {
		t.Fatalf("tool controls = mode:%q web_search:%t", req.ToolChoiceMode, req.EnableNativeWebSearch)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != string(toolspec.ToolExecCommand) {
		t.Fatalf("local tools = %+v, want exec_command", req.Tools)
	}
}

func TestWorkflowRequiredToolChoiceAcceptsHostedWebSearchOnly(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolWebSearch, Handler: fakeTool{name: toolspec.ToolWebSearch}},
	), Config{
		Model:                "gpt-5",
		CurrentNodeExecution: testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand),
		EnabledTools:         []toolspec.ID{toolspec.ToolWebSearch},
		WebSearchMode:        "native",
	})
	req, err := eng.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Tools) != 0 || !req.EnableNativeWebSearch || req.ToolChoiceMode != llm.ToolChoiceModeRequired {
		t.Fatalf("request = %+v, want hosted-only required tools", req)
	}
}

func TestWorkflowRequiredToolChoiceRejectsEmptyEffectiveToolSet(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:                "gpt-5",
		CurrentNodeExecution: testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand),
	})
	_, err := eng.buildRequest(context.Background(), "step", true)
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("buildRequest() error = %v, want ErrInvalidRequest", err)
	}
}

func TestWorkflowRequiredToolChoiceRejectsNonResponsesAdapter(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "anthropic",
		SupportsResponsesAPI: false,
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
	})
	_, err := eng.buildRequest(context.Background(), "step", true)
	if !errors.Is(err, llm.ErrUnsupportedToolChoicePolicy) {
		t.Fatalf("buildRequest() error = %v, want ErrUnsupportedToolChoicePolicy", err)
	}
}

func TestAcceptedLiveWorkflowSteeringKeepsConfiguredToolChoice(t *testing.T) {
	for _, test := range []struct {
		name                   string
		useAutomaticToolChoice bool
		want                   llm.ToolChoiceMode
	}{
		{name: "required", want: llm.ToolChoiceModeRequired},
		{name: "automatic", useAutomaticToolChoice: true, want: llm.ToolChoiceModeAutomatic},
	} {
		t.Run(test.name, func(t *testing.T) {
			testAcceptedLiveWorkflowSteeringToolChoice(t, test.useAutomaticToolChoice, test.want)
		})
	}
}

func testAcceptedLiveWorkflowSteeringToolChoice(t *testing.T, useAutomaticToolChoice bool, want llm.ToolChoiceMode) {
	t.Helper()
	store := mustCreateTestSession(t)
	client := newWorkflowSteeringClient()
	releaseClient := sync.OnceFunc(func() { close(client.release) })
	defer releaseClient()
	workflowConfig := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowConfig.UseAutomaticToolChoice = useAutomaticToolChoice
	eng := mustNewWorkflowTestEngine(t, store, client, workflowConfig, Config{
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
	})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitWorkflowTurn(context.Background())
		submitDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active workflow request")
	}
	_, queued, err := eng.SubmitUserMessageOrSteer(context.Background(), "steer active workflow")
	if err != nil {
		t.Fatalf("SubmitUserMessageOrSteer: %v", err)
	}
	if queued == nil {
		t.Fatal("expected accepted live steering to queue on active workflow")
	}
	releaseClient()
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %+v, want initial and steered turns", requests)
	}
	for i, request := range requests {
		if request.ToolChoiceMode != want {
			t.Fatalf("request %d tool choice mode = %q, want %q", i, request.ToolChoiceMode, want)
		}
	}
	foundSteer := false
	for _, message := range requestMessages(requests[1]) {
		if message.Role == llm.RoleUser && messageContent(message) == "steer active workflow" {
			foundSteer = true
		}
	}
	if !foundSteer {
		t.Fatalf("steered user message missing from second request: %+v", requestMessages(requests[1]))
	}
}

func TestWorkflowModePromptInjectedWithoutHeadlessOrUserPrompt(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{commentaryResponse("complete",
		llm.ToolCall{
			ID:    "call_complete",
			Name:  string(toolspec.ToolCompleteNode),
			Input: json.RawMessage(`{"commentary":"complete","summary":"done"}`),
		},
	)}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{
		HeadlessMode: true,
	})
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	messages := requestMessages(client.calls[0])
	workflowIdx := -1
	for idx, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeWorkflowMode {
			workflowIdx = idx
		}
	}
	if workflowIdx < 0 {
		t.Fatalf("workflow prompt missing: messages=%+v", messages)
	}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeHeadlessMode {
			t.Fatalf("headless prompt should not be injected during workflow runs: %+v", messages)
		}
		if msg.Role == llm.RoleUser {
			t.Fatalf("workflow run should not inject user prompt: %+v", messages)
		}
	}
}

func TestWorkflowModePromptResumedCurrentNodeMessageSkipsTaskAwarenessQueryAndRewrite(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	counter := &fakeTaskAwarenessSource{awareness: workflowruntime.TaskAwareness{CommentCount: 2, UnsatisfiedDependencyCount: 1}}
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.TaskAwarenessSource = counter
	workflowCfg.TaskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
	promptIdentity := workflowruntime.CurrentNodePromptIdentity(workflowCfg.Instructions.CurrentNode)
	if _, _, err := appendTestEvent(t, store, "seed", llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), SourcePath: textutil.Value(promptIdentity), Content: textutil.Value("existing workflow instructions")}); err != nil {
		t.Fatalf("seed workflow message: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{commentaryResponse("complete",
		completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
	)}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := counter.calls.Load(); got != 0 {
		t.Fatalf("TaskAwareness calls = %d, want 0", got)
	}
	workflowMessages := workflowPromptMessages(requestMessages(client.calls[0]))
	if len(workflowMessages) != 1 || messageContent(workflowMessages[0]) != "existing workflow instructions" {
		t.Fatalf("workflow messages = %+v, want only the existing current-node prompt", workflowMessages)
	}
	before := eng.transcriptRuntimeState().SnapshotItems()
	counter.awareness.UnsatisfiedDependencyCount = 7
	if err := eng.steerWorkflowModeIfNeeded(context.Background(), "dependency-change"); err != nil {
		t.Fatalf("prepare same-assignment follow-up: %v", err)
	}
	if got := counter.calls.Load(); got != 0 {
		t.Fatalf("TaskAwareness calls after dependency change = %d, want 0", got)
	}
	if after := eng.transcriptRuntimeState().SnapshotItems(); !reflect.DeepEqual(after, before) {
		t.Fatalf("dependency change rewrote model-visible history: before=%+v after=%+v", before, after)
	}
}

func TestWorkflowModePromptSameNodeReentryRefreshesAssignment(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	counter := &fakeTaskAwarenessSource{awareness: workflowruntime.TaskAwareness{CommentCount: 2, UnsatisfiedDependencyCount: 1}}
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.TaskAwarenessSource = counter
	promptIdentity := workflowruntime.CurrentNodePromptIdentity(workflowCfg.Instructions.CurrentNode)
	if _, _, err := appendTestEvent(t, store, "seed", llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), SourcePath: textutil.Value(promptIdentity), Content: textutil.Value("previous assignment instructions")}); err != nil {
		t.Fatalf("seed workflow message: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{commentaryResponse("complete",
		completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
	)}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := counter.calls.Load(); got != 1 {
		t.Fatalf("TaskAwareness calls = %d, want one refreshed assignment prompt", got)
	}
	if err := eng.steerWorkflowModeIfNeeded(context.Background(), "same-assignment-follow-up"); err != nil {
		t.Fatalf("prepare same-assignment follow-up: %v", err)
	}
	if got := counter.calls.Load(); got != 1 {
		t.Fatalf("TaskAwareness calls after same-assignment follow-up = %d, want one", got)
	}
}

func TestWorkflowModeInitialAssignmentQueriesTaskAwarenessOnlyForNewInstructions(t *testing.T) {
	t.Parallel()
	source := &fakeTaskAwarenessSource{awareness: workflowruntime.TaskAwareness{
		UnsatisfiedDependencyCount: 2,
	}}
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.TaskAwarenessSource = source
	eng := mustNewWorkflowTestEngine(t, mustCreateTestSession(t), &fakeClient{}, workflowCfg, Config{})

	if err := eng.steerWorkflowModeIfNeeded(context.Background(), "initial"); err != nil {
		t.Fatalf("prepare initial assignment: %v", err)
	}
	if err := eng.steerWorkflowModeIfNeeded(context.Background(), "follow-up"); err != nil {
		t.Fatalf("prepare same-assignment follow-up: %v", err)
	}
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("TaskAwareness calls = %d, want one for the selected initial instruction", got)
	}
}

func workflowPromptMessages(messages []llm.Message) []llm.Message {
	out := []llm.Message{}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeWorkflowMode {
			out = append(out, msg)
		}
	}
	return out
}

func TestWorkflowStructuredModeUsesStructuredOutput(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeStructuredOutput)
	client := &fakeClient{responses: []llm.Response{structuredFinalResponse(`{"commentary":"complete","summary":"done"}`)}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "node prompt"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	req := client.calls[0]
	if req.StructuredOutput == nil {
		t.Fatal("expected structured output")
	}
	messages := requestMessages(req)
	workflowIdx := -1
	for idx, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeWorkflowMode {
			workflowIdx = idx
		}
	}
	if workflowIdx < 0 {
		t.Fatalf("workflow prompt missing from structured-output request: %+v", messages)
	}
	for _, tool := range req.Tools {
		if tool.Name == string(toolspec.ToolCompleteNode) {
			t.Fatalf("complete_node advertised in structured mode: %+v", req.Tools)
		}
	}
}

func TestWorkflowRuntimeRejectsUnresolvedCompletionMode(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "legacy"}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeAuto), Config{
		Model: "legacy",
	})
	_, err := eng.buildRequest(context.Background(), "step", true)
	if err == nil {
		t.Fatal("expected unresolved completion mode error")
	}
}

func TestWorkflowShellAndUnstructuredModesOmitDynamicCompletionMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode config.WorkflowCompletionMode
	}{
		{name: "shell command", mode: config.WorkflowCompletionModeShellCommand},
		{name: "unstructured output", mode: config.WorkflowCompletionModeUnstructured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, tt.mode)
			workflowCfg.Contract.Transitions[0].Parameters = append(workflowCfg.Contract.Transitions[0].Parameters, workflow.Parameter{Key: "details", Description: "Detailed evidence."})
			eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, workflowCfg, Config{
				EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			})
			if err := eng.ensureMetaContextForRequest(context.Background(), "step"); err != nil {
				t.Fatalf("ensure meta context: %v", err)
			}
			req, err := eng.buildRequest(context.Background(), "step", true)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			if req.StructuredOutput != nil {
				t.Fatalf("%s request has structured output: %+v", tt.name, req.StructuredOutput)
			}
			for _, tool := range req.Tools {
				if tool.Name == string(toolspec.ToolCompleteNode) {
					t.Fatalf("%s request advertised complete_node: %+v", tt.name, req.Tools)
				}
			}
		})
	}
}

func TestWorkflowMixedCompleteNodeRunsSideEffects(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	sideEffect := &countingTool{name: toolspec.ToolExecCommand}
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("mixed",
			completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"echo side-effect"}`)},
		),
		commentaryResponse("complete", completeNodeCall("call_complete_2", json.RawMessage(`{"commentary":"complete","summary":"done"}`))),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: sideEffect}), Config{
		CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := sideEffect.count.Load(); got != 1 {
		t.Fatalf("side-effect tool executions = %d, want 1", got)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	if got := controller.violations.Load(); got != 0 {
		t.Fatalf("violations = %d, want 0", got)
	}
}

func TestWorkflowTerminalCompleteNodePersistsHostedToolResults(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("complete"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		ToolCalls: []llm.ToolCall{
			completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
		},
		OutputItems: []llm.ResponseItem{
			{
				Type:   llm.ResponseItemTypeFunctionCall,
				ID:     textutil.Value("call_complete"),
				CallID: textutil.Value("call_complete"),
			},
			{
				Type: llm.ResponseItemTypeOther,
				Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
			},
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	hostedCallPersisted := false
	hostedResultPersisted := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleAssistant {
			for _, call := range persisted.ToolCalls {
				if call.ID == "ws_1" {
					hostedCallPersisted = true
				}
			}
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID != nil && *persisted.ToolCallID == "ws_1" {
			hostedResultPersisted = true
		}
	}
	if !hostedCallPersisted || !hostedResultPersisted {
		t.Fatalf("hosted call/result persisted = %v/%v, want both", hostedCallPersisted, hostedResultPersisted)
	}
}

func TestWorkflowDuplicateCompleteNodePreflightSkipsSideEffects(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("duplicated",
			completeNodeCall("call_complete_1", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			completeNodeCall("call_complete_2", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
		),
		commentaryResponse("complete", completeNodeCall("call_complete_3", json.RawMessage(`{"commentary":"complete","summary":"done"}`))),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("violations = %d, want 1", got)
	}
	records, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read workflow records: %v", err)
	}
	persistedCalls := make(map[string]bool)
	persistedResults := make(map[string]bool)
	for _, record := range records {
		if record.Kind != "message" {
			continue
		}
		message := persistedMessageForTest(t, record)
		for _, call := range message.ToolCalls {
			persistedCalls[call.ID] = true
		}
		if message.Role == llm.RoleTool && message.ToolCallID != nil {
			persistedResults[*message.ToolCallID] = true
		}
	}
	for _, rejectedID := range []string{"call_complete_1", "call_complete_2"} {
		if persistedCalls[rejectedID] || persistedResults[rejectedID] {
			t.Fatalf(
				"preflight-rejected call %q persisted intent/result: calls=%v results=%v",
				rejectedID,
				persistedCalls,
				persistedResults,
			)
		}
	}
	if !persistedCalls["call_complete_3"] || !persistedResults["call_complete_3"] {
		t.Fatalf(
			"accepted call intent/result missing: calls=%v results=%v",
			persistedCalls,
			persistedResults,
		)
	}
}

func TestWorkflowStructuredCompletionStopsWithoutAnotherTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeStructuredOutput), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	terminal := eng.WorkflowTerminalState()
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceStructuredOutput {
		t.Fatalf("terminal state = %+v, want structured completion", terminal)
	}
}

func TestWorkflowUnstructuredFinalAnswerCompletesRun(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	requests := controller.completionRequests()
	if len(requests) != 1 {
		t.Fatalf("completion request count = %d, want 1: %+v", len(requests), requests)
	}
	if got := requests[0].TransitionID; got != "done" {
		t.Fatalf("completion transition = %q, want done", got)
	}
	if got := requests[0].OutputValues["summary"]; got != "done" {
		t.Fatalf("completion summary = %q, want done", got)
	}
	if got := requests[0].Commentary; got != "complete" {
		t.Fatalf("completion commentary = %q, want complete", got)
	}
	terminal := eng.WorkflowTerminalState()
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceUnstructured {
		t.Fatalf("terminal state = %+v, want unstructured completion", terminal)
	}
}

func TestCompatibleProviderPhaseAbsentWorkflowOutputCompletes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mode   config.WorkflowCompletionMode
		source WorkflowCompletionSource
	}{
		{name: "structured", mode: config.WorkflowCompletionModeStructuredOutput, source: WorkflowCompletionSourceStructuredOutput},
		{name: "unstructured", mode: config.WorkflowCompletionModeUnstructured, source: WorkflowCompletionSourceUnstructured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			controller := &fakeWorkflowController{}
			client := &fakeClient{
				caps: compatibleResponsesCapabilities(),
				responses: []llm.Response{
					compatiblePhaseAbsentResponse(`{"commentary":"complete","summary":"done"}`),
					compatiblePhaseAbsentResponse("must not be requested"),
				},
			}
			eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, tt.mode), Config{})

			if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
				t.Fatalf("submit: %v", err)
			}

			assertModelCallCount(t, client, 1)
			if got := controller.completed.Load(); got != 1 {
				t.Fatalf("completions = %d, want 1", got)
			}
			if got := controller.violations.Load(); got != 0 {
				t.Fatalf("violations = %d, want 0", got)
			}
			requests := controller.completionRequests()
			if len(requests) != 1 {
				t.Fatalf("completion requests = %+v, want exactly one", requests)
			}
			if requests[0].TransitionID != "done" || requests[0].OutputValues["summary"] != "done" || requests[0].Commentary != "complete" {
				t.Fatalf("completion request = %+v, want decoded workflow submission", requests[0])
			}
			terminal := eng.WorkflowTerminalState()
			if !terminal.Completed || terminal.Source != tt.source {
				t.Fatalf("terminal state = %+v, want %s completion", terminal, tt.source)
			}
		})
	}
}

func TestCompatibleProviderInvalidPhaseAbsentWorkflowOutputCanRetry(t *testing.T) {
	t.Parallel()
	for _, mode := range []config.WorkflowCompletionMode{
		config.WorkflowCompletionModeStructuredOutput,
		config.WorkflowCompletionModeUnstructured,
	} {
		t.Run(string(mode), func(t *testing.T) {
			store := mustCreateTestSession(t)
			controller := &fakeWorkflowController{}
			client := &fakeClient{
				caps: compatibleResponsesCapabilities(),
				responses: []llm.Response{
					compatiblePhaseAbsentResponse(`{"summary":""}`),
					compatiblePhaseAbsentResponse(`{"commentary":"complete","summary":"done"}`),
				},
			}
			eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, mode), Config{})

			if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
				t.Fatalf("submit: %v", err)
			}

			assertModelCallCount(t, client, 2)
			if got := controller.violations.Load(); got != 1 {
				t.Fatalf("violations = %d, want 1", got)
			}
			if got := controller.completed.Load(); got != 1 {
				t.Fatalf("completions = %d, want 1", got)
			}
			if !requestHasDeveloperErrorFeedback(client.calls[1]) {
				t.Fatal("invalid phase-absent submission did not append workflow continuation feedback")
			}
			if !eng.WorkflowTerminalState().Completed {
				t.Fatalf("terminal state = %+v, want completion after retry", eng.WorkflowTerminalState())
			}
		})
	}
}

func TestCompatibleProviderCommentaryContinuesWithoutCompletionSideEffects(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{
		caps: compatibleResponsesCapabilities(),
		responses: []llm.Response{
			compatibleCommentaryResponse("continuing"),
			compatiblePhaseAbsentResponse(`{"commentary":"complete","summary":"done"}`),
		},
	}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	assertModelCallCount(t, client, 2)
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want only the later valid completion", got)
	}
	if got := controller.violations.Load(); got != 0 {
		t.Fatalf("violations = %d, want 0", got)
	}
	assertPersistedAssistantContentCount(t, eng, "continuing", 1)
}

func TestCompatibleProviderCommentaryFlushesAcceptedSteeringBeforeContinuing(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	started := make(chan struct{})
	release := make(chan struct{})
	releaseRun := sync.OnceFunc(func() { close(release) })
	defer releaseRun()
	var once sync.Once
	var client *hookClient
	client = &hookClient{
		response: compatibleCommentaryResponse("continuing"),
		caps:     compatibleResponsesCapabilities(),
		beforeReturn: func() error {
			once.Do(func() {
				close(started)
				<-release
				client.mu.Lock()
				client.response = compatiblePhaseAbsentResponse(`{"commentary":"complete","summary":"done"}`)
				client.beforeReturn = nil
				client.mu.Unlock()
			})
			return nil
		},
	}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{})

	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitWorkflowTurn(context.Background())
		submitDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for commentary response")
	}
	_, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "accepted steering", nil)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	releaseRun()
	if err := <-submitDone; err != nil {
		t.Fatalf("submit: %v", err)
	}

	if got := hookClientCallCount(client); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	client.mu.Lock()
	second := client.calls[1]
	client.mu.Unlock()
	hasAcceptedSteering := false
	for _, message := range requestMessages(second) {
		if message.Role == llm.RoleUser && messageContent(message) == "accepted steering" {
			hasAcceptedSteering = true
			break
		}
	}
	if !hasAcceptedSteering {
		t.Fatalf("second request omitted accepted steering: %+v", requestMessages(second))
	}
	if got := controller.violations.Load(); got != 0 {
		t.Fatalf("violations = %d, want 0", got)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
}

func hookClientCallCount(client *hookClient) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.calls)
}

func TestWorkflowDurableCompletionAfterModelResponseSkipsStalePersistence(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &hookClient{
		response: commentaryResponse("stale assistant",
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete"}`)},
		),
		beforeReturn: func() error {
			controller.completedExternally.Store(true)
			return nil
		},
	}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand), Config{})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.calls))
	}
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleAssistant && strings.Contains(messageContent(msg), "stale assistant") {
			t.Fatalf("stale assistant response was persisted after external completion: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
		if msg.Role == llm.RoleTool && msg.ToolCallID != nil && *msg.ToolCallID == "call_shell" {
			t.Fatalf("stale tool result was persisted after external completion: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
	}
}

func TestWorkflowShellToolDurableCompletionStopsAfterToolResult(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	shellTool := &externalCompletionTool{controller: controller}
	events := &liveRunEventCollector{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("run completion command",
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete"}`)},
		),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shellTool}), Config{
		CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
		OnEvent:              events.accept,
	})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	if got := shellTool.count.Load(); got != 1 {
		t.Fatalf("shell tool calls = %d, want 1", got)
	}
	if got := controller.completed.Load(); got != 0 {
		t.Fatalf("runtime completions = %d, want external completion only", got)
	}
	assertToolMessageWithCallID(t, eng, "call_shell")
	result := events.single(t)
	if result.Status != RunStatusCompleted ||
		result.ResultKind != LiveRunResultNoFinalAnswer ||
		result.NoFinalReason != LiveRunNoFinalAnswerReasonWorkflow ||
		result.AssistantMessage.Content != nil {
		t.Fatalf("shell-command workflow completion result = %+v, want workflow no-final terminal fact", result)
	}
}

func TestWorkflowInvalidCompletionAttemptsInterruptAtCap(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("bad", completeNodeCall("call_bad_1", json.RawMessage(`{"summary":""}`))),
		commentaryResponse("bad", completeNodeCall("call_bad_2", json.RawMessage(`{"summary":""}`))),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := controller.maxHits.Load(); got != 1 {
		t.Fatalf("max hits = %d, want 1", got)
	}
}

func TestWorkflowFinalAnswersUseInvalidCompletionCap(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse("done 1"),
		structuredFinalResponse("done 2"),
		structuredFinalResponse("done 3"),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := controller.maxHits.Load(); got != 1 {
		t.Fatalf("max hits = %d, want 1", got)
	}
}

func TestCompatibleProviderPhaseAbsentProseConsumesWorkflowViolationAndCanRecover(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      config.WorkflowCompletionMode
		newEngine func(*testing.T, *session.Store, *fakeClient, *fakeWorkflowController) *Engine
	}{
		{
			name: "tool",
			mode: config.WorkflowCompletionModeTool,
			newEngine: func(t *testing.T, store *session.Store, client *fakeClient, controller *fakeWorkflowController) *Engine {
				return mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})
			},
		},
		{
			name: "shell command",
			mode: config.WorkflowCompletionModeShellCommand,
			newEngine: func(t *testing.T, store *session.Store, client *fakeClient, controller *fakeWorkflowController) *Engine {
				shellTool := &externalCompletionTool{controller: controller}
				return mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shellTool}), Config{
					CurrentNodeExecution: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			controller := &fakeWorkflowController{}
			valid := compatibleCommentaryResponse("complete",
				completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			)
			if tt.mode == config.WorkflowCompletionModeShellCommand {
				valid = compatibleCommentaryResponse("complete",
					llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete"}`)},
				)
			}
			client := &fakeClient{
				caps: compatibleResponsesCapabilities(),
				responses: []llm.Response{
					compatiblePhaseAbsentResponse("ordinary prose"),
					valid,
				},
			}
			eng := tt.newEngine(t, store, client, controller)

			if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
				t.Fatalf("submit: %v", err)
			}

			assertModelCallCount(t, client, 2)
			if got := controller.violations.Load(); got != 1 {
				t.Fatalf("violations = %d, want 1", got)
			}
			if got := controller.maxHits.Load(); got != 0 {
				t.Fatalf("max hits = %d, want 0", got)
			}
			if !requestHasDeveloperErrorFeedback(client.calls[1]) {
				t.Fatal("phase-absent prose did not append workflow continuation feedback")
			}
			assertPersistedAssistantContentCount(t, eng, "ordinary prose", 1)
			terminal := eng.WorkflowTerminalState()
			if !terminal.Completed {
				t.Fatalf("terminal state = %+v, want later valid completion", terminal)
			}
		})
	}
}

func TestWorkflowBlankFinalUsesNormalIncompleteOutputHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response func(string) llm.Response
		content  string
	}{
		{name: "final empty", response: compatibleFinalResponse, content: ""},
		{name: "final whitespace", response: compatibleFinalResponse, content: " \n\t "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			controller := &fakeWorkflowController{}
			client := &fakeClient{
				caps: compatibleResponsesCapabilities(),
				responses: []llm.Response{
					tt.response(tt.content),
					compatibleCommentaryResponse("complete",
						completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
					),
				},
			}
			eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})

			if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
				t.Fatalf("submit: %v", err)
			}

			assertModelCallCount(t, client, 2)
			if got := controller.violations.Load(); got != 0 {
				t.Fatalf("violations = %d, want 0", got)
			}
			if !requestHasDeveloperErrorFeedback(client.calls[1]) {
				t.Fatal("empty response did not append generic developer feedback")
			}
			terminal := eng.WorkflowTerminalState()
			if !terminal.Completed || terminal.Source != WorkflowCompletionSourceTool {
				t.Fatalf("terminal state = %+v, want later tool completion", terminal)
			}
		})
	}
}

func requestHasDeveloperErrorFeedback(request llm.Request) bool {
	for _, message := range requestMessages(request) {
		if message.Role == llm.RoleDeveloper && message.MessageType != nil && *message.MessageType == llm.MessageTypeErrorFeedback {
			return true
		}
	}
	return false
}

func assertPersistedAssistantContentCount(t *testing.T, eng *Engine, content string, want int) {
	t.Helper()
	got := 0
	for _, message := range eng.transcriptRuntimeState().SnapshotMessages() {
		if message.Role == llm.RoleAssistant && messageContent(message) == content {
			got++
		}
	}
	if got != want {
		t.Fatalf("persisted assistant content %q count = %d, want %d", content, got, want)
	}
}

func assertDeveloperErrorFeedbackAfterAssistantFinalContains(t *testing.T, eng *Engine, assistantContent string, required []string, forbidden []string) {
	t.Helper()
	messages := eng.transcriptRuntimeState().SnapshotMessages()
	for index, message := range messages {
		if message.Role != llm.RoleAssistant || message.Phase == nil || *message.Phase != llm.MessagePhaseFinal || messageContent(message) != assistantContent {
			continue
		}
		nextIndex := index + 1
		if nextIndex >= len(messages) {
			t.Fatalf("assistant final %q had no following message: %+v", assistantContent, messages)
		}
		next := messages[nextIndex]
		if next.Role != llm.RoleDeveloper || next.MessageType == nil || *next.MessageType != llm.MessageTypeErrorFeedback {
			t.Fatalf("message after assistant final %q = %+v, want developer error feedback; messages=%+v", assistantContent, next, messages)
		}
		for _, want := range required {
			if !strings.Contains(messageContent(next), want) {
				t.Fatalf("developer error feedback after %q missing %q:\n%s", assistantContent, want, messageContent(next))
			}
		}
		for _, blocked := range forbidden {
			if strings.Contains(messageContent(next), blocked) {
				t.Fatalf("developer error feedback after %q contained forbidden %q:\n%s", assistantContent, blocked, messageContent(next))
			}
		}
		return
	}
	t.Fatalf("assistant final %q not found in messages: %+v", assistantContent, messages)
}

func assertToolMessageWithCallID(t *testing.T, eng *Engine, callID string) {
	t.Helper()
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleTool && msg.ToolCallID != nil && *msg.ToolCallID == callID {
			return
		}
	}
	t.Fatalf("tool message for call %s not found: %+v", callID, eng.transcriptRuntimeState().SnapshotMessages())
}
