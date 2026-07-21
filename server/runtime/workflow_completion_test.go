package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
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
	completedRunID                      string
	completedGeneration                 int64
	completedExternally                 atomic.Bool
	mu                                  sync.Mutex
	requests                            []workflowruntime.CompletionRequest
}

func (c *fakeWorkflowController) CompleteWorkflowRun(_ context.Context, req workflowruntime.CompletionRequest) (workflowruntime.CompletionResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	if c.completeErr != nil {
		return workflowruntime.CompletionResult{}, c.completeErr
	}
	c.completed.Add(1)
	return workflowruntime.CompletionResult{TransitionID: "transition-applied", State: "applied"}, nil
}

func (c *fakeWorkflowController) RecordWorkflowProtocolViolation(_ context.Context, req workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	count := c.violations.Add(1)
	interrupted := count >= int64(req.MaxCount)
	if interrupted {
		c.maxHits.Add(1)
	}
	return workflowruntime.ViolationResult{Count: count, Interrupted: interrupted}, nil
}

func (c *fakeWorkflowController) ResetWorkflowProtocolViolationBudget(_ context.Context, _ workflowruntime.ViolationResetRequest) error {
	if c.protocolBudgetResetErr != nil {
		return c.protocolBudgetResetErr
	}
	c.violations.Store(0)
	c.protocolBudgetResets.Add(1)
	return nil
}

func (c *fakeWorkflowController) ObserveWorkflowRunCompletion(_ context.Context, req workflowruntime.CompletionObservationRequest) (workflowruntime.CompletionObservationResult, error) {
	count := c.completionObservations.Add(1)
	if c.completedRunID != "" && string(req.RunID) != c.completedRunID {
		return workflowruntime.CompletionObservationResult{}, nil
	}
	if c.completedGeneration != 0 && req.ExpectedGeneration != c.completedGeneration {
		return workflowruntime.CompletionObservationResult{}, nil
	}
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

type fakeTaskCommentCounter struct {
	count int64
	err   error
	calls atomic.Int64
}

func (c *fakeTaskCommentCounter) CountTaskComments(context.Context, workflow.TaskID) (int64, error) {
	c.calls.Add(1)
	if c.err != nil {
		return 0, c.err
	}
	return c.count, nil
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
	return llm.ProviderCapabilities{
		ProviderID:              "openai",
		SupportsResponsesAPI:    true,
		SupportsNativeWebSearch: true,
		IsOpenAIFirstParty:      true,
	}, nil
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

func testWorkflowConfig(controller workflowruntime.Controller, mode config.WorkflowCompletionMode) *workflowruntime.Config {
	return &workflowruntime.Config{
		Contract: workflowruntime.CompletionContract{
			RunID:              "run-1",
			ExpectedGeneration: 7,
			RequireGeneration:  true,
			Transitions: []workflowruntime.CompletionTransition{{
				ID:         "done",
				Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary of work."}},
			}},
		},
		CompletionMode:               workflowruntime.CompletionMode(mode),
		MaxInvalidCompletionAttempts: 2,
		Controller:                   controller,
		Instructions: workflowruntime.TaskInstructions{
			TaskID:          "task-1",
			TaskShortID:     "BUI-1",
			TaskTitle:       "Workflow task",
			TaskBody:        "Task body.",
			WorkflowID:      "workflow-1",
			WorkflowShortID: "workflow-1",
			NodeID:          "node-1",
			NodeKey:         "agent",
			NodeDisplayName: "Agent",
			ContextMode:     "new_session",
			Transitions:     []workflowruntime.TransitionInstruction{{ID: "done", DisplayName: "Done"}},
			NodePrompt:      "Do node work.",
		},
	}
}

func completeNodeCall(id string, input json.RawMessage) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: string(toolspec.ToolCompleteNode), Input: input}
}

func structuredFinalResponse(content string) llm.Response {
	return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: content, Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}
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
		Assistant:     llm.Message{Role: llm.RoleAssistant, Content: content},
		ProviderPhase: llm.AbsentProviderPhase(),
		Usage:         llm.Usage{WindowTokens: 200000},
	}
}

