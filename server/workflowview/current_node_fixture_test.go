package workflowview

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/internal/testharness/workflowfixture"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"

	"github.com/google/uuid"
)

type currentNodeViewFixture struct {
	ctx               context.Context
	metadata          *metadata.Store
	store             *workflowstore.Store
	binding           metadata.Binding
	cfg               config.App
	workflowID        runtimeids.WorkflowID
	agentNodeID       workflow.NodeID
	authority         *sessionruntime.Authority
	dependencyCounter *TaskDependencyCounter
	quiescence        *currentNodeViewStatusObservationSource
	projection        *TaskStatusProjection
	board             *Board
	detail            *TaskDetail
	tasks             *TaskList
	search            *TaskSearch
	activity          *Activity
}

type currentNodeViewStatusObservationSource struct {
	authority *sessionruntime.Authority
	blocked   map[workflow.TaskID]bool
}

func (s currentNodeViewStatusObservationSource) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	executions, err := s.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return workflowexecution.WorkflowTaskExecutionObservation{}, err
	}
	quiescence := make(map[workflow.TaskID]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		quiescence[taskID] = !s.blocked[taskID]
	}
	return workflowexecution.WorkflowTaskExecutionObservation{
		Executions: executions,
		Quiescence: quiescence,
	}, nil
}

type startedCurrentNodeViewTask struct {
	task        workflowstore.TaskRecord
	currentNode workflow.CurrentNodeReference
}

type currentNodeViewPrompt struct {
	authority *sessionruntime.Authority
	sessionID runtimeids.SessionID
	request   tools.AskQuestionRequest
	handle    sessionruntime.ExecutionHandle
}

type currentNodeViewQuestion struct {
	currentNodeViewPrompt
}

func workflowViewQuestionAnswer(answer string) tools.AskQuestionAnswer {
	return tools.AskQuestionAnswer{Freeform: &answer}
}

