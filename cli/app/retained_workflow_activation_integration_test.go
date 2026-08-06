package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/cli/app/internal/runtimeattach"
	modelstub "core/internal/testharness/pty/blackbox"
	"core/internal/testharness/testsetup"
	"core/server/metadata"
	agentruntime "core/server/runtime"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"github.com/google/uuid"
)

func TestInteractiveRetainedWorkflowActivationRepairsAssignmentAndStopsOfferingResume(t *testing.T) {
	for _, test := range []struct {
		name            string
		seedPrevious    bool
		wantAssignments int
	}{
		{name: "assignment absent", wantAssignments: 1},
		{name: "previous Node identity", seedPrevious: true, wantAssignments: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRetainedWorkflowAppFixture(t, test.seedPrevious)
			plan := fixture.planRetainedSession(t)
			activation := fixture.activate(t, plan)
			defer func() { _ = activation.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly) }()

			detail, err := fixture.server.inner.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
				TaskID: string(fixture.taskID),
			})
			if err != nil {
				t.Fatalf("GetWorkflowTask: %v", err)
			}
			if detail.Task.Actions.CanResume {
				t.Fatalf("Task actions after interactive activation = %+v, want Resume unavailable", detail.Task.Actions)
			}
			if !detail.Task.Actions.CanInterrupt {
				t.Fatalf("Task actions after interactive activation = %+v, want exact Workflow execution", detail.Task.Actions)
			}
			if count := fixture.workflowAssignmentCount(t); count != test.wantAssignments {
				t.Fatalf("Workflow assignment count = %d, want %d", count, test.wantAssignments)
			}
		})
	}
}

func TestBoardResumeThenInteractiveActivationAttachesRepeatedOwnersToOneResource(t *testing.T) {
	fixture := newRetainedWorkflowAppFixture(t, false)
	resumed, err := fixture.server.inner.WorkflowClient().ResumeWorkflowTask(context.Background(), serverapi.WorkflowTaskResumeRequest{
		TaskID:           string(fixture.taskID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if resumed.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied {
		t.Fatalf("Resume outcome = %+v, want applied", resumed)
	}
	plan := fixture.planRetainedSession(t)
	first := fixture.activate(t, plan)
	defer func() { _ = first.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly) }()
	second := fixture.activate(t, plan)
	defer func() { _ = second.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly) }()
	detail, err := fixture.server.inner.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
		TaskID: string(fixture.taskID),
	})
	if err != nil {
		t.Fatalf("GetWorkflowTask after repeated activation: %v", err)
	}
	if len(detail.Task.LiveSessionIDs) != 1 || detail.Task.LiveSessionIDs[0] != fixture.sessionID.String() {
		t.Fatalf("live Sessions after repeated activation = %v, want one retained Session", detail.Task.LiveSessionIDs)
	}
	if detail.Task.Actions.CanResume || !detail.Task.Actions.CanInterrupt {
		t.Fatalf("Task actions after Board Resume and interactive adoption = %+v", detail.Task.Actions)
	}
	if count := fixture.workflowAssignmentCount(t); count != 1 {
		t.Fatalf("Workflow assignment count = %d, want 1", count)
	}
}

func TestInteractiveRetainedWorkflowSessionInterruptPublishesResumableState(t *testing.T) {
	fixture := newRetainedWorkflowAppFixture(t, false)
	activation := fixture.activate(t, fixture.planRetainedSession(t))
	defer func() { _ = activation.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly) }()
	select {
	case <-fixture.modelEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("retained Workflow execution did not enter the model loop")
	}

	if _, err := fixture.server.inner.RuntimeControlClient().Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.sessionID.String(),
	}); err != nil {
		t.Fatalf("runtime Interrupt: %v", err)
	}
	detail, err := fixture.server.inner.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{
		TaskID: string(fixture.taskID),
	})
	if err != nil {
		t.Fatalf("GetWorkflowTask after runtime Interrupt: %v", err)
	}
	if !detail.Task.Actions.CanResume || detail.Task.Actions.CanInterrupt {
		t.Fatalf("Task actions after runtime Interrupt = %+v, want resumable and not interruptible", detail.Task.Actions)
	}
	if len(detail.Task.LiveSessionIDs) != 0 {
		t.Fatalf("live Sessions after runtime Interrupt = %v, want none", detail.Task.LiveSessionIDs)
	}
}