func compatibleFinalResponse(content string) llm.Response {
	return llm.Response{
		Assistant:     llm.Message{Role: llm.RoleAssistant, Content: content, Phase: llm.MessagePhaseFinal},
		ProviderPhase: llm.FinalProviderPhase(),
		Usage:         llm.Usage{WindowTokens: 200000},
	}
}

func compatibleCommentaryResponse(content string, toolCalls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Assistant:     llm.Message{Role: llm.RoleAssistant, Content: content, Phase: llm.MessagePhaseCommentary, ToolCalls: toolCalls},
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
			Assistant:     llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseCommentary},
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

func TestWorkflowRequiredToolChoiceAcceptsHostedWebSearchOnly(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolWebSearch, Handler: fakeTool{name: toolspec.ToolWebSearch}},
	), Config{
		Model:         "gpt-5",
		WorkflowRun:   testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand),
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
		WebSearchMode: "native",
	})
	req, err := eng.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Tools) != 0 || req.StructuredOutput != nil || !req.EnableNativeWebSearch || req.ToolChoiceMode != llm.ToolChoiceModeRequired {
		t.Fatalf("request = %+v, want hosted-only required tools", req)
	}
}

func TestWorkflowRequiredToolChoiceRejectsEmptyEffectiveToolSet(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:       "gpt-5",
		WorkflowRun: testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand),
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

func TestAcceptedLiveWorkflowSteeringKeepsRequiredToolChoice(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := newWorkflowSteeringClient()
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool), Config{
		Model:         "gpt-5",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
		WebSearchMode: "native",
	})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitWorkflowTurn(context.Background())
		submitDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active workflow request")
	}
	_, queued, err := eng.SubmitUserMessageOrSteer(context.Background(), "steer active workflow", "req-steer")
	if err != nil {
		t.Fatalf("SubmitUserMessageOrSteer: %v", err)
	}
	if queued == nil {
		t.Fatal("expected accepted live steering to queue on active workflow")
	}
	close(client.release)
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %+v, want initial and steered turns", requests)
	}
	for i, request := range requests {
		if request.ToolChoiceMode != llm.ToolChoiceModeRequired || !request.EnableNativeWebSearch {
			t.Fatalf("request %d tool controls = mode:%q web_search:%t", i, request.ToolChoiceMode, request.EnableNativeWebSearch)
		}
		localTools := map[string]bool{
			string(toolspec.ToolExecCommand):  false,
			string(toolspec.ToolCompleteNode): false,
		}
		for _, tool := range request.Tools {
			if _, ok := localTools[tool.Name]; !ok {
				t.Fatalf("request %d serialized unexpected local tool %q: %+v", i, tool.Name, request.Tools)
			}
			localTools[tool.Name] = true
		}
		if len(request.Tools) != len(localTools) || !localTools[string(toolspec.ToolExecCommand)] || !localTools[string(toolspec.ToolCompleteNode)] {
			t.Fatalf("request %d local tools = %+v, want exec_command and complete_node", i, request.Tools)
		}
	}
	foundSteer := false
	for _, message := range requestMessages(requests[1]) {
		if message.Role == llm.RoleUser && message.Content == "steer active workflow" {
			foundSteer = true
		}
	}
	if !foundSteer {
		t.Fatalf("steered user message missing from second request: %+v", requestMessages(requests[1]))
	}
}

func TestWorkflowModePromptExistingRunScopedMessageSkipsCommentCountQuery(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("seed", "message", llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorkflowMode, SourcePath: "run-1", Content: "existing workflow instructions"}); err != nil {
		t.Fatalf("seed workflow message: %v", err)
	}
	counter := &fakeTaskCommentCounter{count: 2}
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.TaskCommentCounter = counter
	client := &fakeClient{responses: []llm.Response{commentaryResponse("complete",
		completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
	)}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := counter.calls.Load(); got != 0 {
		t.Fatalf("CountTaskComments calls = %d, want 0", got)
	}
	workflowMessages := workflowPromptMessages(requestMessages(client.calls[0]))
	if len(workflowMessages) != 1 || workflowMessages[0].Content != "existing workflow instructions" {
		t.Fatalf("workflow messages = %+v, want only the existing run-scoped prompt", workflowMessages)
	}
}

