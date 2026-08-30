package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	testsqlite "core/internal/testharness/sqlite"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runprompt"
	agentruntime "core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/session"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func mustRetained(t *testing.T, err error, format string) {
	if err != nil {
		t.Fatalf(format, err)
	}
}

func TestWorkflowTaskResumeConflictErrorRoundTripsLifecycleState(t *testing.T) {
	source := &serverapi.WorkflowTaskResumeConflictError{
		TaskID: "task-123", State: serverapi.WorkflowTaskResumeConflictMovedCurrentNode,
	}
	mustRetained(t, source.Validate(), "Validate: %v")
	decoded := serverapi.DecodeWorkflowTaskResumeConflictError(source.RPCErrorData(), source.Error())
	var conflict *serverapi.WorkflowTaskResumeConflictError
	if !errors.As(decoded, &conflict) || *conflict != *source {
		t.Fatalf("decoded conflict = %+v, want %+v", conflict, source)
	}
}

type retainedAssignmentOrderClient struct {
	*compactingScriptedClient
	fixture   *currentNodeRunnerFixture
	sessionID runtimeids.SessionID
	test      *testing.T
}

func (c *retainedAssignmentOrderClient) Generate(
	ctx context.Context, request llm.Request, callbacks llm.StreamCallbacks,
) (llm.Response, error) {
	if c.fixture.workflowAssignmentRecordCount(c.test, c.sessionID) == 0 {
		return llm.Response{}, errors.New("provider called before durable Workflow assignment")
	}
	return c.compactingScriptedClient.Generate(ctx, request, callbacks)
}

func prepareCompactedRetainedWorkflowSession(
	t *testing.T, f *currentNodeRunnerFixture, client currentNodeRunnerClient,
) (runtimeids.SessionID, workflow.CurrentNodeReference, sessionruntime.RuntimeAttachment) {
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	mustRetained(t, f.store.LockTaskExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeNone, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{SourceWorkspaceID: f.workspaceID, SourceWorkspaceRoot: f.workspace},
	}), "lock Task execution target: %v")
	started, err := f.store.StartTask(context.Background(), task.ID)
	mustRetained(t, err, "start Task: %v")
	reference := started.Mutation.Created[0].Reference
	store, err := session.Create(
		filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		"sessions", f.workspace, sessioncontract.SessionCategoryMain, f.starter.storeOptions...,
	)
	mustRetained(t, err, "create retained Session: %v")
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	mustRetained(t, err, "parse retained Session ID: %v")
	_, err = f.store.BindSessionToCurrentNode(context.Background(), workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID: sessionID, CurrentNode: reference, AssociatedAt: time.Now().UTC(),
		},
	})
	mustRetained(t, err, "bind retained Session: %v")
	mustRetained(t, f.store.InterruptCurrentNode(context.Background(), reference,
		workflow.CurrentNodeInterruptionReason("test_retained_continuation"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_retained_continuation"}), "interrupt retained Current Node: %v")
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	mustRetained(t, err, "resolve retained Current Node: %v")
	attachment, binding := openRetainedCurrentNodeRuntime(t, f, input, reference, sessionID, client, "retained-continuation-boundary-test")
	t.Cleanup(func() {
		_ = binding.Close()
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
	})
	mustRetained(t, f.authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *agentruntime.Engine) error {
		return engine.CompactContext(ctx, "")
	}), "compact retained Session: %v")
	mustRetained(t, binding.Close(), "close dormant Workflow binding: %v")
	return sessionID, reference, attachment
}

func activateRetainedWorkflowBinding(t *testing.T, f *currentNodeRunnerFixture, sessionID runtimeids.SessionID, reference workflow.CurrentNodeReference) {
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	mustRetained(t, err, "resolve retained binding context: %v")
	var binding *agentruntime.CurrentNodeExecutionBinding
	err = f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		binding, err = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID: runtimeids.NewExecutionScopeID(), CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Instructions: workflowruntime.TaskInstructions{WorkflowID: input.Workflow.ID, CurrentNode: reference},
		})
		return err
	})
	mustRetained(t, err, "activate retained Workflow binding: %v")
	t.Cleanup(func() { _ = binding.Close() })
}

