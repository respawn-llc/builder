package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/internal/testharness/workflowfixture"
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
	controllerClose error

	mu             sync.Mutex
	clientRequests []runtimewire.RuntimeClientRequest
	clientErr      error
}

type currentNodeRunnerClient interface {
	llm.Client
	Requests() []llm.Request
}

type currentNodeAssignmentSteererFactory func(*Starter) workflowexecution.CurrentNodeAssignmentSteerer

type committedDiagnosticCurrentNodeAssignmentSteerer struct {
	delegate   *Starter
	call       atomic.Int64
	targetCall int64
	diagnostic error
	matched    atomic.Bool
}

type failingManualMoveAssignmentSteerer struct {
	delegate *Starter
	cause    error
}

type failAfterManualMoveAssignmentPreparationSteerer struct {
	delegate *Starter
	cause    error
}

type diagnosticManualMoveAssignmentSteerer struct {
	delegate   *Starter
	diagnostic error
}

func (s failingManualMoveAssignmentSteerer) SteerCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return s.delegate.SteerCurrentNodeAssignment(ctx, reference)
}

func (s failingManualMoveAssignmentSteerer) PrepareManualMoveAssignments(
	context.Context,
	[]workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, s.cause
}

func (s failAfterManualMoveAssignmentPreparationSteerer) SteerCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return s.delegate.SteerCurrentNodeAssignment(ctx, reference)
}

func (s failAfterManualMoveAssignmentPreparationSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	preparation, _, err := s.delegate.PrepareManualMoveAssignments(ctx, inputs)
	if err != nil {
		return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, err
	}
	return preparation, nil, s.cause
}

func (s diagnosticManualMoveAssignmentSteerer) SteerCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return s.delegate.SteerCurrentNodeAssignment(ctx, reference)
}

func (s diagnosticManualMoveAssignmentSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	preparation, steers, err := s.delegate.PrepareManualMoveAssignments(ctx, inputs)
	if err != nil {
		return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, err
	}
	preparation.Diagnostic = s.diagnostic
	return preparation, steers, nil
}

func (s *committedDiagnosticCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	prepared, err := s.delegate.SteerCurrentNodeAssignment(ctx, reference)
	if err != nil {
		return nil, err
	}
	if s.call.Add(1) != s.targetCall {
		return prepared, nil
	}
	agent, ok := prepared.(*currentNodeAgentAssignmentSteer)
	if !ok {
		return nil, fmt.Errorf("prepared target assignment has type %T, want Agent assignment", prepared)
	}
	agent.completion = agentruntime.CompletedWorkflowAssignmentSteer(
		session.CommitReceipt{Committed: true},
		s.diagnostic,
	)
	s.matched.Store(true)
	return agent, nil
}

func (s *committedDiagnosticCurrentNodeAssignmentSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	return s.delegate.PrepareManualMoveAssignments(ctx, inputs)
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
	return newCurrentNodeRunnerFixtureWithClientAndPersistence(t, client, false, nil)
}

func newCurrentNodeRunnerFixtureWithPersistenceGate(
	t *testing.T,
	client currentNodeRunnerClient,
) *currentNodeRunnerFixture {
	return newCurrentNodeRunnerFixtureWithClientAndPersistence(t, client, true, nil)
}

func newCurrentNodeRunnerFixtureWithAssignmentSteerer(
	t *testing.T,
	client currentNodeRunnerClient,
	factory currentNodeAssignmentSteererFactory,
) *currentNodeRunnerFixture {
	return newCurrentNodeRunnerFixtureWithClientAndPersistence(t, client, false, factory)
}

func newCurrentNodeRunnerFixtureWithClientAndPersistence(
	t *testing.T,
	client currentNodeRunnerClient,
	withPersistenceGate bool,
	assignmentSteererFactory currentNodeAssignmentSteererFactory,
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
	var finalizationMu sync.Mutex
	var finalizationWG sync.WaitGroup
	finalizationClosed := false
	fixture.authority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    storeOptions,
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			finalizationMu.Lock()
			if finalizationClosed {
				finalizationMu.Unlock()
				return
			}
			finalizationWG.Add(1)
			finalizationMu.Unlock()
			defer finalizationWG.Done()
			controller.ExecutionFinalized(scope)
		}),
		PromptFeed: fixture.runtimes,
		EventFeed: func(resource runtimeids.SessionResourceRef, event agentruntime.Event) {
			fixture.runtimes.PublishAuthorityRuntimeEvent(resource, event)
		},
		ResourceLifecycle: fixture.runtimes,
		StepLifecycle:     currentNodeRunnerStepLifecycle{runtimes: fixture.runtimes},
	})
	t.Cleanup(func() {
		finalizationMu.Lock()
		finalizationClosed = true
		finalizationMu.Unlock()
		if fixture.controller != nil {
			if err := fixture.controller.Close(); fixture.controllerClose != nil {
				if !errors.Is(err, fixture.controllerClose) {
					t.Errorf("close current node controller error = %v, want %v", err, fixture.controllerClose)
				}
			} else if err != nil {
				t.Errorf("close current node controller: %v", err)
			}
		}
		finalizationWG.Wait()
		if fixture.starter != nil {
			if err := fixture.starter.Close(); err != nil {
				t.Errorf("close workflow starter: %v", err)
			}
		}
		if err := fixture.authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	permit := workflowexecution.NewTaskMutationCoordinator()
	dependencyCounter, err := workflowview.NewTaskDependencyCounter(metadataStore)
	if err != nil {
		t.Fatalf("new Task dependency counter: %v", err)
	}
	starter, err := NewStarter(cfg, metadataStore, store, nil, nil, StarterOptions{
		RuntimeAuthority: fixture.authority,
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
	var assignmentSteerer workflowexecution.CurrentNodeAssignmentSteerer = starter
	if assignmentSteererFactory != nil {
		assignmentSteerer = assignmentSteererFactory(starter)
	}
	controller, err = workflowexecution.NewCurrentNodeController(store, starter, fixture.authority, permit, workflowexecution.CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: assignmentSteerer,
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
	finalized := make(chan workflowexecution.TaskPreparationFinalization, 1)
	started, err := f.controller.StartTask(
		context.Background(),
		task.ID,
		workflowexecution.TaskStartPreparation{
			Prepare: func(context.Context) error { return nil },
			Commit: func(ctx context.Context) error {
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
			},
		},
		func(finalization workflowexecution.TaskPreparationFinalization) {
			finalized <- finalization
		},
	)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	select {
	case finalization := <-finalized:
		if finalization.Kind != workflowexecution.TaskPreparationHandedOff {
			t.Fatalf("task preparation finalization = %+v, want handed off", finalization)
		}
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("task preparation did not hand off to Current Node admission")
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("start mutation = %+v, want one Current Node", started.Mutation)
	}
	return started.Mutation.Created[0].Reference
}

func (f *currentNodeRunnerFixture) restartRuntime(t *testing.T) {
	t.Helper()
	if err := f.controller.Close(); err != nil {
		t.Fatalf("close pre-restart Current Node controller: %v", err)
	}
	if err := f.starter.Close(); err != nil {
		t.Fatalf("close pre-restart Workflow starter: %v", err)
	}
	if err := f.authority.Close(context.Background()); err != nil {
		t.Fatalf("close pre-restart runtime authority: %v", err)
	}

	f.runtimes = registry.NewRuntimeRegistry()
	storeOptions := f.metadata.AuthoritativeSessionStoreOptions()
	f.authority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: f.cfg.PersistenceRoot,
		StoreOptions:    storeOptions,
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			f.controller.ExecutionFinalized(scope)
		}),
		PromptFeed: f.runtimes,
		EventFeed: func(resource runtimeids.SessionResourceRef, event agentruntime.Event) {
			f.runtimes.PublishAuthorityRuntimeEvent(resource, event)
		},
		ResourceLifecycle: f.runtimes,
		StepLifecycle:     currentNodeRunnerStepLifecycle{runtimes: f.runtimes},
	})
	permit := workflowexecution.NewTaskMutationCoordinator()
	dependencyCounter, err := workflowview.NewTaskDependencyCounter(f.metadata)
	if err != nil {
		t.Fatalf("new restarted Task dependency counter: %v", err)
	}
	f.starter, err = NewStarter(f.cfg, f.metadata, f.store, nil, nil, StarterOptions{
		RuntimeAuthority: f.authority,
		TaskDependencies: dependencyCounter,
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, request runtimewire.RuntimeClientRequest) (llm.Client, error) {
			f.mu.Lock()
			f.clientRequests = append(f.clientRequests, request)
			clientErr := f.clientErr
			f.mu.Unlock()
			if clientErr != nil {
				return nil, clientErr
			}
			return f.client, nil
		}),
	})
	if err != nil {
		t.Fatalf("new restarted Workflow starter: %v", err)
	}
	f.controller, err = workflowexecution.NewCurrentNodeController(
		f.store,
		f.starter,
		f.authority,
		permit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: f.starter,
		},
	)
	if err != nil {
		t.Fatalf("new restarted Current Node controller: %v", err)
	}
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

