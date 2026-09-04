package workflowrunner

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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
	"core/shared/toolspec"

	"github.com/google/uuid"
)

func mustRetained(t *testing.T, err error, format string) {
	if err != nil {
		t.Fatalf(format, err)
	}
}

func retainedPromptHistory(t *testing.T, f *currentNodeRunnerFixture, sessionID runtimeids.SessionID) []string {
	history, err := f.metadata.ReadPromptHistory(context.Background(), sessionID.String())
	mustRetained(t, err, "read prompt history: %v")
	return history
}

type retainedAssignmentOrderClient struct {
	*compactingScriptedClient
	fixture           *currentNodeRunnerFixture
	sessionID         runtimeids.SessionID
	expectedReference workflow.CurrentNodeReference
	test              *testing.T
}

func (c *retainedAssignmentOrderClient) Generate(
	ctx context.Context, request llm.Request, callbacks llm.StreamCallbacks,
) (llm.Response, error) {
	if c.fixture.workflowAssignmentRecordCount(c.test, c.sessionID, c.expectedReference) == 0 {
		return llm.Response{}, errors.New("provider called before durable Workflow assignment")
	}
	return c.compactingScriptedClient.Generate(ctx, request, callbacks)
}

func prepareRetainedSessionForWorkflow(
	t *testing.T, f *currentNodeRunnerFixture, workflowID runtimeids.WorkflowID, client currentNodeRunnerClient,
) (runtimeids.SessionID, workflow.CurrentNodeReference, sessionruntime.RuntimeAttachment) {
	task := f.createTask(t, workflowID)
	mustRetained(t, f.store.LockTaskExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: workflowstore.ExecutionTargetProvenanceResolved},
		Root:     workflowstore.ExecutionRoot{SourceWorkspaceID: f.workspaceID, SourceWorkspaceRoot: f.workspace},
	}), "lock Task execution target: %v")
	started, err := f.store.StartTask(context.Background(), task.ID)
	mustRetained(t, err, "start Task: %v")
	reference := started.Mutation.Created[0].Reference
	store, err := session.Create(filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"), "sessions", f.workspace, sessioncontract.SessionCategoryMain, f.starter.storeOptions...)
	mustRetained(t, err, "create retained Session: %v")
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	mustRetained(t, err, "parse retained Session ID: %v")
	_, err = f.store.BindSessionToCurrentNode(context.Background(), workflowstore.CurrentNodeSessionBindingRequest{Association: workflowstore.TaskSessionAssociationRequest{SessionID: sessionID, CurrentNode: reference, AssociatedAt: time.Now().UTC()}})
	mustRetained(t, err, "bind retained Session: %v")
	mustRetained(t, f.store.InterruptCurrentNode(context.Background(), reference, workflow.CurrentNodeInterruptionReason("test_retained_continuation"), workflow.CurrentNodeInterruptionDetail{Code: "test_retained_continuation"}), "interrupt retained Current Node: %v")
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	mustRetained(t, err, "resolve retained Current Node: %v")
	attachment, binding := openRetainedCurrentNodeRuntime(t, f, input, reference, sessionID, client, "retained-continuation-boundary-test")
	t.Cleanup(func() {
		_ = binding.Close()
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
	})
	mustRetained(t, f.authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *agentruntime.Engine) error { return engine.CompactContext(ctx, "") }), "compact retained Session: %v")
	mustRetained(t, f.authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *agentruntime.Engine) error {
		return engine.RunWhenIdle(ctx, agentruntime.ActiveKindRuntimeMaintenance, func() error { return nil })
	}), "wait for retained Session compaction: %v")
	mustRetained(t, binding.Close(), "close dormant Workflow binding: %v")
	return sessionID, reference, attachment
}