func openRetainedCurrentNodeRuntime(
	t *testing.T, f *currentNodeRunnerFixture, input workflowstore.CurrentNodeStartContext,
	reference workflow.CurrentNodeReference, sessionID runtimeids.SessionID, client currentNodeRunnerClient, owner string,
) (sessionruntime.RuntimeAttachment, *agentruntime.CurrentNodeExecutionBinding) {
	planner := launch.Planner{
		Config: f.cfg, ContainerDir: filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		StoreOptions: f.starter.storeOptions, PersistedSessions: f.metadata, ExecutionTargets: f.metadata,
		ProjectWorkspaceBoundary: f.metadata,
	}
	plan, err := planner.PlanSession(context.Background(), launch.SessionRequest{
		Mode: launch.ModeHeadless, Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
	})
	mustRetained(t, err, "plan retained Session: %v")
	runtimePlan, err := f.starter.buildCurrentNodeAgentRuntimePlan(input, preparedCurrentNodeAgentSession{
		root: *input.ExecutionRoot, plan: plan, client: client, mode: workflowruntime.CompletionModeTool,
	}, nil)
	mustRetained(t, err, "build retained runtime plan: %v")
	attachment, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID, OwnerID: owner, Runtime: &runtimePlan,
	})
	mustRetained(t, err, "open retained runtime: %v")
	var binding *agentruntime.CurrentNodeExecutionBinding
	err = f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		var err error
		binding, err = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID: runtimeids.NewExecutionScopeID(), CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Instructions: workflowruntime.TaskInstructions{WorkflowID: input.Workflow.ID, CurrentNode: reference},
		})
		return err
	})
	if err != nil {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		t.Fatalf("bind retained Current Node: %v", err)
	}
	return attachment, binding
}

func newRetainedAssignmentFixture(t *testing.T) (*currentNodeRunnerFixture, *retainedAssignmentOrderClient, runtimeids.SessionID) {
	base := NewCompactingScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("pre-activation")},
		ScriptedToolBatch("complete", llm.ToolCall{ID: "complete", Name: "complete_node", Input: []byte(`{"transition":"done"}`)}),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, base)
	order := &retainedAssignmentOrderClient{compactingScriptedClient: base, fixture: f, test: t}
	f.client = order
	sessionID, _, _ := prepareCompactedRetainedWorkflowSession(t, f, order)
	order.sessionID = sessionID
	return f, order, sessionID
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesTUISteerProvider(t *testing.T) {
	f, order, sessionID := newRetainedAssignmentFixture(t)
	service := runtimecontrol.NewService(f.authority).
		WithWorkflowSessionReactivator(f.controller).WithPromptHistoryStore(f.metadata).
		WithPersistedSessionResolver(f.metadata)
	response, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: sessionID.String(), Input: runtimeinput.Text("continue"),
	})
	mustRetained(t, err, "SubmitUserTurn: %v")
	if len(order.Requests()) == 0 || !requestContainsMessage(order.Requests()[0], "continue") {
		t.Fatalf("TUI provider requests = %+v, want selected continuation input", order.Requests())
	}
	if response.ResultKind != clientui.UserTurnResultKindNoFinal {
		t.Fatalf("SubmitUserTurn response = %+v, want no-final result", response)
	}
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesHeadlessContinueProvider(t *testing.T) {
	f, order, sessionID := newRetainedAssignmentFixture(t)
	client := runPromptClientForCurrentNodeFixture(f, f.controller)
	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID), Prompt: "continue",
	}, nil)
	mustRetained(t, err, "RunPrompt: %v")
	if len(order.Requests()) == 0 || !requestContainsMessage(order.Requests()[0], "continue") {
		t.Fatalf("headless provider requests = %+v, want selected continuation input", order.Requests())
	}
	if response.SessionID != sessionID.String() || response.Result != "" {
		t.Fatalf("RunPrompt response = %+v, want selected Session with empty successful result", response)
	}
}

