package runtimecontrol

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestStillOpenDirectlyRetainedWorkflowSessionRoutesAgentStartingOperationsThroughWorkflowAuthority(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *runtimeControlRetainedWorkflowFixture)
		run     func(context.Context, *runtimeControlRetainedWorkflowFixture) error
		started func(*runtimeControlRetainedWorkflowFixture) bool
	}{
		{
			name: "SubmitUserTurn uses Workflow authority instead of lease-less ordinary scope",
			prepare: func(_ *testing.T, fixture *runtimeControlRetainedWorkflowFixture) {
				fixture.client.blockGenerate()
			},
			run: func(ctx context.Context, fixture *runtimeControlRetainedWorkflowFixture) error {
				ref := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
				_, err := fixture.service.SubmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{
					ClientRequestID: ref.ClientRequestID.String(),
					SessionID:       fixture.session.Meta().SessionID,
					Input:           runtimeinput.Text("continue retained Workflow work"),
					OperationRef:    ref,
					PreSubmitCompactionOperationRef: runtimeControlOperationRef(
						clientui.RuntimeOperationKindPreSubmitCompact,
					),
				})
				return err
			},
		},
		{
			name: "SubmitUserShellCommand uses Workflow authority instead of lease-less ordinary scope",
			run: func(ctx context.Context, fixture *runtimeControlRetainedWorkflowFixture) error {
				ref := runtimeControlOperationRef(clientui.RuntimeOperationKindUserShell)
				return fixture.service.SubmitUserShellCommand(ctx, serverapi.RuntimeSubmitUserShellCommandRequest{
					ClientRequestID: ref.ClientRequestID.String(),
					SessionID:       fixture.session.Meta().SessionID,
					Command:         "while :; do sleep 1; done",
					OperationRef:    ref,
				})
			},
			started: func(fixture *runtimeControlRetainedWorkflowFixture) bool {
				return fixture.background.Count() == 1
			},
		},
		{
			name: "CompactContext uses Workflow authority instead of lease-less ordinary scope",
			prepare: func(t *testing.T, fixture *runtimeControlRetainedWorkflowFixture) {
				t.Helper()
				if _, err := fixture.engine.SubmitUserMessage(context.Background(), "seed compactable context"); err != nil {
					t.Fatalf("seed compactable context: %v", err)
				}
				fixture.client.blockCompact()
			},
			run: func(ctx context.Context, fixture *runtimeControlRetainedWorkflowFixture) error {
				ref := runtimeControlOperationRef(clientui.RuntimeOperationKindCompact)
				return fixture.service.CompactContext(ctx, serverapi.RuntimeCompactContextRequest{
					ClientRequestID: ref.ClientRequestID.String(),
					SessionID:       fixture.session.Meta().SessionID,
					Args:            "compact retained Workflow context",
					OperationRef:    ref,
				})
			},
		},
		{
			name: "SetGoal uses Workflow authority instead of lease-less ordinary scope",
			prepare: func(_ *testing.T, fixture *runtimeControlRetainedWorkflowFixture) {
				fixture.client.blockGenerate()
			},
			run: func(ctx context.Context, fixture *runtimeControlRetainedWorkflowFixture) error {
				_, err := fixture.service.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{
					ClientRequestID: "set-retained-workflow-goal",
					SessionID:       fixture.session.Meta().SessionID,
					Objective:       "finish the retained Workflow node",
					Actor:           string(session.GoalActorUser),
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeControlRetainedWorkflowFixture(t)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}

			operationCtx, cancelOperation := context.WithCancel(context.Background())
			operationDone := make(chan error, 1)
			go func() {
				operationDone <- test.run(operationCtx, fixture)
			}()
			defer func() {
				cancelOperation()
				if execution, live := fixture.authority.SessionExecution(fixture.sessionID); live {
					execution.RequestStop()
					fixture.client.release()
					stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer stopCancel()
					if _, err := execution.Wait(stopCtx); err != nil &&
						!errors.Is(err, context.Canceled) {
						t.Errorf("stop runtimecontrol Agent execution: %v", err)
					}
				} else {
					fixture.client.release()
				}
				select {
				case <-operationDone:
				case <-time.After(3 * time.Second):
					t.Error("runtimecontrol operation did not stop after test cancellation")
				}
			}()

			deadline := time.Now().Add(3 * time.Second)
			for test.started != nil && !test.started(fixture) {
				select {
				case err := <-operationDone:
					t.Fatalf("runtimecontrol Service API returned before reaching its controlled execution barrier: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("runtimecontrol Service API did not reach its controlled execution barrier")
				}
				time.Sleep(5 * time.Millisecond)
			}
			var scope sessionruntime.ExecutionScope
			for {
				execution, live := fixture.authority.SessionExecution(fixture.sessionID)
				if live {
					scope = execution.Scope()
					break
				}
				select {
				case err := <-operationDone:
					t.Fatalf("runtimecontrol Service API returned before starting Agent execution: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("runtimecontrol Service API did not start Agent execution")
				}
				time.Sleep(5 * time.Millisecond)
			}
			workflowExecution, workflowOwned := scope.Workflow()
			if !workflowOwned {
				t.Fatal("directly retained Workflow Session started a lease-less ordinary Exact Execution Scope")
			}
			if !workflowExecution.CurrentNode.Equal(fixture.currentNode) {
				t.Fatalf(
					"Workflow authority Current Node = %+v, want directly retained %+v",
					workflowExecution.CurrentNode,
					fixture.currentNode,
				)
			}
		})
	}
}