func openRetainedCurrentNodeRuntime(
	t *testing.T, f *currentNodeRunnerFixture, input workflowstore.CurrentNodeStartContext,
	reference workflow.CurrentNodeReference, sessionID runtimeids.SessionID, client currentNodeRunnerClient, owner string,
) (sessionruntime.RuntimeAttachment, *agentruntime.CurrentNodeExecutionBinding) {
	planner := launch.Planner{Config: f.cfg, ContainerDir: filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"), StoreOptions: f.starter.storeOptions, PersistedSessions: f.metadata, ExecutionTargets: f.metadata, ProjectWorkspaceBoundary: f.metadata}
	plan, err := planner.PlanSession(context.Background(), launch.SessionRequest{Mode: launch.ModeHeadless, Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID)})
	mustRetained(t, err, "plan retained Session: %v")
	runtimePlan, err := f.starter.buildCurrentNodeAgentRuntimePlan(input, preparedCurrentNodeAgentSession{root: *input.ExecutionRoot, plan: plan, client: client, mode: workflowruntime.CompletionModeTool}, nil)
	mustRetained(t, err, "build retained runtime plan: %v")
	attachment, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{SessionID: sessionID, OwnerID: owner, Runtime: &runtimePlan})
	mustRetained(t, err, "open retained runtime: %v")
	var binding *agentruntime.CurrentNodeExecutionBinding
	err = f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		var err error
		binding, err = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{ScopeID: runtimeids.NewExecutionScopeID(), CompletionMode: workflowruntime.CompletionModeTool, Instructions: workflowruntime.TaskInstructions{WorkflowID: input.Workflow.ID, CurrentNode: reference}})
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
	sessionID, reference, _ := prepareRetainedSessionForWorkflow(t, f, createCurrentNodeAgentWorkflow(t, f.store), order)
	order.sessionID, order.expectedReference = sessionID, reference
	return f, order, sessionID
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesSelectedProvider(t *testing.T) {
	for _, headless := range []bool{false, true} {
		t.Run(map[bool]string{false: "TUI", true: "headless"}[headless], func(t *testing.T) {
			f, order, sessionID := newRetainedAssignmentFixture(t)
			if headless {
				_, err := runPromptClientForCurrentNodeFixture(f, f.controller).RunPrompt(context.Background(),
					serverapi.RunPromptRequest{Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID), Prompt: "continue"}, nil)
				mustRetained(t, err, "RunPrompt: %v")
			} else {
				service := runtimecontrol.NewService(f.authority).WithWorkflowSessionReactivator(f.controller).
					WithPromptHistoryStore(f.metadata).WithPersistedSessionResolver(f.metadata)
				_, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
					SessionID: sessionID.String(), Input: runtimeinput.Text("continue"),
				})
				mustRetained(t, err, "SubmitUserTurn: %v")
			}
			if len(order.Requests()) == 0 || !requestContainsMessage(order.Requests()[0], "continue") {
				t.Fatalf("provider requests = %+v, want selected continuation input", order.Requests())
			}
		})
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
			sessionID, reference, _ := prepareRetainedSessionForWorkflow(t, f, createCurrentNodeAgentWorkflow(t, f.store), base)
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
			if history := retainedPromptHistory(t, f, sessionID); len(history) != 0 {
				t.Fatalf("rejected prompt history = %v, want none", history)
			}
		})
	}
}

func TestRetainedRunPromptRejectsLockedContractAssertionBeforeDelivery(t *testing.T) {
	client := NewCompactingScriptedClient(llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}, nil)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	sessionID, _, _ := prepareRetainedSessionForWorkflow(t, f, createCurrentNodeAgentWorkflow(t, f.store), client)
	runClient := runPromptClientForCurrentNodeFixture(f, f.controller)
	_, err := runClient.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		Intent:    serverapi.OpenExistingSessionLaunchIntent(sessionID),
		Prompt:    "continue",
		Overrides: serverapi.RunPromptOverrides{Model: "conflicting-model"},
	}, nil)
	if err == nil {
		t.Fatal("RunPrompt accepted a conflicting retained Workflow model assertion")
	}
	if len(client.Requests()) != 0 {
		t.Fatalf("conflicting retained Workflow provider requests = %d, want none", len(client.Requests()))
	}
	if history := retainedPromptHistory(t, f, sessionID); len(history) != 0 {
		t.Fatalf("conflicting retained Workflow prompt history = %v, want none", history)
	}
}