func TestRetainedContinuationRejectsNonResumableAndNoOpBeforeDelivery(t *testing.T) {
	for _, test := range []struct {
		name   string
		finish bool
		state  serverapi.WorkflowTaskResumeConflictState
	}{
		{"finished non-resumable", true, serverapi.WorkflowTaskResumeConflictMovedCurrentNode},
		{"already resumed no-op", false, serverapi.WorkflowTaskResumeConflictCurrentNodeNotInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := NewCompactingScriptedClient(
				llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
				[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("retained")},
			)
			f := newCurrentNodeRunnerFixtureWithClient(t, base)
			sessionID, reference, _ := prepareCompactedRetainedWorkflowSession(t, f, base)
			activateRetainedWorkflowBinding(t, f, sessionID, reference)
			if _, _, err := f.store.ResumeCurrentNode(context.Background(), reference); err != nil {
				t.Fatalf("prepare rejection: %v", err)
			}
			if test.finish {
				if _, err := f.store.CompleteCurrentNode(context.Background(), workflowstore.CurrentNodeCompletionRequest{
					Source: reference, TransitionID: "done",
				}); err != nil {
					t.Fatalf("finish retained Current Node: %v", err)
				}
			}
			service := runtimecontrol.NewService(f.authority).
				WithWorkflowSessionReactivator(f.controller).WithPromptHistoryStore(f.metadata).
				WithPersistedSessionResolver(f.metadata)
			_, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
				SessionID: sessionID.String(), Input: runtimeinput.Text("continue"),
			})
			var conflict *serverapi.WorkflowTaskResumeConflictError
			if !errors.As(err, &conflict) || conflict.State != test.state {
				t.Fatalf("rejected SubmitUserTurn error = %T %v, want %s", err, err, test.state)
			}
			if len(base.Requests()) != 0 {
				t.Fatalf("rejected provider requests = %d, want none", len(base.Requests()))
			}
			history, historyErr := f.metadata.ReadPromptHistory(context.Background(), sessionID.String())
			if historyErr != nil {
				t.Fatalf("read rejected prompt history: %v", historyErr)
			}
			if len(history) != 0 {
				t.Fatalf("rejected prompt history = %v, want none", history)
			}
		})
	}
}

type retainedFanoutScenario struct {
	name                                                                                   string
	selectedError, selectedResumeError, siblingError, historyError, selectedExecutionError error
	selectedFinal                                                                          string
	cancel, cancelBeforeResume, tui, wantWarnings, resumeLock, historyLock                 bool
}

func TestRetainedSiblingResumeWaitsForFileBackedMetadataLock(t *testing.T) {
	for _, scenario := range []retainedFanoutScenario{
		{name: "all success"},
		{name: "selected assistant final", selectedFinal: "selected final"},
		{name: "selected success sibling failure", siblingError: errors.New("sibling Resume failed"), wantWarnings: true, tui: true},
		{name: "selected delivery failure sibling success", selectedError: errors.New("selected delivery failed")},
		{name: "selected failure sibling success", selectedResumeError: errors.New("selected Resume failed")},
		{name: "history failure after selected success", historyError: errors.New("prompt history failed")},
		{name: "history failure after selected execution failure", historyError: errors.New("prompt history failed"), selectedExecutionError: errors.New("selected execution failed")},
		{name: "TUI cancellation before selected Resume", cancelBeforeResume: true, tui: true},
		{name: "TUI cancellation after acceptance", cancel: true, tui: true},
		{name: "sibling Resume contention", resumeLock: true},
		{name: "prompt history contention", historyLock: true},
	} {
		t.Run(scenario.name, func(t *testing.T) { runRetainedFanoutScenario(t, scenario) })
	}
}

type retainedOperationResult struct {
	response    serverapi.RunPromptResponse
	tuiResponse serverapi.RuntimeSubmitUserTurnResponse
	err         error
}

