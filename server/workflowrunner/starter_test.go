package workflowrunner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/session"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestSchedulerRunsNewSessionWorkflowNodeWithStructuredOutput(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer(`{"commentary":"finished structured"}`))

	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)

	detail, err := fixture.view.GetTask(context.Background(), string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !detail.Summary.Done {
		t.Fatalf("task summary done = false, detail=%+v", detail)
	}
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || strings.TrimSpace(runs[0].SessionID) == "" || runs[0].CompletedAt == 0 {
		t.Fatalf("run not attached/completed: %+v", runs)
	}
	transitions, err := fixture.store.ListTransitions(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].TransitionID != "done" || transitions[1].Commentary != "finished structured" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("completion transition = %+v", transitions)
	}
	reqs := fixture.client.Requests()
	if len(reqs) == 0 {
		t.Fatal("fake model was not called")
	}
	first := reqs[0]
	if first.StructuredOutput == nil {
		t.Fatalf("structured output schema missing in request: %+v", first)
	}
	assertNoUserPrompt(t, first)
	fixture.assertRunSessionUsesTaskWorktree(t, runs[0].SessionID)
	fixture.waitForActiveCountZero(t, scheduler)
}

func TestSchedulerNamesFreshWorkflowSessionFromAcceptedTransition(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer(`{"commentary":"finished structured"}`))
	def, _, err := fixture.store.GetDefinition(context.Background(), fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	startEdgePrompt := starterEdgeByKey(t, def, "start").PromptTemplate
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)

	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || strings.TrimSpace(runs[0].SessionID) == "" {
		t.Fatalf("runs = %+v, want attached run session", runs)
	}
	meta := fixture.sessionMeta(t, runs[0].SessionID)
	if meta.Name != "RUN-1: Backlog -> Agent" {
		t.Fatalf("session name = %q, want accepted workflow transition name", meta.Name)
	}
	if meta.FirstPromptPreview != startEdgePrompt {
		t.Fatalf("session preview = %q, want rendered start edge prompt %q", meta.FirstPromptPreview, startEdgePrompt)
	}
}

func TestSchedulerRunsNewSessionWorkflowNodeWithCompleteNodeTool(t *testing.T) {
	input := json.RawMessage(`{"commentary":"finished tool"}`)
	fixture := newStarterFixture(t, config.WorkflowCompletionModeTool, ScriptedToolBatch("complete", llm.ToolCall{ID: "call-complete", Name: "complete_node", Input: input}))

	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)

	transitions, err := fixture.store.ListTransitions(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].Commentary != "finished tool" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("completion transition = %+v", transitions)
	}
	reqs := fixture.client.Requests()
	if len(reqs) == 0 || !requestHasTool(reqs[0], "complete_node") {
		t.Fatalf("complete_node not exposed in request: %+v", reqs)
	}
}

func TestSchedulerWorkflowPromptIncludesStoreBackedTaskCommentCount(t *testing.T) {
	input := json.RawMessage(`{"commentary":"finished tool"}`)
	fixture := newStarterFixture(t, config.WorkflowCompletionModeTool, ScriptedToolBatch("complete", llm.ToolCall{ID: "call-complete", Name: "complete_node", Input: input}))
	task := fixture.createStartedTask(t)
	if _, err := fixture.store.AddComment(context.Background(), task.ID, "first durable note", "user", "nek"); err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	if _, err := fixture.store.AddComment(context.Background(), task.ID, "second durable note", "agent", "coder"); err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)

	reqs := fixture.client.Requests()
	if len(reqs) == 0 {
		t.Fatal("fake model was not called")
	}
	assertPromptContains(t, reqs[0], []string{"2 comments", "task comment list RUN-1"})
	for _, body := range []string{"first durable note", "second durable note"} {
		if strings.Contains(requestPromptText(reqs[0]), body) {
			t.Fatalf("workflow prompt must not include comment body %q:\n%s", body, requestPromptText(reqs[0]))
		}
	}
}

func TestWorkflowRuntimeAskQuestionWaitsAndResumesSameRunSession(t *testing.T) {
	completeInput := json.RawMessage(`{"commentary":"answered and finished"}`)
	fixture := newStarterFixture(t, config.WorkflowCompletionModeTool,
		ScriptedAskQuestion("call-ask", []byte(`{"question":"Need direction?","suggestions":["ship","stop"],"recommended_option_index":1}`)),
		ScriptedToolBatch("complete", llm.ToolCall{ID: "call-complete", Name: "complete_node", Input: completeInput}),
	)
	role := fixture.cfg.Settings.Subagents["coder"]
	role.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	role.Sources["tools."+toolspec.ConfigName(toolspec.ToolAskQuestion)] = "test"
	fixture.cfg.Settings.Subagents["coder"] = role
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	waiting := fixture.waitForWaitingAsk(t, task.ID, "call-ask")
	pending := fixture.runtimes.ListPendingPrompts(waiting.SessionID)
	if len(pending) != 1 || pending[0].Request.ID != "call-ask" || pending[0].Request.Question != "Need direction?" {
		t.Fatalf("pending prompts = %+v", pending)
	}
	if err := fixture.runtimes.SubmitPromptResponse(waiting.SessionID, askquestion.AskQuestionResponse{RequestID: "call-ask", Answer: "Ship it"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].SessionID != waiting.SessionID || runs[0].WaitingAskID != "" {
		t.Fatalf("run after answer = %+v, want same session and cleared waiting ask", runs)
	}
	transitions, err := fixture.store.ListTransitions(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].Commentary != "answered and finished" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("completion transition = %+v", transitions)
	}
	reqs := fixture.client.Requests()
	if len(reqs) == 0 {
		t.Fatal("fake model was not called")
	}
	if !requestHasTool(reqs[0], string(toolspec.ToolAskQuestion)) {
		t.Fatalf("ask_question missing in workflow request: %+v", reqs[0].Tools)
	}
	if !requestHasTool(reqs[0], string(toolspec.ToolCompleteNode)) {
		t.Fatalf("complete_node missing in workflow request: %+v", reqs[0].Tools)
	}
}

func TestWorkflowRuntimeMultipleAskQuestionsInOneToolBatchResumeSequentially(t *testing.T) {
	completeInput := json.RawMessage(`{"commentary":"answered both and finished"}`)
	fixture := newStarterFixture(t, config.WorkflowCompletionModeTool,
		ScriptedToolBatch("questions",
			llm.ToolCall{ID: "call-ask-1", Name: "ask_question", Input: json.RawMessage(`{"question":"First direction?","suggestions":["ship","stop"],"recommended_option_index":1}`)},
			llm.ToolCall{ID: "call-ask-2", Name: "ask_question", Input: json.RawMessage(`{"question":"Second direction?","suggestions":["fast","safe"],"recommended_option_index":2}`)},
		),
		ScriptedToolBatch("complete", llm.ToolCall{ID: "call-complete", Name: "complete_node", Input: completeInput}),
	)
	role := fixture.cfg.Settings.Subagents["coder"]
	role.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	role.Sources["tools."+toolspec.ConfigName(toolspec.ToolAskQuestion)] = "test"
	fixture.cfg.Settings.Subagents["coder"] = role
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	first := fixture.waitForWaitingAsk(t, task.ID, "call-ask-1")
	if err := fixture.runtimes.SubmitPromptResponse(first.SessionID, askquestion.AskQuestionResponse{RequestID: "call-ask-1", Answer: "Ship it"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse first: %v", err)
	}
	second := fixture.waitForWaitingAsk(t, task.ID, "call-ask-2")
	if second.SessionID != first.SessionID {
		t.Fatalf("second ask session = %q, want first session %q", second.SessionID, first.SessionID)
	}
	if err := fixture.runtimes.SubmitPromptResponse(second.SessionID, askquestion.AskQuestionResponse{RequestID: "call-ask-2", Answer: "Keep it safe"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse second: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].SessionID != first.SessionID || runs[0].WaitingAskID != "" {
		t.Fatalf("run after answers = %+v, want same session and cleared waiting ask", runs)
	}
	askResults := workflowRequestAskQuestionToolMessages(fixture.client.Requests())
	if len(askResults) != 2 {
		t.Fatalf("ask_question tool results = %+v, want two completed asks", askResults)
	}
	seenAskResults := map[string]bool{}
	for _, msg := range askResults {
		seenAskResults[msg.ToolCallID] = true
		trimmed := strings.TrimSpace(msg.Content)
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			if _, ok := payload["error"]; ok {
				t.Fatalf("ask_question tool result contains error payload: %+v", msg)
			}
		}
	}
	if !seenAskResults["call-ask-1"] || !seenAskResults["call-ask-2"] {
		t.Fatalf("ask_question tool result call IDs = %+v, want both asks", askResults)
	}
}

func TestWorkflowRuntimeStarterCloseCancelsInFlightRun(t *testing.T) {
	client := newBlockingClient()
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer("{}"))
	fixture.clientFactory = func(SchedulerStartRunRequest) llm.Client { return client }
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	client.waitForCall(t)
	if err := fixture.starter.Close(); err != nil {
		t.Fatalf("starter.Close: %v", err)
	}
	fixture.waitForInterruptedRun(t, scheduler, task.ID, ReasonRuntimeCanceled)
}

