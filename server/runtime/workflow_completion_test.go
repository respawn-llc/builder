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

	"core/prompts"
	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/toolspec"
)

type fakeWorkflowController struct {
	completed                           atomic.Int64
	violations                          atomic.Int64
	protocolBudgetResets                atomic.Int64
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
	c.completed.Add(1)
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
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

func TestWorkflowToolModeExposesCompleteNodeDespiteEnabledTools(t *testing.T) {
	store := mustCreateTestSession(t)
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.Contract.Transitions[0].Parameters = append(workflowCfg.Contract.Transitions[0].Parameters, workflow.Parameter{Key: "details", Description: "Detailed evidence."})
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
	assertCompletionSchema(t, toolsByName[string(toolspec.ToolCompleteNode)].Schema, map[string]string{
		"summary": "Summary of work.",
		"details": "Detailed evidence.",
	})
	if _, ok := toolsByName[string(toolspec.ToolExecCommand)]; ok {
		t.Fatalf("exec_command should not be re-added from role tools, tools=%+v", req.Tools)
	}
}

func TestCompleteNodeNotAdvertisedOutsideWorkflow(t *testing.T) {
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
}

func TestWorkflowModePromptInjectedWithoutHeadlessOrUserPrompt(t *testing.T) {
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
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorkflowMode {
			workflowIdx = idx
		}
	}
	if workflowIdx < 0 {
		t.Fatalf("workflow prompt missing: messages=%+v", messages)
	}
	if workflowContent := messages[workflowIdx].Content; !strings.Contains(workflowContent, "ticket `BUI-1`") || !strings.Contains(workflowContent, "Do node work.") || !strings.Contains(workflowContent, "complete_node") {
		t.Fatalf("workflow instructions missing expected content:\n%s", workflowContent)
	}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessMode {
			t.Fatalf("headless prompt should not be injected during workflow runs: %+v", messages)
		}
		if msg.Role == llm.RoleUser {
			t.Fatalf("workflow run should not inject user prompt: %+v", messages)
		}
	}
}

func TestWorkflowModePromptIncludesCurrentTaskCommentCount(t *testing.T) {
	store := mustCreateTestSession(t)
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
	if got := counter.calls.Load(); got != 1 {
		t.Fatalf("CountTaskComments calls = %d, want 1", got)
	}
	workflowContent := workflowPromptContentFromRequest(t, client.calls[0])
	for _, want := range []string{
		"2 comments",
		prompts.LaunchCommand() + " task comment list BUI-1",
	} {
		if !strings.Contains(workflowContent, want) {
			t.Fatalf("workflow prompt missing %q:\n%s", want, workflowContent)
		}
	}
}

func TestWorkflowModePromptExistingRunScopedMessageSkipsCommentCountQuery(t *testing.T) {
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

func TestWorkflowModePromptCommentCountErrorFailsBeforeWorkflowPromptAppend(t *testing.T) {
	store := mustCreateTestSession(t)
	countErr := errors.New("count comments failed")
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool)
	workflowCfg.TaskCommentCounter = &fakeTaskCommentCounter{err: countErr}
	client := &fakeClient{responses: []llm.Response{commentaryResponse("unexpected")}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	_, err := eng.SubmitWorkflowTurn(context.Background())
	if !errors.Is(err, countErr) {
		t.Fatalf("SubmitWorkflowTurn error = %v, want %v", err, countErr)
	}
	assertModelCallCount(t, client, 0)
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorkflowMode {
			t.Fatalf("workflow prompt should not be appended after count error: %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
	}
}

func TestWorkflowModePromptReinjectedForNewRunAfterExistingWorkflowPrompt(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("seed", "message", llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorkflowMode, SourcePath: "run-old", Content: "old workflow instructions"}); err != nil {
		t.Fatalf("seed workflow message: %v", err)
	}
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{commentaryResponse("complete",
		completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
	)}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{})
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	messages := requestMessages(client.calls[0])
	workflowMessages := []llm.Message{}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorkflowMode {
			workflowMessages = append(workflowMessages, msg)
		}
	}
	if len(workflowMessages) != 2 {
		t.Fatalf("workflow message count = %d, want old and new messages: %+v", len(workflowMessages), workflowMessages)
	}
	if workflowMessages[0].SourcePath != "run-old" || !strings.Contains(workflowMessages[0].Content, "old workflow instructions") {
		t.Fatalf("old workflow message missing/preserved incorrectly: %+v", workflowMessages)
	}
	if workflowMessages[1].SourcePath != "run-1" || !strings.Contains(workflowMessages[1].Content, "ticket `BUI-1`") {
		t.Fatalf("new workflow message missing run-scoped source path: %+v", workflowMessages)
	}
}