type runtimeControlRetainedWorkflowFixture struct {
	service     *Service
	session     *session.Store
	engine      *runtime.Engine
	currentNode workflow.CurrentNodeReference
	authority   *sessionruntime.Authority
	sessionID   runtimeids.SessionID
	client      *runtimeControlAuthorityProbeClient
	background  *shelltool.Manager
}

type runtimeControlAuthorityProbeClient struct {
	mu              sync.Mutex
	generateBlocked bool
	compactBlocked  bool
	releaseOnce     sync.Once
	released        chan struct{}
}

func (c *runtimeControlAuthorityProbeClient) blockGenerate() {
	c.mu.Lock()
	c.generateBlocked = true
	c.mu.Unlock()
}

func (c *runtimeControlAuthorityProbeClient) blockCompact() {
	c.mu.Lock()
	c.compactBlocked = true
	c.mu.Unlock()
}

func (c *runtimeControlAuthorityProbeClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	blocked := c.generateBlocked
	c.mu.Unlock()
	if blocked {
		select {
		case <-c.released:
		case <-ctx.Done():
			return llm.Response{}, context.Cause(ctx)
		}
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{InputTokens: 330000, WindowTokens: 372000},
	}, nil
}

func (c *runtimeControlAuthorityProbeClient) Compact(ctx context.Context, _ llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.mu.Lock()
	blocked := c.compactBlocked
	c.mu.Unlock()
	if blocked {
		select {
		case <-c.released:
		case <-ctx.Done():
			return llm.CompactionResponse{}, context.Cause(ctx)
		}
	}
	trimmed := 1
	return llm.CompactionResponse{
		OutputItems: []llm.ResponseItem{
			{
				Type:        llm.ResponseItemTypeMessage,
				Role:        textutil.Value(llm.RoleUser),
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("compacted retained context"),
			},
			{
				Type:             llm.ResponseItemTypeCompaction,
				EncryptedContent: textutil.Value("checkpoint"),
			},
		},
		Usage:             llm.Usage{WindowTokens: 200000},
		TrimmedItemsCount: &trimmed,
	}, nil
}

func (c *runtimeControlAuthorityProbeClient) release() {
	c.releaseOnce.Do(func() {
		close(c.released)
	})
}

func (*runtimeControlAuthorityProbeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "test",
		SupportsResponsesAPI: true,
	}, nil
}