func TestWorkflowRuntimeStarterCancelTaskRunsStopsLiveRuntimeAfterTaskCancel(t *testing.T) {
	client := newBlockingClient()
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer("{}"))
	fixture.clientFactory = func(SchedulerStartRunRequest) llm.Client { return client }
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	client.waitForCall(t)
	if err := fixture.store.CancelTask(context.Background(), task.ID, "test cancel"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := fixture.starter.CancelTaskRuns(context.Background(), task.ID); err != nil {
		t.Fatalf("CancelTaskRuns: %v", err)
	}
	if !client.returned() {
		t.Fatal("CancelTaskRuns returned before live runtime stopped")
	}
}

func TestWorkflowRuntimeStarterCancelRunStopsLiveRuntime(t *testing.T) {
	client := newBlockingClient()
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer("{}"))
	fixture.clientFactory = func(SchedulerStartRunRequest) llm.Client { return client }
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	client.waitForCall(t)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one live run", runs)
	}
	if err := fixture.starter.CancelRun(context.Background(), runs[0].ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if !client.returned() {
		t.Fatal("CancelRun returned before live runtime stopped")
	}
}

func TestWorkflowRuntimeStarterRequestCancelRunDoesNotWaitForRuntimeStop(t *testing.T) {
	client := newDrainingBlockingClient()
	defer client.releaseReturn()
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer("{}"))
	fixture.clientFactory = func(SchedulerStartRunRequest) llm.Client { return client }
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	client.waitForCall(t)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one live run", runs)
	}
	if !fixture.starter.RequestCancelRun(runs[0].ID) {
		t.Fatalf("RequestCancelRun returned false for live run %s", runs[0].ID)
	}
	client.waitForCancel(t)
	if client.returned() {
		t.Fatal("RequestCancelRun waited for live runtime to stop")
	}
	client.releaseReturn()
	client.waitForReturn(t)
	fixture.waitForInterruptedRun(t, scheduler, task.ID, ReasonRuntimeCanceled)
}

func TestStarterAutoPersistsShellCommandForContinuationWorkflow(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeAuto)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	claimed, input, plan := fixture.claimPlannedRun(t)

	mode, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
	if err != nil {
		t.Fatalf("resolveAndPersistWorkflowCompletionMode: %v", err)
	}
	if mode != workflowruntime.CompletionModeShellCommand {
		t.Fatalf("mode = %q, want shell_command", mode)
	}
	runs, err := fixture.store.ListRuns(context.Background(), input.Task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].EffectiveCompletionMode != string(workflowruntime.CompletionModeShellCommand) {
		t.Fatalf("stored mode = %+v, want shell_command", runs)
	}
}

func TestStarterAutoUsesRunStartSnapshotForContinuationDetection(t *testing.T) {
	tests := []struct {
		name         string
		snapshotMode workflow.ContextMode
		liveMode     workflow.ContextMode
		wantFlag     bool
		wantMode     workflowruntime.CompletionMode
	}{
		{name: "snapshot keeps continue after live edit removes it", snapshotMode: workflow.ContextModeContinueSession, liveMode: workflow.ContextModeNewSession, wantFlag: true, wantMode: workflowruntime.CompletionModeShellCommand},
		{name: "snapshot keeps non-continue after live edit adds it", snapshotMode: workflow.ContextModeNewSession, liveMode: workflow.ContextModeContinueSession, wantFlag: false, wantMode: workflowruntime.CompletionModeStructuredOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStarterFixture(t, config.WorkflowCompletionModeAuto)
			workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, tt.snapshotMode, "coder")
			if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
				t.Fatalf("LinkWorkflow chained: %v", err)
			}
			claimed, _, plan := fixture.claimPlannedRun(t)
			updateChainedStarterWorkflowNextEdgeContextMode(t, fixture.metadata, workflowID, tt.liveMode)
			input, err := fixture.store.GetRunStartContext(context.Background(), claimed.ID)
			if err != nil {
				t.Fatalf("GetRunStartContext: %v", err)
			}
			if input.WorkflowHasContinueSessionEdge != tt.wantFlag {
				t.Fatalf("snapshot continuation flag = %v, want %v", input.WorkflowHasContinueSessionEdge, tt.wantFlag)
			}
			mode, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
			if err != nil {
				t.Fatalf("resolveAndPersistWorkflowCompletionMode: %v", err)
			}
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
		})
	}
}

func TestStarterAutoPersistsUnstructuredWhenShellUnavailable(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeAuto)
	disableCoderShell(t, &fixture)
	fixture.rebuildStarter(t)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	claimed, input, plan := fixture.claimPlannedRun(t)

	mode, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
	if err != nil {
		t.Fatalf("resolveAndPersistWorkflowCompletionMode: %v", err)
	}
	if mode != workflowruntime.CompletionModeUnstructuredOutput {
		t.Fatalf("mode = %q, want unstructured_output", mode)
	}
	runs, err := fixture.store.ListRuns(context.Background(), input.Task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].EffectiveCompletionMode != string(workflowruntime.CompletionModeUnstructuredOutput) {
		t.Fatalf("stored mode = %+v, want unstructured_output", runs)
	}
}

func TestStarterExplicitShellModeFailsWhenShellUnavailable(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeShellCommand)
	disableCoderShell(t, &fixture)
	fixture.rebuildStarter(t)
	claimed, input, plan := fixture.claimPlannedRun(t)

	_, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
	if err == nil {
		t.Fatal("expected shell unavailable error")
	}
	runs, listErr := fixture.store.ListRuns(context.Background(), input.Task.ID)
	if listErr != nil {
		t.Fatalf("ListRuns: %v", listErr)
	}
	if len(runs) != 1 || runs[0].EffectiveCompletionMode != "" {
		t.Fatalf("stored mode after failed explicit shell = %+v, want empty", runs)
	}
}

func TestStarterSkipsProviderCapabilityProbeWhenModeDoesNotNeedIt(t *testing.T) {
	tests := []struct {
		name               string
		configuredMode     config.WorkflowCompletionMode
		hasContinueEdge    bool
		shellAvailable     bool
		wantCompletionMode workflowruntime.CompletionMode
	}{
		{name: "forced tool", configuredMode: config.WorkflowCompletionModeTool, shellAvailable: true, wantCompletionMode: workflowruntime.CompletionModeTool},
		{name: "forced unstructured", configuredMode: config.WorkflowCompletionModeUnstructured, shellAvailable: true, wantCompletionMode: workflowruntime.CompletionModeUnstructuredOutput},
		{name: "auto shell unavailable", configuredMode: config.WorkflowCompletionModeAuto, shellAvailable: false, wantCompletionMode: workflowruntime.CompletionModeUnstructuredOutput},
		{name: "auto continuation shell", configuredMode: config.WorkflowCompletionModeAuto, hasContinueEdge: true, shellAvailable: true, wantCompletionMode: workflowruntime.CompletionModeShellCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStarterFixture(t, tt.configuredMode)
			if !tt.shellAvailable {
				disableCoderShell(t, &fixture)
				fixture.rebuildStarter(t)
			}
			claimed, input, plan := fixture.claimPlannedRun(t)
			input.WorkflowHasContinueSessionEdge = tt.hasContinueEdge
			client := providerProbeForbiddenClient{}

			mode, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, client)
			if err != nil {
				t.Fatalf("resolveAndPersistWorkflowCompletionMode: %v", err)
			}
			if mode != tt.wantCompletionMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantCompletionMode)
			}
		})
	}
}

