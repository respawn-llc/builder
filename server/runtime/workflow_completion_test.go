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
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type fakeWorkflowController struct {
	engine                 *Engine
	completed              atomic.Int64
	completeErr            error
	completionDiagnostic   error
	violations             atomic.Int64
	protocolBudgetResets   atomic.Int64
	protocolBudgetResetErr error
	maxHits                atomic.Int64
	mu                     sync.Mutex
	requests               []workflowruntime.AgentCompletionRequest
}

func (c *fakeWorkflowController) bindWorkflowCompletionEngine(engine *Engine) {
	c.engine = engine
}

func (c *fakeWorkflowController) CompleteAgentCurrentNode(_ context.Context, req workflowruntime.AgentCompletionRequest) (workflowruntime.CompletionResult, error) {
	result, err := c.acceptCompletionForTest(req)
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return applyWorkflowCompletionForTest(c.engine, req, result)
}

func (c *fakeWorkflowController) acceptCompletionForTest(
	req workflowruntime.AgentCompletionRequest,
) (workflowruntime.CompletionResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	if c.completeErr != nil {
		return workflowruntime.CompletionResult{}, c.completeErr
	}
	c.completed.Add(1)
	return workflowruntime.CompletionResult{
		TransitionID: "transition-applied",
		State:        workflowruntime.CompletionStateApplied,
		Diagnostic:   c.completionDiagnostic,
	}, nil
}

func applyWorkflowCompletionForTest(
	engine *Engine,
	req workflowruntime.AgentCompletionRequest,
	result workflowruntime.CompletionResult,
) (workflowruntime.CompletionResult, error) {
	if engine == nil {
		err := errors.New("test Workflow completion controller is not bound to an Engine")
		return workflowruntime.CompletionResult{}, err
	}
	return engine.ApplyWorkflowAgentCompletion(
		req.Provenance.ScopeID,
		req.Provenance.RunID,
		req.Provenance.StepID,
		func() (workflowruntime.CompletionResult, error) {
			return result, nil
		},
	)
}

func (c *fakeWorkflowController) CompleteScriptCurrentNode(context.Context, workflowruntime.ScriptCompletionRequest) (workflowruntime.CompletionResult, error) {
	err := errors.New("unexpected Script completion")
	return workflowruntime.CompletionResult{}, err
}

func (c *fakeWorkflowController) ContinueCurrentNode(context.Context, workflowstore.CurrentNodeCompletionResult) error {
	return nil
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

func (c *fakeWorkflowController) completionRequests() []workflowruntime.AgentCompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]workflowruntime.AgentCompletionRequest(nil), c.requests...)
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

func TestSubmitWorkflowTurnFlushesTerminalBackgroundNotice(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &hookClient{response: commentaryResponse(
		"complete",
		completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
	)}
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
	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("workflow turn: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want one workflow turn", len(client.calls))
	}
	for _, message := range requestMessages(client.calls[0]) {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeBackgroundNotice {
			return
		}
	}
	t.Fatalf("workflow request omitted terminal background notice: %+v", client.calls[0])
}

func TestSubmitWorkflowTurnReturnsCompletionDiagnosticWithoutTurnFailure(t *testing.T) {
	diagnostic := errors.New("completion event publication failed")
	controller := &fakeWorkflowController{completionDiagnostic: diagnostic}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{responses: []llm.Response{commentaryResponse(
			"complete",
			completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
		)}},
		testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
		Config{},
	)

	result, err := engine.SubmitWorkflowTurn(context.Background())
	if err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	if result.Completion == nil || !errors.Is(result.Completion.Diagnostic, diagnostic) {
		t.Fatalf("turn completion = %+v, want accepted diagnostic %v", result.Completion, diagnostic)
	}
	if got := controller.violations.Load(); got != 0 {
		t.Fatalf("protocol violations = %d, want zero", got)
	}
	terminal := engine.WorkflowTerminalState()
	if !terminal.Completed || !errors.Is(terminal.Completion.Diagnostic, diagnostic) {
		t.Fatalf("terminal state = %+v, want accepted diagnostic", terminal)
	}
}