func newRuntimeControlRetainedWorkflowFixture(t *testing.T) *runtimeControlRetainedWorkflowFixture {
	t.Helper()
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := metadataStore.Close(); err != nil {
			t.Errorf("close metadata Store: %v", err)
		}
	})
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(ctx, binding.ProjectID, "RTC"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	workflowStore, err := workflowstore.New(
		metadataStore,
		workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")),
	)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := runtimeControlCreateAgentWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        &workflowID,
		Title:             "Retained runtimecontrol operation",
		Body:              "Keep the directly retained Workflow authority.",
		SourceWorkspaceID: binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask created Current Nodes = %+v, want one Agent Current Node", started.Mutation.Created)
	}
	currentNode := started.Mutation.Created[0].Reference

	sessionRoot := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	sessionStore, err := session.Create(
		sessionRoot,
		filepath.Base(workspaceRoot),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := workflowStore.BindSessionToCurrentNode(ctx, workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  currentNode,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	if err := workflowStore.ValidateCurrentNodeSessionBinding(ctx, sessionID, currentNode); err != nil {
		t.Fatalf("ValidateCurrentNodeSessionBinding: %v", err)
	}

	client := &runtimeControlAuthorityProbeClient{released: make(chan struct{})}
	background, err := shelltool.NewManager()
	if err != nil {
		t.Fatalf("shell manager: %v", err)
	}
	t.Cleanup(func() {
		if err := background.Close(); err != nil {
			t.Errorf("close shell manager: %v", err)
		}
	})
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
		Background:      background,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime Authority: %v", err)
		}
	})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   binding.ProjectID,
		WorkflowID:  workflowID,
		CurrentNode: currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.Reviewer.Frequency = "off"
	settings.CompactionMode = config.CompactionModeNative
	filesystemContext, err := runtimewire.NewFilesystemContext(
		workspaceRoot,
		workspaceRoot,
		metadata.ProjectWorkspaceBoundary{ProjectID: binding.ProjectID},
	)
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:          settings,
		EnabledTools:      []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolAskQuestion},
		FilesystemContext: filesystemContext,
		Client:            client,
		CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        lease.ScopeID(),
			CompletionMode: workflowruntime.CompletionModeTool,
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode: currentNode,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	initialStarted := make(chan sessionruntime.ExecutionScope, 1)
	initialRelease := make(chan struct{})
	initialExecution, err := authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Resource:   sessionruntime.OpenAgentResource{},
		Workflow:   &lease,
		Runner: func(runCtx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			initialStarted <- scope
			select {
			case <-initialRelease:
				return nil
			case <-runCtx.Done():
				return context.Cause(runCtx)
			}
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	lease.Release()
	var initialScope sessionruntime.ExecutionScope
	select {
	case initialScope = <-initialStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("retained Workflow Exact Execution Scope did not start")
	}
	initialWorkflow, workflowOwned := initialScope.Workflow()
	if !workflowOwned || !initialWorkflow.CurrentNode.Equal(currentNode) {
		t.Fatalf(
			"initial Exact Execution Scope Workflow = (%+v, %t), want retained Current Node %+v",
			initialWorkflow,
			workflowOwned,
			currentNode,
		)
	}
	if _, err := authority.OpenRuntime(ctx, sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "still-open-retained-runtimecontrol",
		Runtime:   nil,
	}); err != nil {
		t.Fatalf("attach owner to retained Workflow Runtime: %v", err)
	}
	close(initialRelease)
	if _, err := initialExecution.Wait(context.Background()); err != nil {
		t.Fatalf("finish initial retained Workflow execution: %v", err)
	}
	if execution, live := authority.SessionExecution(sessionID); live {
		t.Fatalf("initial retained Workflow execution remained live after finalization: %+v", execution.Scope())
	}
	var engine *runtime.Engine
	if err := authority.WithCurrentRuntime(ctx, sessionID, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatalf("WithCurrentRuntime: %v", err)
	}
	service := NewService(authority).
		WithPersistedSessionResolver(metadataStore).
		WithWorkflowTaskSessionResolver(metadataStore).
		WithPromptHistoryStore(metadataStore)
	return &runtimeControlRetainedWorkflowFixture{
		service:     service,
		session:     sessionStore,
		engine:      engine,
		currentNode: currentNode,
		authority:   authority,
		sessionID:   sessionID,
		client:      client,
		background:  background,
	}
}