func TestRetainedRunPromptThinkingOverridePersistsAndAffectsExecution(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "live"}[live], func(t *testing.T) {
			client := NewCompactingScriptedClient(llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true, SupportsResponsesCompact: true},
				[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("pre-activation"), workflowPostCompletionCompactionResponse("selected")},
				ScriptedToolBatch("selected", llm.ToolCall{ID: "selected", Name: "complete_node", Input: []byte(`{"transition":"next"}`)}))
			f := newCurrentNodeRunnerFixtureWithClient(t, client)
			f.cfg.Settings.Workflow.CompletionMode, f.starter.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeTool, config.WorkflowCompletionModeTool
			f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
			threshold := 1
			f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
			f.cfg.Settings.ProviderOverride, f.starter.cfg.Settings.ProviderOverride = "openai", "openai"
			f.cfg.Settings.OpenAIBaseURL, f.starter.cfg.Settings.OpenAIBaseURL = "http://unused.invalid", "http://unused.invalid"
			workflowID := createCurrentNodeTwoStepWorkflowWithTransition(t, f.store, "Retained Thinking post-turn compaction",
				currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Current."}, currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Next."},
				currentNodeLinearTransition{id: "next", mode: workflow.ContextModeCompactAndContinueSession, requiresApproval: true})
			sessionID, reference, _ := prepareRetainedSessionForWorkflow(t, f, workflowID, client)
			setupCompactions := len(client.CompactionCalls())
			if live {
				var binding *agentruntime.CurrentNodeExecutionBinding
				err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
					var err error
					binding, err = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
						ScopeID: runtimeids.NewExecutionScopeID(), CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
						Instructions: workflowruntime.TaskInstructions{CurrentNode: reference},
					})
					return err
				})
				mustRetained(t, err, "bind live retained Thinking Session: %v")
				mustRetained(t, binding.Close(), "close live retained Thinking Session: %v")
			}
			level := "high"
			err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(callbackCtx context.Context, engine *agentruntime.Engine) error {
				return engine.SetThinkingLevel(callbackCtx, level)
			})
			mustRetained(t, err, "set retained Thinking: %v")
			var progress []serverapi.RunPromptProgress
			_, err = runPromptClientForCurrentNodeFixture(f, f.controller).RunPrompt(context.Background(),
				serverapi.RunPromptRequest{Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID), Prompt: "continue"},
				serverapi.RunPromptProgressFunc(func(event serverapi.RunPromptProgress) { progress = append(progress, event) }))
			mustRetained(t, err, "retained Thinking Run Prompt: %v")
			f.waitForTaskQuiescence(t, reference.TaskID)
			compactionStarted := slices.ContainsFunc(progress, func(event serverapi.RunPromptProgress) bool {
				return event.Kind == serverapi.RunPromptProgressKindCompactionStarted
			})
			if len(client.CompactionCalls()) != setupCompactions+1 || !compactionStarted {
				t.Fatalf("selected post-turn compaction = calls %d baseline %d progress %+v, want one selected compaction", len(client.CompactionCalls()), setupCompactions, progress)
			}
			requests := client.Requests()
			if len(requests) != 1 || requests[0].ReasoningEffort != level {
				t.Fatalf("retained Thinking request = %+v, want reasoning effort %q", requests, level)
			}
			record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID.String())
			mustRetained(t, err, "resolve retained Thinking Session: %v")
			if record.Meta == nil || record.Meta.ChatSettings == nil || record.Meta.ChatSettings.Thinking == nil ||
				*record.Meta.ChatSettings.Thinking != level {
				t.Fatalf("retained Chat settings = %+v, want Thinking %q", record.Meta, level)
			}
		})
	}
}

type retainedFanoutScenario struct {
	name                                                                                               string
	selectedResumeError, siblingError, historyError, selectedExecutionError, selectedFinalizationError error
	selectedFinal                                                                                      string
	cancel, cancelBeforeResume, selectedDeliveryRace, tui, wantWarnings, resumeLock, historyLock       bool
	backgroundProgress, pendingInput                                                                   bool
}