func TestWorkflowCompletionSourcesReturnCanonicalExactTerminalSnapshot(t *testing.T) {
	diagnostic := errors.New("completion observer failed")
	for _, test := range []struct {
		name     string
		mode     config.WorkflowCompletionMode
		response llm.Response
		source   WorkflowCompletionSource
	}{
		{
			name: "tool",
			mode: config.WorkflowCompletionModeTool,
			response: commentaryResponse(
				"complete",
				completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
			),
			source: WorkflowCompletionSourceTool,
		},
		{
			name:     "structured",
			mode:     config.WorkflowCompletionModeStructuredOutput,
			response: structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
			source:   WorkflowCompletionSourceStructuredOutput,
		},
		{
			name:     "unstructured",
			mode:     config.WorkflowCompletionModeUnstructured,
			response: structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
			source:   WorkflowCompletionSourceUnstructured,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testWorkflowConfig(nil, test.mode)
			controller := &fakeWorkflowController{
				completionDiagnostic: diagnostic,
			}
			cfg.Controller = controller
			engine := mustNewWorkflowTestEngine(
				t,
				mustCreateTestSession(t),
				&fakeClient{responses: []llm.Response{test.response}},
				cfg,
				Config{},
			)

			result, err := engine.SubmitWorkflowTurn(context.Background())
			if err != nil {
				t.Fatalf("SubmitWorkflowTurn: %v", err)
			}
			terminal := engine.WorkflowTerminalState()
			if terminal.Generation != 1 || terminal.Source != test.source ||
				terminal.Completion.TransitionID != "transition-applied" ||
				terminal.Completion.State != workflowruntime.CompletionStateApplied ||
				!errors.Is(terminal.Completion.Diagnostic, diagnostic) {
				t.Fatalf("terminal snapshot = %+v, want one exact %s completion", terminal, test.source)
			}
			if result.Completion == nil ||
				result.Completion.TransitionID != terminal.Completion.TransitionID ||
				result.Completion.State != terminal.Completion.State ||
				!errors.Is(result.Completion.Diagnostic, terminal.Completion.Diagnostic) {
				t.Fatalf("turn result = %+v, want canonical terminal snapshot %+v", result, terminal)
			}
		})
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
	complete func() error
	count    atomic.Int64
}

func (t *externalCompletionTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	t.count.Add(1)
	if t.complete != nil {
		if err := t.complete(); err != nil {
			return tools.ErrorResult(c, err.Error()), nil
		}
	}
	return tools.Result{CallID: c.ID, Name: c.Name, Output: json.RawMessage(`{"completed":true}`)}, nil
}