func (f *currentNodeRunnerFixture) waitForWorkflowExecution(t *testing.T, reference workflow.CurrentNodeReference) {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		owned, err := f.workflowExecutionOwned(reference)
		if err != nil {
			t.Fatalf("inspect Workflow execution: %v", err)
		}
		if owned {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Current Node %v never reached Workflow execution authority", reference)
}

func (f *currentNodeRunnerFixture) waitForTaskQuiescence(t *testing.T, taskID workflow.TaskID) {
	t.Helper()
	deadline := time.Now().Add(currentNodeRunnerWait)
	for time.Now().Before(deadline) {
		observation, err := f.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID})
		if err != nil {
			t.Fatalf("inspect Task quiescence: %v", err)
		}
		if observation.Quiescence[taskID] {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Task %s did not reach Workflow execution quiescence", taskID)
}

func (f *currentNodeRunnerFixture) workflowExecutionOwned(reference workflow.CurrentNodeReference) (bool, error) {
	snapshots, err := f.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return false, err
	}
	for _, execution := range snapshots[reference.TaskID].Executions {
		if execution.Ref.CurrentNode.Equal(reference) {
			return true, nil
		}
	}
	return false, nil
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

func (f *currentNodeRunnerFixture) workflowAssignmentRecordCount(
	t *testing.T,
	sessionID runtimeids.SessionID,
) int {
	t.Helper()
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID.String())
	if err != nil {
		t.Fatalf("resolve persisted Session %s: %v", sessionID, err)
	}
	store, err := session.Open(record.SessionDir, f.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("open persisted Session %s: %v", sessionID, err)
	}
	var count int
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log for Session %s: %v", sessionID, err)
	}
	const recordsPerWindow = 128
	window, err := eventLog.ReadRecentRecords(recordsPerWindow)
	if err != nil {
		t.Fatalf("read workflow assignment records for Session %s: %v", sessionID, err)
	}
	for {
		for _, event := range window.Records {
			payload, payloadErr := event.Payload()
			if payloadErr != nil {
				t.Fatalf("read workflow assignment event for Session %s: %v", sessionID, payloadErr)
			}
			message, ok := payload.(session.MessageRecord)
			if ok &&
				message.MessageType != nil &&
				*message.MessageType == session.MessageTypeWorkflowMode {
				count++
			}
		}
		if window.ReachedStart {
			break
		}
		seen := 0
		window, err = eventLog.ReadSegmentBackward(window.StartOffset, func(session.EventRecord) bool {
			seen++
			return seen == recordsPerWindow
		})
		if err != nil {
			t.Fatalf("read older workflow assignment records for Session %s: %v", sessionID, err)
		}
	}
	return count
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
		llm.MessageTypeSubagents,
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeEnvironment,
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
	f.waitForTaskQuiescence(t, currentNode.TaskID)
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

func TestApprovalAppliesStrictPreviousTargetOnceAfterSourceRetires(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true, SupportsResponsesCompact: true, SupportsPromptCacheKey: true},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("review")},
		ScriptedFinalAnswer(`{"transition":"review","commentary":"done"}`),
		ScriptedFinalAnswer(`{"transition":"rework","commentary":"changes"}`),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, client)
	task := f.createTask(t, createCurrentNodeApprovalLoopWorkflow(t, f.store, true))
	implementation := f.startTask(t, task)
	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForTaskQuiescence(t, task.ID)
	retained, err := f.store.CurrentTaskSessionForNode(context.Background(), implementation)
	if err != nil {
		t.Fatalf("resolve retained implementation: %v", err)
	}
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply pending Approval: %v", err)
	}
	f.waitForModelRequests(t, 3)
	target := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Reference.Equal(implementation) && nodes[0].SessionID != nil
	})
	if *target[0].SessionID != retained.SessionID {
		t.Fatalf("strict previous target Session = %q, want %q", *target[0].SessionID, retained.SessionID)
	}
}