func TestRetainedSiblingResumeWaitsForFileBackedMetadataLock(t *testing.T) {
	for _, scenario := range []retainedFanoutScenario{
		{name: "selected pending input", pendingInput: true}, {name: "selected assistant final", selectedFinal: "selected final"},
		{name: "selected excludes same-resource background progress", backgroundProgress: true},
		{name: "headless selected success sibling failure", siblingError: errors.New("sibling Resume failed"), wantWarnings: true}, {name: "TUI selected success sibling failure", siblingError: errors.New("sibling Resume failed"), tui: true},
		{name: "selected delivery failure sibling success", selectedDeliveryRace: true}, {name: "selected failure sibling success", selectedResumeError: errors.New("selected Resume failed")},
		{name: "history failure after selected success", historyError: errors.New("prompt history failed")}, {name: "history failure after selected execution failure", historyError: errors.New("prompt history failed"), selectedExecutionError: errors.New("selected execution failed")},
		{name: "selected turn and exact finalization failures", selectedExecutionError: errors.New("selected turn failed"), selectedFinalizationError: errors.New("exact finalization failed")},
		{name: "TUI cancellation before selected Resume", cancelBeforeResume: true, tui: true}, {name: "TUI cancellation after acceptance", cancel: true, tui: true},
		{name: "sibling Resume contention", resumeLock: true}, {name: "prompt history contention", historyLock: true},
	} {
		t.Run(scenario.name, func(t *testing.T) { runRetainedFanoutScenario(t, scenario) })
	}
}

type retainedOperationResult struct {
	response    serverapi.RunPromptResponse
	tuiResponse serverapi.RuntimeSubmitUserTurnResponse
	err         error
}

func retainedWait(t *testing.T, wait <-chan struct{}, message string) {
	select {
	case <-wait:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal(message)
	}
}