func workflowPromptContentFromRequest(t *testing.T, req llm.Request) string {
	t.Helper()
	workflowMessages := workflowPromptMessages(requestMessages(req))
	if len(workflowMessages) != 1 {
		t.Fatalf("workflow message count = %d, want 1: %+v", len(workflowMessages), workflowMessages)
	}
	return workflowMessages[0].Content
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

func TestWorkflowStructuredModeUsesStructuredOutput(t *testing.T) {
	store := mustCreateTestSession(t)
	workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeStructuredOutput)
	workflowCfg.Contract.Transitions[0].Parameters = append(workflowCfg.Contract.Transitions[0].Parameters, workflow.Parameter{Key: "details", Description: "Detailed evidence."})
	client := &fakeClient{responses: []llm.Response{structuredFinalResponse(`{"commentary":"complete","summary":"done","details":"evidence"}`)}}
	eng := mustNewWorkflowTestEngine(t, store, client, workflowCfg, Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "node prompt"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	req := client.calls[0]
	if req.StructuredOutput == nil {
		t.Fatal("expected structured output")
	}
	assertCompletionSchema(t, req.StructuredOutput.Schema, map[string]string{
		"summary": "Summary of work.",
		"details": "Detailed evidence.",
	})
	messages := requestMessages(req)
	workflowIdx := -1
	for idx, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorkflowMode {
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

func TestWorkflowCompletionRecordsTerminalStateForStructuredOutput(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	eng := mustNewWorkflowTestEngine(t, store, &fakeClient{responses: []llm.Response{
		structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
	}}, testWorkflowConfig(controller, config.WorkflowCompletionModeStructuredOutput), Config{
		ProviderCapabilitiesOverride: &llm.ProviderCapabilities{SupportsResponsesAPI: true},
	})

	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	terminal := eng.WorkflowTerminalState()
	if !terminal.Completed || terminal.RunID != "run-1" || terminal.Source != WorkflowCompletionSourceStructuredOutput {
		t.Fatalf("terminal state = %+v, want structured completion", terminal)
	}
}

func TestWorkflowRuntimeUsesPersistedStructuredOutputMode(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "legacy"}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeStructuredOutput), Config{
		Model: "legacy",
	})
	req, err := eng.buildRequest(context.Background(), "step", true)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.StructuredOutput == nil {
		t.Fatalf("structured output missing for persisted structured mode: %+v", req)
	}
}

func TestWorkflowShellAndUnstructuredModesOmitDynamicCompletionMetadata(t *testing.T) {
	tests := []struct {
		name           string
		mode           config.WorkflowCompletionMode
		wantCommand    bool
		forbidCommand  bool
		forbidToolName bool
	}{
		{name: "shell command", mode: config.WorkflowCompletionModeShellCommand, wantCommand: true, forbidToolName: true},
		{name: "unstructured output", mode: config.WorkflowCompletionModeUnstructured, forbidCommand: true, forbidToolName: true},
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
			workflowContent := workflowPromptContentFromRequest(t, req)
			command := prompts.LaunchCommand() + " task complete"
			if tt.wantCommand && !strings.Contains(workflowContent, command) {
				t.Fatalf("%s workflow prompt did not include task completion command:\n%s", tt.name, workflowContent)
			}
			if tt.forbidCommand && strings.Contains(workflowContent, command) {
				t.Fatalf("%s workflow prompt advertised shell completion command:\n%s", tt.name, workflowContent)
			}
			if tt.forbidToolName && strings.Contains(workflowContent, string(toolspec.ToolCompleteNode)) {
				t.Fatalf("%s workflow prompt advertised complete_node:\n%s", tt.name, workflowContent)
			}
		})
	}
}