func TestManualMoveToRetainedTargetAssignsBeforeResumingLockedSession(t *testing.T) {
	auditScriptPath := filepath.Join(t.TempDir(), "audit.sh")
	if err := os.WriteFile(auditScriptPath, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write Audit Script: %v", err)
	}
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedToolBatch(
			"complete implementation",
			llm.ToolCall{
				ID:    "complete-implementation",
				Name:  string(toolspec.ToolCompleteNode),
				Input: json.RawMessage(`{"transition":"next","commentary":"implemented"}`),
			},
		),
		ScriptedRuntimeError(ErrScriptedRuntime),
		ScriptedRuntimeError(ErrScriptedRuntime),
	)
	workflowID := createCurrentNodeLinearWorkflow(
		t,
		f.store,
		"Manual Move retained target assignment",
		[]currentNodeWorkflowStep{
			{kind: workflow.NodeKindAgent, role: "coder", prompt: "Implement the task."},
			{kind: workflow.NodeKindAgent, role: "coder", prompt: "Review the pull request."},
			{kind: workflow.NodeKindScript, scriptPath: auditScriptPath},
		},
		[]currentNodeLinearTransition{
			{
				id:            "review",
				mode:          workflow.ContextModeContinueSession,
				contextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew},
			},
			{id: "audit", mode: workflow.ContextModeNewSession},
		},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	f.waitForModelRequests(t, 2)
	review := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].SessionID != nil &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})[0]
	f.waitForTaskQuiescence(t, task.ID)
	if count := f.workflowAssignmentRecordCount(t, *review.SessionID); count != 1 {
		t.Fatalf("workflow assignment records before Manual Move = %d, want initial Review assignment", count)
	}

	workflowfixture.SaveStoreGraph(t, context.Background(), f.store, workflowID, func(
		_ workflow.Definition,
		request *workflowstore.WorkflowGraphSaveRequest,
	) {
		var auditNodeID workflow.NodeID
		for index := range request.Nodes {
			if request.Nodes[index].ID == review.Reference.NodeID {
				request.Nodes[index].SubagentRole = "reviewer"
			}
			if request.Nodes[index].Key == "step_3" {
				auditNodeID = request.Nodes[index].ID
			}
		}
		if auditNodeID == "" {
			t.Fatal("Audit Script Node not found")
		}
		reworkGroupID := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
		request.TransitionGroups = append(request.TransitionGroups, workflowstore.TransitionGroupRecord{
			ID:           reworkGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: auditNodeID,
			TransitionID: "rework",
			DisplayName:  "Rework",
		})
		request.Edges = append(request.Edges, workflowstore.EdgeRecord{
			ID:                workflow.EdgeID(runtimeids.NewGraphEntityID()),
			WorkflowID:        workflowID,
			TransitionGroupID: reworkGroupID,
			Key:               "rework",
			TargetNodeID:      review.Reference.NodeID,
			AssigneeSelection: workflow.AssigneeSelectionConfigured,
			ThinkingSelection: workflow.ThinkingSelectionConfigured,
			ContextMode:       workflow.ContextModeContinueSession,
			ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
			PromptTemplate:    "Review the updated pull request.",
		})
	})
	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var auditNodeID workflow.NodeID
	for _, node := range definition.Nodes {
		if workflow.NodeKey(node) == "step_3" {
			auditNodeID = workflow.NodeIDOf(node)
			break
		}
	}
	if auditNodeID == "" {
		t.Fatal("updated workflow has no Audit Script Node")
	}
	auditMove, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: auditNodeID,
	})
	if err != nil {
		t.Fatalf("prepare Manual Move to Audit Script: %v", err)
	}
	if _, err := f.controller.ApplyManualMove(context.Background(), auditMove, nil); err != nil {
		t.Fatalf("apply Manual Move to Audit Script: %v", err)
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.NodeID == auditNodeID &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	retainedRecord, err := f.metadata.ResolvePersistedSession(context.Background(), review.SessionID.String())
	if err != nil {
		t.Fatalf("resolve retained Review Session: %v", err)
	}
	retainedStore, err := session.Open(
		retainedRecord.SessionDir,
		f.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("open retained Review Session: %v", err)
	}
	legacySnapshot := retainedStore.PromptFacingMetadataSnapshot()
	legacySnapshot.ActiveWorkflowAssignment = nil
	legacySnapshot.ActiveWorkflowAssignmentState = nil
	if err := retainedStore.RestorePromptFacingMetadata(legacySnapshot); err != nil {
		t.Fatalf("simulate retained Session created before assignment projection: %v", err)
	}
	rework := workflow.TransitionID("rework")
	prepared, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  review.Reference.NodeID,
		TransitionKey: &rework,
	})
	if err != nil {
		t.Fatalf("prepare Manual Move to retained Review: %v", err)
	}
	moved, err := f.controller.ApplyManualMove(context.Background(), prepared, nil)
	if err != nil {
		t.Fatalf("apply Manual Move to retained Review: %v", err)
	}
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].SessionID == nil ||
		*moved.Mutation.Created[0].SessionID != *review.SessionID {
		t.Fatalf("Manual Move target = %+v, want retained Session %s", moved.Mutation.Created, *review.SessionID)
	}
	if count := f.workflowAssignmentRecordCount(t, *review.SessionID); count != 2 {
		t.Fatalf("workflow assignment records after Manual Move = %d, want one appended target assignment", count)
	}

	requests := f.waitForModelRequests(t, 3)
	assignments := workflowAssignments(requests[2])
	if len(assignments) != 1 ||
		assignments[0].sourcePath != workflowruntime.CurrentNodePromptIdentity(review.Reference) {
		t.Fatalf("resumed workflow assignments = %+v, want Manual Move target assignment", assignments)
	}
	runtimeRequests := f.runtimeRequests()
	runtimeModels := make([]string, 0, len(runtimeRequests))
	for _, request := range runtimeRequests {
		runtimeModels = append(runtimeModels, request.ActiveSettings.Model)
	}
	if len(runtimeModels) == 0 || runtimeModels[len(runtimeModels)-1] != "workflow-coder" {
		t.Fatalf("Manual Move runtime models = %v, want retained coder model last", runtimeModels)
	}
}