func TestStarterReusesPersistedEffectiveCompletionMode(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput)
	claimed, input, plan := fixture.claimPlannedRun(t)
	if err := fixture.store.SetRunEffectiveCompletionMode(context.Background(), claimed.ID, claimed.Generation, string(workflowruntime.CompletionModeTool)); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode: %v", err)
	}
	input, err := fixture.store.GetRunStartContext(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext after set mode: %v", err)
	}

	mode, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
	if err != nil {
		t.Fatalf("resolveAndPersistWorkflowCompletionMode: %v", err)
	}
	if mode != workflowruntime.CompletionModeTool {
		t.Fatalf("mode = %q, want persisted tool", mode)
	}
}

func TestStarterNodeCompletionModeOverridesGlobalConfig(t *testing.T) {
	tests := []struct {
		name       string
		globalMode config.WorkflowCompletionMode
		nodeMode   string
		wantMode   workflowruntime.CompletionMode
	}{
		{name: "explicit tool overrides global structured", globalMode: config.WorkflowCompletionModeStructuredOutput, nodeMode: "tool", wantMode: workflowruntime.CompletionModeTool},
		{name: "explicit auto overrides global tool", globalMode: config.WorkflowCompletionModeTool, nodeMode: "auto", wantMode: workflowruntime.CompletionModeStructuredOutput},
		{name: "empty inherits global tool", globalMode: config.WorkflowCompletionModeTool, nodeMode: "", wantMode: workflowruntime.CompletionModeTool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStarterFixture(t, tt.globalMode)
			claimed, input, plan := fixture.claimPlannedRun(t)
			input.Node.CompletionMode = tt.nodeMode

			mode, _, err := fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
			if err != nil {
				t.Fatalf("resolveAndPersistWorkflowCompletionMode: %v", err)
			}
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
		})
	}
}

func TestStarterRechecksShellAvailabilityForPersistedShellMode(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeAuto)
	disableCoderShell(t, &fixture)
	fixture.rebuildStarter(t)
	claimed, input, plan := fixture.claimPlannedRun(t)
	if err := fixture.store.SetRunEffectiveCompletionMode(context.Background(), claimed.ID, claimed.Generation, string(workflowruntime.CompletionModeShellCommand)); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode: %v", err)
	}
	input, err := fixture.store.GetRunStartContext(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext after set mode: %v", err)
	}

	_, _, err = fixture.starter.resolveAndPersistWorkflowCompletionMode(context.Background(), SchedulerStartRunRequest{RunID: claimed.ID, Generation: claimed.Generation}, input, plan, NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}))
	if err == nil || !errors.Is(err, errWorkflowShellCompletionRequiresShell) {
		t.Fatalf("resolveAndPersistWorkflowCompletionMode error = %v, want shell availability failure", err)
	}
}

func TestStarterStartWorkflowRunPersistsEffectiveCompletionModeBeforeModelRequest(t *testing.T) {
	client := newBlockingClient()
	fixture := newStarterFixture(t, config.WorkflowCompletionModeAuto)
	fixture.clientFactory = func(SchedulerStartRunRequest) llm.Client { return client }
	fixture.rebuildStarter(t)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	client.waitForCall(t)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].EffectiveCompletionMode != string(workflowruntime.CompletionModeShellCommand) {
		t.Fatalf("stored mode = %+v, want shell_command", runs)
	}
	if err := fixture.starter.Close(); err != nil {
		t.Fatalf("starter.Close: %v", err)
	}
}

func TestStarterStartWorkflowRunFailsExplicitShellModeWithoutShell(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeShellCommand)
	disableCoderShell(t, &fixture)
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err == nil {
		t.Fatal("expected scheduler start error")
	}
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InterruptedAt == 0 || runs[0].EffectiveCompletionMode != "" || runs[0].InterruptionReason != ReasonSchedulerRuntimeStartFailed {
		t.Fatalf("run after explicit shell failure = %+v, want interrupted without stored mode", runs)
	}
}

func TestStarterRestoresReusedSessionMetadataWhenSetupFailsAfterPlanning(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer(`{"commentary":"first comments","prior_summary":"first summary"}`))
	disableCoderShell(t, &fixture)
	fixture.rebuildStarter(t)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)
	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	fixture.waitForCompletedRunCount(t, task.ID, 1)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns after first run: %v", err)
	}
	if len(runs) != 2 || strings.TrimSpace(runs[0].SessionID) == "" || strings.TrimSpace(runs[1].SessionID) != "" {
		t.Fatalf("runs after first run = %+v, want source session and unattached continuation", runs)
	}
	sourceSessionID := runs[0].SessionID
	metaBefore := fixture.sessionMeta(t, sourceSessionID)

	fixture.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeShellCommand
	fixture.rebuildStarter(t)
	if err := fixture.scheduler(t).Process(context.Background()); err == nil {
		t.Fatalf("expected scheduler start error")
	}
	runs, err = fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns after failed continuation: %v", err)
	}
	if len(runs) != 2 || runs[1].InterruptedAt == 0 || strings.TrimSpace(runs[1].SessionID) != "" {
		t.Fatalf("runs after failed continuation = %+v, want interrupted unattached continuation", runs)
	}
	metaAfter := fixture.sessionMeta(t, sourceSessionID)
	if metaAfter.Name != metaBefore.Name || metaAfter.FirstPromptPreview != metaBefore.FirstPromptPreview {
		t.Fatalf("source session metadata after failed setup = name %q preview %q, want restored name %q preview %q", metaAfter.Name, metaAfter.FirstPromptPreview, metaBefore.Name, metaBefore.FirstPromptPreview)
	}
}

func TestSchedulerRunsNextAgentWithBoundInputsAndTaskWorktreeContext(t *testing.T) {
	fixture := newChainedStarterFixture(t)
	workflowID := createChainedStarterWorkflow(t, fixture.store)
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	fixture.waitForAllRunsCompleted(t, task.ID, 2)

	reqs := fixture.client.Requests()
	if len(reqs) < 2 {
		t.Fatalf("fake model request count = %d, want 2", len(reqs))
	}
	assertPromptContains(t, reqs[1], []string{
		"Use Run workflow and first summary.",
	})
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	worktreeRoot := fixture.assertRunSessionUsesTaskWorktree(t, runs[1].SessionID)
	if strings.TrimSpace(runs[0].SessionID) == "" || strings.TrimSpace(runs[1].SessionID) == "" || runs[0].SessionID == runs[1].SessionID {
		t.Fatalf("runs = %+v, want new_session edge to create separate target session", runs)
	}
	assertPromptContains(t, reqs[1], []string{"\nCWD: " + worktreeRoot + "\n"})
}

func TestBuildWorkflowTaskInstructionsRendersTransitionParameters(t *testing.T) {
	instructions, err := BuildWorkflowTaskInstructions(workflowstore.RunStartContext{
		Task: workflowstore.TaskRecord{
			ID:         "task-1",
			WorkflowID: "workflow-1",
			ShortID:    "RUN-1",
			Title:      "Task title",
			Body:       "Task body",
		},
		Workflow: workflowstore.WorkflowRecord{ID: "workflow-1"},
		Node: workflowstore.NodeRecord{
			ID:          "node-review",
			Key:         "review",
			DisplayName: "Review",
		},
		PromptTemplate:       "Use {{.Params.direct}} and {{.Params.plan.summary}}.",
		ParameterValues:      map[string]string{"direct": "direct parameter"},
		PriorParameterValues: map[string]map[string]string{"plan": {"summary": "plan parameter"}},
	})
	if err != nil {
		t.Fatalf("BuildWorkflowTaskInstructions: %v", err)
	}
	if instructions.NodePrompt != "Use direct parameter and plan parameter." {
		t.Fatalf("node prompt = %q", instructions.NodePrompt)
	}
}

