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

type retainedAssignmentOrderClient struct {
	*compactingScriptedClient
	fixture           *currentNodeRunnerFixture
	sessionID         runtimeids.SessionID
	expectedReference workflow.CurrentNodeReference
}

func (c *retainedAssignmentOrderClient) Generate(
	ctx context.Context,
	request llm.Request,
	callbacks llm.StreamCallbacks,
) (llm.Response, error) {
	matched, err := c.fixture.workflowAssignmentMatchesForTest(c.sessionID, c.expectedReference)
	if err != nil {
		return llm.Response{}, err
	}
	if !matched {
		return llm.Response{}, errors.New("provider called before durable Workflow assignment")
	}
	return c.compactingScriptedClient.Generate(ctx, request, callbacks)
}
func (f *currentNodeRunnerFixture) workflowAssignmentMatchesForTest(
	sessionID runtimeids.SessionID,
	expected workflow.CurrentNodeReference,
) (bool, error) {
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID.String())
	if err != nil || record.Meta == nil {
		return false, err
	}
	store, err := session.Open(record.SessionDir, f.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		return false, err
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return false, err
	}
	window, err := eventLog.ReadRecentRecords(128)
	if err != nil {
		return false, err
	}
	for _, event := range window.Records {
		payload, payloadErr := event.Payload()
		if payloadErr != nil {
			return false, payloadErr
		}
		message, ok := payload.(session.MessageRecord)
		if !ok || message.MessageType == nil ||
			*message.MessageType != session.MessageTypeWorkflowMode || message.SourcePath == nil {
			continue
		}
		if *message.SourcePath == workflowruntime.CurrentNodePromptIdentity(expected) {
			return true, nil
		}
	}
	return false, nil
}
func prepareCompactedRetainedWorkflowSession(
	t *testing.T,
	f *currentNodeRunnerFixture,
	client currentNodeRunnerClient,
) (runtimeids.SessionID, workflow.CurrentNodeReference, sessionruntime.RuntimeAttachment) {
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	if err := f.store.LockTaskExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   f.workspaceID,
			SourceWorkspaceRoot: f.workspace,
		},
	}); err != nil {
		t.Fatalf("lock Task execution target: %v", err)
	}
	started, err := f.store.StartTask(context.Background(), task.ID)
	mustRetained(t, err, "start Task: %v")
	reference := started.Mutation.Created[0].Reference
	sessionStore, err := session.Create(
		filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		"sessions",
		f.workspace,
		sessioncontract.SessionCategoryMain,
		f.starter.storeOptions...,
	)
	mustRetained(t, err, "create retained Session: %v")
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	mustRetained(t, err, "parse retained Session ID: %v")
	if _, err := f.store.BindSessionToCurrentNode(context.Background(), workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  reference,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("bind retained Session: %v", err)
	}
	if err := f.store.InterruptCurrentNode(
		context.Background(),
		reference,
		workflow.CurrentNodeInterruptionReason("test_retained_continuation"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_retained_continuation"},
	); err != nil {
		t.Fatalf("interrupt retained Current Node: %v", err)
	}
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	if err != nil || input.ExecutionRoot == nil {
		t.Fatalf("resolve retained Current Node start context: %v", err)
	}
	attachment, binding := openRetainedCurrentNodeRuntime(t, f, input, reference, sessionID, client, "retained-continuation-boundary-test")
	t.Cleanup(func() {
		_ = binding.Close()
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
	})
	if err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *agentruntime.Engine) error {
		return engine.CompactContext(ctx, "")
	}); err != nil {
		t.Fatalf("compact retained Session before first activation: %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("close dormant Workflow execution binding: %v", err)
	}
	return sessionID, reference, attachment
}
func attachRetainedCurrentNodeRuntime(
	t *testing.T,
	f *currentNodeRunnerFixture,
	reference workflow.CurrentNodeReference,
) func() {
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	if err != nil || input.ExecutionRoot == nil {
		t.Fatalf("resolve retained Current Node start context: %v", err)
	}
	sessionID := *input.CurrentNode.SessionID
	attachment, binding := openRetainedCurrentNodeRuntime(t, f, input, reference, sessionID, f.client, "retained-sibling-contention-test")
	if err := binding.Close(); err != nil {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		t.Fatalf("close dormant retained branch binding: %v", err)
	}
	return func() {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
	}
}
func openRetainedCurrentNodeRuntime(
	t *testing.T,
	f *currentNodeRunnerFixture,
	input workflowstore.CurrentNodeStartContext,
	reference workflow.CurrentNodeReference,
	sessionID runtimeids.SessionID,
	client currentNodeRunnerClient,
	owner string,
) (sessionruntime.RuntimeAttachment, *agentruntime.CurrentNodeExecutionBinding) {
	plan, err := (launch.Planner{
		Config:                   f.cfg,
		ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		StoreOptions:             f.starter.storeOptions,
		PersistedSessions:        f.metadata,
		ExecutionTargets:         f.metadata,
		ProjectWorkspaceBoundary: f.metadata,
	}).PlanSession(context.Background(), launch.SessionRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
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
	if err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		var bindErr error
		binding, bindErr = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Instructions:   workflowruntime.TaskInstructions{WorkflowID: input.Workflow.ID, CurrentNode: reference},
		})
		return bindErr
	}); err != nil {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		t.Fatalf("bind retained Current Node execution: %v", err)
	}
	return attachment, binding
}
func newRetainedAssignmentFixture(
	t *testing.T,
) (*currentNodeRunnerFixture, *retainedAssignmentOrderClient, runtimeids.SessionID) {
	base := NewCompactingScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("pre-activation")},
		ScriptedToolBatch("complete", llm.ToolCall{
			ID:    "complete",
			Name:  "complete_node",
			Input: []byte(`{"transition":"done"}`),
		}),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, base)
	order := &retainedAssignmentOrderClient{compactingScriptedClient: base, fixture: f}
	f.client = order
	sessionID, reference, _ := prepareCompactedRetainedWorkflowSession(t, f, order)
	order.sessionID = sessionID
	order.expectedReference = reference
	return f, order, sessionID
}
func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesTUISteerProvider(t *testing.T) {
	f, order, sessionID := newRetainedAssignmentFixture(t)
	service := runtimecontrol.NewService(f.authority).
		WithWorkflowSessionReactivator(f.controller).
		WithPromptHistoryStore(f.metadata).
		WithPersistedSessionResolver(f.metadata)
	response, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: sessionID.String(),
		Input:     runtimeinput.Text("continue"),
	})
	mustRetained(t, err, "SubmitUserTurn: %v")
	if requests := order.Requests(); len(requests) == 0 || !requestContainsMessage(requests[0], "continue") {
		t.Fatalf("TUI provider request = %+v, want selected continuation input", requests)
	}
	if response.ResultKind != clientui.UserTurnResultKindNoFinal {
		t.Fatalf("SubmitUserTurn response = %+v, want no-final result", response)
	}
}
func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesHeadlessContinueProvider(t *testing.T) {
	f, _, sessionID := newRetainedAssignmentFixture(t)
	client := runPromptClientForCurrentNodeFixture(f, f.controller)
	_, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
		Prompt: "continue",
	}, nil)
	mustRetained(t, err, "RunPrompt: %v")
}
func TestRetainedSiblingResumeWaitsForFileBackedMetadataLock(t *testing.T) {
	for _, scenario := range []retainedFanoutScenario{
		{name: "all success"},
		{
			name:         "selected success sibling failure",
			siblingError: errors.New("sibling Resume failed"),
			wantWarnings: true,
		},
		{
			name:          "selected failure sibling success",
			selectedError: errors.New("selected execution failed"),
		},
		{
			name:         "history failure after selected success",
			historyError: errors.New("prompt history failed"),
		},
		{
			name:                   "history failure after selected execution failure",
			historyError:           errors.New("prompt history failed"),
			selectedExecutionError: errors.New("selected execution failed"),
		},
		{
			name:   "caller cancellation after acceptance",
			cancel: true,
		},
		{name: "prompt history contention", historyLock: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runRetainedFanoutScenario(t, scenario)
		})
	}
}