func TestCompleteNodeOutsideWorkflowReturnsToolError(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("complete", completeNodeCall("call_complete", json.RawMessage(`{"transition":"done"}`))),
		structuredFinalResponse("done"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	found := false
	for _, msg := range eng.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleTool && msg.Name == string(toolspec.ToolCompleteNode) && strings.Contains(msg.Content, "only available during a workflow run") {
			found = true
		}
	}
	if !found {
		t.Fatalf("complete_node error output missing from messages: %+v", eng.transcriptRuntimeState().SnapshotMessages())
	}
}

func TestWorkflowMixedCompleteNodeRunsSideEffects(t *testing.T) {
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
		WorkflowRun: testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
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
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "complete", Phase: llm.MessagePhaseFinal},
		ToolCalls: []llm.ToolCall{
			completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`)),
		},
		OutputItems: []llm.ResponseItem{{
			Type: llm.ResponseItemTypeOther,
			Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
		}},
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
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	hostedCallPersisted := false
	hostedResultPersisted := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if persisted.Role == llm.RoleAssistant {
			for _, call := range persisted.ToolCalls {
				if call.ID == "ws_1" {
					hostedCallPersisted = true
				}
			}
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID == "ws_1" {
			hostedResultPersisted = true
		}
	}
	if !hostedCallPersisted || !hostedResultPersisted {
		t.Fatalf("hosted call/result persisted = %v/%v, want both", hostedCallPersisted, hostedResultPersisted)
	}
}

func TestWorkflowDuplicateCompleteNodePreflightSkipsSideEffects(t *testing.T) {
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
}

func TestWorkflowStructuredCompletionStopsWithoutAnotherTurn(t *testing.T) {
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
}

func TestWorkflowUnstructuredFinalAnswerCompletesRun(t *testing.T) {
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
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceUnstructured || terminal.RunID != "run-1" {
		t.Fatalf("terminal state = %+v, want unstructured completion", terminal)
	}
}

func TestWorkflowUnstructuredTerminalCompletionFailsQueuedSteeringDuringCloseDrain(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
		structuredFinalResponse("unexpected queued turn"),
	}}
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	queued := eng.QueueUserMessageWithClientRequestID("do not submit after completion", "req-after-complete")
	if err := eng.DrainQueuedUserMessagesBeforeClose(context.Background()); err != nil {
		t.Fatalf("DrainQueuedUserMessagesBeforeClose: %v", err)
	}
	assertModelCallCount(t, client, 1)
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-after-complete" || statuses[1].RestoreText != "do not submit after completion" || statuses[1].FailureReason != QueuedUserMessageFailureTerminalWorkflowCompletion {
		t.Fatalf("failed queue status = %+v, want terminal completion failure for %q", statuses[1], queued.ID)
	}
}

func TestWorkflowTerminalCompletionFailsQueuedSteeringAtRunRelease(t *testing.T) {
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

func TestQueuedSubmitRetryFailsQueuedSteeringWhenWorkflowCompletesWhileBusy(t *testing.T) {
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
	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "run")
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow turn")
	}
	queued := eng.QueueUserMessageWithClientRequestID("do not submit after terminal retry", "req-terminal-retry")
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitQueuedUserMessages(context.Background())
		submitDone <- err
	}()

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitQueuedUserMessages: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := hookClientCallCount(client); got != 1 {
		t.Fatalf("model calls = %d, want queued retry not to call model after terminal completion", got)
	}
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-terminal-retry" || statuses[1].RestoreText != "do not submit after terminal retry" || statuses[1].FailureReason != QueuedUserMessageFailureTerminalWorkflowCompletion {
		t.Fatalf("failed queue status = %+v, want terminal completion failure for %q", statuses[1], queued.ID)
	}
}

func TestWorkflowInterruptedAutoDrainLeavesLaterIdleQueuedSteeringPending(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := newBlockingThenQueuedResponseClient(structuredFinalResponse(`{"commentary":"complete","summary":"done"}`))
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	blockingMessages := &blockingQueueMessageLifecycle{
		wrapped:      eng.messageFlow,
		queueEntered: make(chan struct{}),
		releaseQueue: make(chan struct{}),
	}
	eng.messageFlow = blockingMessages

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "run")
		firstDone <- err
	}()
	client.waitStarted(t)
	queueDone := make(chan struct{})
	go func() {
		eng.QueueUserMessageWithClientRequestID("busy marked queue", "req-busy")
		close(queueDone)
	}()
	blockingMessages.waitQueueEntered(t)
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	explicit := eng.QueueUserMessageWithClientRequestID("idle explicit queue", "req-idle")
	blockingMessages.release()
	select {
	case <-queueDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued steering call to return")
	}
	waitEngineLifecycleTasks(t, eng)
	if got := client.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want interrupted run only", got)
	}
	if !eng.HasQueuedUserWork() {
		t.Fatal("expected explicit idle queue to remain pending")
	}
	if !eng.DiscardQueuedUserMessage(explicit.ID) {
		t.Fatalf("expected explicit idle queue %q to remain discardable; statuses=%+v", explicit.ID, statuses)
	}
}

func TestWorkflowObservedDurableCompletionFailsQueuedSteeringDuringCloseDrain(t *testing.T) {
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

func TestWorkflowUnstructuredInvalidFinalAnswerNudgeUsesCurrentContract(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(`{"summary":""}`),
		structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
	}}
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
	assertDeveloperErrorFeedbackAfterAssistantFinalContains(t, eng, `{"summary":""}`, []string{"summary", "JSON"}, []string{prompts.LaunchCommand() + " task complete", string(toolspec.ToolCompleteNode)})
}

func TestWorkflowShellFinalAnswerNudgeUsesShellCompletionInstructions(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse("done"),
		structuredFinalResponse("done again"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := controller.violations.Load(); got != 2 {
		t.Fatalf("violations = %d, want 2", got)
	}
	assertDeveloperErrorFeedbackAfterAssistantFinalContains(t, eng, "done", []string{prompts.LaunchCommand() + " task complete", "summary"}, []string{string(toolspec.ToolCompleteNode)})
}

func TestWorkflowDurableCompletionBeforeModelTurnStopsWithoutRequest(t *testing.T) {
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

func TestWorkflowShellToolDurableCompletionStopsAfterToolResult(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	shellTool := &externalCompletionTool{controller: controller}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("run completion command",
			llm.ToolCall{ID: "call_shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"kent task complete"}`)},
		),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: shellTool}), Config{
		WorkflowRun: testWorkflowConfig(controller, config.WorkflowCompletionModeShellCommand),
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
}

