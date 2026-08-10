package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	agentruntime "core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

const currentNodeRunnerWait = 60 * time.Second

type currentNodeRunnerFixture struct {
	cfg             config.App
	metadata        *metadata.Store
	store           *workflowstore.Store
	authority       *sessionruntime.Authority
	runtimes        *registry.RuntimeRegistry
	controller      *workflowexecution.CurrentNodeController
	starter         *Starter
	dependencies    *workflowview.TaskDependencies
	projectID       string
	workspaceID     string
	workspace       string
	client          currentNodeRunnerClient
	persistenceGate *sessiontest.PersistenceGate

	mu             sync.Mutex
	clientRequests []runtimewire.RuntimeClientRequest
	clientErr      error
}

type currentNodeRunnerClient interface {
	llm.Client
	Requests() []llm.Request
}

func workflowPostCompletionCompactionResponse(summary string) llm.CompactionResponse {
	return llm.CompactionResponse{
		OutputItems: []llm.ResponseItem{
			{
				Type:        llm.ResponseItemTypeMessage,
				Role:        textutil.Value(llm.RoleUser),
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value(summary),
			},
			{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("workflow-post-completion"),
				EncryptedContent: textutil.Value("encrypted"),
			},
		},
		Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
	}
}

type currentNodeRunnerStepLifecycle struct {
	runtimes *registry.RuntimeRegistry
}

func (s currentNodeRunnerStepLifecycle) StepBegan(
	ctx context.Context,
	resource sessionruntime.AgentResourceDescriptor,
	snapshot agentruntime.StepLifecycleSnapshot,
) error {
	return runtimewire.NewStepLifecycleSink(
		resource.Ref.SessionID().String(),
		s.runtimes,
	).StepBegan(ctx, snapshot)
}

func (s currentNodeRunnerStepLifecycle) StepEnded(
	ctx context.Context,
	resource sessionruntime.AgentResourceDescriptor,
	snapshot agentruntime.StepLifecycleSnapshot,
) error {
	return runtimewire.NewStepLifecycleSink(
		resource.Ref.SessionID().String(),
		s.runtimes,
	).StepEnded(ctx, snapshot)
}

type currentNodeStartContextStore struct {
	RuntimeStore
	transform func(workflowstore.CurrentNodeStartContext) workflowstore.CurrentNodeStartContext
}

func (s currentNodeStartContextStore) ResolveCurrentNodeStartContext(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowstore.CurrentNodeStartContext, error) {
	input, err := s.RuntimeStore.ResolveCurrentNodeStartContext(ctx, reference)
	if err != nil {
		return workflowstore.CurrentNodeStartContext{}, err
	}
	return s.transform(input), nil
}

func newCurrentNodeRunnerFixture(t *testing.T, steps ...ScriptedRuntimeStep) *currentNodeRunnerFixture {
	return newCurrentNodeRunnerFixtureWithClient(
		t,
		NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			steps...,
		),
	)
}

func newCurrentNodeRunnerFixtureWithClient(t *testing.T, client currentNodeRunnerClient) *currentNodeRunnerFixture {
	return newCurrentNodeRunnerFixtureWithClientAndPersistence(t, client, false)
}

func newCurrentNodeRunnerFixtureWithPersistenceGate(
	t *testing.T,
	client currentNodeRunnerClient,
) *currentNodeRunnerFixture {
	return newCurrentNodeRunnerFixtureWithClientAndPersistence(t, client, true)
}

func newCurrentNodeRunnerFixtureWithClientAndPersistence(
	t *testing.T,
	client currentNodeRunnerClient,
	withPersistenceGate bool,
) *currentNodeRunnerFixture {
	return newCurrentNodeRunnerFixtureWithRunner(t, client, withPersistenceGate, nil)
}

func newCurrentNodeRunnerFixtureWithRunner(
	t *testing.T,
	client currentNodeRunnerClient,
	withPersistenceGate bool,
	decorate func(workflowexecution.CurrentNodeRunner) workflowexecution.CurrentNodeRunner,
) *currentNodeRunnerFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "persistence"))
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Settings.Model = "workflow-base"
	cfg.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeStructuredOutput
	cfg.Settings.Reviewer.Frequency = "off"
	cfg.Settings.Subagents["coder"] = config.SubagentRole{
		Description: "Coder",
		Settings:    config.Settings{Model: "workflow-coder"},
		Sources:     map[string]string{"model": "test"},
	}
	cfg.Settings.Subagents["reviewer"] = config.SubagentRole{
		Description: "Reviewer",
		Settings:    config.Settings{Model: "workflow-reviewer"},
		Sources:     map[string]string{"model": "test"},
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), workspace)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "RUN"); err != nil {
		t.Fatalf("set project key: %v", err)
	}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder", "reviewer")))
	if err != nil {
		t.Fatalf("new workflow store: %v", err)
	}
	fixture := &currentNodeRunnerFixture{
		cfg:         cfg,
		metadata:    metadataStore,
		store:       store,
		projectID:   binding.ProjectID,
		workspaceID: binding.WorkspaceID,
		workspace:   binding.CanonicalRoot,
		client:      client,
	}
	storeOptions := metadataStore.AuthoritativeSessionStoreOptions()
	if withPersistenceGate {
		persisted := gatedMetadataSessionPersistence{
			sessions: sessiontest.NewPersistence(),
			metadata: metadataStore,
		}
		fixture.persistenceGate = sessiontest.NewPersistenceGate(persisted)
		storeOptions = []session.StoreOption{
			session.WithPersistenceObserver(fixture.persistenceGate),
			session.WithPersistedSessionResolver(persisted),
		}
	}
	fixture.runtimes = registry.NewRuntimeRegistry()
	var controller *workflowexecution.CurrentNodeController
	fixture.authority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    storeOptions,
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
		PromptFeed: fixture.runtimes,
		EventFeed: func(resource sessionruntime.AgentResourceDescriptor, event agentruntime.Event) {
			fixture.runtimes.PublishAuthorityRuntimeEvent(resource.Ref, event)
		},
		ResourceLifecycle: fixture.runtimes,
		StepLifecycle:     currentNodeRunnerStepLifecycle{runtimes: fixture.runtimes},
	})
	t.Cleanup(func() {
		if fixture.controller != nil {
			if err := fixture.controller.Close(); err != nil {
				t.Errorf("close current node controller: %v", err)
			}
		}
		if fixture.starter != nil {
			if err := fixture.starter.Close(); err != nil {
				t.Errorf("close workflow starter: %v", err)
			}
		}
		if err := fixture.authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	taskMutations := workflowexecution.NewTaskMutationCoordinator()
	dependencyCounter, err := workflowview.NewTaskDependencyCounter(metadataStore)
	if err != nil {
		t.Fatalf("new Task dependency counter: %v", err)
	}
	starter, err := NewStarter(cfg, metadataStore, store, nil, nil, StarterOptions{
		RuntimeAuthority: fixture.authority,
		TaskMutations:    taskMutations,
		TaskDependencies: dependencyCounter,
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, request runtimewire.RuntimeClientRequest) (llm.Client, error) {
			fixture.mu.Lock()
			fixture.clientRequests = append(fixture.clientRequests, request)
			err := fixture.clientErr
			fixture.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return fixture.client, nil
		}),
	})
	if err != nil {
		t.Fatalf("new starter: %v", err)
	}
	fixture.starter = starter
	var runner workflowexecution.CurrentNodeRunner = starter
	if decorate != nil {
		runner = decorate(runner)
	}
	controller, err = workflowexecution.NewCurrentNodeController(store, runner, fixture.authority, taskMutations, workflowexecution.CurrentNodeControllerConfig{
		AgentConcurrency:      1,
		AssignmentSteerer:     starter,
		LifecycleAvailability: workflowexecution.NewLifecycleFatalAvailability(),
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	fixture.controller = controller
	projection, err := workflowview.NewTaskStatusProjection(
		metadataStore,
		store,
		workflowview.NewTaskProjector(),
		controller,
	)
	if err != nil {
		t.Fatalf("new Task status projection: %v", err)
	}
	dependencies, err := workflowview.NewTaskDependencies(metadataStore, projection, dependencyCounter)
	if err != nil {
		t.Fatalf("new Task dependency projection: %v", err)
	}
	fixture.dependencies = dependencies
	return fixture
}

type barrierCurrentNodeRunner struct {
	delegate workflowexecution.CurrentNodeRunner
	entered  chan workflow.CurrentNodeReference
	release  <-chan struct{}
}

func (r barrierCurrentNodeRunner) StartCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	delivery workflowruntime.TaskPromptDelivery,
	assignment workflowexecution.CurrentNodeAssignmentSteer,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) error {
	r.entered <- reference
	select {
	case <-r.release:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	return r.delegate.StartCurrentNode(ctx, reference, delivery, assignment, lease, controller)
}

func (f *currentNodeRunnerFixture) createTask(t *testing.T, workflowID runtimeids.WorkflowID) workflowstore.TaskRecord {
	t.Helper()
	if _, err := f.store.LinkWorkflow(context.Background(), f.projectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	task, err := f.store.CreateTask(context.Background(), workflowstore.CreateTaskRequest{
		ProjectID: f.projectID, WorkflowID: &workflowID, Title: "Runner task", Body: "Implement it.",
		SourceWorkspaceID: f.workspaceID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func (f *currentNodeRunnerFixture) startTask(t *testing.T, task workflowstore.TaskRecord) workflow.CurrentNodeReference {
	t.Helper()
	started, err := f.controller.StartTask(context.Background(), task.ID, func(ctx context.Context) error {
		return f.store.LockTaskExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
			Snapshot: workflowstore.ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: workflowstore.ExecutionTargetProvenanceResolved,
			},
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   f.workspaceID,
				SourceWorkspaceRoot: f.workspace,
			},
		})
	})
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("start mutation = %+v, want one Current Node", started.Mutation)
	}
	return started.Mutation.Created[0].Reference
}

func (f *currentNodeRunnerFixture) waitForCurrentNode(t *testing.T, taskID workflow.TaskID, predicate func([]workflow.CurrentNode) bool) []workflow.CurrentNode {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		nodes, err := f.store.ListCurrentNodes(context.Background(), taskID)
		if err != nil {
			t.Fatalf("list Current Nodes: %v", err)
		}
		if predicate(nodes) {
			return nodes
		}
		time.Sleep(10 * time.Millisecond)
	}
	nodes, err := f.store.ListCurrentNodes(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list Current Nodes after timeout: %v", err)
	}
	encoded, marshalErr := json.Marshal(nodes)
	if marshalErr != nil {
		t.Fatalf("Current Nodes = %+v did not reach expected state", nodes)
	}
	t.Fatalf("Current Nodes = %s did not reach expected state", encoded)
	return nil
}

func (f *currentNodeRunnerFixture) waitForControllerCurrentNode(t *testing.T, reference workflow.CurrentNodeReference) {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		snapshot := f.controller.Snapshot()
		for _, gate := range snapshot.Gates {
			if gate.CurrentNode.Equal(reference) {
				return
			}
		}
		for _, live := range snapshot.LiveScopes {
			if live.CurrentNode.Equal(reference) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Current Node %v never reached controller admission or live state", reference)
}

func (f *currentNodeRunnerFixture) waitForControllerCurrentNodeFinalized(t *testing.T, reference workflow.CurrentNodeReference) {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		if !controllerSnapshotOwnsCurrentNode(f.controller.Snapshot(), reference) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Current Node %v remained owned by the controller after execution finalization", reference)
}

func controllerSnapshotOwnsCurrentNode(snapshot workflowexecution.CurrentNodeExecutionSnapshot, reference workflow.CurrentNodeReference) bool {
	for _, intent := range snapshot.AutomaticIntents {
		if intent.CurrentNode.Equal(reference) {
			return true
		}
	}
	for _, start := range snapshot.ExplicitStarts {
		if start.CurrentNode.Equal(reference) {
			return true
		}
	}
	for _, intent := range snapshot.HeldIntents {
		if intent.CurrentNode.Equal(reference) {
			return true
		}
	}
	for _, gate := range snapshot.Gates {
		if gate.CurrentNode.Equal(reference) {
			return true
		}
	}
	for _, live := range snapshot.LiveScopes {
		if live.CurrentNode.Equal(reference) {
			return true
		}
	}
	return false
}

func (f *currentNodeRunnerFixture) waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect path %q: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("path %q was not created", path)
}

func (f *currentNodeRunnerFixture) runtimeRequests() []runtimewire.RuntimeClientRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtimewire.RuntimeClientRequest(nil), f.clientRequests...)
}

func (f *currentNodeRunnerFixture) onlyProjectSessionMeta(t *testing.T) session.Meta {
	t.Helper()
	sessionIDs, err := f.metadata.ListProjectSessionIDs(context.Background(), f.projectID)
	if err != nil {
		t.Fatalf("list project sessions: %v", err)
	}
	if len(sessionIDs) != 1 {
		t.Fatalf("project Session IDs = %+v, want exactly one", sessionIDs)
	}
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionIDs[0])
	if err != nil {
		t.Fatalf("resolve project Session: %v", err)
	}
	if record.Meta == nil {
		t.Fatal("resolved project Session metadata is absent")
	}
	return *record.Meta
}

func (f *currentNodeRunnerFixture) waitForModelRequests(t *testing.T, count int) []llm.Request {
	return f.waitForModelRequestsWithin(t, count, currentNodeRunnerWait)
}

func (f *currentNodeRunnerFixture) waitForModelRequestsWithin(
	t *testing.T,
	count int,
	timeout time.Duration,
) []llm.Request {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for len(f.client.Requests()) < count && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	requests := f.client.Requests()
	if len(requests) != count {
		t.Fatalf("model requests = %d, want %d", len(requests), count)
	}
	return requests
}