func TestManualMoveFromInterruptedScriptAssignsAgentBeforeModelRequest(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write failing Script: %v", err)
	}
	f := newCurrentNodeRunnerFixture(t, ScriptedRuntimeError(ErrScriptedRuntime))
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Manual Move Script to Agent assignment",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: scriptPath},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the task."},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var target workflow.NodeID
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindAgent {
			target = workflow.NodeIDOf(node)
			break
		}
	}
	if target == "" {
		t.Fatal("workflow has no Agent target")
	}
	prepared, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
	})
	if err != nil {
		t.Fatalf("prepare Script-to-Agent Manual Move: %v", err)
	}
	moved, err := f.controller.ApplyManualMove(context.Background(), prepared, nil)
	if err != nil {
		t.Fatalf("apply Script-to-Agent Manual Move: %v", err)
	}
	if len(moved.Mutation.Created) != 1 {
		t.Fatalf("Script-to-Agent Manual Move target = %+v, want one Agent", moved.Mutation.Created)
	}
	targetNode := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) &&
			nodes[0].SessionID != nil
	})[0]
	requests := f.waitForModelRequests(t, 1)
	assignments := workflowAssignments(requests[0])
	if len(assignments) != 1 ||
		assignments[0].sourcePath != workflowruntime.CurrentNodePromptIdentity(targetNode.Reference) {
		t.Fatalf("Script-to-Agent request assignments = %+v, want exactly one target assignment", assignments)
	}
	runtimeRequests := f.runtimeRequests()
	if len(runtimeRequests) != 1 || runtimeRequests[0].ActiveSettings.Model != "workflow-reviewer" {
		t.Fatalf("Script-to-Agent runtime requests = %+v, want reviewer model", runtimeRequests)
	}
}

func TestManualMoveAssignmentPreparationFailureLeavesOriginCurrent(t *testing.T) {
	cause := errors.New("assignment preparation failed")
	f := newCurrentNodeRunnerFixtureWithAssignmentSteerer(
		t,
		NewScriptedClient(llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}),
		func(starter *Starter) workflowexecution.CurrentNodeAssignmentSteerer {
			return failingManualMoveAssignmentSteerer{delegate: starter, cause: cause}
		},
	)
	scriptPath := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write failing Script: %v", err)
	}
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Manual Move assignment preparation failure",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: scriptPath},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the task."},
	)
	task := f.createTask(t, workflowID)
	origin := f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(origin) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var target workflow.NodeID
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindAgent {
			target = workflow.NodeIDOf(node)
			break
		}
	}
	if target == "" {
		t.Fatal("workflow has no Agent target")
	}
	prepared, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
	})
	if err != nil {
		t.Fatalf("prepare Manual Move: %v", err)
	}
	moved, err := f.controller.ApplyManualMove(context.Background(), prepared, nil)
	if !errors.Is(err, cause) {
		t.Fatalf("Manual Move error = %v, want %v", err, cause)
	}
	if moved.Outcome != "" {
		t.Fatalf("Manual Move result = %+v, want unapplied zero result", moved)
	}
	nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list Current Nodes: %v", err)
	}
	if len(nodes) != 1 || !nodes[0].Reference.Equal(origin) {
		t.Fatalf("Current Nodes after assignment failure = %+v, want origin %v", nodes, origin)
	}
	if len(f.client.Requests()) != 0 {
		t.Fatalf("model requests after assignment failure = %d, want none", len(f.client.Requests()))
	}
}

func TestManualMoveRetainedSessionPreparationFailureRestoresPromptFacingMetadata(t *testing.T) {
	cause := errors.New("assignment preparation failed after retained Session mutation")
	f := newCurrentNodeRunnerFixtureWithAssignmentSteerer(
		t,
		NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			ScriptedRuntimeError(ErrScriptedRuntime),
		),
		func(starter *Starter) workflowexecution.CurrentNodeAssignmentSteerer {
			return failAfterManualMoveAssignmentPreparationSteerer{delegate: starter, cause: cause}
		},
	)
	workflowID := createCurrentNodeChainedWorkflow(t, f.store, workflow.ContextModeCompactAndContinueSession)
	task := f.createTask(t, workflowID)
	origin := f.startTask(t, task)
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(origin) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil &&
			nodes[0].SessionID != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	sessionID := *nodes[0].SessionID
	before, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID.String())
	if err != nil || before.Meta == nil {
		t.Fatalf("resolve retained Session before Manual Move: %+v, %v", before, err)
	}
	beforeAssignments := f.workflowAssignmentRecordCount(t, sessionID)
	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var target workflow.NodeID
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindAgent && workflow.NodeIDOf(node) != origin.NodeID {
			target = workflow.NodeIDOf(node)
			break
		}
	}
	if target == "" {
		t.Fatal("workflow has no retained Agent target")
	}
	prepared, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := f.controller.ApplyManualMove(context.Background(), prepared, nil); !errors.Is(err, cause) {
		t.Fatalf("ApplyManualMove error = %v, want %v", err, cause)
	}
	after, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID.String())
	if err != nil || after.Meta == nil {
		t.Fatalf("resolve retained Session after rejected Manual Move: %+v, %v", after, err)
	}
	if before.Meta.Name != after.Meta.Name ||
		before.Meta.FirstPromptPreview != after.Meta.FirstPromptPreview ||
		!reflect.DeepEqual(before.Meta.Continuation, after.Meta.Continuation) ||
		!reflect.DeepEqual(before.Meta.ChatSettings, after.Meta.ChatSettings) ||
		!reflect.DeepEqual(before.Meta.Locked, after.Meta.Locked) {
		t.Fatalf("retained Session prompt-facing metadata changed after rejected Manual Move:\nbefore=%+v\nafter=%+v", before.Meta, after.Meta)
	}
	if assignments := f.workflowAssignmentRecordCount(t, sessionID); assignments != beforeAssignments+2 {
		t.Fatalf(
			"retained Session assignments after rejected Manual Move = %d, want target plus origin restoration after %d existing",
			assignments,
			beforeAssignments,
		)
	}
}