func workflowPromptMessages(messages []llm.Message) []llm.Message {
	out := []llm.Message{}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorkflowMode {
			out = append(out, msg)
		}
	}
	return out
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

func TestWorkflowMixedCompleteNodeRunsSideEffects(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	sideEffect := &countingTool{name: toolspec.ToolExecCommand}
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("duplicated",
			completeNodeCall("call_complete_1", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			completeNodeCall("call_complete_2", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			llm.ToolCall{ID: "call_shell_skipped", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"echo must-not-run"}`)},
		),
		commentaryResponse("mixed",
			completeNodeCall("call_complete_3", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"echo side-effect"}`)},
		),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: sideEffect}), Config{
		WorkflowRun: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := sideEffect.count.Load(); got != 1 {
		t.Fatalf("side-effect tool executions = %d, want only the valid mixed-turn call", got)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("violations = %d, want 1", got)
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
	request := client.calls[0]
	if request.StructuredOutput == nil {
		t.Fatal("expected structured output")
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", request.ToolChoiceMode)
	}
	if len(workflowPromptMessages(requestMessages(request))) == 0 {
		t.Fatalf("workflow prompt missing from structured-output request: %+v", requestMessages(request))
	}
	for _, tool := range request.Tools {
		if tool.Name == string(toolspec.ToolCompleteNode) {
			t.Fatalf("complete_node advertised in structured mode: %+v", request.Tools)
		}
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	terminal := eng.WorkflowTerminalState()
	if !terminal.Completed || terminal.RunID != "run-1" || terminal.Source != WorkflowCompletionSourceStructuredOutput {
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
	if got := client.calls[0].ToolChoiceMode; got != llm.ToolChoiceModeAutomatic {
		t.Fatalf("tool choice mode = %q, want automatic", got)
	}
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
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceUnstructured || terminal.RunID != "run-1" {
		t.Fatalf("terminal state = %+v, want unstructured completion", terminal)
	}
}

func TestWorkflowCompletionControllerFailureUsesInvalidCompletionCapWithoutTerminalState(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{completeErr: errors.New("workflow completion unavailable")}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(`{"commentary":"first","summary":"done"}`),
		structuredFinalResponse(`{"commentary":"retry","summary":"done"}`),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	const activeGoal = "ship the steering rework end to end"
	if _, err := eng.SetGoal(activeGoal, session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := len(controller.completionRequests()); got != 2 {
		t.Fatalf("completion attempts = %d, want 2", got)
	}
	if got := controller.completed.Load(); got != 0 {
		t.Fatalf("successful completions = %d, want 0", got)
	}
	if got := controller.violations.Load(); got != 2 {
		t.Fatalf("completion violations = %d, want 2", got)
	}
	if got := controller.maxHits.Load(); got != 1 {
		t.Fatalf("invalid completion cap hits = %d, want 1", got)
	}
	if !requestHasDeveloperErrorFeedback(client.calls[1]) {
		t.Fatal("controller failure did not append workflow continuation feedback")
	}
	assertDeveloperErrorFeedbackAfterAssistantFinalContains(t, eng, `{"commentary":"first","summary":"done"}`, []string{activeGoal}, nil)
	if terminal := eng.WorkflowTerminalState(); terminal.Completed {
		t.Fatalf("terminal state = %+v, want incomplete after controller failures", terminal)
	}
}

func TestCompatibleProviderPhaseAbsentStructuredWorkflowOutputCompletes(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{
		caps: compatibleResponsesCapabilities(),
		responses: []llm.Response{
			compatiblePhaseAbsentResponse(`{"commentary":"complete","summary":"done"}`),
			compatiblePhaseAbsentResponse("must not be requested"),
		},
	}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeStructuredOutput), Config{})

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
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceStructuredOutput || terminal.RunID != "run-1" {
		t.Fatalf("terminal state = %+v, want structured completion", terminal)
	}
}

func TestCompatibleProviderInvalidPhaseAbsentUnstructuredWorkflowOutputCanRetry(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{
		caps: compatibleResponsesCapabilities(),
		responses: []llm.Response{
			compatiblePhaseAbsentResponse(`{"summary":""}`),
			compatiblePhaseAbsentResponse(`{"commentary":"complete","summary":"done"}`),
		},
	}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{})

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
}

func TestCompatibleProviderCommentaryFlushesAcceptedSteeringBeforeContinuing(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	started := make(chan struct{})
	release := make(chan struct{})
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
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for commentary response")
	}
	_, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "accepted steering", liveRunTestRequestID(t), nil)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	close(release)
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
		if message.Role == llm.RoleUser && message.Content == "accepted steering" {
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
	assertPersistedAssistantContentCount(t, eng, "continuing", 1)
}