func (f *currentNodeRunnerFixture) waitForPendingApproval(t *testing.T, taskID workflow.TaskID) workflow.PendingApproval {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		approvals, err := f.store.ListPendingApprovals(context.Background(), taskID)
		if err != nil {
			t.Fatalf("list pending Approvals: %v", err)
		}
		if len(approvals) == 1 {
			return approvals[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	approvals, err := f.store.ListPendingApprovals(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list pending Approvals after timeout: %v", err)
	}
	t.Fatalf("pending Approvals = %+v, want exactly one", approvals)
	return workflow.PendingApproval{}
}

func requireToolCompletionRequests(t *testing.T, requests []llm.Request) {
	t.Helper()
	for index, request := range requests {
		if request.StructuredOutput != nil {
			t.Fatalf("request %d changed retained completion mode to structured output", index+1)
		}
		if !requestAdvertisesTool(request, toolspec.ToolCompleteNode) {
			t.Fatalf("request %d omitted retained complete_node tool: %+v", index+1, request.Tools)
		}
	}
}

type indexedWorkflowAssignment struct {
	index      int
	sourcePath string
}

func workflowAssignments(request llm.Request) []indexedWorkflowAssignment {
	assignments := make([]indexedWorkflowAssignment, 0, 2)
	for index, item := range request.Items {
		if item.Type != llm.ResponseItemTypeMessage ||
			item.Role == nil ||
			*item.Role != llm.RoleDeveloper ||
			item.MessageType == nil ||
			*item.MessageType != llm.MessageTypeWorkflowMode ||
			item.SourcePath == nil {
			continue
		}
		assignments = append(assignments, indexedWorkflowAssignment{
			index:      index,
			sourcePath: *item.SourcePath,
		})
	}
	return assignments
}

func requireToolOutputBeforeAssignment(
	t *testing.T,
	request llm.Request,
	callID string,
	assignment indexedWorkflowAssignment,
) {
	t.Helper()
	for index, item := range request.Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput &&
			item.CallID != nil &&
			*item.CallID == callID {
			if index >= assignment.index {
				t.Fatalf("tool output index = %d, assignment index = %d; want tool output first", index, assignment.index)
			}
			return
		}
	}
	t.Fatalf("request omitted tool output for call %q", callID)
}

func TestCurrentNodeAgentStartsFreshSessionWithLatestRoleAndCompletionContract(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t, ScriptedFinalAnswer(`{"commentary":"done"}`))
	writeWorkflowContextFixture(t, f)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)

	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 1 {
		t.Fatalf("retained Session count = %d, %v; want one", count, err)
	}
	requests := f.runtimeRequests()
	if len(requests) == 0 {
		t.Fatal("runtime client was never prepared")
	}
	for _, request := range requests {
		if request.ActiveSettings.Model != "workflow-coder" {
			t.Fatalf("runtime client request model = %q, want latest coder role settings", request.ActiveSettings.Model)
		}
	}
	if len(f.client.Requests()) != 1 {
		t.Fatalf("model requests = %d, want one workflow turn", len(f.client.Requests()))
	}
	modelRequests := f.client.Requests()
	if len(modelRequests) != 1 || modelRequests[0].StructuredOutput == nil {
		t.Fatalf("model requests = %+v, want structured Current Node completion contract", modelRequests)
	}
	baseContextTypes := make([]llm.MessageType, 0, 5)
	assignmentIndex := -1
	for index, item := range modelRequests[0].Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.Role != nil &&
			*item.Role == llm.RoleDeveloper &&
			item.MessageType != nil {
			if *item.MessageType == llm.MessageTypeWorkflowMode {
				assignmentIndex = index
				continue
			}
			baseContextTypes = append(baseContextTypes, *item.MessageType)
		}
	}
	assignments := workflowAssignments(modelRequests[0])
	wantBaseContextTypes := []llm.MessageType{
		llm.MessageTypeEnvironment,
		llm.MessageTypeSkills,
		llm.MessageTypeSubagents,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeAgentsMD,
	}
	if !slices.Equal(baseContextTypes, wantBaseContextTypes) {
		t.Fatalf("fresh workflow request base context types = %v, want %v", baseContextTypes, wantBaseContextTypes)
	}
	if len(assignments) != 1 || assignmentIndex != assignments[0].index || assignmentIndex <= len(baseContextTypes)-1 {
		t.Fatalf("fresh workflow request assignment = index %d/%+v after base context %v, want exactly one assignment last", assignmentIndex, assignments, baseContextTypes)
	}
	meta := f.onlyProjectSessionMeta(t)
	if meta.Continuation == nil || meta.Continuation.AgentRole == nil || *meta.Continuation.AgentRole != "coder" {
		t.Fatalf("fresh workflow Session continuation = %+v, want persisted coder identity", meta.Continuation)
	}
}

func writeWorkflowContextFixture(t *testing.T, f *currentNodeRunnerFixture) {
	t.Helper()
	for path, content := range map[string]string{
		filepath.Join(f.cfg.PersistenceRoot, "AGENTS.md"):                                        "global workflow instructions",
		filepath.Join(f.workspace, "AGENTS.md"):                                                  "workspace workflow instructions",
		filepath.Join(f.workspace, config.ConfigDirName, "skills", "workflow-skill", "SKILL.md"): "---\nname: workflow-skill\ndescription: workflow test skill\n---\n\n# Body\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create workflow context directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write workflow context fixture: %v", err)
		}
	}
}

func TestCurrentNodeAgentWritesToSiblingWorkspaceThroughCreatedRuntime(t *testing.T) {
	sibling := t.TempDir()
	target := filepath.Join(sibling, "workflow.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write workflow sibling fixture: %v", err)
	}
	input, err := json.Marshal(map[string]string{
		"path":       target,
		"old_string": "before",
		"new_string": "workflow sibling",
	})
	if err != nil {
		t.Fatalf("marshal sibling edit input: %v", err)
	}
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedToolBatch("write sibling", llm.ToolCall{
			ID:    "sibling-patch",
			Name:  string(toolspec.ToolEdit),
			Input: input,
		}),
		ScriptedToolBatch("complete", llm.ToolCall{
			ID:    "complete-sibling",
			Name:  string(toolspec.ToolCompleteNode),
			Input: json.RawMessage(`{"transition":"done","commentary":"done"}`),
		}),
		ScriptedFinalAnswer("done"),
	)
	if _, err := f.metadata.AttachWorkspaceToProject(context.Background(), f.projectID, sibling); err != nil {
		t.Fatalf("AttachWorkspaceToProject sibling: %v", err)
	}
	workflowID := createCurrentNodeAgentWorkflowWithCompletionMode(t, f.store, string(config.WorkflowCompletionModeTool))
	task := f.createTask(t, workflowID)
	currentNode := f.startTask(t, task)
	f.waitForControllerCurrentNodeFinalized(t, currentNode)
	if data, err := os.ReadFile(target); err != nil || string(data) != "workflow sibling\n" {
		t.Fatalf("workflow sibling file = %q, error = %v", data, err)
	}
}

func TestContinueSessionRetainsInitialCompletionModeAcrossNodeOverride(t *testing.T) {
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedToolBatch(
			"complete first node",
			llm.ToolCall{
				ID:    "complete-first",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next","commentary":"first done"}`),
			},
		),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Retained Session completion mode",
		workflow.ContextModeContinueSession,
		currentNodeWorkflowStep{
			kind:           workflow.NodeKindAgent,
			role:           "coder",
			prompt:         "Complete the first node.",
			completionMode: string(config.WorkflowCompletionModeTool),
		},
		currentNodeWorkflowStep{
			kind:           workflow.NodeKindAgent,
			role:           "coder",
			prompt:         "Complete the second node.",
			completionMode: string(config.WorkflowCompletionModeStructuredOutput),
		},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)

	requests := f.waitForModelRequests(t, 2)
	requireToolCompletionRequests(t, requests)
	sourceAssignments := workflowAssignments(requests[0])
	targetAssignments := workflowAssignments(requests[1])
	if len(sourceAssignments) != 1 {
		t.Fatalf("source workflow assignments = %+v, want exactly one", sourceAssignments)
	}
	if len(targetAssignments) != 2 {
		t.Fatalf("continued workflow assignments = %+v, want source plus exactly one target assignment", targetAssignments)
	}
	if targetAssignments[0].sourcePath != sourceAssignments[0].sourcePath {
		t.Fatalf("continued source assignment = %+v, want inherited %+v", targetAssignments[0], sourceAssignments[0])
	}
	if targetAssignments[1].sourcePath == targetAssignments[0].sourcePath {
		t.Fatalf("target assignment reused source Current Node identity: %+v", targetAssignments)
	}
	requireToolOutputBeforeAssignment(t, requests[1], "complete-first", targetAssignments[1])
}

func TestApprovalTransitionSteersPreviousTargetSessionExactlyOnceAfterSourceRetires(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("completed review")},
		ScriptedFinalAnswer(`{"transition":"review","commentary":"implementation complete"}`),
		ScriptedFinalAnswer(`{"transition":"rework","commentary":"changes requested"}`),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	threshold := 1
	f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
	workflowID := createCurrentNodeApprovalLoopWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	implementation := f.startTask(t, task)

	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForControllerCurrentNodeFinalized(t, approval.Source)
	implementationSession, err := f.store.LatestTaskSessionForNode(context.Background(), implementation)
	if err != nil {
		t.Fatalf("resolve previous target Session: %v", err)
	}
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply pending Approval: %v", err)
	}
	requests := f.waitForModelRequests(t, 3)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("loop post-completion compactions = %d, want one", len(client.CompactionCalls()))
	}
	targetNodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(implementation) &&
			nodes[0].SessionID != nil
	})
	if *targetNodes[0].SessionID != implementationSession.SessionID {
		t.Fatalf(
			"approved target Session = %q, want previous target Session %q",
			*targetNodes[0].SessionID,
			implementationSession.SessionID,
		)
	}
	initialAssignments := workflowAssignments(requests[0])
	reassignedImplementation := workflowAssignments(requests[2])
	if len(initialAssignments) != 1 {
		t.Fatalf("initial implementation assignments = %+v, want exactly one", initialAssignments)
	}
	if len(reassignedImplementation) != 1 {
		t.Fatalf(
			"approved implementation assignments = %+v, want exactly one reassignment after replacement",
			reassignedImplementation,
		)
	}
	for _, assignment := range reassignedImplementation {
		if assignment.sourcePath != initialAssignments[0].sourcePath {
			t.Fatalf(
				"approved previous-target assignment identity = %q, want implementation identity %q",
				assignment.sourcePath,
				initialAssignments[0].sourcePath,
			)
		}
	}
	for _, item := range requests[2].Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeCompactionSoonReminder {
			t.Fatal("loop reassignment request included a same-assignment compaction reminder")
		}
	}
}

func TestWorkflowPostCompletionDiagnosticPreservesApprovalCACBoundary(t *testing.T) {
	diagnostic := errors.New("workflow post-completion finalization diagnostic")
	var diagnosticMatched atomic.Bool
	var postCompactionObservation atomic.Bool
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("completed review")},
		ScriptedFinalAnswer(`{"transition":"review","commentary":"implementation complete"}`),
		ScriptedFinalAnswer(`{"transition":"rework","commentary":"changes requested"}`),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(t, client)
	f.persistenceGate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		if len(client.CompactionCalls()) == 0 {
			return false
		}
		if !postCompactionObservation.Swap(true) {
			return false
		}
		diagnosticMatched.Store(true)
		return true
	}, diagnostic)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	threshold := 1
	f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
	workflowID := createCurrentNodeApprovalLoopWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)

	approval := f.waitForPendingApproval(t, task.ID)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("loop post-completion compactions = %d, want one", len(client.CompactionCalls()))
	}
	if !diagnosticMatched.Load() {
		t.Fatal("post-completion finalization diagnostic was not exercised")
	}
	pending, err := f.store.ListPendingApprovals(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list pending Approval after finalization diagnostic: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != approval.ID {
		t.Fatalf("pending Approvals after finalization diagnostic = %+v, want original Approval", pending)
	}
	f.waitForControllerCurrentNodeFinalized(t, approval.Source)
	deadline := time.Now().Add(currentNodeRunnerWait)
	for {
		_, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID)
		if err == nil {
			break
		}
		if !errors.Is(err, workflowexecution.ErrTaskExecutionNotQuiescent) {
			t.Fatalf("apply CAC target Approval after finalization diagnostic: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("CAC target Approval remained blocked after finalization diagnostic: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	requests := f.waitForModelRequests(t, 3)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("CAC continuation compactions = %d, want committed source replacement only", len(client.CompactionCalls()))
	}
	if requests[2].PromptCacheKey == "" ||
		requests[2].PromptCacheKey == requests[0].PromptCacheKey ||
		requests[2].PromptCacheKey == requests[1].PromptCacheKey {
		t.Fatalf(
			"CAC continuation cache keys = %q/%q/%q, want fresh key distinct from prior requests",
			requests[0].PromptCacheKey,
			requests[1].PromptCacheKey,
			requests[2].PromptCacheKey,
		)
	}
}

func TestCompactAndContinueSessionEstablishesTargetRoleGeneration(t *testing.T) {
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedToolBatch(
			"complete first node",
			llm.ToolCall{
				ID:    "complete-first",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next","commentary":"first done"}`),
			},
		),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Compact role boundary",
		workflow.ContextModeCompactAndContinueSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the first node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the work."},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	f.waitForModelRequests(t, 2)

	requests := f.runtimeRequests()
	if len(requests) != 2 || requests[0].ActiveSettings.Model != "workflow-coder" || requests[1].ActiveSettings.Model != "workflow-reviewer" {
		t.Fatalf("runtime role models = %+v, want coder then reviewer", requests)
	}
	meta := f.onlyProjectSessionMeta(t)
	if meta.Continuation == nil || meta.Continuation.AgentRole == nil || *meta.Continuation.AgentRole != "reviewer" {
		t.Fatalf("compacted workflow Session continuation = %+v, want persisted reviewer identity", meta.Continuation)
	}
	if meta.PromptCacheLineageGeneration != 1 {
		t.Fatalf("compacted workflow Session cache lineage = %d, want 1", meta.PromptCacheLineageGeneration)
	}
}

func TestWorkflowPostCompletionCompactionReachesCACTargetWithoutSecondSummary(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{
					Type:        llm.ResponseItemTypeMessage,
					Role:        textutil.Value(llm.RoleUser),
					MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
					Content:     textutil.Value("completed source"),
				},
				{
					Type:             llm.ResponseItemTypeCompaction,
					ID:               textutil.Value("workflow-post-completion"),
					EncryptedContent: textutil.Value("encrypted"),
				},
			},
			Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		}},
		ScriptedToolBatch(
			"complete first node",
			llm.ToolCall{
				ID:    "complete-first",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next_1","commentary":"first done"}`),
			},
		),
		ScriptedToolBatch(
			"complete second node",
			llm.ToolCall{
				ID:    "complete-second",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next_2","commentary":"second done"}`),
			},
		),
		ScriptedFinalAnswer(`{"commentary":"target done"}`),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	workflowID := createCurrentNodeThreeStepWorkflow(
		t,
		f.store,
		"Successful post-turn compaction",
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the first node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the second node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the work."},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForControllerCurrentNodeFinalized(t, approval.Source)
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply CAC target Approval: %v", err)
	}
	requests := f.waitForModelRequests(t, 3)

	compactions := client.CompactionCalls()
	if len(compactions) != 1 {
		t.Fatalf("post-completion compactions = %d, want one", len(compactions))
	}
	summaries := 0
	for _, item := range requests[2].Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeCompactionSummary {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("target request compaction summaries = %d, want one", summaries)
	}
	if requests[1].PromptCacheKey == "" || requests[2].PromptCacheKey == "" ||
		requests[1].PromptCacheKey == requests[2].PromptCacheKey {
		t.Fatalf("source/target cache keys = %q/%q, want distinct non-empty keys", requests[1].PromptCacheKey, requests[2].PromptCacheKey)
	}
}