func TestAutomaticFreshSessionBindsOnlyAfterAssignmentCommit(t *testing.T) {
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(
		t,
		NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			ScriptedRuntimeError(ErrScriptedRuntime),
		),
	)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	assignmentPending, releaseAssignment := f.persistenceGate.BlockWhen(
		func(snapshot session.PersistedStoreSnapshot) bool {
			return snapshot.Meta.LastSequence >= 2
		},
	)
	t.Cleanup(releaseAssignment)

	currentNode := f.startTask(t, task)
	select {
	case <-assignmentPending:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("workflow assignment did not reach persistence")
	}
	nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list Current Nodes before assignment commit: %v", err)
	}
	if len(nodes) != 1 || !nodes[0].Reference.Equal(currentNode) || nodes[0].SessionID != nil {
		t.Fatalf("Current Nodes before assignment commit = %+v, want unbound %v", nodes, currentNode)
	}

	releaseAssignment()
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].SessionID != nil &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil &&
			len(f.client.Requests()) == 1
	})
}

func TestAutomaticUncommittedFreshAssignmentDoesNotBindSessionToCurrentNode(t *testing.T) {
	cause := errors.New("assignment persistence failed")
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(
		t,
		NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			ScriptedRuntimeError(ErrScriptedRuntime),
		),
	)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.persistenceGate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2
	}, cause)

	currentNode := f.startTask(t, task)
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	if nodes[0].SessionID != nil {
		t.Fatalf(
			"Current Node retained unassigned Session %s after assignment failure",
			*nodes[0].SessionID,
		)
	}
	sessionIDs, err := f.metadata.ListProjectSessionIDs(context.Background(), f.projectID)
	if err != nil {
		t.Fatalf("list project Sessions: %v", err)
	}
	if len(sessionIDs) != 0 {
		t.Fatalf("project Sessions after uncommitted assignment = %+v, want none", sessionIDs)
	}

	f.restartRuntime(t)
	resumed, err := f.controller.ResumeTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ResumeTask after assignment failure: %v", err)
	}
	if resumed.Outcome != workflowexecution.TaskResumeApplied ||
		len(resumed.CurrentNodes) != 1 ||
		!resumed.CurrentNodes[0].Reference.Equal(currentNode) {
		t.Fatalf("ResumeTask result = %+v, want applied %v", resumed, currentNode)
	}
	nodes = f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(currentNode) &&
			nodes[0].SessionID != nil &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil &&
			len(f.client.Requests()) == 1
	})
	assignments := workflowAssignments(f.client.Requests()[0])
	if len(assignments) != 1 ||
		assignments[0].sourcePath != workflowruntime.CurrentNodePromptIdentity(currentNode) {
		t.Fatalf("resumed request assignments = %+v, want original Current Node assignment", assignments)
	}
}

func TestManualMoveUncommittedFreshAssignmentCleansSessionAndLeavesOriginCurrent(t *testing.T) {
	cause := errors.New("assignment persistence failed")
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(
		t,
		NewScriptedClient(llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}),
	)
	scriptPath := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write failing Script: %v", err)
	}
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Manual Move uncommitted assignment cleanup",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: scriptPath},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the task."},
	)
	task := f.createTask(t, workflowID)
	origin := f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Reference.Equal(origin) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var target workflow.NodeID
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindAgent {
			target = workflow.NodeIDOf(node)
			break
		}
	}
	if target == "" {
		t.Fatal("workflow has no Agent target")
	}
	f.persistenceGate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2
	}, cause)
	prepared, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
	})
	if err != nil {
		t.Fatalf("prepare Manual Move: %v", err)
	}
	moved, err := f.controller.ApplyManualMove(context.Background(), prepared, nil)
	if !errors.Is(err, cause) {
		t.Fatalf("Manual Move error = %v, want %v", err, cause)
	}
	if moved.Outcome != "" {
		t.Fatalf("Manual Move result = %+v, want unapplied zero result", moved)
	}
	nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list Current Nodes: %v", err)
	}
	if len(nodes) != 1 || !nodes[0].Reference.Equal(origin) {
		t.Fatalf("Current Nodes after uncommitted assignment = %+v, want origin %v", nodes, origin)
	}
	sessionIDs, err := f.metadata.ListProjectSessionIDs(context.Background(), f.projectID)
	if err != nil {
		t.Fatalf("list project Sessions: %v", err)
	}
	if len(sessionIDs) != 0 {
		t.Fatalf("project Sessions after uncommitted assignment = %+v, want none", sessionIDs)
	}
	sessionDirs, err := os.ReadDir(filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"))
	if err != nil {
		t.Fatalf("read project Session directory: %v", err)
	}
	if len(sessionDirs) != 0 {
		t.Fatalf("durable Session directories after uncommitted assignment = %d, want none", len(sessionDirs))
	}
}

func TestManualMoveCommittedAssignmentDiagnosticAppliesAndStartsTarget(t *testing.T) {
	diagnostic := errors.New("assignment observer diagnostic")
	f := newCurrentNodeRunnerFixtureWithAssignmentSteerer(
		t,
		NewScriptedClient(
			llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
			ScriptedRuntimeError(ErrScriptedRuntime),
		),
		func(starter *Starter) workflowexecution.CurrentNodeAssignmentSteerer {
			return diagnosticManualMoveAssignmentSteerer{
				delegate:   starter,
				diagnostic: diagnostic,
			}
		},
	)
	scriptPath := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write failing Script: %v", err)
	}
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Manual Move committed assignment diagnostic",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindScript, scriptPath: scriptPath},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review the task."},
	)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	f.waitForTaskQuiescence(t, task.ID)
	definition, _, err := f.store.GetDefinition(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var target workflow.NodeID
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindAgent {
			target = workflow.NodeIDOf(node)
			break
		}
	}
	if target == "" {
		t.Fatal("workflow has no Agent target")
	}
	prepared, err := f.store.PrepareManualMove(context.Background(), workflowstore.ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
	})
	if err != nil {
		t.Fatalf("prepare Manual Move: %v", err)
	}
	moved, err := f.controller.ApplyManualMove(context.Background(), prepared, nil)
	if !errors.Is(err, diagnostic) {
		t.Fatalf("Manual Move error = %v, want committed diagnostic %v", err, diagnostic)
	}
	if moved.Outcome != workflowstore.ManualMoveResultOutcomeApplied {
		t.Fatalf("Manual Move result = %+v, want applied", moved)
	}
	f.waitForModelRequests(t, 1)
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
	f.waitForTaskQuiescence(t, approval.Source.TaskID)
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
		requests[1].PromptCacheKey != requests[2].PromptCacheKey {
		t.Fatalf("source/target cache keys = %q/%q, want the same non-empty Session key", requests[1].PromptCacheKey, requests[2].PromptCacheKey)
	}
}