func TestWorkflowTerminalCompletionFailsQueuedSteeringAtRunRelease(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	started := make(chan struct{})
	release := make(chan struct{})
	client := &hookClient{
		response: structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
		beforeReturn: func() error {
			close(started)
			<-release
			return nil
		},
	}
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "run")
		submitDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow turn")
	}
	queued := eng.QueueUserMessageWithClientRequestID("do not submit after run release", "req-after-release")
	close(release)
	if err := <-submitDone; err != nil {
		t.Fatalf("submit: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := hookClientCallCount(client); got != 1 {
		t.Fatalf("model calls = %d, want terminal completion to avoid queued turn", got)
	}
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-after-release" || statuses[1].RestoreText != "do not submit after run release" || statuses[1].FailureReason != QueuedUserMessageFailureTerminalWorkflowCompletion {
		t.Fatalf("failed queue status = %+v, want terminal completion failure for %q", statuses[1], queued.ID)
	}
}

func hookClientCallCount(client *hookClient) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.calls)
}

func TestWorkflowObservedDurableCompletionFailsQueuedSteeringDuringCloseDrain(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	controller.completedExternally.Store(true)
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse("unexpected queued turn"),
	}}
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand), Config{
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	queued := eng.QueueUserMessageWithClientRequestID("do not submit after observed completion", "req-observed-complete")
	completed, err := eng.observeWorkflowDurableCompletion(context.Background())
	if err != nil {
		t.Fatalf("observeWorkflowDurableCompletion: %v", err)
	}
	if !completed {
		t.Fatal("expected durable workflow completion observation")
	}
	terminal := eng.WorkflowTerminalState()
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceObserved || terminal.RunID != "run-1" {
		t.Fatalf("terminal state = %+v, want observed completion", terminal)
	}
	if err := eng.DrainQueuedUserMessagesBeforeClose(context.Background()); err != nil {
		t.Fatalf("DrainQueuedUserMessagesBeforeClose: %v", err)
	}
	assertModelCallCount(t, client, 0)
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-observed-complete" || statuses[1].RestoreText != "do not submit after observed completion" || statuses[1].FailureReason != QueuedUserMessageFailureTerminalWorkflowCompletion {
		t.Fatalf("failed queue status = %+v, want terminal completion failure for %q", statuses[1], queued.ID)
	}
}

func TestWorkflowDurableCompletionBeforeModelTurnStopsWithoutRequest(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	controller.completedExternally.Store(true)
	client := &fakeClient{responses: []llm.Response{structuredFinalResponse("unexpected")}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand), Config{})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 0)
	if got := controller.completed.Load(); got != 0 {
		t.Fatalf("runtime completions = %d, want external completion only", got)
	}
	if got := controller.completionObservations.Load(); got == 0 {
		t.Fatal("expected runtime to observe durable completion before model request")
	}
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
		if msg.Role == llm.RoleAssistant && strings.Contains(msg.Content, "stale assistant") {
			t.Fatalf("stale assistant response was persisted after external completion: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call_shell" {
			t.Fatalf("stale tool result was persisted after external completion: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
	}
}

func TestWorkflowDelayedDurableCompletionObservedBeforeNextModelTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{completeExternallyAfterObservations: 4}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("run background completion",
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete &"}`)},
		),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand), Config{})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	assertToolMessageWithCallID(t, eng, "call_shell")
	if got := controller.completionObservations.Load(); got < 4 {
		t.Fatalf("completion observations = %d, want post-tool and next-turn checks", got)
	}
}