type retainedFanoutScenario struct {
	name                   string
	selectedError          error
	siblingError           error
	cancel                 bool
	wantWarnings           bool
	historyLock            bool
	historyError           error
	selectedExecutionError error
}

func runRetainedFanoutScenario(t *testing.T, scenario retainedFanoutScenario) {
	var history *retainedDelayedPromptHistoryStore
	var selectedStarted, selectedRelease chan struct{}
	if scenario.historyError != nil {
		history = &retainedDelayedPromptHistoryStore{
			started: make(chan struct{}),
			release: make(chan struct{}),
			err:     scenario.historyError,
		}
		selectedStarted, selectedRelease = make(chan struct{}), make(chan struct{})
	}
	client := &retainedProgressClient{
		selectedError:   scenario.selectedExecutionError,
		selectedStarted: selectedStarted,
		selectedRelease: selectedRelease,
	}
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeTool
	f.starter.cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeTool
	f.cfg.Settings.ProviderOverride = "openai"
	f.cfg.Settings.OpenAIBaseURL = "http://unused.invalid"
	f.starter.cfg.Settings.ProviderOverride = "openai"
	f.starter.cfg.Settings.OpenAIBaseURL = "http://unused.invalid"
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store, false)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	branches := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		if len(nodes) != len(branchNodeIDs) {
			return false
		}
		for _, node := range nodes {
			if node.SessionID == nil ||
				node.Scheduling == nil ||
				node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
				return false
			}
		}
		return true
	})
	selected := branches[0]
	selectedSession := *selected.SessionID
	failureReference := selected.Reference
	if scenario.siblingError != nil {
		failureReference = branches[1].Reference
	}
	if scenario.selectedError != nil || scenario.siblingError != nil {
		originalStore := f.starter.store
		f.starter.store = currentNodeStartContextStore{
			RuntimeStore: originalStore,
			transform: func(input workflowstore.CurrentNodeStartContext) workflowstore.CurrentNodeStartContext {
				if input.CurrentNode.Reference.Equal(failureReference) && input.ExecutionRoot != nil {
					root := *input.ExecutionRoot
					root.SourceWorkspaceID = "workspace-missing"
					input.ExecutionRoot = &root
				}
				return input
			},
		}
	}
	t.Cleanup(attachRetainedCurrentNodeRuntime(t, f, selected.Reference))
	lockReady := make(chan func(), 1)
	attempted := make(chan workflow.CurrentNodeReference, len(branches))
	lockReference := selected.Reference
	if scenario.historyLock {
		lockReference = branches[1].Reference
	}
	mustRetained(t, f.controller.Close(), "close initial Current Node controller: %v")
	lockingStore := &retainedResumeLockStore{
		Store:           f.store,
		Selected:        lockReference,
		FailSibling:     scenario.siblingError != nil,
		PersistenceRoot: f.cfg.PersistenceRoot,
		LockReady:       lockReady,
		Attempted:       attempted,
		Test:            t,
	}
	controller, err := workflowexecution.NewCurrentNodeController(
		lockingStore,
		f.starter,
		f.authority,
		workflowexecution.NewTaskMutationCoordinator(),
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  2,
			AssignmentSteerer: f.starter,
		},
	)
	mustRetained(t, err, "new lock-aware Current Node controller: %v")
	f.controller = controller
	historyStore := interface {
		RecordPromptHistoryEntry(context.Context, metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error)
	}(f.metadata)
	if history != nil {
		historyStore = history
	}
	continuationClient := runPromptClientForCurrentNodeFixture(f, f.controller, historyStore)
	if err := f.authority.WithRetainedWorkflowRuntime(context.Background(), selectedSession, func(context.Context, *agentruntime.Engine) error {
		return nil
	}); err != nil {
		t.Fatalf("selected branch retained runtime: %v", err)
	}
	if err := f.authority.WithCurrentRuntime(context.Background(), selectedSession, func(ctx context.Context, engine *agentruntime.Engine) error {
		_, err := engine.QueueUserMessageForAutoDrain(ctx, "older pending input")
		return err
	}); err != nil {
		t.Fatalf("queue older selected input: %v", err)
	}
	done := make(chan struct {
		response serverapi.RunPromptResponse
		err      error
	}, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var progress []serverapi.RunPromptProgress
	timeout := 2 * time.Second
	if scenario.historyError != nil {
		timeout = 500 * time.Millisecond
	}
	go func() {
		response, err := continuationClient.RunPrompt(
			runCtx,
			serverapi.RunPromptRequest{
				Intent:  serverapi.OpenExistingSessionLaunchIntent(selectedSession),
				Prompt:  "continue",
				Timeout: timeout,
			},
			serverapi.RunPromptProgressFunc(func(event serverapi.RunPromptProgress) {
				progress = append(progress, event)
			}),
		)
		done <- struct {
			response serverapi.RunPromptResponse
			err      error
		}{response: response, err: err}
	}()
	var release func()
	select {
	case release = <-lockReady:
		rawRelease := release
		var releaseOnce sync.Once
		releaseLocked := func() {
			releaseOnce.Do(rawRelease)
		}
		t.Cleanup(releaseLocked)
		release = releaseLocked
	case result := <-done:
		t.Fatalf("RunPrompt completed before selected Resume: response=%+v error=%T %v", result.response, result.err, result.err)
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("selected Resume did not reach the real Run Prompt acceptance boundary")
	}
	select {
	case result := <-done:
		t.Fatalf("RunPrompt returned while sibling Resume was locked: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	if scenario.cancel {
		cancel()
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
			t.Fatalf("RunPrompt returned while history and selected execution were pending: %+v", result)
		case <-time.After(600 * time.Millisecond):
		}
		close(history.release)
		f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
			for _, node := range nodes {
				if !node.Reference.Equal(selected.Reference) &&
					node.Scheduling != nil &&
					node.Scheduling.State == workflow.CurrentNodeSchedulingInterrupted {
					return false
				}
			}
			return true
		})
		close(selectedRelease)
	} else {
		time.Sleep(2200 * time.Millisecond)
		release()
	}
	var result struct {
		response serverapi.RunPromptResponse
		err      error
	}
	select {
	case result = <-done:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("RunPrompt did not finish after metadata lock release")
	}
	if scenario.cancel {
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("RunPrompt error = %v, want caller cancellation", result.err)
		}
	} else if scenario.historyError != nil {
		if !errors.Is(result.err, scenario.historyError) {
			t.Fatalf("RunPrompt error = %v, want prompt history failure", result.err)
		}
		if scenario.selectedExecutionError != nil && !errors.Is(result.err, scenario.selectedExecutionError) {
			t.Fatalf("RunPrompt error = %v, want selected execution failure and history failure; requests=%+v", result.err, client.Requests())
		}
	} else if scenario.selectedError != nil {
		if result.err == nil {
			t.Fatalf("RunPrompt error = %v, want selected failure; provider requests=%d", result.err, len(client.Requests()))
		}
	} else if result.err != nil {
		t.Fatalf("RunPrompt: %v", result.err)
	}
	if (len(result.response.Warnings) > 0) != scenario.wantWarnings {
		t.Fatalf("RunPrompt warnings = %v, want warnings=%t; provider requests=%d", result.response.Warnings, scenario.wantWarnings, len(client.Requests()))
	}
	if scenario.name == "all success" {
		selectedRequests := make([]llm.Request, 0)
		for _, request := range client.Requests() {
			if requestContainsMessage(request, "continue") {
				selectedRequests = append(selectedRequests, request)
			}
		}
		if len(selectedRequests) < 2 ||
			requestContainsMessage(selectedRequests[0], "older pending input") ||
			!requestContainsMessage(selectedRequests[1], "older pending input") {
			t.Fatalf("selected provider requests = %+v, want direct then drained input", selectedRequests)
		}
	}
	attemptedReferences := make([]workflow.CurrentNodeReference, 0, len(branches))
	for range branches {
		select {
		case reference := <-attempted:
			attemptedReferences = append(attemptedReferences, reference)
		case <-time.After(currentNodeRunnerWait):
			t.Fatal("timed out waiting for a retained branch Resume attempt")
		}
	}
	for _, branch := range branches {
		found := false
		for _, reference := range attemptedReferences {
			if reference.Equal(branch.Reference) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Resume attempts = %v, want branch %v", attemptedReferences, branch.Reference)
		}
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		for _, node := range nodes {
			if (scenario.selectedError == nil && scenario.selectedExecutionError == nil && node.Reference.Equal(selected.Reference) ||
				scenario.selectedError != nil && node.Reference.Equal(branches[1].Reference)) &&
				node.Scheduling != nil &&
				node.Scheduling.State == workflow.CurrentNodeSchedulingInterrupted {
				return false
			}
		}
		return true
	})
	if scenario.name == "all success" {
		if !hasAssistantProgress(progress, "selected progress") ||
			hasAssistantProgress(progress, "sibling progress") ||
			hasAssistantProgress(progress, "older progress") {
			t.Fatalf("selected Run Prompt progress = %+v, want selected-only progress", progress)
		}
	}
}