type retainedPromptHistoryStore interface {
	RecordPromptHistoryEntry(context.Context, metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error)
}

func runRetainedFanoutScenario(t *testing.T, scenario retainedFanoutScenario) {
	var history *retainedDelayedPromptHistoryStore
	var selectedStarted, selectedRelease chan struct{}
	if scenario.historyError != nil {
		history = &retainedDelayedPromptHistoryStore{started: make(chan struct{}), release: make(chan struct{}), err: scenario.historyError}
	}
	if scenario.historyError != nil || scenario.cancel {
		selectedStarted, selectedRelease = make(chan struct{}), make(chan struct{})
	}
	releaseSelected := func() {}
	if selectedRelease != nil {
		var once sync.Once
		releaseSelected = func() { once.Do(func() { close(selectedRelease) }) }
		t.Cleanup(releaseSelected)
	}
	client := &retainedProgressClient{
		selectedError: scenario.selectedExecutionError, selectedFinal: scenario.selectedFinal,
		selectedCompletes: scenario.cancel, selectedStarted: selectedStarted, selectedRelease: selectedRelease,
	}
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.cfg.Settings.Workflow.CompletionMode, f.starter.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeTool, config.WorkflowCompletionModeTool
	f.cfg.Settings.ProviderOverride, f.starter.cfg.Settings.ProviderOverride = "openai", "openai"
	f.cfg.Settings.OpenAIBaseURL, f.starter.cfg.Settings.OpenAIBaseURL = "http://unused.invalid", "http://unused.invalid"
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store, false)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	branches := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		if len(nodes) != len(branchNodeIDs) {
			return false
		}
		for _, node := range nodes {
			if node.SessionID == nil || node.Scheduling == nil || node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
				return false
			}
		}
		return true
	})
	selected, selectedSession := branches[0], *branches[0].SessionID
	failureReference := selected.Reference
	if scenario.siblingError != nil {
		failureReference = branches[1].Reference
	}
	if scenario.selectedError != nil || scenario.siblingError != nil {
		original := f.starter.store
		f.starter.store = currentNodeStartContextStore{RuntimeStore: original, transform: func(input workflowstore.CurrentNodeStartContext) workflowstore.CurrentNodeStartContext {
			if input.CurrentNode.Reference.Equal(failureReference) && input.ExecutionRoot != nil {
				root := *input.ExecutionRoot
				root.SourceWorkspaceID = "workspace-missing"
				input.ExecutionRoot = &root
			}
			return input
		}}
	}
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), selected.Reference)
	mustRetained(t, err, "resolve selected Current Node: %v")
	attachment, binding := openRetainedCurrentNodeRuntime(
		t, f, input, selected.Reference, *selected.SessionID, f.client, "retained-sibling-contention-test",
	)
	if err := binding.Close(); err != nil {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		t.Fatalf("close dormant retained binding: %v", err)
	}
	t.Cleanup(func() { _, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose) })
	lockReady := make(chan func(), 1)
	attempted, completed := make(chan workflow.CurrentNodeReference, len(branches)), make(chan workflow.CurrentNodeReference, len(branches))
	lockReference := selected.Reference
	if scenario.resumeLock {
		lockReference = branches[1].Reference
	}
	mustRetained(t, f.controller.Close(), "close initial Current Node controller: %v")
	controller, err := workflowexecution.NewCurrentNodeController(&retainedResumeLockStore{
		Store: f.store, Selected: selected.Reference, LockReference: lockReference,
		SelectedResumeError: scenario.selectedResumeError, LockBeforeSibling: scenario.resumeLock,
		FailSibling: scenario.siblingError != nil, PersistenceRoot: f.cfg.PersistenceRoot,
		LockReady: lockReady, Attempted: attempted, Completed: completed, Test: t,
	}, f.starter, f.authority, workflowexecution.NewTaskMutationCoordinator(),
		workflowexecution.CurrentNodeControllerConfig{AgentConcurrency: 2, AssignmentSteerer: f.starter})
	mustRetained(t, err, "new lock-aware Current Node controller: %v")
	f.controller = controller
	var historyStore retainedPromptHistoryStore = f.metadata
	var historyLockStore *retainedPromptHistoryLockStore
	if scenario.historyLock {
		historyLockStore = &retainedPromptHistoryLockStore{
			Store: f.metadata, PersistenceRoot: f.cfg.PersistenceRoot, LockReady: lockReady,
			Attempted: make(chan struct{}, 1), Completed: make(chan struct{}, 1), Test: t,
		}
		historyStore = historyLockStore
	}
	if history != nil {
		historyStore = history
	}
	runClient := runPromptClientForCurrentNodeFixture(f, f.controller, historyStore)
	tuiService := runtimecontrol.NewService(f.authority).WithWorkflowSessionReactivator(f.controller).
		WithPromptHistoryStore(historyStore).WithPersistedSessionResolver(f.metadata)
	if err := f.authority.WithRetainedWorkflowRuntime(context.Background(), selectedSession, func(context.Context, *agentruntime.Engine) error { return nil }); err != nil {
		t.Fatalf("selected branch retained runtime: %v", err)
	}
	mustRetained(t, f.authority.WithCurrentRuntime(context.Background(), selectedSession, func(ctx context.Context, engine *agentruntime.Engine) error {
		_, err := engine.QueueUserMessageForAutoDrain(ctx, "older pending input")
		return err
	}), "queue older selected input: %v")
	requestCountBeforeSubmit := len(client.Requests())
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if scenario.cancelBeforeResume {
		cancel()
	}
	var progressMu sync.Mutex
	var progress []serverapi.RunPromptProgress
	done := make(chan retainedOperationResult, 1)
	timeout := 2 * time.Second
	if scenario.historyError != nil {
		timeout = 500 * time.Millisecond
	}
	go func() {
		if scenario.tui {
			response, err := tuiService.SubmitUserTurn(runCtx, serverapi.RuntimeSubmitUserTurnRequest{SessionID: selectedSession.String(), Input: runtimeinput.Text("continue")})
			done <- retainedOperationResult{tuiResponse: response, err: err}
			return
		}
		response, err := runClient.RunPrompt(runCtx, serverapi.RunPromptRequest{Intent: serverapi.OpenExistingSessionLaunchIntent(selectedSession), Prompt: "continue", Timeout: timeout},
			serverapi.RunPromptProgressFunc(func(event serverapi.RunPromptProgress) {
				progressMu.Lock()
				progress = append(progress, event)
				progressMu.Unlock()
			}))
		done <- retainedOperationResult{response: response, err: err}
	}()
	release := func() {}
	if scenario.resumeLock || scenario.historyLock {
		select {
		case release = <-lockReady:
		case result := <-done:
			t.Fatalf("continuation completed before contention boundary: %+v", result)
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("continuation did not reach persistence boundary")
		}
		t.Cleanup(func() { release() })
		select {
		case result := <-done:
			t.Fatalf("continuation returned while persistence was locked: %+v", result)
		case <-time.After(150 * time.Millisecond):
		}
	}
	var result retainedOperationResult
	if scenario.cancelBeforeResume {
		if result.err != nil || result.tuiResponse != (serverapi.RuntimeSubmitUserTurnResponse{}) {
			t.Fatalf("pre-Resume cancellation response=%+v error=%v, want no accepted operation", result.tuiResponse, result.err)
		}
		if len(client.Requests()) != requestCountBeforeSubmit {
			t.Fatalf("pre-Resume cancellation provider requests = %d, want unchanged count %d", len(client.Requests()), requestCountBeforeSubmit)
		}
		historyRecords, err := f.metadata.ReadPromptHistory(context.Background(), selectedSession.String())
		mustRetained(t, err, "read pre-Resume cancellation history: %v")
		if len(historyRecords) != 0 {
			t.Fatalf("pre-Resume cancellation history = %v, want none", historyRecords)
		}
		nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
		mustRetained(t, err, "read pre-Resume cancellation Current Nodes: %v")
		for _, node := range nodes {
			if node.Scheduling == nil || node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
				t.Fatalf("pre-Resume cancellation Current Node = %+v, want interrupted", node)
			}
		}
		return
	} else if scenario.cancel {
		select {
		case <-selectedStarted:
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("selected continuation did not reach provider")
		}
		cancel()
		release()
		releaseSelected()
	} else if scenario.resumeLock {
		release()
	} else if scenario.historyError != nil {
		release()
		select {
		case <-history.started:
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("prompt history did not start")
		}
		select {
		case <-selectedStarted:
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("selected execution did not start while history was pending")
		}
		select {
		case result := <-done:
			t.Fatalf("continuation returned while history was pending: %+v", result)
		case <-time.After(600 * time.Millisecond):
		}
		close(history.release)
		releaseSelected()
	} else if scenario.historyLock {
		select {
		case <-historyLockStore.Attempted:
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("prompt history did not reach store")
		}
		select {
		case <-historyLockStore.Completed:
			t.Fatal("prompt history completed while metadata was locked")
		case <-time.After(150 * time.Millisecond):
		}
		release()
	} else {
		release()
	}
	select {
	case result = <-done:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("continuation did not finish")
	}
	if scenario.cancel {
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want caller cancellation", result.err)
		}
		if !scenario.tui {
			t.Fatal("cancellation scenario must exercise TUI boundary")
		}
		foundInput := false
		for _, request := range client.Requests() {
			foundInput = foundInput || requestContainsMessage(request, "continue")
		}
		if !foundInput || result.tuiResponse.ResultKind != clientui.UserTurnResultKindNoFinal {
			t.Fatalf("canceled TUI result=%+v requests=%+v", result.tuiResponse, client.Requests())
		}
	} else if scenario.tui && result.tuiResponse.ResultKind != clientui.UserTurnResultKindNoFinal {
		t.Fatalf("partial-failure TUI result=%+v, want no-final", result.tuiResponse)
	} else if scenario.historyError != nil {
		if !errors.Is(result.err, scenario.historyError) {
			t.Fatalf("history error = %v, want %v", result.err, scenario.historyError)
		}
		if scenario.selectedExecutionError != nil && !errors.Is(result.err, scenario.selectedExecutionError) {
			t.Fatalf("history/selected error = %v", result.err)
		}
	} else if scenario.selectedError != nil || scenario.selectedResumeError != nil {
		if result.err == nil {
			t.Fatal("selected failure was not returned")
		}
		if scenario.selectedResumeError != nil && !errors.Is(result.err, scenario.selectedResumeError) {
			t.Fatalf("selected Resume error = %v", result.err)
		}
	} else if result.err != nil {
		t.Fatalf("continuation: %v", result.err)
	}
	if scenario.selectedFinal != "" && result.response.Result != scenario.selectedFinal {
		t.Fatalf("selected result = %q, want %q", result.response.Result, scenario.selectedFinal)
	}
	if !scenario.tui && (len(result.response.Warnings) > 0) != scenario.wantWarnings {
		t.Fatalf("warnings = %v, want=%t", result.response.Warnings, scenario.wantWarnings)
	}
	if scenario.name == "all success" {
		var selectedRequests []llm.Request
		for _, request := range client.Requests() {
			if requestContainsMessage(request, "continue") {
				selectedRequests = append(selectedRequests, request)
			}
		}
		if len(selectedRequests) < 2 || requestContainsMessage(selectedRequests[0], "older pending input") ||
			!requestContainsMessage(selectedRequests[1], "older pending input") {
			t.Fatalf("selected requests = %+v, want direct then drained input", selectedRequests)
		}
	}
	for range branches {
		select {
		case <-attempted:
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("retained branch Resume was not attempted")
		}
	}
	if scenario.resumeLock {
		for range branches {
			select {
			case <-completed:
			case <-time.After(currentNodeRunnerWait):
				t.Fatal("retained Resume did not complete after lock release")
			}
		}
	}
	f.waitForTaskQuiescence(t, task.ID)
	if scenario.name == "all success" {
		progressMu.Lock()
		gotProgress := append([]serverapi.RunPromptProgress(nil), progress...)
		progressMu.Unlock()
		if !hasAssistantProgress(gotProgress, "selected progress") ||
			hasAssistantProgress(gotProgress, "sibling progress") || hasAssistantProgress(gotProgress, "older progress") {
			t.Fatalf("selected progress = %+v, want selected-only", gotProgress)
		}
	}
	if scenario.selectedError != nil || scenario.selectedResumeError != nil {
		for _, request := range client.Requests() {
			if requestContainsMessage(request, "continue") {
				t.Fatalf("rejected selected input reached provider: %+v", request)
			}
		}
	}
	if scenario.selectedResumeError == nil && scenario.historyError == nil {
		historyRecords, err := f.metadata.ReadPromptHistory(context.Background(), selectedSession.String())
		mustRetained(t, err, "read accepted prompt history: %v")
		if len(historyRecords) != 1 || historyRecords[0] != "continue" {
			t.Fatalf("accepted prompt history = %v, want one continue entry", historyRecords)
		}
	}
}