func TestDisabledCACRetriesExistingTargetOnResumeAfterConfigurationChange(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{
					Type:        llm.ResponseItemTypeMessage,
					Role:        textutil.Value(llm.RoleUser),
					MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
					Content:     textutil.Value("target-time CAC"),
				},
				{
					Type:             llm.ResponseItemTypeCompaction,
					ID:               textutil.Value("target-time-cac"),
					EncryptedContent: textutil.Value("encrypted"),
				},
			},
			Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		}},
		ScriptedToolBatch(
			"complete first node",
			llm.ToolCall{
				ID:    "complete-first",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next_1","commentary":"first done"}`),
			},
		),
		ScriptedToolBatch(
			"complete second node",
			llm.ToolCall{
				ID:    "complete-second",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next_2","commentary":"second done"}`),
			},
		),
		ScriptedFinalAnswer(`{"commentary":"resumed target done"}`),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNone
	workflowID := createCurrentNodeThreeStepWorkflow(
		t,
		f.store,
		"Resume disabled CAC",
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the first node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the second node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the work."},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForControllerCurrentNodeFinalized(t, approval.Source)
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply CAC target Approval: %v", err)
	}
	interrupted := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})[0]
	if interrupted.SessionID == nil {
		t.Fatal("disabled CAC target lost its assigned Session")
	}
	f.waitForControllerCurrentNodeFinalized(t, interrupted.Reference)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	if _, err := f.controller.ResumeTask(context.Background(), task.ID); err != nil {
		t.Fatalf("resume disabled CAC target: %v", err)
	}
	requests := f.waitForModelRequestsWithin(t, 3, 60*time.Second)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("resumed target-time compactions = %d, want one", len(client.CompactionCalls()))
	}
	targets := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Reference.Equal(interrupted.Reference)
	})
	if targets[0].SessionID == nil || *targets[0].SessionID != *interrupted.SessionID {
		t.Fatalf("resumed target Session = %v, want assigned Session %v", targets[0].SessionID, interrupted.SessionID)
	}
	if len(requests) != 3 {
		t.Fatalf("resumed model requests = %d, want source, source, target", len(requests))
	}
}

func TestWorkflowPostCompletionCompactionPreservesOrdinaryContinueReplacementKey(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("ordinary continuation")},
		ScriptedToolBatch(
			"complete first node",
			llm.ToolCall{
				ID:    "complete-first",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next_1","commentary":"first done"}`),
			},
		),
		ScriptedToolBatch(
			"complete second node",
			llm.ToolCall{
				ID:    "complete-second",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next_2","commentary":"second done"}`),
			},
		),
		ScriptedFinalAnswer(`{"commentary":"continued target done"}`),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	threshold := 1
	f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
	workflowID := createCurrentNodeThreeStepWorkflowWithTransition(
		t,
		f.store,
		"Ordinary post-turn continuation",
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the first node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the second node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Continue the work."},
		currentNodeLinearTransition{
			id:               "next_2",
			mode:             workflow.ContextModeContinueSession,
			requiresApproval: true,
			contextSource:    workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForControllerCurrentNodeFinalized(t, approval.Source)
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply ordinary continuation Approval: %v", err)
	}
	requests := f.waitForModelRequests(t, 3)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("ordinary continuation post-completions = %d, want one", len(client.CompactionCalls()))
	}
	if requests[1].PromptCacheKey == "" || requests[2].PromptCacheKey == "" ||
		requests[1].PromptCacheKey == requests[2].PromptCacheKey {
		t.Fatalf("ordinary continuation cache keys = %q/%q, want replacement key after source key", requests[1].PromptCacheKey, requests[2].PromptCacheKey)
	}
}

func TestPostTurnCompactionDiagnosticReleasesAssignedSuccessor(t *testing.T) {
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedToolBatch(
			"complete first node",
			llm.ToolCall{
				ID:    "complete-first",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next","commentary":"first done"}`),
			},
		),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Post-turn diagnostic successor",
		workflow.ContextModeCompactAndContinueSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the first node."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the work."},
	)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)

	target := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			!nodes[0].Reference.Equal(source)
	})[0].Reference
	f.waitForControllerCurrentNodeFinalized(t, source)
	if target.NodeID == source.NodeID {
		t.Fatalf("successor reference = %v, want a distinct target", target)
	}
}

func TestWorkflowRunnerCancellationDuringPostTurnFinalizationFinalizesInterruptedSourceScope(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("completed review")},
		ScriptedFinalAnswer(`{"transition":"rework","commentary":"changes requested"}`),
	)
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	threshold := 1
	f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
	var postCompactionObservation atomic.Bool
	compactionFinalizationStarted, releaseCompactionFinalization := f.persistenceGate.BlockWhen(func(session.PersistedStoreSnapshot) bool {
		if len(client.CompactionCalls()) == 0 {
			return false
		}
		return postCompactionObservation.Swap(true)
	})
	t.Cleanup(releaseCompactionFinalization)
	workflowID := createCurrentNodeTwoStepWorkflowWithTransition(
		t,
		f.store,
		"Approval post-turn cancellation",
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the implementation."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Address the review findings."},
		currentNodeLinearTransition{
			id:               "rework",
			mode:             workflow.ContextModeContinueSession,
			requiresApproval: true,
			contextSource:    workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		},
	)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)

	select {
	case <-compactionFinalizationStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("post-turn compaction finalization did not reach the cancellation gate")
	}
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("post-turn compactions before cancellation = %d, want one committed replacement", len(client.CompactionCalls()))
	}
	var execution sessionruntime.ExecutionHandle
	snapshot := f.controller.Snapshot()
	for _, live := range snapshot.LiveScopes {
		if !live.CurrentNode.Equal(source) {
			continue
		}
		var ok bool
		execution, ok = f.authority.ExecutionByScope(live.ScopeID)
		if !ok {
			t.Fatalf("resolve exact execution scope %s", live.ScopeID)
		}
		break
	}
	if execution == nil {
		for _, gate := range snapshot.Gates {
			if !gate.CurrentNode.Equal(source) {
				continue
			}
			var ok bool
			execution, ok = f.authority.ExecutionByScope(gate.ScopeID)
			if !ok {
				t.Fatalf("resolve exact execution scope %s", gate.ScopeID)
			}
			break
		}
	}
	if execution == nil {
		t.Fatal("post-turn finalization had no live exact execution scope")
	}
	if !execution.RequestStop() {
		t.Fatal("post-turn finalization exact execution scope was already stopped")
	}
	releaseCompactionFinalization()
	stopContext, cancelStop := context.WithTimeout(context.Background(), currentNodeRunnerWait)
	defer cancelStop()
	if err := execution.Stop(stopContext); err != nil &&
		!errors.Is(err, context.Canceled) {
		t.Fatalf("stop workflow exact execution scope: %v", err)
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(source) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForControllerCurrentNodeFinalized(t, source)
	approval := f.waitForPendingApproval(t, task.ID)
	pending, err := f.store.ListPendingApprovals(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list pending Approval after cancellation: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != approval.ID {
		t.Fatalf("pending Approvals after cancellation = %+v, want held source Approval", pending)
	}
	association, err := f.store.LatestTaskSessionForNode(context.Background(), source)
	if err != nil {
		t.Fatalf("resolve canceled source Session: %v", err)
	}
	persisted, err := f.metadata.ResolvePersistedSession(context.Background(), association.SessionID.String())
	if err != nil {
		t.Fatalf("resolve canceled source persisted Session: %v", err)
	}
	sourceStore, err := session.Open(persisted.SessionDir, f.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("open canceled source Session: %v", err)
	}
	eventLog, err := sourceStore.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize canceled source event log: %v", err)
	}
	window, err := eventLog.ReadNewestSegmentBackward(func(record session.EventRecord) bool {
		payload, err := record.Payload()
		if err != nil {
			return false
		}
		replacement, ok := payload.(session.HistoryReplacementRecord)
		return ok && replacement.Mode == session.CompactionModeWorkflowPostCompletion
	})
	if err != nil {
		t.Fatalf("read canceled source replacement segment: %v", err)
	}
	replacementCommitted := false
	for _, record := range window.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("decode canceled source replacement record: %v", err)
		}
		replacement, ok := payload.(session.HistoryReplacementRecord)
		if ok && replacement.Mode == session.CompactionModeWorkflowPostCompletion {
			replacementCommitted = true
			break
		}
	}
	if !replacementCommitted {
		t.Fatal("cancellation lost the committed Workflow Post-Compaction replacement")
	}
}

func TestResumeRetainsEstablishedSessionContractAndAttachedRuntime(t *testing.T) {
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedCancellation(),
		ScriptedCancellation(),
	)
	workflowID := createCurrentNodeAgentWorkflowWithCompletionMode(
		t,
		f.store,
		string(config.WorkflowCompletionModeTool),
	)
	task := f.createTask(t, workflowID)
	currentNode := f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil &&
			len(f.client.Requests()) == 1
	})
	f.waitForControllerCurrentNodeFinalized(t, currentNode)

	meta := f.onlyProjectSessionMeta(t)
	sessionID, err := runtimeids.ParseSessionID(meta.SessionID)
	if err != nil {
		t.Fatalf("parse workflow Session id: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("open workflow Session descriptor: %v", err)
	}
	deadline := time.Now().Add(currentNodeRunnerWait)
	for {
		admission, clearErr := f.authority.WithDormantSessionStore(context.Background(), descriptor, func(_ context.Context, store *session.Store) error {
			return store.SetContinuationContext(session.ContinuationContext{})
		})
		if clearErr != nil {
			t.Fatalf("clear legacy workflow Session role: %v", clearErr)
		}
		if !admission.RuntimeAvailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workflow Session runtime remained available after execution finalized")
		}
		time.Sleep(10 * time.Millisecond)
	}
	initialRuntime := f.runtimeRequests()[0]
	siblingWorkspace := t.TempDir()
	canonicalSiblingWorkspace, err := config.CanonicalWorkspaceRoot(siblingWorkspace)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot sibling: %v", err)
	}
	if _, err := f.metadata.AttachWorkspaceToProject(context.Background(), f.projectID, canonicalSiblingWorkspace); err != nil {
		t.Fatalf("AttachWorkspaceToProject sibling: %v", err)
	}
	projectBoundary, err := f.metadata.ResolveProjectWorkspaceBoundary(context.Background(), f.projectID)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceBoundary: %v", err)
	}
	interactiveFilesystemContext, err := runtimewire.NewFilesystemContext(f.workspace, f.workspace, projectBoundary)
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	if len(interactiveFilesystemContext.Access.ProjectWorkspace.Roots) != 2 {
		t.Fatalf("workflow runtime Project Workspace roots = %+v, want source and sibling", interactiveFilesystemContext.Access.ProjectWorkspace.Roots)
	}
	foundSibling := false
	for _, root := range interactiveFilesystemContext.Access.ProjectWorkspace.Roots {
		if root.RealPath == canonicalSiblingWorkspace {
			foundSibling = true
			break
		}
	}
	if !foundSibling {
		t.Fatalf("workflow runtime roots = %+v, want sibling %q", interactiveFilesystemContext.Access.ProjectWorkspace.Roots, canonicalSiblingWorkspace)
	}
	interactivePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:          initialRuntime.ActiveSettings,
		EnabledTools:      initialRuntime.EnabledTools,
		FilesystemContext: interactiveFilesystemContext,
		Sources:           initialRuntime.Sources,
		Client:            f.client,
	})
	if err != nil {
		t.Fatalf("build attached Session runtime: %v", err)
	}
	attachment, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "tui-test-owner",
		Runtime:   &interactivePlan,
	})
	if err != nil {
		t.Fatalf("attach Session runtime: %v", err)
	}
	t.Cleanup(func() {
		_, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose)
		if releaseErr != nil && !errors.Is(releaseErr, serverapi.ErrRuntimeUnavailable) {
			t.Errorf("release attached Session runtime: %v", releaseErr)
		}
	})
	subscription, err := f.runtimes.SubscribeSessionTranscript(
		context.Background(),
		serverapi.TranscriptSubscribeRequest{SessionID: sessionID.String()},
	)
	if err != nil {
		t.Fatalf("subscribe attached Session transcript: %v", err)
	}
	t.Cleanup(func() { _ = subscription.Close() })
	hydrationCtx, cancelHydration := context.WithTimeout(context.Background(), currentNodeRunnerWait)
	if _, err := subscription.Next(hydrationCtx); err != nil {
		cancelHydration()
		t.Fatalf("hydrate attached Session transcript: %v", err)
	}
	cancelHydration()

	if _, err := f.store.UpdateNode(context.Background(), workflowstore.NodeRecord{
		ID:             currentNode.NodeID,
		WorkflowID:     workflowID,
		Key:            "execute",
		Kind:           workflow.NodeKindAgent,
		DisplayName:    "Execute",
		SubagentRole:   "coder",
		CompletionMode: string(config.WorkflowCompletionModeStructuredOutput),
	}); err != nil {
		t.Fatalf("change latest node completion mode: %v", err)
	}
	if _, err := f.controller.ResumeTask(context.Background(), task.ID); err != nil {
		t.Fatalf("resume task: %v", err)
	}
	eventCtx, cancelEvent := context.WithTimeout(context.Background(), currentNodeRunnerWait)
	event, err := subscription.Next(eventCtx)
	cancelEvent()
	if err != nil {
		t.Fatalf("attached transcript subscription did not receive resumed runtime event: %v", err)
	}
	if event.Sequence <= 1 {
		t.Fatalf("resumed runtime event sequence = %d, want post-hydration delivery", event.Sequence)
	}

	requests := f.waitForModelRequests(t, 2)
	requireToolCompletionRequests(t, requests)
	initialAssignments := workflowAssignments(requests[0])
	resumedAssignments := workflowAssignments(requests[1])
	if len(initialAssignments) != 1 || len(resumedAssignments) != 1 {
		t.Fatalf(
			"workflow assignments before/after Resume = %+v / %+v, want one unchanged assignment",
			initialAssignments,
			resumedAssignments,
		)
	}
	if resumedAssignments[0].sourcePath != initialAssignments[0].sourcePath {
		t.Fatalf("resumed assignment = %+v, want %+v", resumedAssignments[0], initialAssignments[0])
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil &&
			len(f.client.Requests()) == 2
	})
	f.waitForControllerCurrentNodeFinalized(t, currentNode)
	if err := f.authority.WithRuntime(context.Background(), attachment.Resource(), func(_ context.Context, engine *agentruntime.Engine) error {
		if !engine.CurrentNodeExecutionConfigured() {
			t.Fatal("finalized workflow execution discarded the retained Session contract")
		}
		return nil
	}); err != nil {
		t.Fatalf("attached Resource Generation was replaced on Resume: %v", err)
	}
}