func runtimeControlCreateAgentWorkflow(
	t *testing.T,
	ctx context.Context,
	store *workflowstore.Store,
) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Runtime Control"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, record, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var startID, terminalID workflow.NodeID
	nodes := make([]workflowstore.NodeRecord, 0, len(definition.Nodes)+1)
	for index, node := range definition.Nodes {
		nodeID := workflow.NodeIDOf(node)
		switch node.Kind() {
		case workflow.NodeKindStart:
			startID = nodeID
		case workflow.NodeKindTerminal:
			terminalID = nodeID
		}
		nodes = append(nodes, workflowstore.NodeRecord{
			ID:                 nodeID,
			WorkflowID:         created.ID,
			Key:                workflow.NodeKey(node),
			Kind:               node.Kind(),
			DisplayName:        workflow.NodeDisplayName(node),
			GroupID:            workflow.NodeGroupID(node),
			SubagentRole:       workflow.NodeSubagentRole(node),
			CompletionMode:     workflow.NodeCompletionMode(node),
			ScriptPath:         workflow.NodeScriptPath(node).String(),
			JoinInputProviders: workflow.NodeJoinInputProviders(node),
			SortOrder:          int64(index * 100),
		})
	}
	if startID == "" || terminalID == "" {
		t.Fatalf("default Workflow nodes = %+v, want Start and Terminal", definition.Nodes)
	}
	agentID := workflow.NodeID("runtime-control-agent-" + created.ID.String())
	nodes = append(nodes, workflowstore.NodeRecord{
		ID:           agentID,
		WorkflowID:   created.ID,
		Key:          "agent",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Agent",
		SubagentRole: "coder",
		SortOrder:    100,
	})
	startGroupID := workflow.TransitionGroupID("runtime-control-start-" + created.ID.String())
	doneGroupID := workflow.TransitionGroupID("runtime-control-done-" + created.ID.String())
	result, err := store.SaveWorkflowGraph(ctx, workflowstore.WorkflowGraphSaveRequest{
		WorkflowID:      created.ID,
		ExpectedVersion: record.Version,
		Nodes:           nodes,
		TransitionGroups: []workflowstore.TransitionGroupRecord{
			{
				ID:           startGroupID,
				WorkflowID:   created.ID,
				SourceNodeID: startID,
				TransitionID: "start",
				DisplayName:  "Start",
			},
			{
				ID:           doneGroupID,
				WorkflowID:   created.ID,
				SourceNodeID: agentID,
				TransitionID: "done",
				DisplayName:  "Done",
			},
		},
		Edges: []workflowstore.EdgeRecord{
			{
				ID:                workflow.EdgeID("runtime-control-start-edge-" + created.ID.String()),
				WorkflowID:        created.ID,
				TransitionGroupID: startGroupID,
				Key:               "start",
				TargetNodeID:      agentID,
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "Continue the retained Workflow Session.",
			},
			{
				ID:                workflow.EdgeID("runtime-control-done-edge-" + created.ID.String()),
				WorkflowID:        created.ID,
				TransitionGroupID: doneGroupID,
				Key:               "done",
				TargetNodeID:      terminalID,
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
				ContextMode:       workflow.ContextModeNewSession,
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !result.Saved {
		t.Fatalf("SaveWorkflowGraph rejected: blockers=%+v validation=%+v", result.Blockers, result.ValidationErrors)
	}
	return created.ID
}