func TestWorkflowDelayedDurableCompletionObservedBeforeNextModelTurn(t *testing.T) {
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

func TestWorkflowInvalidCompletionFailClosedWhenConfiguredCapInvalid(t *testing.T) {
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

func TestWorkflowFinalAnswersUseInvalidCompletionCap(t *testing.T) {
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
	assertDeveloperErrorFeedbackAfterAssistantFinal(t, eng, "done 1", strings.TrimSpace(prompts.WorkflowFinalAnswerNudgePrompt))
}

func TestWorkflowEmptyFinalAnswersUseInvalidCompletionCap(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(""),
		structuredFinalResponse(""),
		structuredFinalResponse(""),
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

func TestWorkflowStructuredEmptyFinalInterruptsAtInvalidCompletionCap(t *testing.T) {
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		structuredFinalResponse(""),
		structuredFinalResponse(""),
		structuredFinalResponse("unexpected"),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeStructuredOutput), Config{})
	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	if got := controller.maxHits.Load(); got != 1 {
		t.Fatalf("max hits = %d, want 1", got)
	}
}

func assertCompletionSchema(t *testing.T, schema json.RawMessage, parameterDescriptions map[string]string) {
	t.Helper()
	root := schemaRoot(t, schema)
	if got := root["additionalProperties"]; got != false {
		t.Fatalf("schema additionalProperties = %v, want false in %s", got, string(schema))
	}
	if _, ok := schemaProperties(t, schema)["transition"]; ok {
		t.Fatalf("single-transition schema should infer transition instead of advertising it: %s", string(schema))
	}
	assertNullableSchemaProperty(t, schema, "commentary", "Brief explanation of what was completed and why this transition was selected.")
	required := []string{"commentary"}
	for name, description := range parameterDescriptions {
		assertSchemaProperty(t, schema, name, "string", description)
		required = append(required, name)
	}
	assertSchemaRequiredFields(t, schema, required)
}

func assertSchemaProperty(t *testing.T, schema json.RawMessage, name string, propertyType string, description string) {
	t.Helper()
	property := schemaProperty(t, schema, name)
	if got := property["type"]; got != propertyType {
		t.Fatalf("schema property %s type = %v, want %s in %s", name, got, propertyType, string(schema))
	}
	if got := property["description"]; got != description {
		t.Fatalf("schema property %s description = %v, want %q in %s", name, got, description, string(schema))
	}
}

func assertNullableSchemaProperty(t *testing.T, schema json.RawMessage, name string, description string) {
	t.Helper()
	property := schemaProperty(t, schema, name)
	rawTypes, ok := property["type"].([]any)
	if !ok || len(rawTypes) != 2 || rawTypes[0] != "string" || rawTypes[1] != "null" {
		t.Fatalf("schema property %s type = %v, want [string null] in %s", name, property["type"], string(schema))
	}
	if got := property["description"]; got != description {
		t.Fatalf("schema property %s description = %v, want %q in %s", name, got, description, string(schema))
	}
}

func schemaProperty(t *testing.T, schema json.RawMessage, name string) map[string]any {
	t.Helper()
	properties := schemaProperties(t, schema)
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema property %s missing: %s", name, string(schema))
	}
	return property
}