func TestWorkflowRuntimeContinueSessionReusesSourceRunSession(t *testing.T) {
	fixture := newChainedStarterFixture(t)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	fixture.waitForAllRunsCompleted(t, task.ID, 2)

	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 || strings.TrimSpace(runs[0].SessionID) == "" || runs[0].SessionID != runs[1].SessionID {
		t.Fatalf("runs = %+v, want same session reused across continue_session edge", runs)
	}
	meta := fixture.sessionMeta(t, runs[1].SessionID)
	if meta.Name != "RUN-1: Plan -> Implement" {
		t.Fatalf("continued session name = %q, want latest accepted transition name", meta.Name)
	}
	input, err := fixture.store.GetRunStartContext(context.Background(), runs[1].ID)
	if err != nil {
		t.Fatalf("GetRunStartContext continued run: %v", err)
	}
	wantPreview, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		t.Fatalf("render continued prompt: %v", err)
	}
	if meta.FirstPromptPreview != wantPreview {
		t.Fatalf("continued session preview = %q, want %q", meta.FirstPromptPreview, wantPreview)
	}
	reqs := fixture.client.Requests()
	if len(reqs) < 2 {
		t.Fatalf("fake model request count = %d, want 2", len(reqs))
	}
	assertPromptContains(t, reqs[1], []string{"Use Run workflow and first summary."})
}

func TestWorkflowRuntimeContinueSessionKeepsLockedSetupAfterRoleConfigDrift(t *testing.T) {
	fixture := newChainedStarterFixture(t)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)
	firstScheduler := fixture.scheduler(t)

	if err := firstScheduler.Process(context.Background()); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns after first run: %v", err)
	}
	if len(runs) != 2 || runs[0].CompletedAt == 0 || runs[1].StartedAt != 0 {
		t.Fatalf("runs after first process = %+v, want completed source and unstarted target", runs)
	}
	role := fixture.cfg.Settings.Subagents["coder"]
	role.Settings.Model = "gpt-5.5-drifted"
	role.Sources["model"] = "drifted-test"
	fixture.cfg.Settings.Subagents["coder"] = role
	fixture.rebuildStarter(t)
	secondScheduler := fixture.scheduler(t)

	if err := secondScheduler.Process(context.Background()); err != nil {
		t.Fatalf("second Process: %v", err)
	}
	fixture.waitForAllRunsCompleted(t, task.ID, 2)
	reqs := fixture.client.Requests()
	if len(reqs) < 2 {
		t.Fatalf("fake model request count = %d, want 2", len(reqs))
	}
	if reqs[0].Model == "" || reqs[1].Model != reqs[0].Model || reqs[1].Model == "gpt-5.5-drifted" {
		t.Fatalf("request models = %q then %q, want locked source session model reused after config drift", reqs[0].Model, reqs[1].Model)
	}
}

func TestReusesExistingSession(t *testing.T) {
	mk := func(mode workflow.ContextMode, runSessionID string, fanout bool) workflowstore.RunStartContext {
		return workflowstore.RunStartContext{
			ContextMode:    mode,
			IsFanoutBranch: fanout,
			Run:            workflowstore.RunRecord{SessionID: runSessionID},
		}
	}
	cases := []struct {
		name string
		in   workflowstore.RunStartContext
		want bool
	}{
		{"new session is disposable", mk(workflow.ContextModeNewSession, "", false), false},
		{"continue reuses source", mk(workflow.ContextModeContinueSession, "", false), true},
		{"compact in-place reuses source", mk(workflow.ContextModeCompactAndContinueSession, "", false), true},
		{"compact fan-out clones a disposable copy", mk(workflow.ContextModeCompactAndContinueSession, "", true), false},
		{"resume reuses the run session", mk(workflow.ContextModeNewSession, "session-1", false), true},
	}
	for _, tc := range cases {
		if got := reusesExistingSession(tc.in); got != tc.want {
			t.Fatalf("%s: reusesExistingSession = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWorkflowRuntimeCompactAndContinueReusesSourceSessionWithRealCompaction(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput,
		ScriptedFinalAnswer(`{"commentary":"first comments","prior_summary":"first summary"}`),
		ScriptedFinalAnswer("compacted prior work summary"),
		ScriptedFinalAnswer(`{"commentary":"second done"}`),
	)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeCompactAndContinueSession, "coder")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)

	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("second Process: %v", err)
	}
	fixture.waitForAllRunsCompleted(t, task.ID, 2)

	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want 2", runs)
	}
	if runs[1].SessionID == "" || runs[1].SessionID != runs[0].SessionID {
		t.Fatalf("runs = %+v, want compact_and_continue_session to reuse the source session in place", runs)
	}
	meta := fixture.sessionMeta(t, runs[1].SessionID)
	if meta.Name != "RUN-1: Plan -> Implement" {
		t.Fatalf("compact continuation session name = %q, want latest accepted transition name", meta.Name)
	}
	input, err := fixture.store.GetRunStartContext(context.Background(), runs[1].ID)
	if err != nil {
		t.Fatalf("GetRunStartContext compact run: %v", err)
	}
	wantPreview, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		t.Fatalf("render compact prompt: %v", err)
	}
	if meta.FirstPromptPreview != wantPreview {
		t.Fatalf("compact continuation session preview = %q, want %q", meta.FirstPromptPreview, wantPreview)
	}
	events := fixture.sessionEventsText(t, runs[0].SessionID)
	if strings.Contains(events, "Workflow compacted continuation context.") {
		t.Fatalf("session events unexpectedly contain the removed compact-continuation stub: %q", events)
	}
	if !strings.Contains(events, "history_replaced") {
		t.Fatalf("session events missing real compaction (history_replaced): %q", events)
	}
	// plan turn + real compaction summary + node turn = at least three model
	// calls, proving compaction ran in place rather than fabricating a stub.
	if reqs := fixture.client.Requests(); len(reqs) < 3 {
		t.Fatalf("model request count = %d, want >=3 (plan, compaction, node turn)", len(reqs))
	}
	// The history_replaced event durably records the run that committed the
	// compaction, so resume reconstructs it and a resumed run (same ID) skips
	// recompaction while a fresh in-place handoff recompacts.
	if runID := fixture.historyReplacedWorkflowRunID(t, runs[1].SessionID); runID != string(runs[1].ID) {
		t.Fatalf("history_replaced workflow_run_id = %q, want run %q", runID, runs[1].ID)
	}
}

func TestWorkflowRuntimeCompactAndContinueAllowsCrossRole(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput,
		ScriptedFinalAnswer(`{"commentary":"first comments","prior_summary":"first summary"}`),
		ScriptedFinalAnswer(`{"commentary":"compaction summary"}`),
		ScriptedFinalAnswer(`{"commentary":"second done"}`),
	)
	workflowID := createChainedStarterWorkflowWithContextMode(t, fixture.store, workflow.ContextModeCompactAndContinueSession, "reviewer")
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow chained: %v", err)
	}
	task := fixture.createStartedTask(t)

	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("second Process: %v", err)
	}
	fixture.waitForCompletedRunCount(t, task.ID, 2)

	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 || runs[1].InterruptedAt != 0 || runs[1].CompletedAt == 0 || runs[0].SessionID != runs[1].SessionID {
		t.Fatalf("runs = %+v, want cross-role compact_and_continue to complete in source session", runs)
	}
	containerDir := filepath.Join(filepath.Join(fixture.cfg.PersistenceRoot, "projects"), fixture.projectID, "sessions")
	sourceDir, err := session.ResolveScopedSessionDir(containerDir, runs[1].SessionID)
	if err != nil {
		t.Fatalf("ResolveScopedSessionDir: %v", err)
	}
	sourceStore, err := session.Open(sourceDir, fixture.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("Open source session: %v", err)
	}
	if got := sourceStore.Meta().Continuation; got == nil || got.AgentRole != "reviewer" {
		t.Fatalf("continuation role = %+v, want reviewer", got)
	}
}