func requestAdvertisesTool(request llm.Request, id toolspec.ID) bool {
	for _, tool := range request.Tools {
		if tool.Name == string(id) {
			return true
		}
	}
	return false
}

func TestCurrentNodeAgentUsesDurableCommentCountWithoutReadingCommentBodies(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t, ScriptedFinalAnswer(`{"commentary":"done"}`))
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	if _, err := f.store.AddComment(context.Background(), task.ID, "first confidential comment", "user", "user-1"); err != nil {
		t.Fatalf("add first comment: %v", err)
	}
	if _, err := f.store.AddComment(context.Background(), task.ID, "second confidential comment", "user", "user-1"); err != nil {
		t.Fatalf("add second comment: %v", err)
	}
	f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	if requests := f.client.Requests(); len(requests) != 1 {
		t.Fatalf("model requests = %d, want one", len(requests))
	}
}

func TestCurrentNodeTaskAwarenessMatchesCanonicalDependencyProjectionAcrossTerminalChanges(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	blocker := f.createTask(t, workflowID)
	blocked := f.createTask(t, workflowID)
	if _, err := f.store.AddTaskDependency(context.Background(), workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	}); err != nil {
		t.Fatalf("add Task dependency: %v", err)
	}
	assertAwareness := func(want int64) {
		t.Helper()
		awareness, err := f.starter.taskAwarenessSource.TaskAwareness(context.Background(), blocked.ID)
		if err != nil {
			t.Fatalf("TaskAwareness: %v", err)
		}
		projected, err := f.dependencies.GetTaskDependencies(context.Background(), string(blocked.ID))
		if err != nil {
			t.Fatalf("GetTaskDependencies: %v", err)
		}
		if awareness.UnsatisfiedDependencyCount != want ||
			awareness.UnsatisfiedDependencyCount != int64(projected.UnsatisfiedBlockerCount) {
			t.Fatalf("awareness/projection unsatisfied count = %d/%d, want %d", awareness.UnsatisfiedDependencyCount, projected.UnsatisfiedBlockerCount, want)
		}
	}
	assertAwareness(1)

	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := f.store.ManualMoveTask(context.Background(), workflowstore.ManualMoveRequest{
		TaskID: blocker.ID, TargetNodeID: currentNodeKindID(t, definition, workflow.NodeKindTerminal),
	}); err != nil {
		t.Fatalf("manual terminal move: %v", err)
	}
	assertAwareness(0)
	if _, err := f.store.ManualMoveTask(context.Background(), workflowstore.ManualMoveRequest{
		TaskID: blocker.ID, TargetNodeID: currentNodeKindID(t, definition, workflow.NodeKindStart),
	}); err != nil {
		t.Fatalf("reopen blocker: %v", err)
	}
	assertAwareness(1)

	started, err := f.store.StartTask(context.Background(), blocker.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("started Current Nodes = %+v, want one", started.Mutation.Created)
	}
	if _, err := f.store.CompleteCurrentNode(context.Background(), workflowstore.CurrentNodeCompletionRequest{
		Source: started.Mutation.Created[0].Reference, TransitionID: "done",
	}); err != nil {
		t.Fatalf("complete blocker to ordinary terminal: %v", err)
	}
	assertAwareness(0)
}

func currentNodeKindID(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatalf("workflow definition has no %s node", kind)
	return ""
}

func TestCurrentNodeRuntimePreparationFailureRetainsAssignedFreshSession(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.starter.cfg.Settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:           "test",
		SupportsResponsesAPI: true,
	}
	f.mu.Lock()
	f.clientErr = errors.New("provider unavailable")
	f.mu.Unlock()

	started, err := f.controller.StartTask(context.Background(), task.ID, func(ctx context.Context) error {
		return f.store.LockTaskExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
			Snapshot: workflowstore.ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: workflowstore.ExecutionTargetProvenanceResolved,
			},
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   f.workspaceID,
				SourceWorkspaceRoot: f.workspace,
			},
		})
	})
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("start mutation = %+v, want one Current Node", started.Mutation)
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted
	})
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 1 {
		t.Fatalf("retained Session count after runtime preparation failure = %d, %v; want assigned Session", count, err)
	}
}

func TestFreshRetainedUserActivationOwnsTheSameWorkflowExecutionAsTaskResume(t *testing.T) {
	f, task, sessionID := newInterruptedRetainedActivationFixture(t)
	api := newCurrentNodeActivationAPI(t, f)

	if _, err := api.ActivateSessionRuntime(
		context.Background(),
		currentNodeActivationRequest(t, sessionID, "user_activation"),
	); err != nil {
		t.Fatalf("fresh retained user activation: %v", err)
	}
	if _, live := f.authority.SessionExecution(sessionID); !live {
		t.Fatal("fresh retained user activation returned without creating or joining the Task Resume Workflow execution")
	}
	nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(nodes) != 1 ||
		nodes[0].Scheduling == nil ||
		nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("fresh retained user activation left Task Resume authority untouched: %+v", nodes)
	}
}

func TestTechnicalReattachmentLeavesInterruptedRetainedWorkflowWithoutStartingWork(t *testing.T) {
	f, task, sessionID := newInterruptedRetainedActivationFixture(t)
	api := newCurrentNodeActivationAPI(t, f)

	_, err := api.ActivateSessionRuntime(
		context.Background(),
		currentNodeActivationRequest(t, sessionID, "technical_reattachment"),
	)
	if err == nil {
		t.Fatal("technical reattachment created an ordinary Runtime for an interrupted retained Workflow Session")
	}
	nodes, listErr := f.store.ListCurrentNodes(context.Background(), task.ID)
	if listErr != nil {
		t.Fatalf("ListCurrentNodes: %v", listErr)
	}
	if len(nodes) != 1 ||
		nodes[0].Scheduling == nil ||
		nodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("technical reattachment changed interrupted Current Node state: %+v", nodes)
	}
	if _, live := f.authority.SessionExecution(sessionID); live {
		t.Fatal("technical reattachment created an Exact Workflow execution")
	}
}

func newInterruptedRetainedActivationFixture(
	t *testing.T,
) (*currentNodeRunnerFixture, workflowstore.TaskRecord, runtimeids.SessionID) {
	t.Helper()
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.starter.cfg.Settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:           "test",
		SupportsResponsesAPI: true,
	}
	f.mu.Lock()
	f.clientErr = errors.New("provider unavailable")
	f.mu.Unlock()
	f.startTask(t, task)
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].SessionID != nil &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted
	})
	f.mu.Lock()
	f.clientErr = nil
	f.mu.Unlock()
	return f, task, *nodes[0].SessionID
}

func newCurrentNodeActivationAPI(
	t *testing.T,
	f *currentNodeRunnerFixture,
) *sessionruntime.API {
	t.Helper()
	return sessionruntime.NewAPI(f.metadata, nil, f.authority, sessionruntime.APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(
			context.Context,
			runtimewire.RuntimeClientRequest,
		) (llm.Client, error) {
			client, ok := f.client.(llm.Client)
			if !ok {
				return nil, fmt.Errorf("fixture client %T does not implement llm.Client", f.client)
			}
			return client, nil
		}),
	})
}

func currentNodeActivationRequest(
	t *testing.T,
	sessionID runtimeids.SessionID,
	operation string,
) serverapi.SessionRuntimeActivateRequest {
	t.Helper()
	request := serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activation-request",
		SessionID:       sessionID.String(),
		OwnerID:         "activation-owner",
		ActiveSettings: config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
			Shell: config.ShellSettings{
				PostprocessingMode: config.ShellPostprocessingModeBuiltin,
			},
		},
		Source: config.SourceReport{Sources: map[string]string{}},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal activation request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode activation request: %v", err)
	}
	payload["operation"] = operation
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal typed activation request: %v", err)
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatalf("decode typed activation request: %v", err)
	}
	return request
}

func TestCurrentNodeContinuationModesReuseTheRetainedSession(t *testing.T) {
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedFinalAnswer(`{"transition":"next","commentary":"first"}`),
		ScriptedFinalAnswer(`{"commentary":"second"}`),
	)
	workflowID := createCurrentNodeChainedWorkflow(t, f.store, workflow.ContextModeContinueSession)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 1 {
		t.Fatalf("retained Session count = %d, %v; want one reused Session", count, err)
	}
	if requests := f.client.Requests(); len(requests) != 2 {
		t.Fatalf("model requests = %d, want one turn for each Current Node", len(requests))
	}
}

func TestCurrentNodeFanoutContinuationClonesAndBindsEachBranchSession(t *testing.T) {
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedFinalAnswer(`{"transition":"split","commentary":"source"}`),
		ScriptedFinalAnswer(`{"commentary":"branch"}`),
		ScriptedFinalAnswer(`{"commentary":"branch"}`),
	)
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store, false)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)

	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && !nodes[0].Reference.IsBranchScoped() && nodes[0].Scheduling == nil
	})
	sourceAssociation, err := f.store.LatestTaskSessionForNode(context.Background(), source)
	if err != nil {
		t.Fatalf("resolve source Session association: %v", err)
	}
	branchAssociations := make([]workflowstore.TaskSessionAssociation, 0, len(branchNodeIDs))
	for branchKey, nodeID := range branchNodeIDs {
		reference, err := workflow.NewCurrentNodeReference(task.ID, nodeID, &branchKey)
		if err != nil {
			t.Fatalf("create branch %q Current Node reference: %v", branchKey, err)
		}
		association, err := f.store.LatestTaskSessionForNode(context.Background(), reference)
		if err != nil {
			t.Fatalf("resolve branch %q Session association: %v", branchKey, err)
		}
		if association.SessionID == sourceAssociation.SessionID {
			t.Fatalf("branch %q reused source Session %q, want fan-out clone", branchKey, association.SessionID)
		}
		branchAssociations = append(branchAssociations, association)
	}
	if len(branchAssociations) != 2 || branchAssociations[0].SessionID == branchAssociations[1].SessionID {
		t.Fatalf("branch Session associations = %+v, want two distinct clones", branchAssociations)
	}
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 3 {
		t.Fatalf("retained Session count = %d, %v; want source plus two fan-out clones", count, err)
	}
}

func TestWorkflowPostCompletionCompactsFanoutSourceBeforeBranchClones(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{
			ProviderID:               "test",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			SupportsPromptCacheKey:   true,
		},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("completed fan-out source")},
		ScriptedFinalAnswer(`{"transition":"split","commentary":"source"}`),
		ScriptedFinalAnswer(`{"commentary":"branch a"}`),
		ScriptedFinalAnswer(`{"commentary":"branch b"}`),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	threshold := 1
	f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store, true)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)

	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForControllerCurrentNodeFinalized(t, source)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("fan-out source post-completion compactions = %d, want one", len(client.CompactionCalls()))
	}
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply fan-out Approval: %v", err)
	}
	requests := f.waitForModelRequests(t, 3)
	for index, request := range requests[1:] {
		summaries := 0
		for _, item := range request.Items {
			if item.Type == llm.ResponseItemTypeMessage &&
				item.MessageType != nil &&
				*item.MessageType == llm.MessageTypeCompactionSummary {
				summaries++
			}
		}
		if summaries != 1 {
			t.Fatalf("branch request %d compaction summaries = %d, want one", index+2, summaries)
		}
	}
	if requests[1].PromptCacheKey == "" ||
		requests[2].PromptCacheKey == "" ||
		requests[1].PromptCacheKey == requests[2].PromptCacheKey ||
		requests[0].PromptCacheKey == requests[1].PromptCacheKey ||
		requests[0].PromptCacheKey == requests[2].PromptCacheKey {
		t.Fatalf(
			"fan-out cache lineage keys = %q/%q/%q, want three distinct non-empty keys",
			requests[0].PromptCacheKey,
			requests[1].PromptCacheKey,
			requests[2].PromptCacheKey,
		)
	}
	sourceAssociation, err := f.store.LatestTaskSessionForNode(context.Background(), source)
	if err != nil {
		t.Fatalf("resolve source Session association: %v", err)
	}
	branchSessionIDs := make(map[runtimeids.SessionID]struct{}, len(branchNodeIDs))
	for branchKey, nodeID := range branchNodeIDs {
		reference, err := workflow.NewCurrentNodeReference(task.ID, nodeID, &branchKey)
		if err != nil {
			t.Fatalf("create branch %q Current Node reference: %v", branchKey, err)
		}
		association, err := f.store.LatestTaskSessionForNode(context.Background(), reference)
		if err != nil {
			t.Fatalf("resolve branch %q Session association: %v", branchKey, err)
		}
		if association.SessionID == sourceAssociation.SessionID {
			t.Fatalf("branch %q reused source Session %q after pre-compaction", branchKey, association.SessionID)
		}
		branchSessionIDs[association.SessionID] = struct{}{}
	}
	if len(branchSessionIDs) != len(branchNodeIDs) {
		t.Fatalf("fan-out branch Session lineages = %+v, want one distinct clone per branch", branchSessionIDs)
	}
}

