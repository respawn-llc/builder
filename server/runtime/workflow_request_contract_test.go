package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/toolspec"
)

func TestWorkflowToolModeAdvertisesCompleteNodeWithRequiredChoice(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		},
	)

	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build workflow request: %v", err)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeRequired {
		t.Fatalf("workflow tool choice mode = %q, want required", request.ToolChoiceMode)
	}

	advertised := make(map[toolspec.ID]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		id, ok := toolspec.ParseID(tool.Name)
		if !ok {
			t.Fatalf("workflow request advertised unknown tool: %+v", tool)
		}
		advertised[id] = struct{}{}
	}
	if len(advertised) != 2 {
		t.Fatalf("workflow request advertised tools = %v, want ask_question and complete_node", request.Tools)
	}
	for _, id := range []toolspec.ID{toolspec.ToolAskQuestion, toolspec.ToolCompleteNode} {
		if _, ok := advertised[id]; !ok {
			t.Fatalf("workflow request omitted tool %q: %+v", id, request.Tools)
		}
	}
}

func TestNonWorkflowRequestOmitsCompleteNodeWithAutomaticChoice(t *testing.T) {
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolCompleteNode},
		},
	)

	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build non-workflow request: %v", err)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
		t.Fatalf("non-workflow tool choice mode = %q, want automatic", request.ToolChoiceMode)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("non-workflow request advertised workflow-only tools: %+v", request.Tools)
	}
}

func TestShellWorkflowUsesNativeWebSearchAsRequiredToolChoice(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeShellCommand,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{
			Model:         "gpt-5",
			EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch},
			WebSearchMode: "native",
		},
	)

	request, err := engine.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("build shell workflow request: %v", err)
	}
	if request.ToolChoiceMode != llm.ToolChoiceModeRequired ||
		!request.EnableNativeWebSearch ||
		len(request.Tools) != 0 {
		t.Fatalf("shell workflow tool policy = %+v, want required native-only web search", request)
	}
}

func TestShellWorkflowRejectsRequiredChoiceWithoutEffectiveTools(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	client := &fakeClient{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			WorkflowRun: &workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     &externallyCompletedWorkflowController{},
			},
		},
	)

	if _, err := engine.buildRequest(context.Background(), "step", true); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("build shell workflow request error = %v, want invalid request", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("shell workflow invalid request dispatched provider calls = %d", len(client.calls))
	}
}

func TestShellWorkflowRejectsProviderWithoutRequiredToolChoice(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	client := &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "provider-without-required-choice",
		SupportsResponsesAPI: false,
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeShellCommand,
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
		},
	)

	if _, err := engine.buildRequest(context.Background(), "step", true); !errors.Is(err, llm.ErrUnsupportedToolChoicePolicy) {
		t.Fatalf("build shell workflow request error = %v, want unsupported tool choice", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("unsupported shell workflow request dispatched provider calls = %d", len(client.calls))
	}
}

func TestWorkflowRequestRejectsUnresolvedCompletionModeBeforeProviderDispatch(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	client := &fakeClient{}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionMode("unknown"),
			Controller:     &externallyCompletedWorkflowController{},
		},
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
		},
	)

	if _, err := engine.buildRequest(context.Background(), "step", true); err == nil {
		t.Fatal("build workflow request with unresolved completion mode succeeded")
	}
	if len(client.calls) != 0 {
		t.Fatalf("unresolved workflow completion mode dispatched provider calls = %d", len(client.calls))
	}
}