func requestContainsMessage(request llm.Request, content string) bool {
	for _, message := range llm.MessagesFromItems(request.Items) {
		if message.Content != nil && *message.Content == content {
			return true
		}
	}
	return false
}

type retainedResumeLockStore struct {
	*workflowstore.Store
	Selected        workflow.CurrentNodeReference
	FailSibling     bool
	PersistenceRoot string
	LockReady       chan<- func()
	Attempted       chan<- workflow.CurrentNodeReference
	Test            testing.TB
}

type retainedDelayedPromptHistoryStore struct {
	started chan struct{}
	release chan struct{}
	err     error
}

func (s *retainedDelayedPromptHistoryStore) RecordPromptHistoryEntry(
	ctx context.Context,
	_ metadata.PromptHistoryEntry,
) (metadata.PromptHistoryRecord, error) {
	close(s.started)
	select {
	case <-s.release:
		return metadata.PromptHistoryRecord{}, s.err
	case <-ctx.Done():
		return metadata.PromptHistoryRecord{}, ctx.Err()
	}
}

func (s *retainedResumeLockStore) ResumeCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error) {
	projection, found, err := s.Store.ResumeCurrentNode(ctx, reference)
	if err != nil {
		return projection, found, err
	}
	if s.FailSibling && !reference.Equal(s.Selected) {
		s.Attempted <- reference
		return projection, false, errors.New("sibling Resume failed")
	}
	if reference.Equal(s.Selected) {
		s.LockReady <- testsqlite.AcquireWriteLock(s.Test, s.PersistenceRoot)
	}
	s.Attempted <- reference
	return projection, found, nil
}