func bindExternalAgentCompletion(
	t *testing.T,
	engine *Engine,
	controller *fakeWorkflowController,
	scopeID runtimeids.ExecutionScopeID,
) func() error {
	t.Helper()
	return func() error {
		run := engine.ActiveRun()
		if run == nil {
			return errors.New("test Agent completion has no active Run")
		}
		runID, err := runtimeids.ParseRunID(run.RunID)
		if err != nil {
			return err
		}
		stepID, err := runtimeids.ParseStepID(run.StepID)
		if err != nil {
			return err
		}
		_, completionErr := engine.ApplyWorkflowAgentCompletion(scopeID, runID, stepID, func() (workflowruntime.CompletionResult, error) {
			return controller.acceptCompletionForTest(workflowruntime.AgentCompletionRequest{
				Provenance: workflowruntime.AgentCompletionProvenance{
					ScopeID: scopeID,
					RunID:   runID,
					StepID:  stepID,
				},
				SessionID: runtimeids.NewSessionID(),
			})
		})
		return completionErr
	}
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
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t,
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolWebSearch, Handler: fakeTool{name: toolspec.ToolWebSearch}},
	), Config{
		Model:         "gpt-5",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
		WebSearchMode: "native",
	})
	publishTestWorkflowExecution(t, eng, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand))
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
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t,
		tools.HandlerRegistration{ID: toolspec.ToolWebSearch, Handler: fakeTool{name: toolspec.ToolWebSearch}},
	), Config{
		Model:         "gpt-5",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
		WebSearchMode: "native",
	})
	publishTestWorkflowExecution(t, eng, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand))
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
		Model: "gpt-5",
	})
	publishTestWorkflowExecution(t, eng, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeShellCommand))
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
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for active workflow request")
	}
	type steeringResult struct {
		queued *QueuedUserMessage
		err    error
	}
	steeringDone := make(chan steeringResult, 1)
	go func() {
		_, queued, err := eng.SubmitUserMessageOrSteerWithAcceptance(context.Background(), "steer active workflow", nil)
		steeringDone <- steeringResult{queued: queued, err: err}
	}()
	releaseClient()
	steering := <-steeringDone
	if steering.err != nil {
		t.Fatalf("SubmitUserMessageOrSteerWithAcceptance: %v", steering.err)
	}
	queued := steering.queued
	if queued == nil {
		t.Fatal("expected accepted live steering to queue on active workflow")
	}
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

func TestWorkflowModePromptResumeRejectsMissingCurrentNodeAssignmentBeforeModelRequest(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	counter := &fakeTaskAwarenessSource{
		awareness: workflowruntime.TaskAwareness{CommentCount: 2},
	}
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.TaskAwarenessSource = counter
	workflowCfg.TaskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
	client := &fakeClient{responses: []llm.Response{commentaryResponse(
		"complete",
		completeNodeCall(
			"call_complete",
			json.RawMessage(`{"commentary":"complete","summary":"done"}`),
		),
	)}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	before := eng.transcriptRuntimeState().SnapshotItems()

	_, err := eng.SubmitWorkflowTurn(context.Background())
	if !errors.Is(err, errWorkflowResumeAssignmentUnavailable) {
		t.Fatalf(
			"submit resumed workflow turn error = %v, want missing assignment invariant",
			err,
		)
	}
	assertModelCallCount(t, client, 0)
	if after := eng.transcriptRuntimeState().SnapshotItems(); !reflect.DeepEqual(after, before) {
		t.Fatalf("Resume mutated model-visible history: before=%+v after=%+v", before, after)
	}
	if got := counter.calls.Load(); got != 0 {
		t.Fatalf("TaskAwareness calls = %d, want no assignment reconstruction", got)
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

	initialStepID := runtimeTestStepID("initial")
	if err := runTestActiveStep(eng, initialStepID, func() error {
		return eng.steerWorkflowModeIfNeeded(context.Background(), initialStepID)
	}); err != nil {
		t.Fatalf("prepare initial assignment: %v", err)
	}
	followUpStepID := runtimeTestStepID("follow-up")
	if err := runTestActiveStep(eng, followUpStepID, func() error {
		return eng.steerWorkflowModeIfNeeded(context.Background(), followUpStepID)
	}); err != nil {
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
			stepID := runtimeTestStepID("step")
			if err := runTestActiveStep(eng, stepID, func() error {
				return eng.ensureMetaContextForRequest(context.Background(), stepID)
			}); err != nil {
				t.Fatalf("ensure meta context: %v", err)
			}
			req, err := eng.buildRequest(context.Background(), stepID, true)
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
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: sideEffect}), Config{})
	publishTestWorkflowExecution(t, eng, testWorkflowConfig(controller, config.WorkflowCompletionModeTool))
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
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeStructuredOutput), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := eng.SetGoal(t.Context(), "complete structured workflow", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
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
	if goal := eng.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after structured workflow completion = %+v, want complete", goal)
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
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := eng.SetGoal(t.Context(), "complete unstructured workflow", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
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
	if goal := eng.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after unstructured workflow completion = %+v, want complete", goal)
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
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for commentary response")
	}
	steeringDone := make(chan error, 1)
	go func() {
		_, _, err := eng.SubmitUserMessageOrSteerWithAcceptance(context.Background(), "accepted steering", nil)
		steeringDone <- err
	}()
	releaseRun()
	if err := <-steeringDone; err != nil {
		t.Fatalf("SubmitUserMessageOrSteerWithAcceptance: %v", err)
	}
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

func TestWorkflowTerminalCompletionFailsQueuedSteeringAtRunRelease(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	started := make(chan struct{})
	release := make(chan struct{})
	releaseRun := sync.OnceFunc(func() { close(release) })
	defer releaseRun()
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
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for workflow turn")
	}
	type queuedResult struct {
		item QueuedUserMessage
		err  error
	}
	queueDone := make(chan queuedResult, 1)
	go func() {
		item, err := eng.QueueUserMessage(t.Context(), "do not submit after run release")
		queueDone <- queuedResult{item: item, err: err}
	}()
	releaseRun()
	queuedSubmission := <-queueDone
	if queuedSubmission.err != nil {
		t.Fatalf("queue pending message: %v", queuedSubmission.err)
	}
	queued := queuedSubmission.item
	if err := <-submitDone; err != nil {
		t.Fatalf("submit: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := hookClientCallCount(client); got != 1 {
		t.Fatalf("model calls = %d, want terminal completion to avoid queued turn", got)
	}
	if len(statuses) != 2 ||
		statuses[0].Status != QueuedUserMessageAccepted ||
		statuses[1].Status != QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID ||
		statuses[1].Text != "do not submit after run release" ||
		statuses[1].FailureReason != QueuedUserMessageFailureTerminalWorkflowCompletion {
		t.Fatalf("failed queue status = %+v, want terminal completion failure for %q", statuses[1], queued.ID)
	}
	if pending := eng.messageFlow.PendingUserMessages(); len(pending) != 0 {
		t.Fatalf("pending queue = %+v, want terminal steering removed", pending)
	}
}

func hookClientCallCount(client *hookClient) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.calls)
}

