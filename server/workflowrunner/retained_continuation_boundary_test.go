package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsqlite "core/internal/testharness/sqlite"
	"core/server/launch"
	"core/server/llm"
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

type retainedAssignmentOrderClient struct {
	base              *compactingScriptedClient
	fixture           *currentNodeRunnerFixture
	sessionID         runtimeids.SessionID
	expectedReference workflow.CurrentNodeReference
	observed          atomic.Bool
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
	c.observed.Store(true)
	return c.base.Generate(ctx, request, callbacks)
}

func (c *retainedAssignmentOrderClient) Compact(ctx context.Context, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	return c.base.Compact(ctx, request)
}

func (c *retainedAssignmentOrderClient) ProviderCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	return c.base.ProviderCapabilities(ctx)
}

func (c *retainedAssignmentOrderClient) Requests() []llm.Request {
	return c.base.Requests()
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
	for {
		for _, event := range window.Records {
			payload, payloadErr := event.Payload()
			if payloadErr != nil {
				return false, payloadErr
			}
			message, ok := payload.(session.MessageRecord)
			if !ok ||
				message.MessageType == nil ||
				*message.MessageType != session.MessageTypeWorkflowMode ||
				message.SourcePath == nil {
				continue
			}
			identity := workflowruntime.CurrentNodePromptIdentity(expected)
			if *message.SourcePath == identity {
				return true, nil
			}
		}
		if window.ReachedStart {
			return false, nil
		}
		seen := 0
		window, err = eventLog.ReadSegmentBackward(window.StartOffset, func(session.EventRecord) bool {
			seen++
			return seen == 128
		})
		if err != nil {
			return false, err
		}
	}
}