func schemaProperties(t *testing.T, schema json.RawMessage) map[string]any {
	t.Helper()
	root := schemaRoot(t, schema)
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %s", string(schema))
	}
	return properties
}

func schemaRequired(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	root := schemaRoot(t, schema)
	raw, ok := root["required"].([]any)
	if !ok {
		t.Fatalf("schema required missing: %s", string(schema))
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("schema required item is %T: %v", item, item)
		}
		out = append(out, text)
	}
	if len(out) == 0 {
		t.Fatalf("schema required empty: %s", string(schema))
	}
	return out
}

func assertSchemaRequiredFields(t *testing.T, schema json.RawMessage, expected []string) {
	t.Helper()
	required := schemaRequired(t, schema)
	seen := map[string]bool{}
	for _, name := range required {
		if seen[name] {
			t.Fatalf("schema required contains duplicate %s, required=%+v schema=%s", name, required, string(schema))
		}
		seen[name] = true
	}
	if len(seen) != len(expected) {
		t.Fatalf("schema required = %+v, want exactly %+v in %s", required, expected, string(schema))
	}
	for _, name := range expected {
		if !seen[name] {
			t.Fatalf("schema required missing %s, required=%+v schema=%s", name, required, string(schema))
		}
	}
}

func assertDeveloperErrorFeedbackAfterAssistantFinal(t *testing.T, eng *Engine, assistantContent string, feedbackContent string) {
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
		if next.Role != llm.RoleDeveloper || next.MessageType != llm.MessageTypeErrorFeedback || next.Content != feedbackContent {
			t.Fatalf("message after assistant final %q = %+v, want developer error feedback %q; messages=%+v", assistantContent, next, feedbackContent, messages)
		}
		return
	}
	t.Fatalf("assistant final %q not found in messages: %+v", assistantContent, messages)
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

func schemaRoot(t *testing.T, schema json.RawMessage) map[string]any {
	t.Helper()
	root := map[string]any{}
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return root
}