type retainedProgressClient struct {
	mu              sync.Mutex
	requests        []llm.Request
	generations     int
	selectedError   error
	selectedStarted chan<- struct{}
	selectedRelease <-chan struct{}
	selectedGate    sync.Once
}

func (c *retainedProgressClient) Generate(
	_ context.Context,
	request llm.Request,
	_ llm.StreamCallbacks,
) (llm.Response, error) {
	selected := false
	for _, message := range llm.MessagesFromItems(request.Items) {
		if message.Content != nil && *message.Content == "continue" {
			selected = true
			break
		}
	}
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
		return llm.Response{}, &llm.ProviderAPIError{
			ProviderID: "test", StatusCode: 400, Code: llm.UnifiedErrorCodeProviderContract, Err: c.selectedError,
		}
	}
	switch generation {
	case 1:
		return retainedResponse("older progress", llm.ToolCall{
			ID:    "split",
			Name:  "complete_node",
			Input: []byte(`{"transition":"split"}`),
		}), nil
	case 2, 3:
		return llm.Response{}, context.Canceled
	default:
		progress := "sibling progress"
		if selected {
			progress = "selected progress"
		}
		if selected && selectedFirst {
			return retainedResponse(progress), nil
		}
		return retainedResponse(progress, llm.ToolCall{
			ID:    "complete",
			Name:  "complete_node",
			Input: []byte(retainedCompletionInput(request, "join_a", progress)),
		}), nil
	}
}