func TestPostCommitDiagnosticPreservesApprovalAndCACBoundary(t *testing.T) {
	client := NewCompactingScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true, SupportsResponsesCompact: true, SupportsPromptCacheKey: true},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("source")},
		ScriptedToolBatch("first", llm.ToolCall{ID: "first", Name: string(toolspec.ToolCompleteNode), Input: json.RawMessage(`{"transition":"next_1","commentary":"done"}`)}),
		ScriptedToolBatch("second", llm.ToolCall{ID: "second", Name: string(toolspec.ToolCompleteNode), Input: json.RawMessage(`{"transition":"next_2","commentary":"done"}`)}),
		ScriptedFinalAnswer(`{"commentary":"target"}`),
	)
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(t, client)
	f.starter.cfg.Settings.CompactionMode = config.CompactionModeNative
	threshold := 1
	f.starter.cfg.Settings.Workflow.PreCompactionTokens = &threshold
	var observed atomic.Bool
	f.persistenceGate.FailWhen(func(session.PersistedStoreSnapshot) bool {
		return len(client.CompactionCalls()) > 0 && observed.Swap(true)
	}, errors.New("post-commit diagnostic"))
	task := f.createTask(t, createCurrentNodeThreeStepWorkflow(
		t, f.store, "Post-commit diagnostic",
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "First."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Second."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Review."},
	))
	f.startTask(t, task)
	approval := f.waitForPendingApproval(t, task.ID)
	f.waitForTaskQuiescence(t, task.ID)
	if !observed.Load() {
		t.Fatal("post-commit persistence diagnostic was not injected")
	}
	if pending, err := f.store.ListPendingApprovals(context.Background(), task.ID); err != nil ||
		len(pending) != 1 || pending[0].ID != approval.ID {
		t.Fatalf("pending Approval after diagnostic = %+v, err = %v", pending, err)
	}
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply Approval after diagnostic: %v", err)
	}
	requests := f.waitForModelRequests(t, 3)
	summaries := 0
	for _, item := range requests[2].Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeCompactionSummary {
			summaries++
		}
	}
	if len(client.CompactionCalls()) != 1 || summaries != 1 {
		t.Fatalf("CAC boundary = %d compactions, %d summaries; want 1, 1", len(client.CompactionCalls()), summaries)
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
	f.waitForTaskQuiescence(t, approval.Source.TaskID)
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
	f.waitForTaskQuiescence(t, interrupted.Reference.TaskID)
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
	f.waitForTaskQuiescence(t, approval.Source.TaskID)
	if _, err := f.controller.ApplyPendingApproval(context.Background(), approval.ID); err != nil {
		t.Fatalf("apply ordinary continuation Approval: %v", err)
	}
	requests := f.waitForModelRequests(t, 3)
	if len(client.CompactionCalls()) != 1 {
		t.Fatalf("ordinary continuation post-completions = %d, want one", len(client.CompactionCalls()))
	}
	if requests[1].PromptCacheKey == "" || requests[2].PromptCacheKey == "" ||
		requests[1].PromptCacheKey != requests[2].PromptCacheKey {
		t.Fatalf("ordinary continuation cache keys = %q/%q, want the same non-empty Session key", requests[1].PromptCacheKey, requests[2].PromptCacheKey)
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
	f.waitForTaskQuiescence(t, source.TaskID)
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
	execution, exists := f.authority.ExecutionByWorkflow(sessionruntime.WorkflowExecutionRef{
		ProjectID:   f.projectID,
		WorkflowID:  workflowID,
		CurrentNode: source,
	})
	if !exists {
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
	f.waitForTaskQuiescence(t, source.TaskID)
	approval := f.waitForPendingApproval(t, task.ID)
	pending, err := f.store.ListPendingApprovals(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list pending Approval after cancellation: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != approval.ID {
		t.Fatalf("pending Approvals after cancellation = %+v, want held source Approval", pending)
	}
	association, err := f.store.CurrentTaskSessionForNode(context.Background(), source)
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
	f.waitForTaskQuiescence(t, currentNode.TaskID)

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
		Settings:              initialRuntime.ActiveSettings,
		EnabledTools:          initialRuntime.EnabledTools,
		FilesystemContext:     interactiveFilesystemContext,
		Sources:               initialRuntime.Sources,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Client:                f.client,
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

	workflowfixture.SaveStoreGraph(t, context.Background(), f.store, workflowID, func(_ workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		for index := range request.Nodes {
			if request.Nodes[index].ID == currentNode.NodeID {
				request.Nodes[index].CompletionMode = string(config.WorkflowCompletionModeStructuredOutput)
				return
			}
		}
		t.Fatalf("current Node %q missing from Workflow graph", currentNode.NodeID)
	})
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
	f.waitForTaskQuiescence(t, currentNode.TaskID)
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

	finalized := make(chan workflowexecution.TaskPreparationFinalization, 1)
	started, err := f.controller.StartTask(
		context.Background(),
		task.ID,
		workflowexecution.TaskStartPreparation{
			Prepare: func(context.Context) error { return nil },
			Commit: func(ctx context.Context) error {
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
			},
		},
		func(finalization workflowexecution.TaskPreparationFinalization) {
			finalized <- finalization
		},
	)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	select {
	case finalization := <-finalized:
		if finalization.Kind != workflowexecution.TaskPreparationHandedOff {
			t.Fatalf("task preparation finalization = %+v, want handed off", finalization)
		}
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("task preparation did not hand off before runtime preparation")
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
	nodes, err := f.store.ListCurrentNodes(context.Background(), task.ID)
	if err != nil || len(nodes) != 1 || nodes[0].SessionID == nil {
		t.Fatalf("Current Nodes after runtime preparation failure = %+v, %v; want retained binding", nodes, err)
	}
	startContext, err := f.store.ResolveCurrentNodeStartContext(context.Background(), nodes[0].Reference)
	if err != nil || startContext.CurrentNode.SessionID == nil || *startContext.CurrentNode.SessionID != *nodes[0].SessionID {
		t.Fatalf("resumed start context Session = %+v, %v; want retained binding %q", startContext.CurrentNode.SessionID, err, nodes[0].SessionID)
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

func TestAutomaticCommittedAssignmentDiagnosticStartsRealAgentExactlyOnce(t *testing.T) {
	sourceResponseStarted := make(chan struct{})
	sourceResponseRelease := make(chan struct{})
	var releaseSource sync.Once
	t.Cleanup(func() {
		releaseSource.Do(func() { close(sourceResponseRelease) })
	})
	client := NewScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
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
			Response: ScriptedFinalAnswer(`{"transition":"next","commentary":"source complete"}`).Response,
		},
		ScriptedFinalAnswer(`{"commentary":"target complete"}`),
	)
	diagnostic := errors.New("target assignment observer diagnostic")
	assignmentSteerer := &committedDiagnosticCurrentNodeAssignmentSteerer{
		targetCall: 2,
		diagnostic: diagnostic,
	}
	f := newCurrentNodeRunnerFixtureWithAssignmentSteerer(
		t,
		client,
		func(starter *Starter) workflowexecution.CurrentNodeAssignmentSteerer {
			assignmentSteerer.delegate = starter
			return assignmentSteerer
		},
	)
	f.controllerClose = diagnostic
	workflowID := createCurrentNodeTwoStepWorkflow(
		t,
		f.store,
		"Automatic committed assignment diagnostic",
		workflow.ContextModeNewSession,
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "coder", prompt: "Complete the source."},
		currentNodeWorkflowStep{kind: workflow.NodeKindAgent, role: "reviewer", prompt: "Complete the target."},
	)
	task := f.createTask(t, workflowID)
	source := f.startTask(t, task)
	select {
	case <-sourceResponseStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("source Current Node did not reach its model response")
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Reference.Equal(source) && nodes[0].SessionID != nil
	})

	releaseSource.Do(func() { close(sourceResponseRelease) })
	f.waitForModelRequestsWithin(t, 2, 3*time.Second)
	if !assignmentSteerer.matched.Load() {
		t.Fatal("target assignment observer diagnostic was not exercised")
	}
	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 && nodes[0].Scheduling == nil
	})
	if requests := client.Requests(); len(requests) != 2 {
		t.Fatalf("model requests = %d, want one source and one target execution", len(requests))
	}
}