func TestWorkflowRuntimeFanoutCompactAndContinueClonesUseBranchTransitionMetadata(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput,
		ScriptedFinalAnswer(`{"commentary":"planned","summary":"plan summary"}`),
		ScriptedFinalAnswer("branch compacted"),
		ScriptedFinalAnswer(`{"commentary":"branch done","joined":"branch joined"}`),
		ScriptedFinalAnswer("branch compacted"),
		ScriptedFinalAnswer(`{"commentary":"branch done"}`),
	)
	workflowID := createFanoutCompactStarterWorkflow(t, fixture.store)
	if _, err := fixture.store.LinkWorkflow(context.Background(), fixture.projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow fanout: %v", err)
	}
	def, _, err := fixture.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition fanout: %v", err)
	}
	nodeKeyByID := map[workflow.NodeID]workflow.ModelKey{}
	for _, node := range def.Nodes {
		nodeKeyByID[workflow.NodeID(node.ID)] = node.Key
	}
	task := fixture.createStartedTask(t)

	ctx := context.Background()
	if err := fixture.scheduler(t).Process(ctx); err != nil {
		t.Fatalf("plan Process: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 3)
	fixture.waitForCompletedRunCount(t, task.ID, 1)

	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) < 3 {
		t.Fatalf("runs = %+v, want plan and two branch runs", runs)
	}
	sourceSessionID := ""
	branchRecords := map[string]workflowstore.RunRecord{}
	for _, run := range runs {
		switch nodeKeyByID[run.NodeID] {
		case "plan":
			sourceSessionID = run.SessionID
		case "impl_a", "impl_b":
			branchRecords[string(nodeKeyByID[run.NodeID])] = run
		}
	}
	if sourceSessionID == "" || branchRecords["impl_a"].ID == "" || branchRecords["impl_b"].ID == "" {
		t.Fatalf("run records by node: source=%q branches=%+v from runs %+v", sourceSessionID, branchRecords, runs)
	}

	startClaimedWorkflowRun(t, ctx, fixture, branchRecords["impl_a"])
	fixture.waitForCompletedRunCount(t, task.ID, 2)
	startClaimedWorkflowRun(t, ctx, fixture, branchRecords["impl_b"])
	fixture.waitForCompletedRunCount(t, task.ID, 3)

	runs, err = fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns after branches: %v", err)
	}
	branchSessions := map[string]string{}
	for _, run := range runs {
		if key := string(nodeKeyByID[run.NodeID]); key == "impl_a" || key == "impl_b" {
			branchSessions[key] = run.SessionID
		}
	}
	if branchSessions["impl_a"] == sourceSessionID || branchSessions["impl_b"] == sourceSessionID || branchSessions["impl_a"] == branchSessions["impl_b"] {
		t.Fatalf("fanout branch sessions = %+v source=%q, want isolated clones", branchSessions, sourceSessionID)
	}
	metaA := fixture.sessionMeta(t, branchSessions["impl_a"])
	wantPreviewA := renderedPromptForRun(t, fixture.store, branchRecords["impl_a"].ID)
	if metaA.Name != "RUN-1: Plan -> Implement A" || metaA.FirstPromptPreview != wantPreviewA {
		t.Fatalf("branch A metadata = name %q preview %q, want branch transition metadata", metaA.Name, metaA.FirstPromptPreview)
	}
	metaB := fixture.sessionMeta(t, branchSessions["impl_b"])
	wantPreviewB := renderedPromptForRun(t, fixture.store, branchRecords["impl_b"].ID)
	if metaB.Name != "RUN-1: Plan -> Implement B" || metaB.FirstPromptPreview != wantPreviewB {
		t.Fatalf("branch B metadata = name %q preview %q, want branch transition metadata", metaB.Name, metaB.FirstPromptPreview)
	}
}

func TestWorkflowRuntimeDefaultRoleClearsInvalidPersistedRoleBeforeValidation(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer("{}"))
	roleSettings := fixture.cfg.Settings
	roleSettings.Model = "gpt-5.3-codex-spark"
	roleSettings.ContextCompactionThresholdTokens = 200_000
	roleSettings.Subagents = nil
	fixture.cfg.Settings.Subagents["worker"] = config.SubagentRole{
		Settings: roleSettings,
		Sources:  map[string]string{"model": "test", "context_compaction_threshold_tokens": "test"},
	}
	fixture.rebuildStarter(t)
	containerDir := filepath.Join(filepath.Join(fixture.cfg.PersistenceRoot, "projects"), fixture.projectID, "sessions")
	source, err := session.Create(containerDir, filepath.Base(containerDir), fixture.cfg.WorkspaceRoot, fixture.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("create source session: %v", err)
	}
	if err := source.SetContinuationContext(session.ContinuationContext{AgentRole: "worker"}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}

	plan, _, err := fixture.starter.planSession(context.Background(), workflowstore.RunStartContext{
		ContextMode:     workflow.ContextModeContinueSession,
		SourceSessionID: source.Meta().SessionID,
		Task: workflowstore.TaskRecord{
			ID:        "task-1",
			ProjectID: fixture.projectID,
			ShortID:   "RUN-1",
			Title:     "Task title",
		},
		Workflow:       workflowstore.WorkflowRecord{ID: "workflow-1"},
		Node:           workflowstore.NodeRecord{ID: "node-1", Key: "default", SubagentRole: workflow.DefaultAgentRole},
		PromptTemplate: "Continue.",
	})
	if err != nil {
		t.Fatalf("planSession: %v", err)
	}
	if plan.ActiveSettings.Model != fixture.cfg.Settings.Model {
		t.Fatalf("model = %q, want base model %q", plan.ActiveSettings.Model, fixture.cfg.Settings.Model)
	}
	reopened, err := session.Open(source.Dir(), fixture.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("open source session: %v", err)
	}
	if got := reopened.Meta().Continuation; got != nil && got.AgentRole != "" {
		t.Fatalf("continuation = %+v, want cleared role", got)
	}
}

func TestWorkflowRuntimeStartFailsWhenRoleDisappearedAfterTaskStart(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer("{}"))
	delete(fixture.cfg.Settings.Subagents, "coder")
	fixture.rebuildStarter(t)
	task := fixture.createStartedTask(t)
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); err == nil {
		t.Fatalf("expected scheduler start error")
	}
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != ReasonSchedulerRuntimeStartFailed {
		t.Fatalf("run after missing role = %+v", runs)
	}
	var detail string
	_ = fixture.metadata.DB().QueryRowContext(context.Background(), `SELECT interruption_detail_json FROM task_runs WHERE id = ?`, string(runs[0].ID)).Scan(&detail)
	if !strings.Contains(detail, string(workflow.CodeAgentRoleMissing)) {
		t.Fatalf("interruption detail = %s, want %s", detail, workflow.CodeAgentRoleMissing)
	}
}

func TestWorkflowRuntimeStartFailsWhenTransitionPromptPreviewCannotRender(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer(`{"commentary":"should not run"}`))
	task := fixture.createStartedTask(t)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns initial: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("initial runs = %+v, want one run", runs)
	}
	if _, err := fixture.metadata.DB().ExecContext(context.Background(), `UPDATE task_runs SET metadata_json = json_set(metadata_json, '$.prompt_template', ?) WHERE id = ?`, "{{.Missing}}", string(runs[0].ID)); err != nil {
		t.Fatalf("update run prompt metadata: %v", err)
	}
	scheduler := fixture.scheduler(t)

	if err := scheduler.Process(context.Background()); !errors.Is(err, ErrSchedulerRuntimeStartFailed) {
		t.Fatalf("Process error = %v, want scheduler runtime start failure", err)
	}
	runs, err = fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != ReasonSchedulerRuntimeStartFailed || strings.TrimSpace(runs[0].SessionID) != "" {
		t.Fatalf("run after prompt render failure = %+v, want interrupted without attached session", runs)
	}
	if reqs := fixture.client.Requests(); len(reqs) != 0 {
		t.Fatalf("model requests = %+v, want none before invalid prompt render failure", reqs)
	}
}