func retainedResponse(content string, calls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Assistant: llm.Message{
			Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary), Content: textutil.Value(content),
		},
		ToolCalls: calls,
	}
}

func (c *retainedProgressClient) Requests() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.requests...)
}

func (c *retainedProgressClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "test",
		SupportsResponsesAPI: true,
	}, nil
}

func retainedCompletionInput(request llm.Request, fallback, commentary string) string {
	for _, tool := range request.Tools {
		if tool.Name != "complete_node" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.Schema.JSON(), &schema); err != nil {
			continue
		}
		transitionSchema, ok := schema.Properties["transition"]
		if !ok {
			return `{"commentary":"` + commentary + `"}`
		}
		var transition struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(transitionSchema, &transition); err != nil {
			continue
		}
		if len(transition.Enum) == 1 {
			return `{"commentary":"` + commentary + `"}`
		}
		for _, value := range transition.Enum {
			if value == fallback {
				return `{"transition":"` + value + `","commentary":"` + commentary + `"}`
			}
		}
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
	f *currentNodeRunnerFixture,
	controller *workflowexecution.CurrentNodeController,
	histories ...interface {
		RecordPromptHistoryEntry(context.Context, metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error)
	},
) apicontract.RunPromptService {
	history := interface {
		RecordPromptHistoryEntry(context.Context, metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error)
	}(f.metadata)
	if len(histories) != 0 {
		history = histories[0]
	}
	return runprompt.NewInProcessRunPromptClient(runprompt.HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   f.cfg,
			ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
			StoreOptions:             f.starter.storeOptions,
			PersistedSessions:        f.metadata,
			ExecutionTargets:         f.metadata,
			ProjectWorkspaceBoundary: f.metadata,
		}),
		PromptHistory:              history,
		RuntimeAuthority:           f.authority,
		WorkflowSessionReactivator: controller,
	})
}