func TestAgentShellCompletionFromInteractiveRetainedWorkflowSessionResolvesSameRun(t *testing.T) {
	fixture := newRetainedWorkflowAppFixture(t, false)
	activation := fixture.activate(t, fixture.planRetainedSession(t))
	defer func() { _ = activation.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly) }()
	select {
	case <-fixture.modelEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("retained Workflow execution did not enter the model loop")
	}

	response, err := fixture.server.inner.WorkflowClient().CompleteWorkflowTask(
		context.Background(),
		serverapi.WorkflowTaskCompleteRequest{
			ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
			AgentSessionID: fixture.sessionID.String(),
			TransitionID:   "done",
			Commentary:     "completed from kent task complete",
		},
	)
	if err != nil {
		t.Fatalf("agent-originated kent task complete: %v", err)
	}
	if response.TaskID != string(fixture.taskID) {
		t.Fatalf("completed Task ID = %q, want %q", response.TaskID, fixture.taskID)
	}
	fixture.releaseProvider()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		detail, detailErr := fixture.server.inner.WorkflowClient().GetWorkflowTask(
			context.Background(),
			serverapi.WorkflowTaskGetRequest{TaskID: string(fixture.taskID)},
		)
		return detailErr == nil &&
			detail.Task.Status.Kind == serverapi.WorkflowTaskStatusKindDone &&
			!detail.Task.Actions.CanInterrupt &&
			!detail.Task.Actions.CanResume
	}, "agent-originated shell completion did not finalize the retained Workflow Run")
}

type retainedWorkflowAppFixture struct {
	server       *embeddedAppServer
	provider     *httptest.Server
	release      chan struct{}
	releaseOnce  sync.Once
	modelEntered <-chan struct{}
	taskID       workflow.TaskID
	sessionID    runtimeids.SessionID
	cfg          config.App
}