type retainedResumeLockStore struct {
	*workflowstore.Store
	Selected, LockReference        workflow.CurrentNodeReference
	SelectedResumeError            error
	LockBeforeSibling, FailSibling bool
	PersistenceRoot                string
	LockReady                      chan<- func()
	Attempted, Completed           chan<- workflow.CurrentNodeReference
	Test                           testing.TB
}

func (s *retainedResumeLockStore) ResumeCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error) {
	if reference.Equal(s.Selected) && s.SelectedResumeError != nil {
		s.Attempted <- reference
		return workflowstore.InterruptedCurrentNodeAttentionProjection{}, false, s.SelectedResumeError
	}
	var release func()
	if s.LockBeforeSibling && reference.Equal(s.LockReference) {
		release = testsqlite.AcquireWriteLock(s.Test, s.PersistenceRoot)
		s.LockReady <- release
		defer release()
	}
	s.Attempted <- reference
	projection, found, err := s.Store.ResumeCurrentNode(ctx, reference)
	if err != nil {
		return projection, found, err
	}
	if s.FailSibling && !reference.Equal(s.Selected) {
		return projection, false, errors.New("sibling Resume failed")
	}
	s.Completed <- reference
	return projection, found, nil
}

type retainedPromptHistoryLockStore struct {
	*metadata.Store
	PersistenceRoot      string
	LockReady            chan<- func()
	Attempted, Completed chan struct{}
	Test                 testing.TB
}