func prepareCompactedRetainedWorkflowSession(
	t *testing.T,
	f *currentNodeRunnerFixture,
	client currentNodeRunnerClient,
) (runtimeids.SessionID, workflow.CurrentNodeReference, sessionruntime.RuntimeAttachment) {
	t.Helper()
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
	if err != nil {
		t.Fatalf("start Task: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	sessionStore, err := session.Create(
		filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		"sessions",
		f.workspace,
		sessioncontract.SessionCategoryMain,
		f.starter.storeOptions...,
	)
	if err != nil {
		t.Fatalf("create retained Session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse retained Session ID: %v", err)
	}
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
	if input.CurrentNode.SessionID == nil {
		t.Fatalf("retained Current Node %v has no Session", reference)
	}
	planner := launch.Planner{
		Config:                   f.cfg,
		ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		StoreOptions:             f.starter.storeOptions,
		PersistedSessions:        f.metadata,
		ExecutionTargets:         f.metadata,
		ProjectWorkspaceBoundary: f.metadata,
	}
	plan, err := planner.PlanSession(context.Background(), launch.SessionRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
	})
	if err != nil {
		t.Fatalf("plan retained Session: %v", err)
	}
	prepared := preparedCurrentNodeAgentSession{
		root:   *input.ExecutionRoot,
		plan:   plan,
		client: client,
		mode:   workflowruntime.CompletionModeTool,
	}
	runtimePlan, err := f.starter.buildCurrentNodeAgentRuntimePlan(input, prepared, nil)
	if err != nil {
		t.Fatalf("build retained runtime plan: %v", err)
	}
	attachment, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "retained-continuation-boundary-test",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open retained runtime: %v", err)
	}
	var binding *agentruntime.CurrentNodeExecutionBinding
	if err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		var bindErr error
		binding, bindErr = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Instructions: workflowruntime.TaskInstructions{
				WorkflowID:  input.Workflow.ID,
				CurrentNode: reference,
			},
		})
		return bindErr
	}); err != nil {
		t.Fatalf("bind retained Current Node execution: %v", err)
	}
	t.Cleanup(func() {
		if binding != nil {
			_ = binding.Close()
		}
		if _, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); releaseErr != nil &&
			!errors.Is(releaseErr, serverapi.ErrRuntimeUnavailable) {
			t.Errorf("release retained runtime: %v", releaseErr)
		}
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
	t.Helper()
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	if err != nil || input.ExecutionRoot == nil {
		t.Fatalf("resolve retained Current Node start context: %v", err)
	}
	planner := launch.Planner{
		Config:                   f.cfg,
		ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		StoreOptions:             f.starter.storeOptions,
		PersistedSessions:        f.metadata,
		ExecutionTargets:         f.metadata,
		ProjectWorkspaceBoundary: f.metadata,
	}
	sessionID := *input.CurrentNode.SessionID
	plan, err := planner.PlanSession(context.Background(), launch.SessionRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
	})
	if err != nil {
		t.Fatalf("plan retained branch Session: %v", err)
	}
	prepared := preparedCurrentNodeAgentSession{
		root:   *input.ExecutionRoot,
		plan:   plan,
		client: f.client,
		mode:   workflowruntime.CompletionModeTool,
	}
	runtimePlan, err := f.starter.buildCurrentNodeAgentRuntimePlan(input, prepared, nil)
	if err != nil {
		t.Fatalf("build retained branch runtime plan: %v", err)
	}
	attachment, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "retained-sibling-contention-test",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open retained branch runtime: %v", err)
	}
	var binding *agentruntime.CurrentNodeExecutionBinding
	if err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		binding, err = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Instructions: workflowruntime.TaskInstructions{
				WorkflowID:  input.Workflow.ID,
				CurrentNode: reference,
			},
		})
		return err
	}); err != nil {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		t.Fatalf("bind retained branch runtime: %v", err)
	}
	if err := binding.Close(); err != nil {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		t.Fatalf("close dormant retained branch binding: %v", err)
	}
	binding = nil
	return func() {
		if binding != nil {
			_ = binding.Close()
		}
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
	}
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesTUISteerProvider(t *testing.T) {
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
	order := &retainedAssignmentOrderClient{base: base, fixture: f}
	f.client = order
	sessionID, reference, _ := prepareCompactedRetainedWorkflowSession(t, f, order)
	order.sessionID = sessionID
	order.expectedReference = reference

	service := runtimecontrol.NewService(f.authority).
		WithWorkflowSessionReactivator(f.controller).
		WithPromptHistoryStore(f.metadata).
		WithPersistedSessionResolver(f.metadata)
	response, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: sessionID.String(),
		Input:     runtimeinput.Text("continue"),
	})
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if !order.observed.Load() {
		t.Fatalf("provider was not called after exact durable assignment: err=%v requests=%d", err, len(order.Requests()))
	}
	if response.ResultKind != clientui.UserTurnResultKindNoFinal {
		t.Fatalf("SubmitUserTurn response = %+v, want no-final result", response)
	}
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesHeadlessContinueProvider(t *testing.T) {
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
	order := &retainedAssignmentOrderClient{base: base, fixture: f}
	f.client = order
	sessionID, reference, _ := prepareCompactedRetainedWorkflowSession(t, f, order)
	order.sessionID = sessionID
	order.expectedReference = reference

	client := runprompt.NewInProcessRunPromptClient(runprompt.HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   f.cfg,
			ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
			StoreOptions:             f.starter.storeOptions,
			PersistedSessions:        f.metadata,
			ExecutionTargets:         f.metadata,
			ProjectWorkspaceBoundary: f.metadata,
		}),
		PromptHistory:              f.metadata,
		RuntimeAuthority:           f.authority,
		WorkflowSessionReactivator: f.controller,
	})
	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
		Prompt: "continue",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if !order.observed.Load() {
		t.Fatalf("provider was not called after exact durable assignment: err=%v requests=%d", err, len(order.Requests()))
	}
	if response.SessionID != sessionID.String() {
		t.Fatalf("RunPrompt response Session = %q, want %q", response.SessionID, sessionID)
	}
}