func runRetainedFanoutScenario(t *testing.T, scenario retainedFanoutScenario) {
	var history *retainedDelayedPromptHistoryStore
	var selectedStarted, selectedRelease chan struct{}
	if scenario.historyError != nil {
		history = &retainedDelayedPromptHistoryStore{started: make(chan struct{}), release: make(chan struct{}), err: scenario.historyError}
	}
	if scenario.historyError != nil || scenario.cancel || scenario.resumeLock || scenario.historyLock || scenario.backgroundProgress || scenario.pendingInput {
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
	client.transitionByAssignment = map[string]string{
		workflowruntime.CurrentNodePromptIdentity(branches[0].Reference): "join_a",
		workflowruntime.CurrentNodePromptIdentity(branches[1].Reference): "join_b",
	}
	if scenario.selectedFinalizationError != nil {
		*f.stepLifecycleFailure = scenario.selectedFinalizationError
		f.stepLifecycleFailureSession = &selectedSession
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
	attempted := make(chan workflow.CurrentNodeReference, len(branches))
	lockReference := selected.Reference
	if scenario.resumeLock {
		lockReference = branches[1].Reference
	}
	mustRetained(t, f.controller.Close(), "close initial Current Node controller: %v")
	controller, err := workflowexecution.NewCurrentNodeController(&retainedResumeLockStore{
		Store: f.store, Selected: selected.Reference, LockReference: lockReference,
		SelectedResumeError: scenario.selectedResumeError, LockBeforeSibling: scenario.resumeLock,
		FailSibling: scenario.siblingError != nil, SelectedDeliveryRace: scenario.selectedDeliveryRace,
		PersistenceRoot: f.cfg.PersistenceRoot,
		LockReady:       lockReady, Attempted: attempted, Test: t,
	}, f.starter, f.authority, workflowexecution.NewTaskMutationCoordinator(),
		workflowexecution.CurrentNodeControllerConfig{AgentConcurrency: 2, AssignmentSteerer: f.starter})
	mustRetained(t, err, "new lock-aware Current Node controller: %v")
	f.controller = controller
	var historyStore runtimecontrol.PromptHistoryStore = f.metadata
	var historyLockStore *retainedPromptHistoryLockStore
	if scenario.historyLock {
		historyLockStore = &retainedPromptHistoryLockStore{
			Store: f.metadata, PersistenceRoot: f.cfg.PersistenceRoot, LockReady: lockReady,
			Attempted: make(chan struct{}, 1), Test: t,
		}
		historyStore = historyLockStore
	}
	if history != nil {
		historyStore = history
	}
	runClient := runPromptClientForCurrentNodeFixture(f, f.controller, historyStore)
	tuiService := runtimecontrol.NewService(f.authority).WithWorkflowSessionReactivator(f.controller).
		WithPromptHistoryStore(historyStore).WithPersistedSessionResolver(f.metadata)
	var selectedEngine *agentruntime.Engine
	if err := f.authority.WithRetainedWorkflowRuntime(context.Background(), selectedSession, func(_ context.Context, engine *agentruntime.Engine) error {
		selectedEngine = engine
		return nil
	}); err != nil {
		t.Fatalf("selected branch retained runtime: %v", err)
	}
	var backgroundDone chan error
	var releaseBackground func()
	if scenario.backgroundProgress {
		client.backgroundFirst = make(chan struct{})
		client.backgroundSecond = make(chan struct{})
		client.backgroundRelease = make(chan struct{})
		backgroundDone = make(chan error, 1)
		releaseBackground = func() {
			select {
			case <-client.backgroundRelease:
			default:
				close(client.backgroundRelease)
			}
		}
		t.Cleanup(releaseBackground)
		go func() {
			backgroundDone <- f.authority.WithCurrentRuntime(context.Background(), selectedSession, func(
				ctx context.Context, engine *agentruntime.Engine,
			) error {
				return engine.RunBackgroundShellContinuation(ctx, agentruntime.BackgroundShellEvent{
					Type: agentruntime.BackgroundShellEventCompleted, ID: "background",
					ActivityID: uuid.New(), NoticeText: "background notice",
				})
			})
		}()
		retainedWait(t, client.backgroundFirst, "background did not reach first provider request")
		retainedWait(t, client.backgroundSecond, "background did not reach second provider request")
	}
	requestCountBeforeSubmit := len(client.Requests())
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if scenario.cancelBeforeResume {
		cancel()
	}
	var progress []serverapi.RunPromptProgress
	done := make(chan retainedOperationResult, 1)
	timeout := 2 * time.Second
	if scenario.resumeLock || scenario.historyLock {
		timeout = time.Second
	}
	if scenario.historyError != nil {
		timeout = 500 * time.Millisecond
	}
	startedAt := time.Now()
	go func() {
		if scenario.tui {
			response, err := tuiService.SubmitUserTurn(runCtx, serverapi.RuntimeSubmitUserTurnRequest{SessionID: selectedSession.String(), Input: runtimeinput.Text("continue")})
			done <- retainedOperationResult{tuiResponse: response, err: err}
			return
		}
		response, err := runClient.RunPrompt(runCtx, serverapi.RunPromptRequest{Intent: serverapi.OpenExistingSessionLaunchIntent(selectedSession), Prompt: "continue", Timeout: timeout},
			serverapi.RunPromptProgressFunc(func(event serverapi.RunPromptProgress) {
				progress = append(progress, event)
			}))
		done <- retainedOperationResult{response: response, err: err}
	}()
	if scenario.backgroundProgress {
		deadline := time.After(currentNodeRunnerWait)
		for {
			if _, active := f.authority.SessionExecution(selectedSession); active {
				break
			}
			select {
			case <-time.After(time.Millisecond):
			case <-deadline:
				t.Fatal("selected retained execution did not become live while background was active")
			}
		}
		releaseBackground()
		if err := <-backgroundDone; err == nil {
			t.Fatal("background continuation unexpectedly succeeded")
		}
		releaseSelected()
	} else if scenario.pendingInput {
		retainedWait(t, selectedStarted, "selected continuation did not reach provider")
		queueCtx, cancelQueue := context.WithTimeout(context.Background(), currentNodeRunnerWait)
		olderInputDone := make(chan error, 1)
		go func() {
			_, err := selectedEngine.QueueUserMessageForAutoDrain(queueCtx, "older pending input")
			olderInputDone <- err
		}()
		deadline := time.After(currentNodeRunnerWait)
		for !selectedEngine.HasPendingRuntimeOperations() {
			select {
			case <-time.After(time.Millisecond):
			case <-deadline:
				cancelQueue()
				t.Fatal("older selected input was not admitted while selected provider was held")
			}
		}
		releaseSelected()
		if err := <-olderInputDone; err != nil {
			cancelQueue()
			t.Fatalf("queue older selected input while selected provider was held: %v", err)
		}
		cancelQueue()
	}
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
		case <-time.After(1500 * time.Millisecond):
		}
	}
	var result retainedOperationResult
	if scenario.cancelBeforeResume {
		result = <-done
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("pre-Resume cancellation error = %v, want cancellation", result.err)
		}
		if result.tuiResponse != (serverapi.RuntimeSubmitUserTurnResponse{}) {
			t.Fatalf("pre-Resume cancellation response = %+v, want empty", result.tuiResponse)
		}
		if len(client.Requests()) != requestCountBeforeSubmit {
			t.Fatalf("pre-Resume cancellation provider requests = %d, want %d", len(client.Requests()), requestCountBeforeSubmit)
		}
		if historyRecords := retainedPromptHistory(t, f, selectedSession); len(historyRecords) != 0 {
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
		retainedWait(t, selectedStarted, "selected continuation did not reach provider")
		cancel()
		release()
		releaseSelected()
	} else if scenario.resumeLock {
		release()
		retainedWait(t, selectedStarted, "selected continuation did not reach provider after Resume contention")
		select {
		case result := <-done:
			t.Fatalf("continuation returned before selected result release: %+v", result)
		case <-time.After(100 * time.Millisecond):
		}
		releaseSelected()
	} else if scenario.historyError != nil {
		release()
		retainedWait(t, history.started, "prompt history did not start")
		select {
		case result := <-done:
			t.Fatalf("continuation returned while history was pending: %+v", result)
		case <-time.After(600 * time.Millisecond):
		}
		close(history.release)
		retainedWait(t, selectedStarted, "selected execution did not start after history release")
		releaseSelected()
	} else if scenario.historyLock {
		retainedWait(t, historyLockStore.Attempted, "prompt history did not reach store")
		release()
		retainedWait(t, selectedStarted, "selected continuation did not reach provider after history contention")
		select {
		case result := <-done:
			t.Fatalf("continuation returned before selected result release: %+v", result)
		case <-time.After(100 * time.Millisecond):
		}
		releaseSelected()
	} else {
		release()
	}
	result = <-done
	if scenario.resumeLock || scenario.historyLock {
		if elapsed := time.Since(startedAt); elapsed <= timeout {
			t.Fatalf("contention elapsed time = %s, want beyond requested timeout %s", elapsed, timeout)
		}
	}
	if scenario.cancel {
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want caller cancellation", result.err)
		}
		if !scenario.tui {
			t.Fatal("cancellation scenario must exercise TUI boundary")
		}
		foundInput := slices.ContainsFunc(client.Requests(), func(request llm.Request) bool { return requestContainsMessage(request, "continue") })
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
	} else if scenario.selectedDeliveryRace || scenario.selectedResumeError != nil {
		if result.err == nil {
			t.Fatal("selected failure was not returned")
		}
		if scenario.selectedResumeError != nil && !errors.Is(result.err, scenario.selectedResumeError) {
			t.Fatalf("selected Resume error = %v", result.err)
		}
	} else if result.err != nil && scenario.selectedFinalizationError == nil {
		t.Fatalf("continuation: %v", result.err)
	}
	if scenario.selectedFinalizationError != nil && (!errors.Is(result.err, scenario.selectedExecutionError) || !errors.Is(result.err, scenario.selectedFinalizationError)) {
		t.Fatalf("selected result errors = %v, want turn and exact finalization causes", result.err)
	}
	if scenario.selectedFinal != "" && result.response.Result != scenario.selectedFinal {
		t.Fatalf("selected result = %q, want %q", result.response.Result, scenario.selectedFinal)
	}
	if !scenario.tui && (len(result.response.Warnings) > 0) != scenario.wantWarnings {
		t.Fatalf("warnings = %v, want=%t", result.response.Warnings, scenario.wantWarnings)
	}
	expectedKeys := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(branches))
	for _, branch := range branches {
		key, err := branch.Reference.Key()
		mustRetained(t, err, "key branch reference: %v")
		expectedKeys[key] = struct{}{}
	}
	for range branches {
		select {
		case reference := <-attempted:
			key, err := reference.Key()
			mustRetained(t, err, "key attempted Resume reference: %v")
			if _, exists := expectedKeys[key]; !exists {
				t.Fatalf("duplicate attempted Resume reference = %v", reference)
			}
			delete(expectedKeys, key)
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("retained branch Resume was not attempted")
		}
	}
	if len(expectedKeys) != 0 {
		t.Fatalf("attempted Resume references missing = %v", expectedKeys)
	}
	f.waitForTaskQuiescence(t, task.ID)
	if scenario.siblingError != nil || scenario.selectedResumeError != nil ||
		scenario.selectedDeliveryRace || scenario.resumeLock || scenario.selectedFinalizationError != nil {
		nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
		mustRetained(t, err, "read retained branch outcomes: %v")
		hasCurrent := func(reference workflow.CurrentNodeReference) bool {
			for _, node := range nodes {
				if node.Reference.Equal(reference) {
					return true
				}
			}
			return false
		}
		switch {
		case scenario.siblingError != nil:
			if hasCurrent(selected.Reference) || !hasCurrent(branches[1].Reference) {
				t.Fatalf("sibling-failure branch Current Nodes = %+v, want selected completed and sibling retained", nodes)
			}
		case scenario.selectedResumeError != nil, scenario.selectedFinalizationError != nil:
			if !hasCurrent(selected.Reference) || hasCurrent(branches[1].Reference) {
				t.Fatalf("selected-failure branch Current Nodes = %+v, want selected retained and sibling completed", nodes)
			}
		case scenario.selectedDeliveryRace, scenario.resumeLock:
			if hasCurrent(selected.Reference) || hasCurrent(branches[1].Reference) {
				t.Fatalf("successful branch Current Nodes = %+v, want both branches completed", nodes)
			}
		}
	}
	if scenario.pendingInput || scenario.backgroundProgress {
		if !hasAssistantProgress(progress, "selected progress") ||
			hasAssistantProgress(progress, "sibling progress") || hasAssistantProgress(progress, "older progress") ||
			hasAssistantProgress(progress, "background progress") {
			t.Fatalf("selected progress = %+v, want selected-only", progress)
		}
	}
	if scenario.pendingInput {
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
	if scenario.selectedDeliveryRace || scenario.selectedResumeError != nil {
		for _, request := range client.Requests() {
			if requestContainsMessage(request, "continue") {
				t.Fatalf("rejected selected input reached provider: %+v", request)
			}
		}
	}
	if scenario.selectedResumeError != nil {
		if historyRecords := retainedPromptHistory(t, f, selectedSession); len(historyRecords) != 0 {
			t.Fatalf("rejected selected prompt history = %v, want none", historyRecords)
		}
	} else if scenario.historyError == nil {
		historyRecords := retainedPromptHistory(t, f, selectedSession)
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
	SelectedDeliveryRace           bool
	PersistenceRoot                string
	LockReady                      chan<- func()
	Attempted                      chan<- workflow.CurrentNodeReference
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
	if s.FailSibling && !reference.Equal(s.Selected) {
		return workflowstore.InterruptedCurrentNodeAttentionProjection{}, false, errors.New("sibling Resume failed")
	}
	projection, found, err := s.Store.ResumeCurrentNode(ctx, reference)
	if err != nil {
		return projection, found, err
	}
	if s.SelectedDeliveryRace && reference.Equal(s.Selected) {
		transition := "join_a"
		if branch, ok := reference.TransitionBranchKey(); ok && branch == "branch_b" {
			transition = "join_b"
		}
		if _, err := s.Store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
			Source: reference, TransitionID: transition,
		}); err != nil {
			return projection, false, err
		}
	}
	return projection, found, nil
}