func TestWorkflowRejectsDuplicateCompletionBeforeExecutingMixedToolCalls(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	sideEffect := &workflowSideEffectTool{}
	controller := &workflowCompletionAccountingController{}
	var resultMu sync.Mutex
	var results []tools.Result
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse(
			"",
			workflowCompleteNodeCall("first-completion", `{"transition":"done","summary":"done"}`),
			workflowCompleteNodeCall("duplicate-completion", `{"transition":"done","summary":"done"}`),
			llm.ToolCall{ID: "skipped-side-effect", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"ignored"}`)},
		),
		commentaryResponse(
			"",
			workflowCompleteNodeCall("accepted-completion", `{"transition":"done","summary":"done"}`),
			llm.ToolCall{ID: "accepted-side-effect", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"executed"}`)},
		),
	}}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: sideEffect,
		}),
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			OnEvent: func(event Event) {
				if event.Kind != EventToolCallCompleted || event.ToolResult == nil {
					return
				}
				resultMu.Lock()
				results = append(results, *event.ToolResult)
				resultMu.Unlock()
			},
			WorkflowRun: &workflowruntime.Config{
				RunID: runID,
				Contract: workflowruntime.CompletionContract{
					RunID: runID,
					Transitions: []workflowruntime.CompletionTransition{{
						ID:         "done",
						Parameters: []workflow.Parameter{{Key: "summary"}},
					}},
				},
				CompletionMode:               workflowruntime.CompletionModeTool,
				MaxInvalidCompletionAttempts: 2,
				Controller:                   controller,
			},
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if got := sideEffect.calls.Load(); got != 1 {
		t.Fatalf("side-effect executions = %d, want one accepted mixed-turn call", got)
	}
	if got := controller.completions.Load(); got != 1 {
		t.Fatalf("workflow completions = %d, want one", got)
	}
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("workflow completion violations = %d, want one", got)
	}
	resultMu.Lock()
	defer resultMu.Unlock()
	if len(results) != 2 {
		t.Fatalf("workflow tool results = %+v, want two accepted mixed-turn results", results)
	}
	resultsByCallID := make(map[string]tools.Result, len(results))
	for _, result := range results {
		resultsByCallID[result.CallID] = result
	}
	if completion, ok := resultsByCallID["accepted-completion"]; !ok ||
		completion.Name != toolspec.ToolCompleteNode ||
		completion.IsError ||
		!completion.Terminal {
		t.Fatalf("accepted completion result = %+v", completion)
	}
	if sideEffectResult, ok := resultsByCallID["accepted-side-effect"]; !ok ||
		sideEffectResult.Name != toolspec.ToolExecCommand ||
		sideEffectResult.IsError ||
		sideEffectResult.Terminal {
		t.Fatalf("accepted side-effect result = %+v", sideEffectResult)
	}
	if terminal := engine.WorkflowTerminalState(); !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceTool {
		t.Fatalf("workflow terminal state = %+v, want tool completion", terminal)
	}
}

func TestStructuredWorkflowCompletionStopsAfterSingleProviderDispatch(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	controller := &workflowCompletionAccountingController{}
	client := &fakeClient{responses: []llm.Response{
		finalTextResponse(`{"commentary":"complete","summary":"done"}`),
		finalTextResponse(`{"commentary":"unexpected","summary":"done"}`),
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.Config{
			RunID: runID,
			Contract: workflowruntime.CompletionContract{
				RunID: runID,
				Transitions: []workflowruntime.CompletionTransition{{
					ID:         "done",
					Parameters: []workflow.Parameter{{Key: "summary"}},
				}},
			},
			CompletionMode: workflowruntime.CompletionModeStructuredOutput,
			Controller:     controller,
		},
		Config{Model: "gpt-5"},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit structured workflow turn: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("structured workflow provider calls = %d, want one", len(client.calls))
	}
	if request := client.calls[0]; request.StructuredOutput == nil || !request.StructuredOutput.Strict {
		t.Fatalf("structured workflow request output = %+v, want strict structured output", request.StructuredOutput)
	}
	if got := controller.completions.Load(); got != 1 {
		t.Fatalf("structured workflow completions = %d, want one", got)
	}
	if terminal := engine.WorkflowTerminalState(); !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceStructuredOutput {
		t.Fatalf("structured workflow terminal state = %+v", terminal)
	}
}

func TestUnstructuredWorkflowCompletionRecordsParsedRequest(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	controller := &workflowCompletionAccountingController{}
	client := &fakeClient{responses: []llm.Response{
		finalTextResponse(`{"commentary":"complete","summary":"done"}`),
		finalTextResponse(`{"commentary":"unexpected","summary":"done"}`),
	}}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		&workflowruntime.Config{
			RunID: runID,
			Contract: workflowruntime.CompletionContract{
				RunID: runID,
				Transitions: []workflowruntime.CompletionTransition{{
					ID:         "done",
					Parameters: []workflow.Parameter{{Key: "summary"}},
				}},
			},
			CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Controller:     controller,
		},
		Config{Model: "gpt-5"},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit unstructured workflow turn: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("unstructured workflow provider calls = %d, want one", len(client.calls))
	}
	requests := controller.CompletionRequests()
	if len(requests) != 1 {
		t.Fatalf("unstructured workflow completion requests = %+v, want one", requests)
	}
	request := requests[0]
	if request.RunID != runID ||
		request.TransitionID != "done" ||
		request.OutputValues["summary"] != "done" {
		t.Fatalf("unstructured workflow completion request = %+v", request)
	}
	if terminal := engine.WorkflowTerminalState(); !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceUnstructured {
		t.Fatalf("unstructured workflow terminal state = %+v", terminal)
	}
}