func TestWorkflowRuntimeResumeInterruptedRunUsesSameSession(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer(`{"commentary":"resumed"}`))
	task := fixture.createStartedTask(t)
	if err := fixture.worktrees.EnsureTaskWorktree(context.Background(), string(task.ID)); err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
	}
	def, _, err := fixture.store.GetDefinition(context.Background(), fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	startEdgePrompt := starterEdgeByKey(t, def, "start").PromptTemplate
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns initial: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("initial runs = %+v", runs)
	}
	claimed, err := fixture.store.ClaimRun(context.Background(), runs[0].ID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	input, err := fixture.store.GetRunStartContext(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	plan, _, err := fixture.starter.planSession(context.Background(), input)
	if err != nil {
		t.Fatalf("planSession initial: %v", err)
	}
	if _, err := fixture.metadata.ResolvePersistedSession(context.Background(), plan.Store.Meta().SessionID); err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if err := fixture.store.AttachRunSession(context.Background(), claimed.ID, claimed.Generation, plan.Store.Meta().SessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := fixture.store.InterruptRunGeneration(context.Background(), claimed.ID, claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	runs, err = fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns interrupted: %v", err)
	}
	if len(runs) != 1 || runs[0].InterruptedAt == 0 || strings.TrimSpace(runs[0].SessionID) == "" {
		t.Fatalf("interrupted run session = %+v", runs)
	}
	originalSessionID := runs[0].SessionID
	resumed, err := fixture.store.ResumeTaskRun(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRun: %v", err)
	}
	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("Process resumed: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)
	runs, err = fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns resumed: %v", err)
	}
	if len(runs) != 1 || resumed.ID != runs[0].ID || runs[0].SessionID != originalSessionID {
		t.Fatalf("resumed run = %+v, want same session %s for run %s", runs, originalSessionID, resumed.ID)
	}
	meta := fixture.sessionMeta(t, runs[0].SessionID)
	if meta.Name != "RUN-1: Backlog -> Agent" || meta.FirstPromptPreview != startEdgePrompt {
		t.Fatalf("resumed session metadata = name %q preview %q, want accepted transition metadata", meta.Name, meta.FirstPromptPreview)
	}
}

func TestRemoveFanoutCloneDeletesOrphanedClone(t *testing.T) {
	fixture := newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput, ScriptedFinalAnswer(`{"commentary":"done"}`))
	task := fixture.createStartedTask(t)
	if err := fixture.scheduler(t).Process(context.Background()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	fixture.waitForCompletedRun(t, task.ID)
	runs, err := fixture.store.ListRuns(context.Background(), task.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %+v, err %v", runs, err)
	}

	containerDir := filepath.Join(filepath.Join(fixture.cfg.PersistenceRoot, "projects"), fixture.projectID, "sessions")
	cloneID, err := fixture.starter.cloneSourceSessionForFanout(containerDir, runs[0].SessionID)
	if err != nil {
		t.Fatalf("cloneSourceSessionForFanout: %v", err)
	}
	cloneDir, err := session.ResolveScopedSessionDir(containerDir, cloneID)
	if err != nil {
		t.Fatalf("ResolveScopedSessionDir: %v", err)
	}
	if _, err := os.Stat(cloneDir); err != nil {
		t.Fatalf("clone dir should exist after clone: %v", err)
	}

	fixture.starter.removeFanoutClone(context.Background(), containerDir, cloneID)
	if _, err := os.Stat(cloneDir); !os.IsNotExist(err) {
		t.Fatalf("clone dir should be removed, stat err = %v", err)
	}
}

type starterFixture struct {
	cfg      config.App
	metadata *metadata.Store
	store    *workflowstore.Store
	view     interface {
		GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error)
	}
	worktrees     *metadataTaskWorktrees
	client        *ScriptedClient
	clientFactory func(SchedulerStartRunRequest) llm.Client
	runtimes      starterRuntimeRegistry
	starter       *Starter
	workflowID    workflow.WorkflowID
	projectID     string
}

type starterRuntimeRegistry interface {
	RuntimeEventRegistry
	ListPendingPrompts(sessionID string) []registry.PendingPromptSnapshot
	SubmitPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) error
}

func newStarterFixture(t *testing.T, mode config.WorkflowCompletionMode, steps ...ScriptedRuntimeStep) starterFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Settings.Workflow.CompletionMode = mode
	cfg.Settings.Reviewer.Frequency = "off"
	cfg.Settings.Subagents["coder"] = config.SubagentRole{Description: "Coder", Settings: config.Settings{Model: "gpt-5.4-mini"}, Sources: map[string]string{"model": "test"}}
	cfg.Settings.Subagents["reviewer"] = config.SubagentRole{Description: "Reviewer", Settings: config.Settings{Model: "gpt-5.4-reviewer"}, Sources: map[string]string{"model": "test"}}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "RUN"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(workflow.StaticRoleResolver{"coder": true, "reviewer": true}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	view, err := workflowview.New(metadataStore)
	if err != nil {
		t.Fatalf("workflowview.New: %v", err)
	}
	worktrees := &metadataTaskWorktrees{t: t, metadata: metadataStore, workspaceID: binding.WorkspaceID, root: filepath.Join(home, "task-worktrees")}
	client := NewScriptedClient(llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: mode == config.WorkflowCompletionModeStructuredOutput}, steps...)
	clientFactory := func(SchedulerStartRunRequest) llm.Client { return client }
	runtimes := registry.NewRuntimeRegistry()
	starter, err := NewStarter(cfg, metadataStore, store, nil, nil, nil, runtimes, StarterOptions{
		ClientFactory: clientFactory,
		Worktrees:     worktrees,
	})
	if err != nil {
		t.Fatalf("NewStarter: %v", err)
	}
	t.Cleanup(func() { _ = starter.Close() })
	workflowID := createStarterWorkflow(t, store)
	if _, err := store.LinkWorkflow(context.Background(), binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	return starterFixture{cfg: cfg, metadata: metadataStore, store: store, view: view, worktrees: worktrees, client: client, clientFactory: clientFactory, runtimes: runtimes, starter: starter, workflowID: workflowID, projectID: binding.ProjectID}
}

func newChainedStarterFixture(t *testing.T) starterFixture {
	t.Helper()
	return newStarterFixture(t, config.WorkflowCompletionModeStructuredOutput,
		ScriptedFinalAnswer(`{"commentary":"first comments","prior_summary":"first summary"}`),
		ScriptedFinalAnswer(`{"commentary":"second done"}`),
	)
}

func (f *starterFixture) rebuildStarter(t *testing.T) {
	t.Helper()
	if f.starter != nil {
		_ = f.starter.Close()
	}
	if f.runtimes == nil {
		f.runtimes = registry.NewRuntimeRegistry()
	}
	starter, err := NewStarter(f.cfg, f.metadata, f.store, nil, nil, nil, f.runtimes, StarterOptions{
		ClientFactory: f.clientFactory,
		Worktrees:     f.worktrees,
	})
	if err != nil {
		t.Fatalf("NewStarter: %v", err)
	}
	f.starter = starter
	t.Cleanup(func() { _ = starter.Close() })
}

func (f starterFixture) scheduler(t *testing.T) *SchedulerService {
	t.Helper()
	scheduler, err := NewSchedulerService(f.store, f.starter, SchedulerConfig{Concurrency: 1})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	f.starter.SetRuntimeFinished(scheduler.RuntimeFinished)
	t.Cleanup(func() { _ = scheduler.Close() })
	return scheduler
}