func TestCurrentNodeFanoutRuntimePreparationFailureKeepsAssignedBranchesResumable(t *testing.T) {
	sourceResponseStarted := make(chan struct{})
	sourceResponseRelease := make(chan struct{})
	var releaseSource sync.Once
	t.Cleanup(func() {
		releaseSource.Do(func() { close(sourceResponseRelease) })
	})
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(sourceResponseStarted)
				select {
				case <-sourceResponseRelease:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"transition":"split","commentary":"source"}`).Response,
		},
	)
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store, false)
	task := f.createTask(t, workflowID)
	f.starter.store = currentNodeStartContextStore{
		RuntimeStore: f.store,
		transform: func(input workflowstore.CurrentNodeStartContext) workflowstore.CurrentNodeStartContext {
			if !input.IsFanoutBranch {
				return input
			}
			root := *input.ExecutionRoot
			root.SourceWorkspaceID = "workspace-missing"
			input.ExecutionRoot = &root
			return input
		},
	}
	source := f.startTask(t, task)
	select {
	case <-sourceResponseStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("source Current Node did not start")
	}
	sourceNodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(source) &&
			nodes[0].SessionID != nil
	})
	sourceSessionID := *sourceNodes[0].SessionID

	releaseSource.Do(func() { close(sourceResponseRelease) })
	branches := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		if len(nodes) != len(branchNodeIDs) {
			return false
		}
		for _, node := range nodes {
			if node.Scheduling == nil ||
				node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
				return false
			}
		}
		return true
	})
	branchSessionIDs := map[runtimeids.SessionID]struct{}{}
	for _, branch := range branches {
		if branch.SessionID == nil {
			t.Fatalf("branch %v has no resumable Session", branch.Reference)
		}
		if *branch.SessionID == sourceSessionID {
			t.Fatalf("branch %v reused source Session %q instead of its assigned clone", branch.Reference, sourceSessionID)
		}
		branchSessionIDs[*branch.SessionID] = struct{}{}
		if err := f.store.ValidateCurrentNodeSessionBinding(
			context.Background(),
			*branch.SessionID,
			branch.Reference,
		); err != nil {
			t.Fatalf("validate resumable branch Session binding %v: %v", branch.Reference, err)
		}
	}
	if len(branchSessionIDs) != len(branchNodeIDs) {
		t.Fatalf("retained clone Sessions = %d, want one per branch", len(branchSessionIDs))
	}
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 3 {
		t.Fatalf("retained Session count after branch runtime failures = %d, %v; want source plus branch clones", count, err)
	}
}

func TestMixedAgentScriptSuccessorKeepsHealthyScriptOwnedWhenAgentAssignmentFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	sourceStarted := make(chan struct{})
	releaseSource := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSource) }) })
	client := NewScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(sourceStarted)
				select {
				case <-releaseSource:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"transition":"split","commentary":"source done"}`).Response,
		},
	)
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(t, client)
	scriptMarker := filepath.Join(f.workspace, "mixed-successor-script.started")
	scriptPath := filepath.Join(f.workspace, "mixed-successor-script.sh")
	if err := os.WriteFile(
		scriptPath,
		[]byte("#!/bin/sh\n: > "+workflowRunnerShellQuote(scriptMarker)+"\nprintf '%s' '{\"commentary\":\"script done\"}'\n"),
		0o755,
	); err != nil {
		t.Fatalf("write mixed successor script: %v", err)
	}
	workflowID := createCurrentNodeMixedFanoutWorkflow(t, f.store, scriptPath)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)
	select {
	case <-sourceStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("source Agent did not reach its model turn")
	}
	sourceNodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Reference.Equal(source) && nodes[0].SessionID != nil
	})
	sourceSessionID := *sourceNodes[0].SessionID
	assignmentFailure := errors.New("injected retained Agent assignment failure")
	f.persistenceGate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.SessionID != sourceSessionID.String() && snapshot.Meta.LastSequence > 0
	}, assignmentFailure)

	releaseOnce.Do(func() { close(releaseSource) })
	if !testsetup.Until(time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := os.Stat(scriptMarker)
		return err == nil
	}) {
		t.Fatal("mixed Agent/Script successor stranded the healthy Script after retained Agent assignment failed")
	}
	if !testsetup.Until(time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return f.controller.EnsureTaskQuiescent(task.ID) == nil
	}) {
		t.Fatal("mixed Agent/Script successor execution did not reach a stable disposition")
	}
}

func TestAcceptedInertActiveReadRaceCannotRemainStableAfterSuccessorOwnershipFailureReturns(t *testing.T) {
	sourceStarted := make(chan struct{})
	releaseSource := make(chan struct{})
	var releaseSourceOnce sync.Once
	t.Cleanup(func() { releaseSourceOnce.Do(func() { close(releaseSource) }) })
	client := NewScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(sourceStarted)
				select {
				case <-releaseSource:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"transition":"next","commentary":"source done"}`).Response,
		},
	)
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(t, client)
	workflowID := createCurrentNodeChainedWorkflow(t, f.store, workflow.ContextModeNewSession)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)
	select {
	case <-sourceStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("source Agent did not reach its model turn")
	}
	sourceNodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Reference.Equal(source) && nodes[0].SessionID != nil
	})
	sourceSessionID := *sourceNodes[0].SessionID
	assignmentEntered, releaseAssignment := f.persistenceGate.BlockWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.SessionID != sourceSessionID.String() && snapshot.Meta.LastSequence > 0
	})
	t.Cleanup(releaseAssignment)
	sourceExecution, live := f.authority.SessionExecution(sourceSessionID)
	if !live {
		t.Fatal("source Agent has no Exact Workflow execution")
	}
	releaseSourceOnce.Do(func() { close(releaseSource) })
	select {
	case <-assignmentEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("successor assignment did not reach the post-commit handoff")
	}
	successorNodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && !nodes[0].Reference.Equal(source)
	})
	successor := successorNodes[0].Reference
	surfaces := newCurrentNodeRunnerReadSurfaces(t, f)
	surfaces.requireState(
		t,
		task,
		workflowID,
		successor.NodeID,
		serverapi.WorkflowTaskStatusKindActive,
		false,
		false,
		"accepted post-commit read race",
	)

	if !sourceExecution.RequestStop() {
		t.Fatal("source Workflow execution finalized before the ownership-failure barrier was released")
	}
	releaseAssignment()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = sourceExecution.Wait(waitCtx)
	cancelWait()
	f.waitForControllerCurrentNodeFinalized(t, source)
	surfaces.requireState(
		t,
		task,
		workflowID,
		successor.NodeID,
		serverapi.WorkflowTaskStatusKindInterrupted,
		true,
		false,
		"stable state after successor ownership failure returned",
	)
}

func TestAutomaticAgentCapacityRemainsLeasedForTheFullExactLifetime(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		releaseSecondOnce.Do(func() { close(releaseSecond) })
	})
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(firstStarted)
				select {
				case <-releaseFirst:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"commentary":"first done"}`).Response,
		},
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(secondStarted)
				select {
				case <-releaseSecond:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"commentary":"second done"}`).Response,
		},
	)
	if runtime.GOOS == "windows" {
		t.Skip("capacity scheduler barrier uses a POSIX shell script")
	}
	scriptMarker := filepath.Join(f.workspace, "capacity-scheduler-barrier.started")
	scriptPath := filepath.Join(f.workspace, "capacity-scheduler-barrier.sh")
	if err := os.WriteFile(
		scriptPath,
		[]byte("#!/bin/sh\n: > "+workflowRunnerShellQuote(scriptMarker)+"\nprintf '%s' '{\"commentary\":\"capacity barrier done\"}'\n"),
		0o755,
	); err != nil {
		t.Fatalf("write capacity scheduler barrier: %v", err)
	}
	scriptWorkflowID := createCurrentNodeScriptWorkflow(t, f.store, scriptPath)
	scriptTask := f.createTask(t, scriptWorkflowID)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	firstTask := f.createTask(t, workflowID)
	secondTask := f.createTask(t, workflowID)
	f.startTask(t, firstTask)
	select {
	case <-firstStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("first Agent did not reach its Exact execution")
	}
	f.startTask(t, secondTask)
	f.startTask(t, scriptTask)
	f.waitForPath(t, scriptMarker)
	f.waitForCurrentNode(t, scriptTask.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	select {
	case <-secondStarted:
		t.Fatal("second Agent entered Exact execution while the first Agent was exact")
	case <-time.After(3 * time.Second):
	}
	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case <-secondStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("second Agent did not start after first Exact finalized")
	}
}

type currentNodeRunnerReadSurfaces struct {
	projection *workflowview.TaskStatusProjection
	detail     *workflowview.TaskDetail
	tasks      *workflowview.TaskList
	board      *workflowview.Board
	search     *workflowview.TaskSearch
	projectID  string
}

func TestCurrentNodeAgentReadSurfacesTrackReadyAdmittedExactAndInterrupted(t *testing.T) {
	modelEntered := make(chan struct{})
	releaseModel := make(chan struct{})
	var releaseModelOnce sync.Once
	t.Cleanup(func() { releaseModelOnce.Do(func() { close(releaseModel) }) })
	runnerEntered := make(chan workflow.CurrentNodeReference, 1)
	releaseRunner := make(chan struct{})
	var releaseRunnerOnce sync.Once
	t.Cleanup(func() { releaseRunnerOnce.Do(func() { close(releaseRunner) }) })
	f := newCurrentNodeRunnerFixtureWithRunner(
		t,
		NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			ScriptedRuntimeStep{
				BeforeResponse: func(ctx context.Context) error {
					close(modelEntered)
					select {
					case <-releaseModel:
						return nil
					case <-ctx.Done():
						return context.Cause(ctx)
					}
				},
				Response: ScriptedFinalAnswer(`{"commentary":"done"}`).Response,
			},
		),
		false,
		func(delegate workflowexecution.CurrentNodeRunner) workflowexecution.CurrentNodeRunner {
			return barrierCurrentNodeRunner{
				delegate: delegate,
				entered:  runnerEntered,
				release:  releaseRunner,
			}
		},
	)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	preparationEntered := make(chan struct{})
	releasePreparation := make(chan struct{})
	var releasePreparationOnce sync.Once
	t.Cleanup(func() { releasePreparationOnce.Do(func() { close(releasePreparation) }) })
	started, err := f.controller.StartTask(context.Background(), task.ID, func(ctx context.Context) error {
		close(preparationEntered)
		select {
		case <-releasePreparation:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		return f.store.LockTaskExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
			Snapshot: workflowstore.ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: workflowstore.ExecutionTargetProvenanceResolved,
			},
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   f.workspaceID,
				SourceWorkspaceRoot: f.workspace,
			},
		})
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v", started.Mutation)
	}
	reference := started.Mutation.Created[0].Reference
	surfaces := newCurrentNodeRunnerReadSurfaces(t, f)
	select {
	case <-preparationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent launch did not enter ready-scheduling preparation")
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindQueued, false, true, "ready-scheduling launch")
	requireInterruptibleLaunchingRun(t, f.controller, reference)

	releasePreparationOnce.Do(func() { close(releasePreparation) })
	select {
	case entered := <-runnerEntered:
		if !entered.Equal(reference) {
			t.Fatalf("admitted runner reference = %v, want %v", entered, reference)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Agent launch did not enter admitted-scheduling runner barrier")
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindQueued, false, true, "admitted-scheduling launch")
	requireInterruptibleLaunchingRun(t, f.controller, reference)

	releaseRunnerOnce.Do(func() { close(releaseRunner) })
	select {
	case <-modelEntered:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("Agent did not reach Exact execution")
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindRunning, false, true, "Exact execution")

	if err := f.controller.Interrupt(context.Background(), workflowexecution.InterruptSelector{TaskID: task.ID}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindInterrupted, true, false, "durable interruption")
}

func TestCurrentNodeAgentReadSurfacesProjectCapacityWaitAsNonInterruptibleQueued(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	t.Cleanup(func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) })
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(firstEntered)
				select {
				case <-releaseFirst:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"commentary":"first done"}`).Response,
		},
		ScriptedFinalAnswer(`{"commentary":"second done"}`),
	)
	scriptPath := filepath.Join(f.workspace, "automatic-agent-source.sh")
	if err := os.WriteFile(
		scriptPath,
		[]byte("#!/bin/sh\nprintf '%s' '{\"transition\":\"next\",\"commentary\":\"source done\"}'\n"),
		0o755,
	); err != nil {
		t.Fatalf("write automatic Agent source Script: %v", err)
	}
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Automatic Agent capacity read",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: scriptPath},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Continue."},
	)
	first := f.createTask(t, workflowID)
	f.startTask(t, first)
	select {
	case <-firstEntered:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("first automatic Agent did not occupy capacity")
	}
	second := f.createTask(t, workflowID)
	secondSource := f.startTask(t, second)
	secondNodes := f.waitForCurrentNode(t, second.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && !nodes[0].Reference.Equal(secondSource)
	})
	secondReference := secondNodes[0].Reference
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		observation, err := f.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{second.ID})
		if err != nil {
			return false
		}
		runs := observation.Runs[second.ID]
		return len(runs.Queued) == 1 &&
			runs.Queued[0].Equal(secondReference) &&
			len(runs.InterruptibleLaunching) == 0
	}, "second Agent did not remain an ordinary queued Run")

	surfaces := newCurrentNodeRunnerReadSurfaces(t, f)
	surfaces.requireState(
		t,
		second,
		workflowID,
		secondReference.NodeID,
		serverapi.WorkflowTaskStatusKindQueued,
		false,
		false,
		"Agent capacity wait",
	)
	releaseFirstOnce.Do(func() { close(releaseFirst) })
}

