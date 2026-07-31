package workflowview

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"github.com/google/uuid"
)

type currentNodeViewFixture struct {
	ctx         context.Context
	metadata    *metadata.Store
	store       *workflowstore.Store
	binding     metadata.Binding
	cfg         config.App
	workflowID  runtimeids.WorkflowID
	agentNodeID workflow.NodeID
	authority   *sessionruntime.Authority
	quiescence  *currentNodeViewQuiescence
	board       *Board
	detail      *TaskDetail
	tasks       *TaskList
	activity    *Activity
}

type startedCurrentNodeViewTask struct {
	task        workflowstore.TaskRecord
	currentNode workflow.CurrentNodeReference
}

type currentNodeViewQuestion struct {
	authority *sessionruntime.Authority
	sessionID runtimeids.SessionID
	request   tools.AskQuestionRequest
	handle    sessionruntime.ExecutionHandle
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
	quiescence := &currentNodeViewQuiescence{blocked: map[workflow.TaskID]bool{}}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close fixture authority: %v", err)
		}
	})
	projector := NewTaskProjector()
	board, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projector, authority, quiescence)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	detail, err := NewTaskDetail(metadataStore, definitions, projector, authority, quiescence)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	tasks, err := NewTaskList(metadataStore, definitions, projector, authority)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	activity, err := NewActivity(metadataStore, projector)
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}
	return currentNodeViewFixture{
		ctx:         t.Context(),
		metadata:    metadataStore,
		store:       store,
		binding:     binding,
		cfg:         cfg,
		workflowID:  workflowID,
		agentNodeID: agentNodeID,
		authority:   authority,
		quiescence:  quiescence,
		board:       board,
		detail:      detail,
		tasks:       tasks,
		activity:    activity,
	}
}

type currentNodeViewQuiescence struct {
	blocked map[workflow.TaskID]bool
}

func (q *currentNodeViewQuiescence) CurrentTaskQuiescence(taskIDs []workflow.TaskID) (map[workflow.TaskID]bool, error) {
	result := make(map[workflow.TaskID]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		result[taskID] = !q.blocked[taskID]
	}
	return result, nil
}

func (f currentNodeViewFixture) startTask(t *testing.T, title string) startedCurrentNodeViewTask {
	t.Helper()
	workflowID := f.workflowID
	task, err := f.store.CreateTask(f.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  f.binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      title,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
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
		ProjectID:  f.binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      title,
	})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	return task
}

func (f currentNodeViewFixture) bindCurrentNodeSession(t *testing.T, started startedCurrentNodeViewTask) runtimeids.SessionID {
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
	sessionID := f.bindCurrentNodeSession(t, started)
	authority, plan := f.newAgentAuthority(t)
	request := tools.AskQuestionRequest{
		ID:                     uuid.NewString(),
		StepID:                 uuid.NewString(),
		Question:               "Proceed?",
		Suggestions:            []string{"Yes", "No"},
		RecommendedOptionIndex: 1,
	}
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   f.binding.ProjectID,
		WorkflowID:  f.workflowID,
		CurrentNode: started.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	handle, err := authority.StartAgentExecution(f.ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			_, awaitErr := authority.AwaitPromptResponse(ctx, scope.ID(), request)
			return awaitErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, snapshotErr := authority.CurrentWorkflowTaskExecutionSnapshots()
		if snapshotErr != nil {
			return false
		}
		executions := snapshots[started.task.ID].Executions
		return len(executions) == 1 && executions[0].WaitingQuestion
	}, "timed out waiting for live workflow Question")
	return currentNodeViewQuestion{
		authority: authority,
		sessionID: sessionID,
		request:   request,
		handle:    handle,
	}
}

func (q currentNodeViewQuestion) resolve(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := q.authority.SubmitPromptResponse(q.sessionID, tools.AskQuestionResponse{
		RequestID: q.request.ID,
		Answer:    "Yes",
	}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if _, err := q.handle.Wait(ctx); err != nil {
		t.Fatalf("wait Question execution: %v", err)
	}
}

func (f currentNodeViewFixture) newAgentRuntimePlan(t *testing.T) sessionruntime.AgentRuntimePlan {
	t.Helper()
	settings := f.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  f.cfg.WorkspaceRoot,
		Client:   currentNodeViewLLMClient{},
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
	definition, _, err := store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition initial: %v", err)
	}
	startNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindStart)
	terminalNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal)
	if _, err := store.AddNode(t.Context(), workflowstore.NodeRecord{
		WorkflowID:   created.ID,
		Key:          "agent",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Agent",
		SubagentRole: "coder",
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	definition, _, err = store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after node: %v", err)
	}
	agentNodeID := currentNodeViewNodeID(t, definition, "agent")
	if _, err := store.AddTransitionGroup(t.Context(), workflowstore.TransitionGroupRecord{
		WorkflowID:   created.ID,
		SourceNodeID: startNodeID,
		TransitionID: "start",
		DisplayName:  "Start",
	}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	definition, _, err = store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after start group: %v", err)
	}
	startGroupID := currentNodeViewTransitionGroupID(t, definition, startNodeID, "start")
	if _, err := store.AddEdge(t.Context(), workflowstore.EdgeRecord{
		WorkflowID:        created.ID,
		TransitionGroupID: startGroupID,
		Key:               "start",
		TargetNodeID:      agentNodeID,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Do work.",
	}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(t.Context(), workflowstore.TransitionGroupRecord{
		WorkflowID:   created.ID,
		SourceNodeID: agentNodeID,
		TransitionID: "done",
		DisplayName:  "Done",
	}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	definition, _, err = store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after done group: %v", err)
	}
	doneGroupID := currentNodeViewTransitionGroupID(t, definition, agentNodeID, "done")
	if _, err := store.AddEdge(t.Context(), workflowstore.EdgeRecord{
		WorkflowID:        created.ID,
		TransitionGroupID: doneGroupID,
		Key:               "done",
		TargetNodeID:      terminalNodeID,
		ContextMode:       workflow.ContextModeNewSession,
		RequiresApproval:  requiresApproval,
	}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
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

func currentNodeViewTransitionGroupID(t *testing.T, definition workflow.Definition, sourceNodeID workflow.NodeID, transitionID string) workflow.TransitionGroupID {
	t.Helper()
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID == sourceNodeID && group.TransitionID == workflow.TransitionID(transitionID) {
			return group.ID
		}
	}
	t.Fatalf("workflow transition %q from node %q missing", transitionID, sourceNodeID)
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