func (f starterFixture) createStartedTask(t *testing.T) workflowstore.TaskRecord {
	t.Helper()
	task, err := f.store.CreateTask(context.Background(), workflowstore.CreateTaskRequest{ProjectID: f.projectID, Title: "Run workflow", Body: "Body for workflow"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := f.store.StartTask(context.Background(), task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	return task
}

func (f starterFixture) claimPlannedRun(t *testing.T) (workflowstore.RunnableRunRecord, workflowstore.RunStartContext, launch.SessionPlan) {
	t.Helper()
	task := f.createStartedTask(t)
	if err := f.worktrees.EnsureTaskWorktree(context.Background(), string(task.ID)); err != nil {
		t.Fatalf("EnsureTaskWorktree: %v", err)
	}
	runs, err := f.store.ListRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one runnable run", runs)
	}
	claimed, err := f.store.ClaimRun(context.Background(), runs[0].ID, runs[0].Generation)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	input, err := f.store.GetRunStartContext(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	plan, _, err := f.starter.planSession(context.Background(), input)
	if err != nil {
		t.Fatalf("planSession: %v", err)
	}
	return claimed, input, plan
}

func disableCoderShell(t *testing.T, fixture *starterFixture) {
	t.Helper()
	role := fixture.cfg.Settings.Subagents["coder"]
	role.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: false}
	if role.Sources == nil {
		role.Sources = map[string]string{}
	}
	role.Sources["tools."+toolspec.ConfigName(toolspec.ToolExecCommand)] = "test"
	fixture.cfg.Settings.Subagents["coder"] = role
}

func (f starterFixture) waitForCompletedRun(t *testing.T, taskID workflow.TaskID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) == 1 && runs[0].CompletedAt != 0 {
			return
		}
		if len(runs) == 1 && runs[0].InterruptedAt != 0 {
			var detail string
			_ = f.metadata.DB().QueryRowContext(context.Background(), `SELECT interruption_detail_json FROM task_runs WHERE id = ?`, string(runs[0].ID)).Scan(&detail)
			t.Fatalf("run interrupted: %+v detail=%s", runs[0], detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for workflow run completion")
}

func (f starterFixture) waitForCompletedRunCount(t *testing.T, taskID workflow.TaskID, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		completed := 0
		for _, run := range runs {
			if run.InterruptedAt != 0 {
				var detail string
				_ = f.metadata.DB().QueryRowContext(context.Background(), `SELECT interruption_detail_json FROM task_runs WHERE id = ?`, string(run.ID)).Scan(&detail)
				t.Fatalf("run interrupted: %+v detail=%s", run, detail)
			}
			if run.CompletedAt != 0 {
				completed++
			}
		}
		if completed == count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d completed workflow runs", count)
}

func (f starterFixture) waitForWaitingAsk(t *testing.T, taskID workflow.TaskID, askID string) workflowstore.RunRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) == 1 && runs[0].WaitingAskID == askID {
			if strings.TrimSpace(runs[0].SessionID) == "" {
				t.Fatalf("waiting run has no session: %+v", runs[0])
			}
			if runs[0].CompletedAt != 0 || runs[0].InterruptedAt != 0 {
				t.Fatalf("waiting run has terminal outcome: %+v", runs[0])
			}
			return runs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for workflow run ask %s", askID)
	return workflowstore.RunRecord{}
}

func (f starterFixture) waitForRunCount(t *testing.T, taskID workflow.TaskID, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) == count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d workflow runs", count)
}

func (f starterFixture) waitForAllRunsCompleted(t *testing.T, taskID workflow.TaskID, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		completed := 0
		for _, run := range runs {
			if run.InterruptedAt != 0 {
				t.Fatalf("run interrupted: %+v", run)
			}
			if run.CompletedAt != 0 {
				completed++
			}
		}
		if len(runs) == count && completed == count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d completed workflow runs", count)
}

func (f starterFixture) waitForInterruptedRun(t *testing.T, scheduler *SchedulerService, taskID workflow.TaskID, reason string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := f.store.ListRuns(context.Background(), taskID)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) == 1 && runs[0].InterruptedAt != 0 {
			if runs[0].InterruptionReason != reason {
				t.Fatalf("interruption reason = %q, want %q", runs[0].InterruptionReason, reason)
			}
			f.waitForActiveCountZero(t, scheduler)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for workflow run interruption")
}

func (f starterFixture) waitForActiveCountZero(t *testing.T, scheduler *SchedulerService) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if scheduler.ActiveCount() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("scheduler active count = %d, want 0 after runtime finish", scheduler.ActiveCount())
}