func (s *retainedPromptHistoryLockStore) RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error) {
	release := testsqlite.AcquireWriteLock(s.Test, s.PersistenceRoot)
	s.LockReady <- release
	s.Attempted <- struct{}{}
	defer func() { release(); s.Completed <- struct{}{} }()
	return s.Store.RecordPromptHistoryEntry(ctx, entry)
}

type retainedDelayedPromptHistoryStore struct {
	started, release chan struct{}
	err              error
}

func (s *retainedDelayedPromptHistoryStore) RecordPromptHistoryEntry(ctx context.Context, _ metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error) {
	close(s.started)
	select {
	case <-s.release:
		return metadata.PromptHistoryRecord{}, s.err
	case <-ctx.Done():
		return metadata.PromptHistoryRecord{}, ctx.Err()
	}
}

type retainedProgressClient struct {
	mu                sync.Mutex
	requests          []llm.Request
	generations       int
	selectedError     error
	selectedFinal     string
	selectedCompletes bool
	selectedStarted   chan<- struct{}
	selectedRelease   <-chan struct{}
	selectedGate      sync.Once
}

func (c *retainedProgressClient) Generate(_ context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	selected := requestContainsMessage(request, "continue")
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.generations++
	generation := c.generations
	c.mu.Unlock()
	selectedFirst := false
	if selected {
		c.selectedGate.Do(func() {
			selectedFirst = true
			if c.selectedStarted != nil {
				close(c.selectedStarted)
			}
			if c.selectedRelease != nil {
				<-c.selectedRelease
			}
		})
	}
	if selected && c.selectedError != nil {
		return llm.Response{}, &llm.ProviderAPIError{ProviderID: "test", StatusCode: 400, Code: llm.UnifiedErrorCodeProviderContract, Err: c.selectedError}
	}
	switch generation {
	case 1:
		return retainedResponse("older progress", llm.ToolCall{ID: "split", Name: "complete_node", Input: []byte(`{"transition":"split"}`)}), nil
	case 2, 3:
		return llm.Response{}, context.Canceled
	}
	content := "sibling progress"
	if selected {
		content = "selected progress"
	}
	if selected && selectedFirst {
		if c.selectedFinal != "" {
			return retainedFinalResponse(c.selectedFinal, llm.ToolCall{ID: "selected-final", Name: "complete_node", Input: []byte(retainedCompletionInput(request, "join_a", c.selectedFinal))}), nil
		}
		if c.selectedCompletes {
			return retainedResponse(content, llm.ToolCall{ID: "selected-complete", Name: "complete_node", Input: []byte(retainedCompletionInput(request, "join_a", content))}), nil
		}
		return retainedResponse(content), nil
	}
	return retainedResponse(content, llm.ToolCall{ID: "complete", Name: "complete_node", Input: []byte(retainedCompletionInput(request, "join_a", content))}), nil
}