func TestWorkflowInvalidCompletionAttemptsInterruptAtCap(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse("invalid final answer"),
		commentaryResponse("bad", completeNodeCall("call_bad_2", json.RawMessage(`{"summary":""}`))),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := controller.violations.Load(); got != 2 {
		t.Fatalf("violations = %d, want 2", got)
	}
	if got := controller.maxHits.Load(); got != 1 {
		t.Fatalf("max hits = %d, want 1", got)
	}
}

func TestWorkflowInvalidCompletionFailClosedWhenConfiguredCapInvalid(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse("normal final answer is invalid in tool mode"),
		structuredFinalResponse("unexpected"),
	}}
	workflowCfg := testWorkflowConfig(controller, config.WorkflowCompletionModeTool)
	workflowCfg.MaxInvalidCompletionAttempts = 0
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("violations = %d, want 1", got)
	}
	if got := controller.maxHits.Load(); got != 1 {
		t.Fatalf("max hits = %d, want immediate fail-closed interruption", got)
	}
}

func TestCompatibleProviderPhaseAbsentProseConsumesShellWorkflowViolationAndCanRecover(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{
		caps: compatibleResponsesCapabilities(),
		responses: []llm.Response{
			compatiblePhaseAbsentResponse("ordinary prose"),
			compatibleCommentaryResponse("complete",
				llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete"}`)},
			),
		},
	}
	shellTool := &externalCompletionTool{controller: controller}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shellTool}), Config{
		WorkflowRun: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
	})

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
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceObserved {
		t.Fatalf("terminal state = %+v, want observed shell completion", terminal)
	}
	if got := controller.completed.Load(); got != 0 {
		t.Fatalf("runtime completions = %d, want external shell completion only", got)
	}
	if got := shellTool.count.Load(); got != 1 {
		t.Fatalf("shell tool calls = %d, want 1", got)
	}
	assertToolMessageWithCallID(t, eng, "call_shell")
}

func TestCompatibleProviderEmptyNoToolResponsesContinueWithoutWorkflowViolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response func(string) llm.Response
		content  string
	}{
		{name: "absent empty", response: compatiblePhaseAbsentResponse, content: ""},
		{name: "final whitespace", response: compatibleFinalResponse, content: " \n\t "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
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
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeErrorFeedback {
			return true
		}
	}
	return false
}

func assertPersistedAssistantContentCount(t *testing.T, eng *Engine, content string, want int) {
	t.Helper()
	got := 0
	for _, message := range eng.transcriptRuntimeState().SnapshotMessages() {
		if message.Role == llm.RoleAssistant && message.Content == content {
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
		if message.Role != llm.RoleAssistant || message.Phase != llm.MessagePhaseFinal || message.Content != assistantContent {
			continue
		}
		nextIndex := index + 1
		if nextIndex >= len(messages) {
			t.Fatalf("assistant final %q had no following message: %+v", assistantContent, messages)
		}
		next := messages[nextIndex]
		if next.Role != llm.RoleDeveloper || next.MessageType != llm.MessageTypeErrorFeedback {
			t.Fatalf("message after assistant final %q = %+v, want developer error feedback; messages=%+v", assistantContent, next, messages)
		}
		for _, want := range required {
			if !strings.Contains(next.Content, want) {
				t.Fatalf("developer error feedback after %q missing %q:\n%s", assistantContent, want, next.Content)
			}
		}
		for _, blocked := range forbidden {
			if strings.Contains(next.Content, blocked) {
				t.Fatalf("developer error feedback after %q contained forbidden %q:\n%s", assistantContent, blocked, next.Content)
			}
		}
		return
	}
	t.Fatalf("assistant final %q not found in messages: %+v", assistantContent, messages)
}

func assertToolMessageWithCallID(t *testing.T, eng *Engine, callID string) {
	t.Helper()
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleTool && msg.ToolCallID == callID {
			return
		}
	}
	t.Fatalf("tool message for call %s not found: %+v", callID, eng.transcriptRuntimeState().SnapshotMessages())
}