func workflowViewApprovalRequest() tools.AskQuestionRequest {
	return tools.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Approve this workflow action?",
		Approval: true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{
			{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: tools.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
}

func newCurrentNodeViewFixture(t *testing.T, requiresApproval bool) currentNodeViewFixture {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(t.Context(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := currentNodeViewWorkflow(t, store, requiresApproval)
	if _, err := store.LinkWorkflow(t.Context(), binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	definitions, err := NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	definition, _, err := store.GetDefinition(t.Context(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentNodeID := currentNodeViewNodeID(t, definition, "agent")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	quiescence := &currentNodeViewStatusObservationSource{
		authority: authority,
		blocked:   map[workflow.TaskID]bool{},
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close fixture authority: %v", err)
		}
	})
	projector := NewTaskProjector()
	projection, err := NewTaskStatusProjection(
		store,
		projector,
		quiescence,
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	dependencyCounter, err := NewTaskDependencyCounter(metadataStore)
	if err != nil {
		t.Fatalf("NewTaskDependencyCounter: %v", err)
	}
	dependencies, err := NewTaskDependencies(metadataStore, projection, dependencyCounter)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	board, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projection)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	detail, err := NewTaskDetail(metadataStore, projection, dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	tasks, err := NewTaskList(metadataStore, definitions, projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, projection)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	activity, err := NewActivity(metadataStore, projector)
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}
	return currentNodeViewFixture{
		ctx:               t.Context(),
		metadata:          metadataStore,
		store:             store,
		binding:           binding,
		cfg:               cfg,
		workflowID:        workflowID,
		agentNodeID:       agentNodeID,
		authority:         authority,
		dependencyCounter: dependencyCounter,
		quiescence:        quiescence,
		projection:        projection,
		board:             board,
		detail:            detail,
		tasks:             tasks,
		search:            search,
		activity:          activity,
	}
}

func (f currentNodeViewFixture) startTask(t *testing.T, title string) startedCurrentNodeViewTask {
	t.Helper()
	workflowID := f.workflowID
	task, err := f.store.CreateTask(f.ctx, workflowstore.CreateTaskRequest{
		ProjectID:         f.binding.ProjectID,
		WorkflowID:        &workflowID,
		Title:             title,
		SourceWorkspaceID: f.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return f.startExistingTask(t, task)
}

func (f currentNodeViewFixture) startExistingTask(t *testing.T, task workflowstore.TaskRecord) startedCurrentNodeViewTask {
	t.Helper()
	started, err := f.store.StartTask(f.ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one Current Node", started.Mutation)
	}
	return startedCurrentNodeViewTask{task: task, currentNode: started.Mutation.Created[0].Reference}
}

func (f currentNodeViewFixture) createBacklogTask(t *testing.T, title string) workflowstore.TaskRecord {
	t.Helper()
	workflowID := f.workflowID
	task, err := f.store.CreateTask(f.ctx, workflowstore.CreateTaskRequest{
		ProjectID:         f.binding.ProjectID,
		WorkflowID:        &workflowID,
		Title:             title,
		SourceWorkspaceID: f.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	return task
}

func (f currentNodeViewFixture) bindCurrentNodeSession(t *testing.T, started startedCurrentNodeViewTask) runtimeids.SessionID {
	t.Helper()
	sessionID := f.newCurrentNodeViewSession(t)
	if _, err := f.store.BindSessionToCurrentNode(f.ctx, workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.currentNode,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	return sessionID
}

func (f currentNodeViewFixture) newCurrentNodeViewSession(t *testing.T) runtimeids.SessionID {
	t.Helper()
	sessionRoot := filepath.Join(f.cfg.PersistenceRoot, "projects", f.binding.ProjectID, "sessions")
	sessionStore, err := session.Create(
		sessionRoot,
		filepath.Base(f.cfg.WorkspaceRoot),
		f.cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		f.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("session.EnsureDurable: %v", err)
	}
	if err := sessionStore.SetName("Current Node session"); err != nil {
		t.Fatalf("session.SetName: %v", err)
	}
	if _, err := f.metadata.ResolvePersistedSession(f.ctx, sessionStore.Meta().SessionID); err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func (f currentNodeViewFixture) setTaskUpdatedAt(t *testing.T, taskID workflow.TaskID, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE tasks SET updated_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		string(taskID),
	); err != nil {
		t.Fatalf("set task updated at: %v", err)
	}
}

func (f currentNodeViewFixture) setSessionCreatedAt(t *testing.T, sessionID runtimeids.SessionID, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE sessions SET created_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		sessionID.String(),
	); err != nil {
		t.Fatalf("set session created at: %v", err)
	}
}

func (f currentNodeViewFixture) setCommentUpdatedAt(t *testing.T, commentID string, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE task_comments SET updated_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		commentID,
	); err != nil {
		t.Fatalf("set comment updated at: %v", err)
	}
}

func (f currentNodeViewFixture) setApprovalCreatedAt(t *testing.T, approvalID string, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE task_pending_approvals SET created_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		approvalID,
	); err != nil {
		t.Fatalf("set Approval created at: %v", err)
	}
}

func (f currentNodeViewFixture) setCurrentNodeInterruptedAt(
	t *testing.T,
	reference workflow.CurrentNodeReference,
	unixMs int64,
) {
	t.Helper()
	branchKey, branchScoped := reference.TransitionBranchKey()
	var err error
	if branchScoped {
		_, err = f.metadata.DB().ExecContext(
			f.ctx,
			`UPDATE task_current_nodes
SET interrupted_at_unix_ms = ?
WHERE task_id = ? AND node_id = ? AND transition_branch_key = ?`,
			unixMs,
			string(reference.TaskID),
			string(reference.NodeID),
			string(branchKey),
		)
	} else {
		_, err = f.metadata.DB().ExecContext(
			f.ctx,
			`UPDATE task_current_nodes
SET interrupted_at_unix_ms = ?
WHERE task_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
			unixMs,
			string(reference.TaskID),
			string(reference.NodeID),
		)
	}
	if err != nil {
		t.Fatalf("set Current Node interrupted at: %v", err)
	}
}

func (f currentNodeViewFixture) newAgentAuthority(t *testing.T) (*sessionruntime.Authority, sessionruntime.AgentRuntimePlan) {
	t.Helper()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: f.cfg.PersistenceRoot,
		StoreOptions:    f.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close Agent authority: %v", err)
		}
	})
	return authority, f.newAgentRuntimePlan(t)
}

func (f currentNodeViewFixture) startCurrentNodeQuestion(t *testing.T, started startedCurrentNodeViewTask) currentNodeViewQuestion {
	t.Helper()
	request := tools.AskQuestionRequest{
		ID:                     uuid.NewString(),
		StepID:                 uuid.NewString(),
		Question:               "Proceed?",
		Suggestions:            []string{"Yes", "No"},
		RecommendedOptionIndex: 1,
	}
	return currentNodeViewQuestion{currentNodeViewPrompt: f.startCurrentNodePrompt(t, started, request)}
}

func (f currentNodeViewFixture) startCurrentNodePrompt(
	t *testing.T,
	started startedCurrentNodeViewTask,
	request tools.AskQuestionRequest,
) currentNodeViewPrompt {
	t.Helper()
	authority, plan := f.newAgentAuthority(t)
	sessionID := f.bindCurrentNodeSession(t, started)
	workflowRef := sessionruntime.WorkflowExecutionRef{
		ProjectID:   f.binding.ProjectID,
		WorkflowID:  f.workflowID,
		CurrentNode: started.currentNode,
	}
	handle, err := authority.StartAgentExecution(f.ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow: &sessionruntime.WorkflowAgentExecution{
			Reference: workflowRef,
			Config: &workflowruntime.CurrentNodeExecutionConfig{
				Instructions: workflowruntime.TaskInstructions{
					CurrentNode: workflowRef.CurrentNode,
					WorkflowID:  workflowRef.WorkflowID,
				},
			},
		},
		Resource: sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			_, awaitErr := authority.AwaitPromptResolution(ctx, scope.ID(), request)
			return awaitErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Stop(context.Background()); err != nil {
			t.Errorf("stop live workflow prompt execution: %v", err)
		}
	})
	pendingKind := sessionruntime.PendingPromptKindQuestion
	if request.Approval {
		pendingKind = sessionruntime.PendingPromptKindSessionApproval
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, snapshotErr := authority.CurrentWorkflowTaskExecutionSnapshots()
		if snapshotErr != nil {
			return false
		}
		executions := snapshots[started.task.ID].Executions
		return len(executions) == 1 && executions[0].HasPendingPromptKind(pendingKind)
	}, "timed out waiting for live workflow prompt")
	return currentNodeViewPrompt{
		authority: authority,
		sessionID: sessionID,
		request:   request,
		handle:    handle,
	}
}

func (p currentNodeViewPrompt) resolve(t *testing.T, ctx context.Context, resolution tools.AskQuestionResolution) {
	t.Helper()
	if err := resolveWorkflowViewPrompt(p.authority, p.sessionID, p.request, resolution); err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	if _, err := p.handle.Wait(ctx); err != nil {
		t.Fatalf("wait prompt execution: %v", err)
	}
}

func resolveWorkflowViewPrompt(
	authority *sessionruntime.Authority,
	sessionID runtimeids.SessionID,
	request tools.AskQuestionRequest,
	resolution tools.AskQuestionResolution,
) error {
	stepID, err := runtimeids.ParseStepID(request.StepID)
	if err != nil {
		return err
	}
	var payload sessionruntime.PromptAnswerPayload
	switch value := resolution.(type) {
	case tools.AskQuestionAnswer:
		payload = sessionruntime.PromptQuestionAnswerCommand{Answer: value}
	case tools.AskQuestionApproval:
		payload = sessionruntime.PromptApprovalAnswerCommand{Answer: value}
	default:
		return fmt.Errorf("unsupported prompt resolution %T", resolution)
	}
	_, err = authority.ResolvePromptBatch(context.Background(), sessionID, stepID, []sessionruntime.PromptAnswerCommand{{
		PromptID: clientui.PromptID(request.ID),
		Payload:  payload,
	}})
	return err
}

func (q currentNodeViewQuestion) resolve(t *testing.T, ctx context.Context) {
	t.Helper()
	q.currentNodeViewPrompt.resolve(t, ctx, workflowViewQuestionAnswer("Yes"))
}

func (f currentNodeViewFixture) newAgentRuntimePlan(t *testing.T) sessionruntime.AgentRuntimePlan {
	t.Helper()
	settings := f.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:              settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(f.cfg.WorkspaceRoot, f.cfg.WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client: currentNodeViewLLMClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	return plan
}

func (f currentNodeViewFixture) attention(t *testing.T) *Attention {
	t.Helper()
	attention, err := NewAttention(f.metadata, mustDefinitionProjection(t, f.store), f.authority, emptyCurrentNodeViewPrompts{})
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}
	return attention
}

func currentNodeViewWorkflow(t *testing.T, store *workflowstore.Store, requiresApproval bool) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(t.Context(), workflowstore.CreateWorkflowRequest{Name: "Current Node workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	agentNodeID := workflow.NodeID(runtimeids.NewGraphEntityID())
	startGroupID := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	doneGroupID := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
	startEdgeID := workflow.EdgeID(runtimeids.NewGraphEntityID())
	doneEdgeID := workflow.EdgeID(runtimeids.NewGraphEntityID())
	workflowfixture.SaveStoreGraph(t, t.Context(), store, created.ID, func(definition workflow.Definition, request *workflowstore.WorkflowGraphSaveRequest) {
		startNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindStart)
		terminalNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal)
		request.Nodes = append(request.Nodes, workflowstore.NodeRecord{
			ID: agentNodeID, WorkflowID: created.ID, Key: "agent",
			Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder",
		})
		request.TransitionGroups = append(request.TransitionGroups,
			workflowstore.TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: startNodeID, TransitionID: "start", DisplayName: "Start"},
			workflowstore.TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: agentNodeID, TransitionID: "done", DisplayName: "Done"},
		)
		request.Edges = append(request.Edges,
			workflowstore.EdgeRecord{
				ID: startEdgeID, WorkflowID: created.ID,
				TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentNodeID,
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
				ContextMode:       workflow.ContextModeNewSession, PromptTemplate: "Do work.",
			},
			workflowstore.EdgeRecord{
				ID: doneEdgeID, WorkflowID: created.ID,
				TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: terminalNodeID,
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
				ContextMode:       workflow.ContextModeNewSession, RequiresApproval: requiresApproval,
			},
		)
	})
	return created.ID
}

func currentNodeViewNodeID(t *testing.T, definition workflow.Definition, key string) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if workflow.NodeKey(node) == workflow.ModelKey(key) {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatalf("workflow node key %q missing", key)
	return ""
}

func currentNodeViewNodeIDByKind(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatalf("workflow node kind %q missing", kind)
	return ""
}

func workflowViewBoardColumn(t *testing.T, board serverapi.WorkflowBoard, nodeID workflow.NodeID) serverapi.WorkflowBoardColumn {
	t.Helper()
	for _, column := range board.Columns {
		if column.Node.NodeID == string(nodeID) {
			return column
		}
	}
	t.Fatalf("board column for node %q missing", nodeID)
	return serverapi.WorkflowBoardColumn{}
}

func mustDefinitionProjection(t *testing.T, store *workflowstore.Store) *DefinitionProjection {
	t.Helper()
	projection, err := NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	return projection
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStatusKinds(left, right []serverapi.WorkflowTaskStatusKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mustOpenCurrentNodeViewSessionDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	return descriptor
}

type currentNodeViewLLMClient struct{}

func (currentNodeViewLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (currentNodeViewLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type currentNodeViewPrompts struct {
	bySession map[string][]PendingPromptSnapshot
}

func (p currentNodeViewPrompts) ListPendingPrompts(sessionID string) ([]PendingPromptSnapshot, error) {
	return append([]PendingPromptSnapshot(nil), p.bySession[sessionID]...), nil
}

type emptyCurrentNodeViewPrompts struct{}

func (emptyCurrentNodeViewPrompts) ListPendingPrompts(string) ([]PendingPromptSnapshot, error) {
	return []PendingPromptSnapshot{}, nil
}