func retainedResponse(content string, calls ...llm.ToolCall) llm.Response {
	return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary), Content: textutil.Value(content)}, ToolCalls: calls}
}

func retainedFinalResponse(content string, calls ...llm.ToolCall) llm.Response {
	return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value(content)}, ToolCalls: calls}
}

func (c *retainedProgressClient) Requests() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.requests...)
}

func (c *retainedProgressClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}, nil
}

func requestContainsMessage(request llm.Request, content string) bool {
	for _, message := range llm.MessagesFromItems(request.Items) {
		if message.Content != nil && *message.Content == content {
			return true
		}
	}
	return false
}

func retainedCompletionInput(request llm.Request, fallback, commentary string) string {
	for _, tool := range request.Tools {
		if tool.Name != "complete_node" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(tool.Schema.JSON(), &schema) != nil {
			continue
		}
		raw, ok := schema.Properties["transition"]
		if !ok {
			return `{"commentary":"` + commentary + `"}`
		}
		var transition struct {
			Enum []string `json:"enum"`
		}
		if json.Unmarshal(raw, &transition) != nil {
			continue
		}
		for _, value := range transition.Enum {
			if value == fallback {
				return `{"transition":"` + value + `","commentary":"` + commentary + `"}`
			}
		}
		return `{"commentary":"` + commentary + `"}`
	}
	return `{"transition":"` + fallback + `","commentary":"` + commentary + `"}`
}

func hasAssistantProgress(progress []serverapi.RunPromptProgress, content string) bool {
	for _, event := range progress {
		if event.AssistantMessage != nil && event.AssistantMessage.Content == content {
			return true
		}
	}
	return false
}

func runPromptClientForCurrentNodeFixture(
	f *currentNodeRunnerFixture, controller *workflowexecution.CurrentNodeController, histories ...retainedPromptHistoryStore,
) apicontract.RunPromptService {
	var history retainedPromptHistoryStore = f.metadata
	if len(histories) != 0 {
		history = histories[0]
	}
	return runprompt.NewInProcessRunPromptClient(runprompt.HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config: f.cfg, ContainerDir: filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
			StoreOptions: f.starter.storeOptions, PersistedSessions: f.metadata, ExecutionTargets: f.metadata,
			ProjectWorkspaceBoundary: f.metadata,
		}),
		PromptHistory: history, RuntimeAuthority: f.authority, WorkflowSessionReactivator: controller,
	})
}