func TestRequestToolsRespectLockedVisionCapability(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		capabilities session.LockedModelCapabilities
		wantVision   bool
	}{
		{
			name:       "text-only catalog model",
			model:      "gpt-3.5-turbo",
			wantVision: false,
		},
		{
			name:         "explicit vision override",
			model:        "gpt-4.1-2026-01-15",
			capabilities: session.LockedModelCapabilities{SupportsVisionInputs: true},
			wantVision:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(
				t,
				store,
				&fakeClient{},
				tools.NewRegistry(),
				Config{
					Model:             test.model,
					ModelCapabilities: test.capabilities,
					EnabledTools:      []toolspec.ID{toolspec.ToolViewImage},
				},
			)

			request, err := engine.buildRequest(context.Background(), "step", true)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			locked := store.Meta().Locked
			if locked == nil {
				t.Fatal("request build did not lock model capabilities")
			}
			if got := locked.ModelCapabilities.SupportsVisionInputs; got != test.wantVision {
				t.Fatalf("locked vision capability = %t, want %t", got, test.wantVision)
			}

			advertised := false
			for _, tool := range request.Tools {
				id, ok := toolspec.ParseID(tool.Name)
				if !ok {
					t.Fatalf("request advertised unknown tool: %+v", tool)
				}
				if id == toolspec.ToolViewImage {
					advertised = true
				}
			}
			if advertised != test.wantVision {
				t.Fatalf("view_image advertised = %t, want %t; tools=%+v", advertised, test.wantVision, request.Tools)
			}
		})
	}
}

func workflowCompleteNodeCall(id, input string) llm.ToolCall {
	return llm.ToolCall{
		ID:    id,
		Name:  string(toolspec.ToolCompleteNode),
		Input: json.RawMessage(input),
	}
}

type workflowSideEffectTool struct {
	calls atomic.Int32
}

func (t *workflowSideEffectTool) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	t.calls.Add(1)
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

type workflowCompletionAccountingController struct {
	externallyCompletedWorkflowController
	completionMu       sync.Mutex
	completionRequests []workflowruntime.CompletionRequest
	completions        atomic.Int32
	violations         atomic.Int32
}

func (c *workflowCompletionAccountingController) CompleteWorkflowRun(
	_ context.Context,
	request workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	c.completionMu.Lock()
	c.completionRequests = append(c.completionRequests, request)
	c.completionMu.Unlock()
	c.completions.Add(1)
	return workflowruntime.CompletionResult{}, nil
}

func (c *workflowCompletionAccountingController) CompletionRequests() []workflowruntime.CompletionRequest {
	c.completionMu.Lock()
	defer c.completionMu.Unlock()
	return append([]workflowruntime.CompletionRequest(nil), c.completionRequests...)
}

func (c *workflowCompletionAccountingController) RecordWorkflowProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{Count: int64(c.violations.Add(1))}, nil
}