func (f starterFixture) assertRunSessionUsesTaskWorktree(t *testing.T, sessionID string) string {
	t.Helper()
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if got, want := filepath.Dir(record.SessionDir), filepath.Join(filepath.Join(f.cfg.PersistenceRoot, "projects"), f.projectID, "sessions"); got != want {
		t.Fatalf("session dir parent = %q, want project sessions root %q", got, want)
	}
	target, err := f.metadata.ResolveSessionExecutionTarget(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if strings.TrimSpace(target.WorktreeID) == "" || !strings.HasSuffix(target.EffectiveWorkdir, string(filepath.Separator)+"RUN-1") {
		t.Fatalf("session target = %+v, want task worktree", target)
	}
	return target.EffectiveWorkdir
}

func (f starterFixture) sessionEventsText(t *testing.T, sessionID string) string {
	t.Helper()
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(record.SessionDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	return string(data)
}

func (f starterFixture) sessionMeta(t *testing.T, sessionID string) session.Meta {
	t.Helper()
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	store, err := session.Open(record.SessionDir, f.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	return store.Meta()
}

func renderedPromptForRun(t *testing.T, store *workflowstore.Store, runID workflow.RunID) string {
	t.Helper()
	input, err := store.GetRunStartContext(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	prompt, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		t.Fatalf("render transition prompt: %v", err)
	}
	return prompt
}

func startClaimedWorkflowRun(t *testing.T, ctx context.Context, fixture starterFixture, run workflowstore.RunRecord) {
	t.Helper()
	claimed, err := fixture.store.ClaimRun(ctx, run.ID, run.Generation)
	if err != nil {
		t.Fatalf("ClaimRun %s: %v", run.ID, err)
	}
	req := SchedulerStartRunRequest{
		RunID:       claimed.ID,
		TaskID:      claimed.TaskID,
		PlacementID: claimed.PlacementID,
		NodeID:      claimed.NodeID,
		Generation:  claimed.Generation,
	}
	if err := fixture.starter.StartWorkflowRun(ctx, req); err != nil {
		t.Fatalf("StartWorkflowRun %s: %v", run.ID, err)
	}
}

func workflowRequestAskQuestionToolMessages(reqs []llm.Request) []llm.Message {
	messages := []llm.Message{}
	for _, req := range reqs {
		for _, msg := range llm.MessagesFromItems(req.Items) {
			if msg.Role != llm.RoleTool || msg.Name != string(toolspec.ToolAskQuestion) {
				continue
			}
			switch msg.ToolCallID {
			case "call-ask-1", "call-ask-2":
				messages = append(messages, msg)
			}
		}
	}
	return messages
}

// historyReplacedWorkflowRunID decodes the latest history_replaced event in the
// session and returns its recorded workflow run provenance.
func (f starterFixture) historyReplacedWorkflowRunID(t *testing.T, sessionID string) string {
	t.Helper()
	runID := ""
	for _, line := range strings.Split(strings.TrimSpace(f.sessionEventsText(t, sessionID)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event struct {
			Kind    string `json:"kind"`
			Payload struct {
				WorkflowRunID string `json:"workflow_run_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode session event: %v", err)
		}
		if event.Kind == "history_replaced" {
			runID = event.Payload.WorkflowRunID
		}
	}
	return runID
}

type metadataTaskWorktrees struct {
	t           *testing.T
	metadata    *metadata.Store
	workspaceID string
	root        string
}

func (e *metadataTaskWorktrees) EnsureTaskWorktree(ctx context.Context, taskID string) error {
	if e == nil || e.metadata == nil {
		return nil
	}
	var shortID string
	if err := e.metadata.DB().QueryRowContext(ctx, `SELECT short_id FROM tasks WHERE id = ?`, taskID).Scan(&shortID); err != nil {
		return err
	}
	worktreeID := "worktree-" + taskID
	worktreeRoot := filepath.Join(e.root, shortID)
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return err
	}
	if err := e.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              worktreeID,
		WorkspaceID:     e.workspaceID,
		CanonicalRoot:   worktreeRoot,
		DisplayName:     shortID,
		Availability:    "available",
		Managed:         true,
		CreatedBranch:   true,
		GitMetadataJSON: `{}`,
	}); err != nil {
		return err
	}
	result, err := e.metadata.DB().ExecContext(ctx, `UPDATE tasks SET managed_worktree_id = ?, updated_at_unix_ms = ? WHERE id = ?`, sql.NullString{String: worktreeID, Valid: true}, time.Now().UTC().UnixMilli(), taskID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func createStarterWorkflow(t *testing.T, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Runner Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := starterNodeByKind(t, def, workflow.NodeKindStart)
	done := starterNodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Implement the task.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement the task."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func createChainedStarterWorkflow(t *testing.T, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	return createChainedStarterWorkflowWithContextMode(t, store, workflow.ContextModeNewSession, "coder")
}

func createChainedStarterWorkflowWithContextMode(t *testing.T, store *workflowstore.Store, contextMode workflow.ContextMode, targetRole string) workflow.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Chained Runner Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := starterNodeByKind(t, def, workflow.NodeKindStart)
	done := starterNodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implID := workflow.NodeID("node-impl-" + string(created.ID))
	for _, node := range []workflowstore.NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan the task.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implID, WorkflowID: created.ID, Key: "implement", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: targetRole, PromptTemplate: "Use {{.TaskTitle}} and {{.Inputs.prior_summary}}.", InputFields: []workflow.InputField{{Name: "prior_summary", Description: "Prior summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: implID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan the task."},
		{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: implID, ContextMode: contextMode, PromptTemplate: "Use {{.TaskTitle}} and {{.Params.prior_summary}}.", Parameters: []workflow.Parameter{{Key: "prior_summary", Description: "Prior summary."}}},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createFanoutCompactStarterWorkflow(t *testing.T, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Fanout Compact Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := starterNodeByKind(t, def, workflow.NodeKindStart)
	done := starterNodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implAID := workflow.NodeID("node-impl-a-" + string(created.ID))
	implBID := workflow.NodeID("node-impl-b-" + string(created.ID))
	joinID := workflow.NodeID("node-join-" + string(created.ID))
	synthID := workflow.NodeID("node-synth-" + string(created.ID))
	joinAEdgeID := workflow.EdgeID("edge-join-a-" + string(created.ID))
	joinBEdgeID := workflow.EdgeID("edge-join-b-" + string(created.ID))
	for _, node := range []workflowstore.NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implAID, WorkflowID: created.ID, Key: "impl_a", Kind: workflow.NodeKindAgent, DisplayName: "Implement A", SubagentRole: "coder", PromptTemplate: "A.", InputFields: []workflow.InputField{{Name: "summary", Description: "Summary."}}},
		{ID: implBID, WorkflowID: created.ID, Key: "impl_b", Kind: workflow.NodeKindAgent, DisplayName: "Implement B", SubagentRole: "coder", PromptTemplate: "B.", InputFields: []workflow.InputField{{Name: "summary", Description: "Summary."}}},
		{ID: joinID, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join", JoinInputProviders: []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: joinAEdgeID}}},
		{ID: synthID, WorkflowID: created.ID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synthesize", SubagentRole: "coder", PromptTemplate: "Synthesize.", InputFields: []workflow.InputField{{Name: "joined", Description: "Joined branch summary."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	splitGroup := workflow.TransitionGroupID("group-split-" + string(created.ID))
	joinAGroup := workflow.TransitionGroupID("group-join-a-" + string(created.ID))
	joinBGroup := workflow.TransitionGroupID("group-join-b-" + string(created.ID))
	synthGroup := workflow.TransitionGroupID("group-synth-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
		{ID: joinAGroup, WorkflowID: created.ID, SourceNodeID: implAID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinBGroup, WorkflowID: created.ID, SourceNodeID: implBID, TransitionID: "join", DisplayName: "Join"},
		{ID: synthGroup, WorkflowID: created.ID, SourceNodeID: joinID, TransitionID: "synth", DisplayName: "Synthesize"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: synthID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-split-a-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_a", TargetNodeID: implAID, ContextMode: workflow.ContextModeCompactAndContinueSession, PromptTemplate: "A {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary."}}},
		{ID: workflow.EdgeID("edge-split-b-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_b", TargetNodeID: implBID, ContextMode: workflow.ContextModeCompactAndContinueSession, PromptTemplate: "B {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary."}}},
		{ID: joinAEdgeID, WorkflowID: created.ID, TransitionGroupID: joinAGroup, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "joined", Description: "Joined branch summary."}}},
		{ID: joinBEdgeID, WorkflowID: created.ID, TransitionGroupID: joinBGroup, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-synth-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: synthGroup, Key: "synth", TargetNodeID: synthID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize {{.Params.joined}}."},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func updateChainedStarterWorkflowNextEdgeContextMode(t *testing.T, metadataStore *metadata.Store, workflowID workflow.WorkflowID, contextMode workflow.ContextMode) {
	t.Helper()
	result, err := metadataStore.DB().ExecContext(context.Background(), `UPDATE workflow_edges SET context_mode = ? WHERE id = ?`, string(contextMode), "edge-next-"+string(workflowID))
	if err != nil {
		t.Fatalf("update live edge context mode: %v", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		t.Fatalf("update live edge rows affected: %v", err)
	} else if rows != 1 {
		t.Fatalf("updated live edge rows = %d, want 1", rows)
	}
}

func starterNodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind == kind {
			return node
		}
	}
	t.Fatalf("node kind %s missing", kind)
	return workflow.Node{}
}

func starterEdgeByKey(t *testing.T, def workflow.Definition, key workflow.ModelKey) workflow.Edge {
	t.Helper()
	for _, edge := range def.Edges {
		if edge.Key == key {
			return edge
		}
	}
	t.Fatalf("edge key %s missing", key)
	return workflow.Edge{}
}

func assertPromptContains(t *testing.T, req llm.Request, needles []string) {
	t.Helper()
	text := requestPromptText(req)
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			t.Fatalf("request prompt missing %q in:\n%s", needle, text)
		}
	}
}

func requestPromptText(req llm.Request) string {
	var haystack strings.Builder
	for _, item := range req.Items {
		haystack.WriteString(item.Content)
		haystack.WriteString("\n")
	}
	return haystack.String()
}

func assertNoUserPrompt(t *testing.T, req llm.Request) {
	t.Helper()
	userPrompts := []string{}
	for _, item := range req.Items {
		if item.Role == llm.RoleUser {
			userPrompts = append(userPrompts, item.Content)
		}
	}
	if len(userPrompts) == 0 {
		return
	}
	t.Fatalf("request should not include user prompts, got %+v", userPrompts)
}

func requestHasTool(req llm.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

type providerProbeForbiddenClient struct{}

func (providerProbeForbiddenClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("generate was not expected")
}

func (providerProbeForbiddenClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{}, errors.New("provider capability probe was not expected")
}

type blockingClient struct {
	called chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newBlockingClient() *blockingClient {
	return &blockingClient{called: make(chan struct{}), done: make(chan struct{})}
}

func (c *blockingClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.once.Do(func() { close(c.called) })
	defer close(c.done)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func (c *blockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}, nil
}

func (c *blockingClient) waitForCall(t *testing.T) {
	t.Helper()
	select {
	case <-c.called:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake model call")
	}
}

type drainingBlockingClient struct {
	called      chan struct{}
	canceled    chan struct{}
	release     chan struct{}
	done        chan struct{}
	callOnce    sync.Once
	releaseOnce sync.Once
}

func newDrainingBlockingClient() *drainingBlockingClient {
	return &drainingBlockingClient{called: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}

func (c *drainingBlockingClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.callOnce.Do(func() { close(c.called) })
	defer close(c.done)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return llm.Response{}, ctx.Err()
}

func (c *drainingBlockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}, nil
}

func (c *drainingBlockingClient) waitForCall(t *testing.T) {
	t.Helper()
	select {
	case <-c.called:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake model call")
	}
}

func (c *drainingBlockingClient) waitForCancel(t *testing.T) {
	t.Helper()
	select {
	case <-c.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake model cancellation")
	}
}

func (c *drainingBlockingClient) waitForReturn(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake model return")
	}
}

func (c *drainingBlockingClient) releaseReturn() {
	c.releaseOnce.Do(func() { close(c.release) })
}

func (c *drainingBlockingClient) returned() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *blockingClient) waitForReturn(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake model return")
	}
}

func (c *blockingClient) returned() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