type retainedPromptHistoryLockStore struct {
	*metadata.Store
	PersistenceRoot string
	LockReady       chan<- func()
	Attempted       chan struct{}
	Test            testing.TB
}

func (s *retainedPromptHistoryLockStore) RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error) {
	release := testsqlite.AcquireWriteLock(s.Test, s.PersistenceRoot)
	s.LockReady <- release
	s.Attempted <- struct{}{}
	defer release()
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
	mu                     sync.Mutex
	requests               []llm.Request
	generations            int
	transitionByAssignment map[string]string
	selectedError          error
	selectedFinal          string
	selectedCompletes      bool
	selectedStarted        chan<- struct{}
	selectedRelease        <-chan struct{}
	selectedGate           sync.Once
	backgroundCalls        int
	backgroundFirst        chan struct{}
	backgroundSecond       chan struct{}
	backgroundRelease      chan struct{}
	backgroundFirstGate    sync.Once
	backgroundSecondGate   sync.Once
}

func (c *retainedProgressClient) Generate(_ context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	selected := requestContainsMessage(request, "continue")
	background := !selected && slices.ContainsFunc(llm.MessagesFromItems(request.Items), func(message llm.Message) bool {
		return message.MessageType != nil && *message.MessageType == llm.MessageTypeBackgroundNotice
	})
	transition := ""
	for _, assignment := range workflowAssignments(request) {
		if candidate, ok := c.transitionByAssignment[assignment.sourcePath]; ok {
			transition = candidate
			break
		}
	}
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.generations++
	generation := c.generations
	backgroundCall := 0
	if background {
		c.backgroundCalls++
		backgroundCall = c.backgroundCalls
	}
	c.mu.Unlock()
	if background {
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
			return retainedFinalResponse(c.selectedFinal, llm.ToolCall{ID: "selected-final", Name: "complete_node", Input: []byte(retainedCompletionInput(request, transition, c.selectedFinal))}), nil
		}
		if c.selectedCompletes {
			return retainedResponse(content, llm.ToolCall{ID: "selected-complete", Name: "complete_node", Input: []byte(retainedCompletionInput(request, transition, content))}), nil
		}
		return retainedResponse(content), nil
	}
	return retainedResponse(content, llm.ToolCall{ID: "complete", Name: "complete_node", Input: []byte(retainedCompletionInput(request, transition, content))}), nil
}

