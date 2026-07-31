package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/runtimewire"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

const currentNodeRunnerWait = 5 * time.Second

type currentNodeRunnerFixture struct {
	cfg         config.App
	metadata    *metadata.Store
	store       *workflowstore.Store
	authority   *sessionruntime.Authority
	runtimes    *registry.RuntimeRegistry
	controller  *workflowexecution.CurrentNodeController
	starter     *Starter
	projectID   string
	workspaceID string
	workspace   string
	client      *ScriptedClient

	mu             sync.Mutex
	clientRequests []runtimewire.RuntimeClientRequest
	clientErr      error
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

type currentNodeRestoreFailureStore struct {
	RuntimeStore
	mu              sync.RWMutex
	sourceSessionID *runtimeids.SessionID
}

func (s *currentNodeRestoreFailureStore) setSourceSessionID(sourceSessionID runtimeids.SessionID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceSessionID = &sourceSessionID
}

func (s *currentNodeRestoreFailureStore) BindSessionToCurrentNode(
	ctx context.Context,
	req workflowstore.CurrentNodeSessionBindingRequest,
) (workflowstore.TaskSessionAssociation, error) {
	s.mu.RLock()
	sourceSessionID := s.sourceSessionID
	s.mu.RUnlock()
	if sourceSessionID != nil &&
		req.Association.SessionID == *sourceSessionID &&
		req.ExpectedCurrentSessionID != nil &&
		*req.ExpectedCurrentSessionID != *sourceSessionID {
		return workflowstore.TaskSessionAssociation{}, errors.New("restore source Session unavailable")
	}
	return s.RuntimeStore.BindSessionToCurrentNode(ctx, req)
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
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
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
		PromptFeed:        fixture.runtimes,
		ResourceLifecycle: fixture.runtimes,
	})
	t.Cleanup(func() {
		if fixture.controller != nil {
			_ = fixture.controller.Close()
		}
		if fixture.starter != nil {
			_ = fixture.starter.Close()
		}
		_ = fixture.authority.Close(context.Background())
	})
	permit := workflowexecution.NewMutationPermit()
	starter, err := NewStarter(cfg, metadataStore, store, nil, nil, StarterOptions{
		RuntimeAuthority: fixture.authority,
		MutationPermit:   permit,
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
	controller, err = workflowexecution.NewCurrentNodeController(store, starter, fixture.authority, permit, workflowexecution.CurrentNodeControllerConfig{AutomaticConcurrency: 1})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	fixture.controller = controller
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
	started, err := f.controller.StartTaskWithExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   f.workspaceID,
			SourceWorkspaceRoot: f.workspace,
		},
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

func TestCurrentNodeAgentStartsFreshSessionWithLatestRoleAndCompletionContract(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t, ScriptedFinalAnswer(`{"commentary":"done"}`))
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
	if len(requests) != 1 || requests[0].ActiveSettings.Model != "workflow-coder" {
		t.Fatalf("runtime client requests = %+v, want latest coder role settings", requests)
	}
	modelRequests := f.client.Requests()
	if len(modelRequests) != 1 || modelRequests[0].StructuredOutput == nil {
		t.Fatalf("model requests = %+v, want structured Current Node completion contract", modelRequests)
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

	requireToolCompletionRequests(t, f.waitForModelRequests(t, 2))
}

func TestResumeRetainsInitialCompletionModeAcrossNodeOverride(t *testing.T) {
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

	if _, err := f.store.UpdateNode(context.Background(), workflowstore.NodeRecord{
		ID:             currentNode.NodeID,
		WorkflowID:     workflowID,
		Key:            "execute",
		Kind:           workflow.NodeKindAgent,
		DisplayName:    "Execute",
		SubagentRole:   "coder",
		PromptTemplate: "Do the work.",
		CompletionMode: string(config.WorkflowCompletionModeStructuredOutput),
	}); err != nil {
		t.Fatalf("change latest node completion mode: %v", err)
	}
	if _, err := f.controller.ResumeTask(context.Background(), task.ID); err != nil {
		t.Fatalf("resume task: %v", err)
	}

	requireToolCompletionRequests(t, f.waitForModelRequests(t, 2))
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

func TestCurrentNodePreparationFailureCleansOnlyFreshDisposableSession(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	f.mu.Lock()
	f.clientErr = errors.New("provider unavailable")
	f.mu.Unlock()

	started, err := f.controller.StartTaskWithExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   f.workspaceID,
			SourceWorkspaceRoot: f.workspace,
		},
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
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 0 {
		t.Fatalf("retained Session count after fresh preparation failure = %d, %v; want zero", count, err)
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

func TestCurrentNodeFanoutPreparationFailureKeepsEachBranchResumable(t *testing.T) {
	for _, test := range []struct {
		name             string
		restoreFails     bool
		wantTaskSessions int64
	}{
		{name: "source restoration succeeds", wantTaskSessions: 1},
		{name: "source restoration fails", restoreFails: true, wantTaskSessions: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			var restoreFailureStore *currentNodeRestoreFailureStore
			var runtimeStore RuntimeStore = f.store
			if test.restoreFails {
				restoreFailureStore = &currentNodeRestoreFailureStore{RuntimeStore: runtimeStore}
				runtimeStore = restoreFailureStore
			}
			f.starter.store = currentNodeStartContextStore{
				RuntimeStore: runtimeStore,
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
			if restoreFailureStore != nil {
				restoreFailureStore.setSourceSessionID(sourceSessionID)
			}

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
				if test.restoreFails {
					if *branch.SessionID == sourceSessionID {
						t.Fatalf("branch %v retained source Session after forced restoration failure", branch.Reference)
					}
					branchSessionIDs[*branch.SessionID] = struct{}{}
				} else if *branch.SessionID != sourceSessionID {
					t.Fatalf(
						"branch %v Session = %v, want restored source Session %q",
						branch.Reference,
						branch.SessionID,
						sourceSessionID,
					)
				}
				if err := f.store.ValidateCurrentNodeSessionBinding(
					context.Background(),
					*branch.SessionID,
					branch.Reference,
				); err != nil {
					t.Fatalf("validate resumable branch Session binding %v: %v", branch.Reference, err)
				}
			}
			if test.restoreFails && len(branchSessionIDs) != len(branchNodeIDs) {
				t.Fatalf("retained clone Sessions = %d, want one per branch", len(branchSessionIDs))
			}
			if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != test.wantTaskSessions {
				t.Fatalf(
					"retained Session count after branch preparation failures = %d, %v; want %d",
					count,
					err,
					test.wantTaskSessions,
				)
			}
		})
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

func createCurrentNodeAgentWorkflowWithCompletionMode(t *testing.T, store *workflowstore.Store, completionMode string) workflow.WorkflowID {
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
			SubagentRole: first.role, ScriptPath: first.scriptPath, PromptTemplate: first.prompt,
			CompletionMode: first.completionMode,
		},
		{
			ID: secondID, WorkflowID: created.ID, Key: "second", Kind: second.kind, DisplayName: "Second",
			SubagentRole: second.role, ScriptPath: second.scriptPath, PromptTemplate: second.prompt,
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
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	return created.ID
}

func createCurrentNodeWorkflow(t *testing.T, store *workflowstore.Store, kind workflow.NodeKind, role, scriptPath string) runtimeids.WorkflowID {
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
		SubagentRole: role, PromptTemplate: "Do the work.", ScriptPath: scriptPath,
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
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add edge: %v", err)
		}
	}
	return created.ID
}

func workflowRunnerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