func newRetainedWorkflowAppFixture(t *testing.T, seedPrevious bool) *retainedWorkflowAppFixture {
	t.Helper()
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)
	modelEntered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 11) {
			return
		}
		enteredOnce.Do(func() { close(modelEntered) })
		select {
		case <-release:
			modelstub.WriteCompletedResponseStream(w, "still working", 11, 7)
		case <-r.Context().Done():
		}
	}))
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{
		Model:         "gpt-5",
		OpenAIBaseURL: provider.URL,
	})
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	project, err := metadata.ResolveBinding(context.Background(), cfg.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(retainedActivationRoleResolver{}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	task, currentNode := createRetainedWorkflowTask(t, store, project, cfg)
	sessionStore, sessionID := createRetainedWorkflowSession(t, metadataStore, project.ProjectID, cfg.WorkspaceRoot)
	if _, err := store.BindSessionToCurrentNode(context.Background(), workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  currentNode,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	if seedPrevious {
		seedPreviousWorkflowAssignment(t, sessionStore, cfg, task.WorkflowID, currentNode)
	}
	if err := store.InterruptCurrentNode(
		context.Background(),
		currentNode,
		workflow.CurrentNodeInterruptionReasonRuntimeCanceled,
		workflow.NewCurrentNodeInterruptionDetail("test_retained_activation", nil),
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	if err := metadataStore.Close(); err != nil {
		t.Fatalf("close setup metadata store: %v", err)
	}
	server, err := startAppTestEmbeddedServer(t, context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         provider.URL,
		OpenAIBaseURLExplicit: true,
	}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	fixture := &retainedWorkflowAppFixture{
		server:       server,
		provider:     provider,
		release:      release,
		modelEntered: modelEntered,
		taskID:       task.ID,
		sessionID:    sessionID,
		cfg:          cfg,
	}
	t.Cleanup(func() {
		_, _ = server.inner.WorkflowClient().InterruptWorkflowTask(context.Background(), serverapi.WorkflowTaskInterruptRequest{
			TaskID: string(task.ID),
		})
		fixture.releaseProvider()
		_ = server.Close()
		provider.Close()
	})
	return fixture
}

func (f *retainedWorkflowAppFixture) planRetainedSession(t *testing.T) sessionLaunchPlan {
	t.Helper()
	plan, err := newSessionLaunchPlanner(f.server).PlanSession(context.Background(), sessionLaunchRequest{
		Mode:   launchModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(f.sessionID),
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	return plan
}

func (f *retainedWorkflowAppFixture) activate(t *testing.T, plan sessionLaunchPlan) *runtimeattach.Activation {
	t.Helper()
	activation, err := runtimeattach.Activate(context.Background(), f.server.RuntimeAttachmentClients().SessionRuntime, runtimeattach.Request{
		SessionID:      plan.SessionID,
		ActiveSettings: plan.ActiveSettings,
		EnabledTools:   plan.EnabledTools,
		Source:         plan.Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime through CLI/TUI adapter: %v", err)
	}
	return activation
}

func (f *retainedWorkflowAppFixture) releaseProvider() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func (f *retainedWorkflowAppFixture) workflowAssignmentCount(t *testing.T) int {
	t.Helper()
	store := openAuthoritativeAppSession(t, f.cfg.PersistenceRoot, f.sessionID.String())
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("MaterializeEventLog: %v", err)
	}
	window, err := eventLog.ReadRecentRecords(64)
	if err != nil {
		t.Fatalf("ReadRecentRecords: %v", err)
	}
	count := 0
	for _, record := range window.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("decode Session record: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeWorkflowMode {
			count++
		}
	}
	return count
}

func createRetainedWorkflowTask(
	t *testing.T,
	store *workflowstore.Store,
	binding metadata.Binding,
	cfg config.App,
) (workflowstore.TaskRecord, workflow.CurrentNodeReference) {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Retained activation"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := retainedActivationNodeByKind(t, definition, workflow.NodeKindStart)
	terminal := retainedActivationNodeByKind(t, definition, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-" + uuid.NewString())
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID:           agentID,
		WorkflowID:   created.ID,
		Key:          "agent",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Agent",
		SubagentRole: workflow.DefaultAgentRole,
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	startGroupID := workflow.TransitionGroupID("group-" + uuid.NewString())
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           startGroupID,
		WorkflowID:   created.ID,
		SourceNodeID: workflow.NodeIDOf(start),
		TransitionID: "start",
		DisplayName:  "Start",
	}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                workflow.EdgeID("edge-" + uuid.NewString()),
		WorkflowID:        created.ID,
		TransitionGroupID: startGroupID,
		Key:               "start",
		TargetNodeID:      agentID,
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Keep working until interrupted.",
	}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	doneGroupID := workflow.TransitionGroupID("group-" + uuid.NewString())
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           doneGroupID,
		WorkflowID:   created.ID,
		SourceNodeID: agentID,
		TransitionID: "done",
		DisplayName:  "Done",
	}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                workflow.EdgeID("edge-" + uuid.NewString()),
		WorkflowID:        created.ID,
		TransitionGroupID: doneGroupID,
		Key:               "done",
		TargetNodeID:      workflow.NodeIDOf(terminal),
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
	}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, created.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        &created.ID,
		Title:             "Retained activation",
		Body:              "Exercise retained Workflow Session activation.",
		SourceWorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := store.LockTaskExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	publication, err := workflowstore.NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		testsetup.PreparedPublicationStage(workflowstore.NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("Task Start created Current Nodes = %+v", started.Mutation.Created)
	}
	return task, started.Mutation.Created[0].Reference
}

func createRetainedWorkflowSession(
	t *testing.T,
	metadataStore *metadata.Store,
	projectID string,
	workspace string,
) (*session.Store, runtimeids.SessionID) {
	t.Helper()
	container := filepath.Join(metadataStore.PersistenceRoot(), "projects", projectID, "sessions")
	store, err := session.Create(
		container,
		filepath.Base(container),
		workspace,
		sessioncontract.SessionCategorySubagent,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return store, sessionID
}

func seedPreviousWorkflowAssignment(
	t *testing.T,
	store *session.Store,
	cfg config.App,
	workflowID runtimeids.WorkflowID,
	current workflow.CurrentNodeReference,
) {
	t.Helper()
	var branch *workflow.TransitionBranchKey
	if branchKey, branchScoped := current.TransitionBranchKey(); branchScoped {
		branch = &branchKey
	}
	previous, err := workflow.NewCurrentNodeReference(
		current.TaskID,
		workflow.NodeID("node-"+uuid.NewString()),
		branch,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference previous: %v", err)
	}
	ensure, err := agentruntime.EnsurePersistedWorkflowAssignment(store, agentruntime.WorkflowAssignment{
		ContextMode:    workflow.ContextModeNewSession,
		CompletionMode: workflowruntime.CompletionModeTool,
		Prompt: workflowruntime.PromptContract{
			Identity:       workflowruntime.CurrentNodePromptIdentity(previous),
			CompletionMode: workflowruntime.CompletionModeTool,
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode:      previous,
				WorkflowID:       workflowID,
				TransitionPrompt: "Previous Workflow assignment.",
			},
		},
	}, agentruntime.PersistedWorkflowAssignmentContext{
		Workdir:                 cfg.WorkspaceRoot,
		GlobalConfigDir:         cfg.PersistenceRoot,
		Model:                   "gpt-5",
		ThinkingLevel:           "medium",
		SkillPolicy:             config.ResolveSkillPolicy(cfg.Settings),
		SubagentCatalogSettings: cfg.Settings,
	})
	if err != nil {
		t.Fatalf("EnsurePersistedWorkflowAssignment previous: %v", err)
	}
	if receipt, err := ensure.Wait(context.Background()); err != nil || !receipt.Committed {
		t.Fatalf("previous assignment receipt = %+v, error = %v", receipt, err)
	}
}

func retainedActivationNodeByKind(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("Workflow has no %q Node", kind)
	return nil
}

type retainedActivationRoleResolver struct{}

func (retainedActivationRoleResolver) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	if role != workflow.DefaultAgentRole {
		return workflow.TargetAgentRole{}, false
	}
	return workflow.TargetAgentRole{
		Identity:         role,
		Model:            "gpt-5",
		QuestionsEnabled: true,
	}, true
}

func (retainedActivationRoleResolver) ExplicitCallableRoles() []workflow.TargetAgentRole {
	role, _ := (retainedActivationRoleResolver{}).ResolveConfiguredRole(workflow.DefaultAgentRole)
	return []workflow.TargetAgentRole{role}
}