func TestWorkflowShellToolDurableCompletionStopsAfterToolResult(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	diagnostic := errors.New("completion observer failed")
	controller := &fakeWorkflowController{completionDiagnostic: diagnostic}
	shellTool := &externalCompletionTool{}
	events := &liveRunEventCollector{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("run completion command",
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete"}`)},
		),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shellTool}), Config{
		OnEvent: events.accept,
	})
	workflowConfig := testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand)
	publishTestWorkflowExecution(t, eng, workflowConfig)
	shellTool.complete = bindExternalAgentCompletion(t, eng, controller, workflowConfig.ScopeID)

	turn, err := eng.SubmitWorkflowTurn(context.Background())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	if got := shellTool.count.Load(); got != 1 {
		t.Fatalf("shell tool calls = %d, want 1", got)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("runtime completions = %d, want one exact external completion", got)
	}
	terminal := eng.WorkflowTerminalState()
	if terminal.Generation != 1 ||
		terminal.Source != WorkflowCompletionSourceShellCommand ||
		terminal.Completion.TransitionID != "transition-applied" ||
		terminal.Completion.State != workflowruntime.CompletionStateApplied ||
		!errors.Is(terminal.Completion.Diagnostic, diagnostic) ||
		turn.Completion == nil ||
		turn.Completion.TransitionID != terminal.Completion.TransitionID ||
		turn.Completion.State != terminal.Completion.State {
		t.Fatalf("shell completion turn/terminal = %+v / %+v, want one canonical exact snapshot", turn, terminal)
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
				shellTool := &externalCompletionTool{}
				engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shellTool}), Config{})
				workflowConfig := testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand)
				publishTestWorkflowExecution(t, engine, workflowConfig)
				shellTool.complete = bindExternalAgentCompletion(t, engine, controller, workflowConfig.ScopeID)
				return engine
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