func TestCurrentNodeScriptReadSurfacesTrackReadyAdmittedExactAndInterrupted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	runnerEntered := make(chan workflow.CurrentNodeReference, 1)
	releaseRunner := make(chan struct{})
	var releaseRunnerOnce sync.Once
	t.Cleanup(func() { releaseRunnerOnce.Do(func() { close(releaseRunner) }) })
	f := newCurrentNodeRunnerFixtureWithRunner(
		t,
		NewScriptedClient(llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}),
		false,
		func(delegate workflowexecution.CurrentNodeRunner) workflowexecution.CurrentNodeRunner {
			return barrierCurrentNodeRunner{
				delegate: delegate,
				entered:  runnerEntered,
				release:  releaseRunner,
			}
		},
	)
	scriptEntered := filepath.Join(f.workspace, "read-surface-script.entered")
	releaseScript := filepath.Join(f.workspace, "read-surface-script.release")
	scriptPath := filepath.Join(f.workspace, "read-surface-script.sh")
	script := "#!/bin/sh\n: > " + workflowRunnerShellQuote(scriptEntered) + "\nwhile [ ! -f " + workflowRunnerShellQuote(releaseScript) + " ]; do sleep 0.05; done\nprintf '%s' '{\"commentary\":\"done\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write Script: %v", err)
	}
	workflowID := createCurrentNodeScriptWorkflow(t, f.store, scriptPath)
	task := f.createTask(t, workflowID)
	preparationEntered := make(chan struct{})
	releasePreparation := make(chan struct{})
	var releasePreparationOnce sync.Once
	t.Cleanup(func() { releasePreparationOnce.Do(func() { close(releasePreparation) }) })
	started, err := f.controller.StartTask(context.Background(), task.ID, func(ctx context.Context) error {
		close(preparationEntered)
		select {
		case <-releasePreparation:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		return f.store.LockTaskExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
			Snapshot: workflowstore.ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: workflowstore.ExecutionTargetProvenanceResolved,
			},
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   f.workspaceID,
				SourceWorkspaceRoot: f.workspace,
			},
		})
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v", started.Mutation)
	}
	reference := started.Mutation.Created[0].Reference
	surfaces := newCurrentNodeRunnerReadSurfaces(t, f)
	select {
	case <-preparationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Script launch did not enter ready-scheduling preparation")
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindQueued, false, true, "ready-scheduling Script launch")
	requireInterruptibleLaunchingRun(t, f.controller, reference)

	releasePreparationOnce.Do(func() { close(releasePreparation) })
	select {
	case entered := <-runnerEntered:
		if !entered.Equal(reference) {
			t.Fatalf("admitted Script reference = %v, want %v", entered, reference)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Script launch did not enter admitted-scheduling runner barrier")
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindQueued, false, true, "admitted-scheduling Script launch")
	requireInterruptibleLaunchingRun(t, f.controller, reference)

	releaseRunnerOnce.Do(func() { close(releaseRunner) })
	f.waitForPath(t, scriptEntered)
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindRunning, false, true, "Exact Script execution")
	if err := f.controller.Interrupt(context.Background(), workflowexecution.InterruptSelector{TaskID: task.ID}); err != nil {
		t.Fatalf("Interrupt Script: %v", err)
	}
	surfaces.requireState(t, task, workflowID, reference.NodeID, serverapi.WorkflowTaskStatusKindInterrupted, true, false, "durable Script interruption")
}

func TestLaunchInterruptPersistenceFailureLeavesTheSameRunInterruptible(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	preparationEntered := make(chan struct{})
	releasePreparation := make(chan struct{})
	var releasePreparationOnce sync.Once
	t.Cleanup(func() { releasePreparationOnce.Do(func() { close(releasePreparation) }) })
	started, err := f.controller.StartTask(context.Background(), task.ID, func(ctx context.Context) error {
		close(preparationEntered)
		select {
		case <-releasePreparation:
			return errors.New("test launch released")
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	select {
	case <-preparationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("launch did not enter ready-scheduling preparation")
	}
	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`CREATE TRIGGER fail_user_launch_interruption
BEFORE UPDATE OF scheduling_state ON task_current_nodes
WHEN OLD.scheduling_state = 'ready' AND NEW.scheduling_state = 'interrupted'
BEGIN
    SELECT RAISE(ABORT, 'injected user launch interruption failure');
END`,
	); err != nil {
		t.Fatalf("install launch interruption failure: %v", err)
	}
	if err := f.controller.Interrupt(context.Background(), workflowexecution.InterruptSelector{TaskID: task.ID}); err == nil {
		t.Fatal("Interrupt succeeded despite injected persistence failure")
	} else if errors.Is(err, workflowexecution.ErrNoInterruptibleExecution) {
		t.Fatalf("ready launch was not selected as interruptible: %v", err)
	}
	requireInterruptibleLaunchingRun(t, f.controller, reference)
	newCurrentNodeRunnerReadSurfaces(t, f).requireState(
		t,
		task,
		workflowID,
		reference.NodeID,
		serverapi.WorkflowTaskStatusKindQueued,
		false,
		true,
		"failed user launch interruption",
	)
	if _, err := f.metadata.DB().ExecContext(context.Background(), `DROP TRIGGER fail_user_launch_interruption`); err != nil {
		t.Fatalf("remove launch interruption failure: %v", err)
	}
	if err := f.controller.Interrupt(context.Background(), workflowexecution.InterruptSelector{TaskID: task.ID}); err != nil {
		t.Fatalf("retry ready launch Interrupt: %v", err)
	}
}

func TestAdmittedLaunchInterruptPersistenceFailureLeavesTheSameRunInterruptible(t *testing.T) {
	runnerEntered := make(chan workflow.CurrentNodeReference, 1)
	releaseRunner := make(chan struct{})
	var releaseRunnerOnce sync.Once
	t.Cleanup(func() { releaseRunnerOnce.Do(func() { close(releaseRunner) }) })
	f := newCurrentNodeRunnerFixtureWithRunner(
		t,
		NewScriptedClient(llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}),
		false,
		func(delegate workflowexecution.CurrentNodeRunner) workflowexecution.CurrentNodeRunner {
			return barrierCurrentNodeRunner{
				delegate: delegate,
				entered:  runnerEntered,
				release:  releaseRunner,
			}
		},
	)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	reference := f.startTask(t, task)
	select {
	case entered := <-runnerEntered:
		if !entered.Equal(reference) {
			t.Fatalf("admitted launch reference = %v, want %v", entered, reference)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("launch did not enter admitted-scheduling runner barrier")
	}
	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`CREATE TRIGGER fail_user_admitted_launch_interruption
BEFORE UPDATE OF scheduling_state ON task_current_nodes
WHEN OLD.scheduling_state = 'admitted' AND NEW.scheduling_state = 'interrupted'
BEGIN
    SELECT RAISE(ABORT, 'injected admitted user launch interruption failure');
END`,
	); err != nil {
		t.Fatalf("install admitted launch interruption failure: %v", err)
	}
	if err := f.controller.Interrupt(context.Background(), workflowexecution.InterruptSelector{TaskID: task.ID}); err == nil {
		t.Fatal("Interrupt succeeded despite injected admitted persistence failure")
	} else if errors.Is(err, workflowexecution.ErrNoInterruptibleExecution) {
		t.Fatalf("admitted launch was not selected as interruptible: %v", err)
	}
	requireInterruptibleLaunchingRun(t, f.controller, reference)
	newCurrentNodeRunnerReadSurfaces(t, f).requireState(
		t,
		task,
		workflowID,
		reference.NodeID,
		serverapi.WorkflowTaskStatusKindQueued,
		false,
		true,
		"failed admitted user launch interruption",
	)
	if _, err := f.metadata.DB().ExecContext(context.Background(), `DROP TRIGGER fail_user_admitted_launch_interruption`); err != nil {
		t.Fatalf("remove admitted launch interruption failure: %v", err)
	}
	if err := f.controller.Interrupt(context.Background(), workflowexecution.InterruptSelector{TaskID: task.ID}); err != nil {
		t.Fatalf("retry admitted launch Interrupt: %v", err)
	}
}

func TestLaunchFailureInterruptionPersistenceFailureRejectsEveryTaskReadSurface(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	f.clientErr = errors.New("provider unavailable")
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`CREATE TRIGGER fail_launch_failure_interruption
BEFORE UPDATE OF scheduling_state ON task_current_nodes
WHEN OLD.scheduling_state IN ('ready', 'admitted') AND NEW.scheduling_state = 'interrupted'
BEGIN
    SELECT RAISE(ABORT, 'injected launch failure interruption failure');
END`,
	); err != nil {
		t.Fatalf("install launch failure interruption failure: %v", err)
	}
	reference := f.startTask(t, task)
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := f.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{task.ID})
		return err != nil
	}, "launch failure interruption persistence failure did not install fatal read unavailability")
	newCurrentNodeRunnerReadSurfaces(t, f).requireUnavailable(t, task, workflowID, reference.NodeID)
	_ = f.controller.Close()
	f.controller = nil
}

func requireInterruptibleLaunchingRun(
	t *testing.T,
	controller *workflowexecution.CurrentNodeController,
	reference workflow.CurrentNodeReference,
) {
	t.Helper()
	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	runs := observation.Runs[reference.TaskID]
	if len(runs.Queued) != 0 ||
		len(runs.InterruptibleLaunching) != 1 ||
		!runs.InterruptibleLaunching[0].Equal(reference) {
		t.Fatalf("Run observation = %+v, want exact interruptible launch target %v", runs, reference)
	}
}

func newCurrentNodeRunnerReadSurfaces(
	t *testing.T,
	f *currentNodeRunnerFixture,
) currentNodeRunnerReadSurfaces {
	t.Helper()
	projection, err := workflowview.NewTaskStatusProjection(
		f.metadata,
		f.store,
		workflowview.NewTaskProjector(),
		f.controller,
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	definitions, err := workflowview.NewDefinitionProjection(f.store)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	detail, err := workflowview.NewTaskDetail(f.metadata, projection, f.dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	tasks, err := workflowview.NewTaskList(f.metadata, definitions, projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	board, err := workflowview.NewBoard(
		f.metadata,
		definitions,
		testsetup.QuestionsEnabled("coder", "reviewer"),
		projection,
	)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	search, err := workflowview.NewTaskSearch(f.metadata, projection)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	return currentNodeRunnerReadSurfaces{
		projection: projection,
		detail:     detail,
		tasks:      tasks,
		board:      board,
		search:     search,
		projectID:  f.projectID,
	}
}

func (s currentNodeRunnerReadSurfaces) requireState(
	t *testing.T,
	task workflowstore.TaskRecord,
	workflowID runtimeids.WorkflowID,
	nodeID workflow.NodeID,
	wantStatus serverapi.WorkflowTaskStatusKind,
	wantResume bool,
	wantInterrupt bool,
	phase string,
) {
	t.Helper()
	var direct workflowview.TaskStatusProjectionResult
	if err := s.projection.WithSnapshot(
		context.Background(),
		[]workflow.TaskID{task.ID},
		func(
			observation workflowview.TaskStatusObservation,
			durable *workflowview.TaskStatusDurableSnapshot,
		) error {
			projected, err := s.projection.Project(
				context.Background(),
				observation,
				durable,
				[]workflow.TaskID{task.ID},
			)
			if err != nil {
				return err
			}
			direct = projected[task.ID]
			return nil
		},
	); err != nil {
		t.Fatalf("%s Task status projection: %v", phase, err)
	}
	if direct.Status.Kind != wantStatus ||
		direct.Actions.CanResume != wantResume ||
		direct.Actions.CanInterrupt != wantInterrupt {
		t.Fatalf("%s Task status = %+v actions=%+v", phase, direct.Status, direct.Actions)
	}
	detail, err := s.detail.GetTask(context.Background(), string(task.ID))
	if err != nil {
		t.Fatalf("%s Task detail: %v", phase, err)
	}
	if detail.Status.Kind != wantStatus ||
		detail.Actions.CanResume != wantResume ||
		detail.Actions.CanInterrupt != wantInterrupt {
		t.Fatalf("%s Task detail status/actions = %+v/%+v", phase, detail.Status, detail.Actions)
	}
	projectID := s.projectID
	limit := 20
	listed, err := s.tasks.List(context.Background(), serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{wantStatus},
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	})
	if err != nil {
		t.Fatalf("%s Task List: %v", phase, err)
	}
	listedTask, listedFound := findWorkflowTaskListItem(listed.Tasks, task.ID)
	if !listedFound || listedTask.Status.Kind != wantStatus {
		t.Fatalf("%s Task List = %+v", phase, listed.Tasks)
	}
	cards, err := s.board.ListNodeCards(context.Background(), serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   s.projectID,
		WorkflowID:  workflowID,
		NodeID:      string(nodeID),
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		PageSize:    20,
	})
	if err != nil {
		t.Fatalf("%s Board cards: %v", phase, err)
	}
	card, cardFound := findWorkflowBoardCard(cards.Cards, task.ID)
	if !cardFound ||
		card.Status.Kind != wantStatus ||
		card.Actions.CanResume != wantResume ||
		card.Actions.CanInterrupt != wantInterrupt {
		t.Fatalf("%s Board cards = %+v", phase, cards.Cards)
	}
	searched, err := s.search.Search(context.Background(), serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       task.Title,
		Context:     serverapi.TaskSearchDefaultContext,
		ProjectIDs:  []string{s.projectID},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{wantStatus},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("%s Task Search: %v", phase, err)
	}
	searchedGroup, searchedFound := findTaskSearchGroup(searched.Groups, task.ID)
	if !searchedFound || searchedGroup.Status.Kind != wantStatus {
		t.Fatalf("%s Task Search = %+v", phase, searched.Groups)
	}
}

func (s currentNodeRunnerReadSurfaces) requireUnavailable(
	t *testing.T,
	task workflowstore.TaskRecord,
	workflowID runtimeids.WorkflowID,
	nodeID workflow.NodeID,
) {
	t.Helper()
	if _, err := s.detail.GetTask(context.Background(), string(task.ID)); err == nil {
		t.Fatal("Task detail returned after fatal lifecycle unavailability")
	}
	projectID := s.projectID
	limit := 1
	if _, err := s.tasks.List(context.Background(), serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	}); err == nil {
		t.Fatal("Task List returned after fatal lifecycle unavailability")
	}
	if _, err := s.board.ListNodeCards(context.Background(), serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   s.projectID,
		WorkflowID:  workflowID,
		NodeID:      string(nodeID),
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		PageSize:    1,
	}); err == nil {
		t.Fatal("Board returned after fatal lifecycle unavailability")
	}
	if _, err := s.search.Search(context.Background(), serverapi.TaskSearchRequest{
		Mode:       serverapi.TaskSearchModeLiteral,
		Query:      task.Title,
		Context:    serverapi.TaskSearchDefaultContext,
		ProjectIDs: []string{s.projectID},
		PageSize:   1,
	}); err == nil {
		t.Fatal("Task Search returned after fatal lifecycle unavailability")
	}
}

