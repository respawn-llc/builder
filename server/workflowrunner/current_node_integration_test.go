package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	agentruntime "core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

const currentNodeRunnerWait = 30 * time.Second

type currentNodeRunnerFixture struct {
	cfg          config.App
	metadata     *metadata.Store
	store        *workflowstore.Store
	authority    *sessionruntime.Authority
	runtimes     *registry.RuntimeRegistry
	controller   *workflowexecution.CurrentNodeController
	starter      *Starter
	dependencies *workflowview.TaskDependencies
	projectID    string
	workspaceID  string
	workspace    string
	client       *ScriptedClient

	mu             sync.Mutex
	clientRequests []runtimewire.RuntimeClientRequest
	clientErr      error
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
		client: NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			steps...,
		),
	}
	fixture.runtimes = registry.NewRuntimeRegistry()
	var controller *workflowexecution.CurrentNodeController
	fixture.authority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
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
	permit := workflowexecution.NewMutationPermit()
	dependencyCounter, err := workflowview.NewTaskDependencyCounter(metadataStore)
	if err != nil {
		t.Fatalf("new Task dependency counter: %v", err)
	}
	starter, err := NewStarter(cfg, metadataStore, store, nil, nil, StarterOptions{
		RuntimeAuthority: fixture.authority,
		MutationPermit:   permit,
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
	controller, err = workflowexecution.NewCurrentNodeController(store, starter, fixture.authority, permit, workflowexecution.CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: starter,
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
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
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
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedFinalAnswer(`{"transition":"review","commentary":"implementation complete"}`),
		ScriptedFinalAnswer(`{"transition":"rework","commentary":"changes requested"}`),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
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
	if len(reassignedImplementation) != 2 {
		t.Fatalf(
			"approved implementation assignments = %+v, want initial plus exactly one reassignment",
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
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store)
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
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store)
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
		{ID: workflow.EdgeID("edge-branch-a-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "branch_a", TargetNodeID: branchNodeIDs["branch_a"], ContextMode: workflow.ContextModeContinueSession, PromptTemplate: "Branch A."},
		{ID: workflow.EdgeID("edge-branch-b-" + workflowSuffix), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "branch_b", TargetNodeID: branchNodeIDs["branch_b"], ContextMode: workflow.ContextModeContinueSession, PromptTemplate: "Branch B."},
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

func createCurrentNodeTwoStepWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	name string,
	mode workflow.ContextMode,
	first currentNodeWorkflowStep,
	second currentNodeWorkflowStep,
) runtimeids.WorkflowID {
	t.Helper()
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
	firstID := workflow.NodeID("node-first-" + created.ID.String())
	secondID := workflow.NodeID("node-second-" + created.ID.String())
	for _, node := range []workflowstore.NodeRecord{
		{
			ID: firstID, WorkflowID: created.ID, Key: "first", Kind: first.kind, DisplayName: "First",
			SubagentRole: first.role, ScriptPath: first.scriptPath,
			CompletionMode: first.completionMode,
		},
		{
			ID: secondID, WorkflowID: created.ID, Key: "second", Kind: second.kind, DisplayName: "Second",
			SubagentRole: second.role, ScriptPath: second.scriptPath,
			CompletionMode: second.completionMode,
		},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("add node: %v", err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + created.ID.String())
	nextGroup := workflow.TransitionGroupID("group-next-" + created.ID.String())
	doneGroup := workflow.TransitionGroupID("group-done-" + created.ID.String())
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: firstID, TransitionID: "next", DisplayName: "Next"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: secondID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add transition group: %v", err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{
			ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID,
			TransitionGroupID: startGroup, Key: "start", TargetNodeID: firstID,
			ContextMode: workflow.ContextModeNewSession, PromptTemplate: first.prompt,
		},
		{
			ID: workflow.EdgeID("edge-next-" + created.ID.String()), WorkflowID: created.ID,
			TransitionGroupID: nextGroup, Key: "next", TargetNodeID: secondID,
			ContextMode: mode, PromptTemplate: second.prompt,
		},
		{ID: workflow.EdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession},
	} {
		edge = normalizeWorkflowEdgeRecordForTest(edge)
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add edge: %v", err)
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