func TestInitialStartCommittedAssignmentDiagnosticInterruptsWithoutStartingRuntime(t *testing.T) {
	diagnostic := errors.New("initial assignment observer diagnostic")
	var diagnosticMatched atomic.Bool
	f := newCurrentNodeRunnerFixtureWithPersistenceGate(t, NewScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
	))
	f.persistenceGate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		if snapshot.Meta.LastSequence < 2 {
			return false
		}
		return !diagnosticMatched.Swap(true)
	}, diagnostic)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.startTask(t, task)

	f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted
	})
	if !diagnosticMatched.Load() {
		t.Fatal("initial assignment observer diagnostic was not exercised")
	}
	if requests := f.client.Requests(); len(requests) != 0 {
		t.Fatalf("model requests = %d, want no Runtime start", len(requests))
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
	sourceAssociation, err := f.store.CurrentTaskSessionForNode(context.Background(), source)
	if err != nil {
		t.Fatalf("resolve source Session association: %v", err)
	}
	branchAssociations := make([]workflowstore.TaskSessionAssociation, 0, len(branchNodeIDs))
	for branchKey, nodeID := range branchNodeIDs {
		reference, err := workflow.NewCurrentNodeReference(task.ID, nodeID, &branchKey)
		if err != nil {
			t.Fatalf("create branch %q Current Node reference: %v", branchKey, err)
		}
		association, err := f.store.CurrentTaskSessionForNode(context.Background(), reference)
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
	f.waitForTaskQuiescence(t, source.TaskID)
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
	sourceAssociation, err := f.store.CurrentTaskSessionForNode(context.Background(), source)
	if err != nil {
		t.Fatalf("resolve source Session association: %v", err)
	}
	branchSessionIDs := make(map[runtimeids.SessionID]struct{}, len(branchNodeIDs))
	for branchKey, nodeID := range branchNodeIDs {
		reference, err := workflow.NewCurrentNodeReference(task.ID, nodeID, &branchKey)
		if err != nil {
			t.Fatalf("create branch %q Current Node reference: %v", branchKey, err)
		}
		association, err := f.store.CurrentTaskSessionForNode(context.Background(), reference)
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
	f.waitForWorkflowExecution(t, successor)
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

func TestCurrentNodeScriptInvalidCompletionInterruptsTheNode(t *testing.T) {
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
	f.startTask(t, task)
	nodes := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		return len(nodes) == 1 &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	})
	if got := nodes[0].Scheduling.Interruption.Reason; got != ReasonScriptCompletionFailed {
		t.Fatalf("script completion interruption reason = %q, want %q", got, ReasonScriptCompletionFailed)
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

func createCurrentNodeApprovalLoopWorkflow(
	t *testing.T,
	store *workflowstore.Store,
	retainReviewTarget bool,
) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Approval previous-target loop"})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	implementationID := workflow.NodeID(runtimeids.NewGraphEntityID())
	reviewID := workflow.NodeID(runtimeids.NewGraphEntityID())
	startGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	reviewGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	doneGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	reworkGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	workflowfixture.SaveStoreGraph(t, ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		startID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindStart))
		doneID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindTerminal))
		reviewRole := "coder"
		if retainReviewTarget {
			reviewRole = "reviewer"
		}
		request.Nodes = append(request.Nodes,
			workflowstore.NodeRecord{
				ID: implementationID, WorkflowID: created.ID, Key: "implementation",
				Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder",
			},
			workflowstore.NodeRecord{
				ID: reviewID, WorkflowID: created.ID, Key: "review",
				Kind: workflow.NodeKindAgent, DisplayName: "Review", SubagentRole: reviewRole,
			},
		)
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: reviewGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "review", DisplayName: "Review"},
			workflowstore.TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "done", DisplayName: "Done"},
			workflowstore.TransitionGroupRecord{ID: reworkGroup, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "rework", DisplayName: "Rework"},
		)
		reviewMode := workflow.ContextModeContinueSession
		reviewSource := workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
		if retainReviewTarget {
			reviewSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		}
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID,
				TransitionGroupID: startGroup, Key: "start", TargetNodeID: implementationID,
				ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement the task.", AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
			},
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID,
				TransitionGroupID: reviewGroup, Key: "review", TargetNodeID: reviewID,
				ContextMode: reviewMode, ContextSource: reviewSource, PromptTemplate: "Review the implementation.", AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
			},
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID,
				TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID,
				ContextMode: workflow.ContextModeNewSession, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
			},
			workflowstore.EdgeRecord{
				ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID,
				TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: implementationID,
				RequiresApproval: true,
				ContextMode:      workflow.ContextModeContinueSession,
				ContextSource:    workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
				PromptTemplate:   "Address the review findings.", AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
			},
		)
	})
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
	sourceID := workflow.NodeID(runtimeids.NewGraphEntityID())
	branchNodeIDs := map[workflow.TransitionBranchKey]workflow.NodeID{
		"branch_a": workflow.NodeID(runtimeids.NewGraphEntityID()),
		"branch_b": workflow.NodeID(runtimeids.NewGraphEntityID()),
	}
	joinID := workflow.NodeID(runtimeids.NewGraphEntityID())
	startGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	splitGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	branchAGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	branchBGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	doneGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	workflowfixture.SaveStoreGraph(t, ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		startID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindStart))
		doneID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindTerminal))
		request.Nodes = append(request.Nodes,
			workflowstore.NodeRecord{ID: sourceID, WorkflowID: created.ID, Key: "source", Kind: workflow.NodeKindAgent, DisplayName: "Source", SubagentRole: "coder"},
			workflowstore.NodeRecord{ID: branchNodeIDs["branch_a"], WorkflowID: created.ID, Key: "branch_a", Kind: workflow.NodeKindAgent, DisplayName: "Branch A", SubagentRole: "coder"},
			workflowstore.NodeRecord{ID: branchNodeIDs["branch_b"], WorkflowID: created.ID, Key: "branch_b", Kind: workflow.NodeKindAgent, DisplayName: "Branch B", SubagentRole: "coder"},
			workflowstore.NodeRecord{ID: joinID, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join"},
		)
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: sourceID, TransitionID: "split", DisplayName: "Split"},
			workflowstore.TransitionGroupRecord{ID: branchAGroup, WorkflowID: created.ID, SourceNodeID: branchNodeIDs["branch_a"], TransitionID: "join_a", DisplayName: "Join"},
			workflowstore.TransitionGroupRecord{ID: branchBGroup, WorkflowID: created.ID, SourceNodeID: branchNodeIDs["branch_b"], TransitionID: "join_b", DisplayName: "Join"},
			workflowstore.TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
		)
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: sourceID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Source."},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "branch_a", TargetNodeID: branchNodeIDs["branch_a"], AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeContinueSession, RequiresApproval: requiresApproval, PromptTemplate: "Branch A."},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "branch_b", TargetNodeID: branchNodeIDs["branch_b"], AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeContinueSession, RequiresApproval: requiresApproval, PromptTemplate: "Branch B."},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: branchAGroup, Key: "join_a", TargetNodeID: joinID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: branchBGroup, Key: "join_b", TargetNodeID: joinID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession},
		)
	})
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
	nodeIDs := make([]workflow.NodeID, len(steps))
	for index := range steps {
		nodeIDs[index] = workflow.NodeID(runtimeids.NewGraphEntityID())
	}
	groupIDs := make([]workflow.TransitionGroupID, len(steps)+1)
	groupIDs[0] = workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	for index := range transitions {
		groupIDs[index+1] = workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	}
	groupIDs[len(steps)] = workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	workflowfixture.SaveStoreGraph(t, ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		startID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindStart))
		doneID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindTerminal))
		for index, step := range steps {
			request.Nodes = append(request.Nodes, workflowstore.NodeRecord{
				ID: nodeIDs[index], WorkflowID: created.ID,
				Key:  workflow.ModelKey(fmt.Sprintf("step_%d", index+1)),
				Kind: step.kind, DisplayName: fmt.Sprintf("Step %d", index+1),
				SubagentRole: step.role, ScriptPath: step.scriptPath,
				CompletionMode: step.completionMode,
			})
		}
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
			request.TransitionGroups = append(request.TransitionGroups, workflowstore.TransitionGroupRecord{
				ID: groupID, WorkflowID: created.ID, SourceNodeID: sourceID,
				TransitionID: workflow.TransitionID(transitionID), DisplayName: displayName,
			})
		}
		request.Edges = append(request.Edges, workflowstore.EdgeRecord{
			ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID,
			TransitionGroupID: groupIDs[0], Key: "start", TargetNodeID: nodeIDs[0],
			ContextMode: workflow.ContextModeNewSession, PromptTemplate: steps[0].prompt, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
		})
		for index, transition := range transitions {
			request.Edges = append(request.Edges, workflowstore.EdgeRecord{
				ID:         workflow.EdgeID(runtimeids.NewGraphEntityID()),
				WorkflowID: created.ID, TransitionGroupID: groupIDs[index+1],
				Key: workflow.ModelKey(transition.id), TargetNodeID: nodeIDs[index+1],
				ContextMode: transition.mode, RequiresApproval: transition.requiresApproval,
				ContextSource: transition.contextSource, PromptTemplate: steps[index+1].prompt, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
			})
		}
		request.Edges = append(request.Edges, workflowstore.EdgeRecord{
			ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID,
			TransitionGroupID: groupIDs[len(groupIDs)-1], Key: "done",
			TargetNodeID: doneID, ContextMode: workflow.ContextModeNewSession, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured,
		})
	})
	return created.ID
}

func createCurrentNodeWorkflow(t *testing.T, store *workflowstore.Store, kind workflow.NodeKind, role, scriptPath, completionMode string) runtimeids.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Current Node runner"})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	nodeID := workflow.NodeID(runtimeids.NewGraphEntityID())
	startGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	doneGroup := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	workflowfixture.SaveStoreGraph(t, ctx, store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		startID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindStart))
		doneID := workflow.NodeIDOf(nodeByKindRunnerTest(t, definition, workflow.NodeKindTerminal))
		request.Nodes = append(request.Nodes, workflowstore.NodeRecord{
			ID: nodeID, WorkflowID: created.ID, Key: "execute", Kind: kind, DisplayName: "Execute",
			SubagentRole: role, ScriptPath: scriptPath, CompletionMode: completionMode,
		})
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroup, WorkflowID: created.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: nodeID, TransitionID: "done", DisplayName: "Done"},
		)
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: nodeID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession, PromptTemplate: func() string {
				if kind == workflow.NodeKindAgent {
					return "Do the work."
				}
				return ""
			}()},
			workflowstore.EdgeRecord{ID: workflow.EdgeID(runtimeids.NewGraphEntityID()), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}

func workflowRunnerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