func retainedResponse(content string, calls ...llm.ToolCall) llm.Response {
	return retainedPhaseResponse(llm.MessagePhaseCommentary, content, calls...)
}

func retainedFinalResponse(content string, calls ...llm.ToolCall) llm.Response {
	return retainedPhaseResponse(llm.MessagePhaseFinal, content, calls...)
}

func retainedPhaseResponse(phase llm.MessagePhase, content string, calls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(phase), Content: textutil.Value(content)},
		ToolCalls: calls, Usage: llm.Usage{InputTokens: 200_000, WindowTokens: 200_000},
	}
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
	return slices.ContainsFunc(llm.MessagesFromItems(request.Items), func(message llm.Message) bool { return message.Content != nil && *message.Content == content })
}

func retainedCompletionInput(_ llm.Request, transition, commentary string) string {
	return `{"transition":"` + transition + `","commentary":"` + commentary + `"}`
}

func hasAssistantProgress(progress []serverapi.RunPromptProgress, content string) bool {
	return slices.ContainsFunc(progress, func(event serverapi.RunPromptProgress) bool {
		return event.AssistantMessage != nil && event.AssistantMessage.Content == content
	})
}

func runPromptClientForCurrentNodeFixture(
	f *currentNodeRunnerFixture, controller *workflowexecution.CurrentNodeController, histories ...runtimecontrol.PromptHistoryStore,
) apicontract.RunPromptService {
	var history runtimecontrol.PromptHistoryStore = f.metadata
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