func findWorkflowTaskListItem(items []serverapi.WorkflowTaskListItem, taskID workflow.TaskID) (serverapi.WorkflowTaskListItem, bool) {
	for _, item := range items {
		if item.TaskID == string(taskID) {
			return item, true
		}
	}
	return serverapi.WorkflowTaskListItem{}, false
}

func findWorkflowBoardCard(cards []serverapi.WorkflowBoardTaskCard, taskID workflow.TaskID) (serverapi.WorkflowBoardTaskCard, bool) {
	for _, card := range cards {
		if card.TaskID == string(taskID) {
			return card, true
		}
	}
	return serverapi.WorkflowBoardTaskCard{}, false
}

func findTaskSearchGroup(groups []serverapi.TaskSearchGroup, taskID workflow.TaskID) (serverapi.TaskSearchGroup, bool) {
	for _, group := range groups {
		if group.TaskID == string(taskID) {
			return group, true
		}
	}
	return serverapi.TaskSearchGroup{}, false
}

func TestOrdinaryOutcomeLessFinalizationKeepsAuthorityUntilFailedDurableDispositionAndStartupRecovery(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(started)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"commentary":"returned without completing the Current Node"}`).Response,
		},
	)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	currentNode := f.startTask(t, task)
	select {
	case <-started:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("ordinary Workflow Agent did not reach its Exact execution")
	}
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].SessionID != nil &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingAdmitted
	})
	sessionID := *nodes[0].SessionID
	expectedExecution, live := f.authority.SessionExecution(sessionID)
	if !live {
		t.Fatal("ordinary Workflow Agent lost Exact authority before finalization")
	}
	expectedScopeID := expectedExecution.Scope().ID()
	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`CREATE TRIGGER fail_ordinary_outcome_less_interruption
BEFORE UPDATE OF scheduling_state ON task_current_nodes
WHEN OLD.scheduling_state = 'admitted' AND NEW.scheduling_state = 'interrupted'
BEGIN
    SELECT RAISE(ABORT, 'injected ordinary outcome-less interruption failure');
END`,
	); err != nil {
		t.Fatalf("install ordinary outcome-less persistence failure: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
		return err == nil &&
			len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingAdmitted
	}, "ordinary outcome-less Current Node did not remain admitted after interruption persistence failed")
	if _, err := f.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{task.ID}); err == nil {
		t.Fatal("ordinary outcome-less persistence failure allowed lifecycle reads before fatal unavailability")
	}
	if execution, live := f.authority.SessionExecution(sessionID); !live ||
		execution.Scope().ID() != expectedScopeID {
		t.Fatal("ordinary outcome-less persistence failure released Exact authority before durable disposition")
	}

	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`DROP TRIGGER fail_ordinary_outcome_less_interruption`,
	); err != nil {
		t.Fatalf("remove ordinary outcome-less persistence failure: %v", err)
	}
	if err := f.controller.Close(); err != nil {
		t.Fatalf("close failed controller before startup recovery: %v", err)
	}
	restarted, err := workflowexecution.NewCurrentNodeController(
		f.store,
		f.starter,
		f.authority,
		workflowexecution.NewTaskMutationCoordinator(),
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:      1,
			AssignmentSteerer:     f.starter,
			LifecycleAvailability: workflowexecution.NewLifecycleFatalAvailability(),
		},
	)
	if err != nil {
		t.Fatalf("construct startup recovery controller: %v", err)
	}
	f.controller = restarted
	if _, err := restarted.Recover(context.Background()); err != nil {
		t.Fatalf("startup recovery after fatal persistence failure: %v", err)
	}
	recovered := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted
	})
	if recovered[0].Scheduling.Interruption == nil ||
		recovered[0].Scheduling.Interruption.Reason != workflowexecution.ReasonCurrentNodeStartupRecovery {
		t.Fatalf("startup recovery outcome = %+v, want resumable interruption", recovered)
	}
}

func TestOutcomeLessAdmittedSchedulingMismatchMakesLifecycleUnavailable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(started)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"commentary":"returned without completing the Current Node"}`).Response,
		},
	)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	currentNode := f.startTask(t, task)
	select {
	case <-started:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("ordinary Workflow Agent did not reach its Exact execution")
	}
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].SessionID != nil &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingAdmitted
	})
	sessionID := *nodes[0].SessionID
	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`UPDATE task_current_nodes
SET scheduling_state = 'ready'
WHERE task_id = ? AND node_id = ?`,
		string(task.ID),
		string(currentNode.NodeID),
	); err != nil {
		t.Fatalf("create real SQLite admitted scheduling mismatch: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, live := f.authority.SessionExecution(sessionID)
		return !live
	}, "outcome-less Exact did not finalize after scheduling mismatch")
	if _, err := f.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{task.ID}); err == nil {
		t.Fatal("scheduling-specific admitted mismatch was treated as successful cleanup instead of fatal unavailability")
	}
}

func TestCurrentNodeContinuationWithActiveTranscriptSubscriberDoesNotBlockLaterAutomaticScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX shell scripts")
	}
	sourceResponseStarted := make(chan struct{})
	sourceResponseRelease := make(chan struct{})
	successorResponseStarted := make(chan struct{})
	successorResponseRelease := make(chan struct{})
	var releaseSource sync.Once
	var releaseSuccessor sync.Once
	t.Cleanup(func() {
		releaseSource.Do(func() { close(sourceResponseRelease) })
		releaseSuccessor.Do(func() { close(successorResponseRelease) })
	})
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(sourceResponseStarted)
				select {
				case <-sourceResponseRelease:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"transition":"next","commentary":"source done"}`).Response,
		},
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(successorResponseStarted)
				select {
				case <-successorResponseRelease:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"commentary":"successor done"}`).Response,
		},
	)
	continuedWorkflowID := createCurrentNodeChainedWorkflow(t, f.store, workflow.ContextModeContinueSession)
	continuedTask := f.createTask(t, continuedWorkflowID)
	source := f.startTask(t, continuedTask)
	select {
	case <-sourceResponseStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("source Current Node did not start")
	}
	sourceNodes := f.waitForCurrentNode(t, continuedTask.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Reference.Equal(source) && nodes[0].SessionID != nil
	})
	sessionID := *sourceNodes[0].SessionID
	sourceExecution, live := f.authority.SessionExecution(sessionID)
	if !live {
		t.Fatal("source Current Node reached its provider without an Exact Execution Scope")
	}
	sourceResource, hasResource := sourceExecution.Scope().Resource()
	if !hasResource {
		t.Fatal("source Exact Execution Scope has no Active Session Runtime")
	}
	transcript, err := f.runtimes.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{
		SessionID: sessionID.String(),
	})
	if err != nil {
		t.Fatalf("subscribe source transcript: %v", err)
	}
	t.Cleanup(func() { _ = transcript.Close() })

	releaseSource.Do(func() { close(sourceResponseRelease) })
	successorNodes := f.waitForCurrentNode(t, continuedTask.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && !nodes[0].Reference.Equal(source)
	})
	successor := successorNodes[0].Reference
	f.waitForControllerCurrentNode(t, successor)
	select {
	case <-successorResponseStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("continued Session successor did not reach its model turn")
	}
	successorExecution, live := f.authority.SessionExecution(sessionID)
	if !live {
		t.Fatal("successor Current Node reached its provider without an Exact Execution Scope")
	}
	successorResource, hasResource := successorExecution.Scope().Resource()
	if !hasResource || successorResource != sourceResource {
		t.Fatalf(
			"successor Active Session Runtime = %+v, want retained source generation %+v",
			successorResource,
			sourceResource,
		)
	}

	sourceScriptPath := filepath.Join(f.workspace, "source-script.sh")
	laterScriptPath := filepath.Join(f.workspace, "later-automatic-script.sh")
	laterScriptMarker := filepath.Join(f.workspace, "later-automatic-script.started")
	if err := os.WriteFile(
		sourceScriptPath,
		[]byte("#!/bin/sh\nprintf '%s' '{\"transition\":\"next\",\"commentary\":\"source script done\"}'\n"),
		0o755,
	); err != nil {
		t.Fatalf("write source script: %v", err)
	}
	if err := os.WriteFile(
		laterScriptPath,
		[]byte("#!/bin/sh\n: > "+workflowRunnerShellQuote(laterScriptMarker)+"\nprintf '%s' '{\"commentary\":\"later script done\"}'\n"),
		0o755,
	); err != nil {
		t.Fatalf("write later automatic script: %v", err)
	}
	scriptWorkflowID := createCurrentNodeScriptChainWorkflow(t, f.store, sourceScriptPath, laterScriptPath)
	scriptTask := f.createTask(t, scriptWorkflowID)
	scriptSource := f.startTask(t, scriptTask)
	f.waitForCurrentNode(t, scriptTask.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && !nodes[0].Reference.Equal(scriptSource)
	})

	releaseSuccessor.Do(func() { close(successorResponseRelease) })
	f.waitForPath(t, laterScriptMarker)
	f.waitForCurrentNode(t, continuedTask.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	f.waitForCurrentNode(t, scriptTask.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
}

func TestCurrentNodeScriptReceivesStructuredInputAndCompletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell script")
	}
	f := newCurrentNodeRunnerFixture(t)
	stdinPath := filepath.Join(f.workspace, "script-input.json")
	scriptPath := filepath.Join(f.workspace, "complete.sh")
	script := "#!/bin/sh\ncat > " + workflowRunnerShellQuote(stdinPath) + "\nprintf '%s' '{\"commentary\":\"done\"}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	workflowID := createCurrentNodeScriptWorkflow(t, f.store, scriptPath)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read script stdin: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdin, &decoded); err != nil {
		t.Fatalf("decode script stdin: %v", err)
	}
	kent, ok := decoded["_kent"].(map[string]any)
	if !ok || kent["task_id"] != string(task.ID) {
		t.Fatalf("script _kent identity = %#v", decoded["_kent"])
	}
}

func TestCurrentNodeScriptFailureSurfacesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell script")
	}
	f := newCurrentNodeRunnerFixture(t)
	scriptPath := filepath.Join(f.workspace, "fail.sh")
	script := "#!/bin/sh\nprintf '%s\\n' 'merge command rejected' >&2\nexit 23\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	workflowID := createCurrentNodeScriptWorkflow(t, f.store, scriptPath)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	got := nodes[0].Scheduling.Interruption.Detail.Fields["error"]
	want := "exit status 23\nscript stderr: merge command rejected"
	if got != want {
		t.Fatalf("script failure detail = %q, want %q", got, want)
	}
}

func TestCurrentNodeScriptInvalidCompletionInterruptsSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell script")
	}
	f := newCurrentNodeRunnerFixture(t)
	scriptPath := filepath.Join(f.workspace, "invalid-completion.sh")
	script := "#!/bin/sh\nprintf '%s' 'not-json'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	workflowID := createCurrentNodeScriptWorkflow(t, f.store, scriptPath)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(source) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	if reason := nodes[0].Scheduling.Interruption.Reason; reason != ReasonScriptCompletionFailed {
		t.Fatalf("invalid Script completion interruption reason = %q, want %q", reason, ReasonScriptCompletionFailed)
	}
}

func createCurrentNodeAgentWorkflow(t *testing.T, store *workflowstore.Store) runtimeids.WorkflowID {
	t.Helper()
	return createCurrentNodeAgentWorkflowWithCompletionMode(t, store, "")
}

func createCurrentNodeAgentWorkflowWithCompletionMode(t *testing.T, store *workflowstore.Store, completionMode string) runtimeids.WorkflowID {
	t.Helper()
	return createCurrentNodeWorkflow(t, store, workflow.NodeKindAgent, "coder", "", completionMode)
}

func createCurrentNodeScriptWorkflow(t *testing.T, store *workflowstore.Store, scriptPath string) runtimeids.WorkflowID {
	t.Helper()
	return createCurrentNodeWorkflow(t, store, workflow.NodeKindScript, "", scriptPath, "")
}

func createCurrentNodeChainedWorkflow(t *testing.T, store *workflowstore.Store, mode workflow.ContextMode) runtimeids.WorkflowID {
	t.Helper()
	return createCurrentNodeTwoStepWorkflow(
		t,
		store,
		"Current Node continuation",
		mode,
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "First."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Second."},
	)
}

func createCurrentNodeApprovalLoopWorkflow(t *testing.T, store *workflowstore.Store) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Approval previous-target loop"})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	var startID, doneID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			doneID = workflow.NodeIDOf(node)
		}
	}
	implementationID := workflow.NodeID("node-implementation-" + created.ID.String())
	reviewID := workflow.NodeID("node-review-" + created.ID.String())
	for _, node := range []workflowstore.NodeRecord{
		{
			ID: implementationID, WorkflowID: created.ID, Key: "implementation",
			Kind: workflow.NodeKindAgent, DisplayName: "Implementation",
			SubagentRole: "coder",
		},
		{
			ID: reviewID, WorkflowID: created.ID, Key: "review",
			Kind: workflow.NodeKindAgent, DisplayName: "Review",
			SubagentRole: "reviewer",
		},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("add node: %v", err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + created.ID.String())
	reviewGroup := workflow.TransitionGroupID("group-review-" + created.ID.String())
	doneGroup := workflow.TransitionGroupID("group-done-" + created.ID.String())
	reworkGroup := workflow.TransitionGroupID("group-rework-" + created.ID.String())
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{ID: reviewGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "review", DisplayName: "Review"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "done", DisplayName: "Done"},
		{ID: reworkGroup, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "rework", DisplayName: "Rework"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add transition group: %v", err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{
			ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID,
			TransitionGroupID: startGroup, Key: "start", TargetNodeID: implementationID,
			ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement the task.",
		},
		{
			ID: workflow.EdgeID("edge-review-" + created.ID.String()), WorkflowID: created.ID,
			TransitionGroupID: reviewGroup, Key: "review", TargetNodeID: reviewID,
			ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review the implementation.",
		},
		{
			ID: workflow.EdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID,
			TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID,
			ContextMode: workflow.ContextModeNewSession,
		},
		{
			ID: workflow.EdgeID("edge-rework-" + created.ID.String()), WorkflowID: created.ID,
			TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: implementationID,
			RequiresApproval: true,
			ContextMode:      workflow.ContextModeContinueSession,
			ContextSource:    workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
			PromptTemplate:   "Address the review findings.",
		},
	} {
		edge = normalizeWorkflowEdgeRecordForTest(edge)
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	return created.ID
}

func createCurrentNodeFanoutContinuationWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	requiresApproval bool,
) (runtimeids.WorkflowID, map[workflow.TransitionBranchKey]workflow.NodeID) {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Current Node fan-out continuation"})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	var startID, doneID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			doneID = workflow.NodeIDOf(node)
		}
	}
	workflowSuffix := created.ID.String()
	sourceID := workflow.NodeID("node-source-" + workflowSuffix)
	branchNodeIDs := map[workflow.TransitionBranchKey]workflow.NodeID{
		"branch_a": workflow.NodeID("node-branch-a-" + workflowSuffix),
		"branch_b": workflow.NodeID("node-branch-b-" + workflowSuffix),
	}
	joinID := workflow.NodeID("node-join-" + workflowSuffix)
	for _, node := range []workflowstore.NodeRecord{
		{ID: sourceID, WorkflowID: created.ID, Key: "source", Kind: workflow.NodeKindAgent, DisplayName: "Source", SubagentRole: "coder"},
		{ID: branchNodeIDs["branch_a"], WorkflowID: created.ID, Key: "branch_a", Kind: workflow.NodeKindAgent, DisplayName: "Branch A", SubagentRole: "coder"},
		{ID: branchNodeIDs["branch_b"], WorkflowID: created.ID, Key: "branch_b", Kind: workflow.NodeKindAgent, DisplayName: "Branch B", SubagentRole: "coder"},
		{ID: joinID, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join"},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("add node: %v", err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + workflowSuffix)
	splitGroup := workflow.TransitionGroupID("group-split-" + workflowSuffix)
	branchAGroup := workflow.TransitionGroupID("group-branch-a-" + workflowSuffix)
	branchBGroup := workflow.TransitionGroupID("group-branch-b-" + workflowSuffix)
	doneGroup := workflow.TransitionGroupID("group-done-" + workflowSuffix)
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: sourceID, TransitionID: "split", DisplayName: "Split"},
		{ID: branchAGroup, WorkflowID: created.ID, SourceNodeID: branchNodeIDs["branch_a"], TransitionID: "join_a", DisplayName: "Join"},
		{ID: branchBGroup, WorkflowID: created.ID, SourceNodeID: branchNodeIDs["branch_b"], TransitionID: "join_b", DisplayName: "Join"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add transition group: %v", err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: sourceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Source."},
		{ID: workflow.EdgeID("edge-branch-a-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "branch_a", TargetNodeID: branchNodeIDs["branch_a"], ContextMode: workflow.ContextModeContinueSession, RequiresApproval: requiresApproval, PromptTemplate: "Branch A."},
		{ID: workflow.EdgeID("edge-branch-b-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "branch_b", TargetNodeID: branchNodeIDs["branch_b"], ContextMode: workflow.ContextModeContinueSession, RequiresApproval: requiresApproval, PromptTemplate: "Branch B."},
		{ID: workflow.EdgeID("edge-join-a-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: branchAGroup, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-b-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: branchBGroup, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-done-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession},
	} {
		edge = normalizeWorkflowEdgeRecordForTest(edge)
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	return created.ID, branchNodeIDs
}

func createCurrentNodeMixedFanoutWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	scriptPath string,
) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Mixed Agent Script successor"})
	if err != nil {
		t.Fatalf("create mixed workflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get mixed workflow: %v", err)
	}
	var startID, doneID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			doneID = workflow.NodeIDOf(node)
		}
	}
	suffix := created.ID.String()
	sourceID := workflow.NodeID("node-source-" + suffix)
	agentID := workflow.NodeID("node-agent-" + suffix)
	scriptID := workflow.NodeID("node-script-" + suffix)
	joinID := workflow.NodeID("node-join-" + suffix)
	for _, node := range []workflowstore.NodeRecord{
		{ID: sourceID, WorkflowID: created.ID, Key: "source", Kind: workflow.NodeKindAgent, DisplayName: "Source", SubagentRole: "coder"},
		{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder"},
		{ID: scriptID, WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: scriptPath},
		{ID: joinID, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join"},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("add mixed node: %v", err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + suffix)
	splitGroup := workflow.TransitionGroupID("group-split-" + suffix)
	agentDoneGroup := workflow.TransitionGroupID("group-agent-done-" + suffix)
	scriptDoneGroup := workflow.TransitionGroupID("group-script-done-" + suffix)
	doneGroup := workflow.TransitionGroupID("group-done-" + suffix)
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: sourceID, TransitionID: "split", DisplayName: "Split"},
		{ID: agentDoneGroup, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "agent_done", DisplayName: "Done"},
		{ID: scriptDoneGroup, WorkflowID: created.ID, SourceNodeID: scriptID, TransitionID: "script_done", DisplayName: "Done"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add mixed transition group: %v", err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + suffix), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: sourceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Source."},
		{ID: workflow.EdgeID("edge-agent-" + suffix), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "agent", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Agent branch."},
		{ID: workflow.EdgeID("edge-script-" + suffix), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "script", TargetNodeID: scriptID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-agent-done-" + suffix), WorkflowID: created.ID, TransitionGroupID: agentDoneGroup, Key: "agent_done", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-script-done-" + suffix), WorkflowID: created.ID, TransitionGroupID: scriptDoneGroup, Key: "script_done", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-done-" + suffix), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession},
	} {
		edge = normalizeWorkflowEdgeRecordForTest(edge)
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add mixed edge: %v", err)
		}
	}
	return created.ID
}

func createCurrentNodeScriptChainWorkflow(t *testing.T, store *workflowstore.Store, sourcePath, successorPath string) runtimeids.WorkflowID {
	t.Helper()
	return createCurrentNodeTwoStepWorkflow(
		t,
		store,
		"Current Node automatic script chain",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: sourcePath},
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: successorPath},
	)
}

type currentNodeWorkflowStep struct {
	kind           workflow.NodeKind
	role           string
	scriptPath     string
	prompt         string
	completionMode string
}

type currentNodeLinearTransition struct {
	id               string
	mode             workflow.ContextMode
	requiresApproval bool
	contextSource    workflow.ContextSource
}

func createCurrentNodeTwoStepWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	name string,
	mode workflow.ContextMode,
	first currentNodeWorkflowStep,
	second currentNodeWorkflowStep,
) runtimeids.WorkflowID {
	return createCurrentNodeTwoStepWorkflowWithTransition(
		t,
		store,
		name,
		first,
		second,
		currentNodeLinearTransition{id: "next", mode: mode},
	)
}

func createCurrentNodeTwoStepWorkflowWithTransition(
	t *testing.T,
	store *workflowstore.Store,
	name string,
	first, second currentNodeWorkflowStep,
	transition currentNodeLinearTransition,
) runtimeids.WorkflowID {
	return createCurrentNodeLinearWorkflow(
		t,
		store,
		name,
		[]currentNodeWorkflowStep{first, second},
		[]currentNodeLinearTransition{transition},
	)
}

func createCurrentNodeThreeStepWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	name string,
	first, second, third currentNodeWorkflowStep,
) runtimeids.WorkflowID {
	return createCurrentNodeThreeStepWorkflowWithTransition(
		t,
		store,
		name,
		first,
		second,
		third,
		currentNodeLinearTransition{
			id:               "next_2",
			mode:             workflow.ContextModeCompactAndContinueSession,
			requiresApproval: true,
			contextSource:    workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		},
	)
}

func createCurrentNodeThreeStepWorkflowWithTransition(
	t *testing.T,
	store *workflowstore.Store,
	name string,
	first, second, third currentNodeWorkflowStep,
	finalTransition currentNodeLinearTransition,
) runtimeids.WorkflowID {
	return createCurrentNodeLinearWorkflow(
		t,
		store,
		name,
		[]currentNodeWorkflowStep{first, second, third},
		[]currentNodeLinearTransition{
			{id: "next_1", mode: workflow.ContextModeNewSession},
			finalTransition,
		},
	)
}

func createCurrentNodeLinearWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	name string,
	steps []currentNodeWorkflowStep,
	transitions []currentNodeLinearTransition,
) runtimeids.WorkflowID {
	t.Helper()
	if len(steps) < 2 || len(transitions) != len(steps)-1 {
		t.Fatalf("linear workflow needs at least two steps and one transition per edge: steps=%d transitions=%d", len(steps), len(transitions))
	}
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: name})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	var startID, doneID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			doneID = workflow.NodeIDOf(node)
		}
	}
	suffix := created.ID.String()
	nodeIDs := make([]workflow.NodeID, len(steps))
	for index, step := range steps {
		nodeIDs[index] = workflow.NodeID(fmt.Sprintf("node-step-%d-%s", index+1, suffix))
		if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
			ID: nodeIDs[index], WorkflowID: created.ID,
			Key:  workflow.ModelKey(fmt.Sprintf("step_%d", index+1)),
			Kind: step.kind, DisplayName: fmt.Sprintf("Step %d", index+1),
			SubagentRole: step.role, ScriptPath: step.scriptPath,
			CompletionMode: step.completionMode,
		}); err != nil {
			t.Fatalf("add workflow step %d: %v", index+1, err)
		}
	}
	groupIDs := make([]workflow.TransitionGroupID, len(steps)+1)
	groupIDs[0] = workflow.TransitionGroupID("group-start-" + suffix)
	for index, transition := range transitions {
		groupIDs[index+1] = workflow.TransitionGroupID(fmt.Sprintf("group-%s-%s", transition.id, suffix))
	}
	groupIDs[len(steps)] = workflow.TransitionGroupID("group-done-" + suffix)
	for index, groupID := range groupIDs {
		sourceID := startID
		transitionID := "start"
		displayName := "Start"
		if index > 0 && index <= len(transitions) {
			sourceID = nodeIDs[index-1]
			transitionID = transitions[index-1].id
			displayName = transitionID
		} else if index == len(groupIDs)-1 {
			sourceID = nodeIDs[len(nodeIDs)-1]
			transitionID = "done"
			displayName = "Done"
		}
		if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
			ID: groupID, WorkflowID: created.ID, SourceNodeID: sourceID,
			TransitionID: workflow.TransitionID(transitionID), DisplayName: displayName,
		}); err != nil {
			t.Fatalf("add workflow transition group %d: %v", index+1, err)
		}
	}
	edges := make([]workflowstore.EdgeRecord, 0, len(steps)+1)
	edges = append(edges, workflowstore.EdgeRecord{
		ID: workflow.EdgeID("edge-start-" + suffix), WorkflowID: created.ID,
		TransitionGroupID: groupIDs[0], Key: "start", TargetNodeID: nodeIDs[0],
		ContextMode: workflow.ContextModeNewSession, PromptTemplate: steps[0].prompt,
	})
	for index, transition := range transitions {
		edges = append(edges, workflowstore.EdgeRecord{
			ID:         workflow.EdgeID(fmt.Sprintf("edge-%s-%s", transition.id, suffix)),
			WorkflowID: created.ID, TransitionGroupID: groupIDs[index+1],
			Key: workflow.ModelKey(transition.id), TargetNodeID: nodeIDs[index+1],
			ContextMode: transition.mode, RequiresApproval: transition.requiresApproval,
			ContextSource: transition.contextSource, PromptTemplate: steps[index+1].prompt,
		})
	}
	edges = append(edges, workflowstore.EdgeRecord{
		ID: workflow.EdgeID("edge-done-" + suffix), WorkflowID: created.ID,
		TransitionGroupID: groupIDs[len(groupIDs)-1], Key: "done",
		TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession,
	})
	for _, edge := range edges {
		edge = normalizeWorkflowEdgeRecordForTest(edge)
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add workflow edge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createCurrentNodeWorkflow(t *testing.T, store *workflowstore.Store, kind workflow.NodeKind, role, scriptPath, completionMode string) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Current Node runner"})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	var startID, doneID workflow.NodeID
	for _, node := range definition.Nodes {
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = workflow.NodeIDOf(node)
		case workflow.NodeKindTerminal:
			doneID = workflow.NodeIDOf(node)
		}
	}
	if startID == "" || doneID == "" {
		t.Fatalf("default workflow nodes = %+v", definition.Nodes)
	}
	nodeID := workflow.NodeID("node-execute-" + created.ID.String())
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID: nodeID, WorkflowID: created.ID, Key: "execute", Kind: kind, DisplayName: "Execute",
		SubagentRole: role, ScriptPath: scriptPath,
		CompletionMode: completionMode,
	}); err != nil {
		t.Fatalf("add executable node: %v", err)
	}
	startGroup := workflow.TransitionGroupID("group-start-" + created.ID.String())
	doneGroup := workflow.TransitionGroupID("group-done-" + created.ID.String())
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: nodeID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add transition group: %v", err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: nodeID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: func() string {
			if kind == workflow.NodeKindAgent {
				return "Do the work."
			}
			return ""
		}()},
		{ID: workflow.EdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession},
	} {
		edge = normalizeWorkflowEdgeRecordForTest(edge)
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	return created.ID
}

func normalizeWorkflowEdgeRecordForTest(edge workflowstore.EdgeRecord) workflowstore.EdgeRecord {
	if edge.AssigneeSelection == "" {
		edge.AssigneeSelection = workflow.AssigneeSelectionConfigured
	}
	if edge.ThinkingSelection == "" {
		edge.ThinkingSelection = workflow.ThinkingSelectionConfigured
	}
	for index := range edge.Parameters {
		if edge.Parameters[index].Purpose == "" {
			edge.Parameters[index].Purpose = workflow.ParameterPurposeOrdinary
		}
	}
	return edge
}

func workflowRunnerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