func TestRetainedSiblingResumeWaitsForFileBackedMetadataLock(t *testing.T) {
	client := &retainedProgressClient{}
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
	t.Cleanup(attachRetainedCurrentNodeRuntime(t, f, selected.Reference))
	lockReady := make(chan func(), 1)
	attempted := make(chan workflow.CurrentNodeReference, len(branches))
	if err := f.controller.Close(); err != nil {
		t.Fatalf("close initial Current Node controller: %v", err)
	}
	lockingStore := &retainedResumeLockStore{
		Store:           f.store,
		Selected:        selected.Reference,
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
	if err != nil {
		t.Fatalf("new lock-aware Current Node controller: %v", err)
	}
	f.controller = controller
	continuationClient := runPromptClientForCurrentNodeFixture(f, f.controller)
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
	var progress []serverapi.RunPromptProgress
	go func() {
		response, err := continuationClient.RunPrompt(
			context.Background(),
			serverapi.RunPromptRequest{
				Intent:  serverapi.OpenExistingSessionLaunchIntent(selectedSession),
				Prompt:  "continue",
				Timeout: 2 * time.Second,
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
	time.Sleep(2200 * time.Millisecond)
	release()
	result := <-done
	if result.err != nil {
		t.Fatalf("RunPrompt: %v", result.err)
	}
	if result.response.SessionID != selectedSession.String() {
		t.Fatalf("RunPrompt response Session = %q, want %q", result.response.SessionID, selectedSession)
	}
	if len(result.response.Warnings) != 0 {
		t.Fatalf("RunPrompt warnings = %v, want no warnings after successful sibling Resume", result.response.Warnings)
	}
	if !hasAssistantProgress(progress, "selected progress") {
		t.Fatalf("selected Run Prompt progress = %+v, want selected provider progress", progress)
	}
	if hasAssistantProgress(progress, "sibling progress") || hasAssistantProgress(progress, "older progress") {
		t.Fatalf("selected Run Prompt leaked unrelated progress: %+v", progress)
	}
	selectedRequests := make([]llm.Request, 0)
	for _, request := range client.Requests() {
		hasSelectedInput := false
		for _, message := range llm.MessagesFromItems(request.Items) {
			if message.Content != nil && *message.Content == "continue" {
				hasSelectedInput = true
				break
			}
		}
		if hasSelectedInput {
			selectedRequests = append(selectedRequests, request)
		}
	}
	if len(selectedRequests) < 2 {
		t.Fatalf("selected provider requests = %d, want first dispatch plus later pending-input drain", len(selectedRequests))
	}
	if requestContainsMessage(selectedRequests[0], "older pending input") {
		t.Fatalf("first selected provider request included older pending input")
	}
	if !requestContainsMessage(selectedRequests[1], "older pending input") {
		t.Fatalf("later selected provider request omitted older pending input")
	}
	attemptedReferences := make([]workflow.CurrentNodeReference, 0, len(branches))
	for range branches {
		attemptedReferences = append(attemptedReferences, <-attempted)
	}
	for _, branch := range branches {
		found := false
		for _, attempted := range attemptedReferences {
			if attempted.Equal(branch.Reference) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Resume attempts = %v, want branch %v", attemptedReferences, branch.Reference)
		}
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		completed := 0
		for _, node := range nodes {
			for _, attempted := range attemptedReferences {
				if node.Reference.Equal(attempted) &&
					node.Scheduling != nil &&
					node.Scheduling.State == workflow.CurrentNodeSchedulingInterrupted {
					return false
				}
			}
		}
		for _, reference := range attemptedReferences {
			found := false
			for _, node := range nodes {
				if node.Reference.Equal(reference) {
					found = true
					break
				}
			}
			if !found {
				completed++
			}
		}
		return completed == len(attemptedReferences)
	})
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
	PersistenceRoot string
	LockReady       chan<- func()
	Attempted       chan<- workflow.CurrentNodeReference
	Test            testing.TB
}

func (s *retainedResumeLockStore) ResumeCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error) {
	projection, found, err := s.Store.ResumeCurrentNode(ctx, reference)
	if err != nil {
		return projection, found, err
	}
	if reference.Equal(s.Selected) {
		s.LockReady <- testsqlite.AcquireWriteLock(s.Test, s.PersistenceRoot)
	}
	s.Attempted <- reference
	return projection, found, nil
}

type retainedProgressClient struct {
	mu            sync.Mutex
	requests      []llm.Request
	generations   int
	selectedCalls int
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
	selectedCall := 0
	if selected {
		c.selectedCalls++
		selectedCall = c.selectedCalls
	}
	c.mu.Unlock()
	switch generation {
	case 1:
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
				Content: textutil.Value("older progress"),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "split",
				Name:  "complete_node",
				Input: []byte(`{"transition":"split"}`),
			}},
		}, nil
	case 2, 3:
		return llm.Response{}, context.Canceled
	default:
		progress := "sibling progress"
		if selected {
			progress = "selected progress"
		}
		if selected && selectedCall == 1 {
			return llm.Response{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Phase:   textutil.Value(llm.MessagePhaseCommentary),
					Content: textutil.Value(progress),
				},
			}, nil
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
				Content: textutil.Value(progress),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "complete",
				Name:  "complete_node",
				Input: []byte(retainedCompletionInput(request, "join_a", progress)),
			}},
		}, nil
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
) apicontract.RunPromptService {
	return runprompt.NewInProcessRunPromptClient(runprompt.HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   f.cfg,
			ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
			StoreOptions:             f.starter.storeOptions,
			PersistedSessions:        f.metadata,
			ExecutionTargets:         f.metadata,
			ProjectWorkspaceBoundary: f.metadata,
		}),
		PromptHistory:              f.metadata,
		RuntimeAuthority:           f.authority,
		WorkflowSessionReactivator: controller,
	})
}
