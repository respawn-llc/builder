package workflowview

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestBoardProjectsStartedCurrentNode(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Board task")

	board, err := fixture.board.Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: stringPointer(string(fixture.workflowID)),
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.Get: %v", err)
	}
	agentColumn := workflowViewBoardColumn(t, board, fixture.agentNodeID)
	if agentColumn.TaskCount != 1 {
		t.Fatalf("agent column task count = %d, want 1 Current Node", agentColumn.TaskCount)
	}

	cards, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: string(fixture.workflowID),
		NodeID:     string(fixture.agentNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 {
		t.Fatalf("board cards = %+v, want one Current Node card", cards.Cards)
	}
	card := cards.Cards[0]
	if card.TaskID != string(started.task.ID) ||
		len(card.ActiveNodeIDs) != 1 ||
		card.ActiveNodeIDs[0] != string(fixture.agentNodeID) ||
		card.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		card.Actions.CanStart {
		t.Fatalf("board card = %+v, want started Current Node projection", card)
	}
}

func TestTaskDetailProjectsCurrentNodeAndDirectRetainedSession(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Detail task")
	sessionID := fixture.bindCurrentNodeSession(t, started)

	detail, err := fixture.detail.GetTask(fixture.ctx, string(started.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if detail.Summary.ID != string(started.task.ID) ||
		len(detail.CurrentNodes) != 1 ||
		detail.CurrentNodes[0].NodeID != string(fixture.agentNodeID) ||
		detail.CurrentNodes[0].SessionID == nil ||
		*detail.CurrentNodes[0].SessionID != sessionID.String() ||
		detail.RetainedSessionCount != 1 ||
		detail.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		detail.Actions.CanStart {
		t.Fatalf("task detail = %+v, want Current Node and directly retained session", detail)
	}
}

func TestTaskListProjectsCurrentNodeStatusAndColumn(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "List task")
	projectID := fixture.binding.ProjectID
	workflowID := string(fixture.workflowID)

	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		ColumnKeys:  []string{"agent"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(list.Tasks) != 1 {
		t.Fatalf("task list = %+v, want one started Current Node", list.Tasks)
	}
	item := list.Tasks[0]
	if item.TaskID != string(started.task.ID) ||
		item.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		item.ColumnKeys == nil ||
		len(*item.ColumnKeys) != 1 ||
		(*item.ColumnKeys)[0] != "agent" {
		t.Fatalf("task list item = %+v, want Current Node status and column", item)
	}
}

func TestActivityProjectsOnlyCommentsAndRetainedSessionCreation(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Activity task")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	comment, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Current Node comment", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	activity, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:   string(started.task.ID),
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("Activity.List: %v", err)
	}
	if len(activity.Items) != 2 {
		t.Fatalf("activity items = %+v, want comment and retained session creation", activity.Items)
	}
	items := map[string]serverapi.WorkflowTaskActivityItem{}
	for _, item := range activity.Items {
		items[item.Type] = item
	}
	if commentItem, ok := items["comment"]; !ok ||
		commentItem.Comment == nil ||
		commentItem.Comment.ID != comment.ID ||
		commentItem.SessionStarted != nil {
		t.Fatalf("comment activity = %+v, want comment only", commentItem)
	}
	if sessionItem, ok := items["session_started"]; !ok ||
		sessionItem.SessionStarted == nil ||
		sessionItem.SessionStarted.SessionID != sessionID.String() ||
		sessionItem.Comment != nil {
		t.Fatalf("session activity = %+v, want retained session creation only", sessionItem)
	}
}

func TestAttentionProjectsPendingApprovalAndInterruptedCurrentNode(t *testing.T) {
	approvalFixture := newCurrentNodeViewFixture(t, true)
	approvalStarted := approvalFixture.startTask(t, "Approval task")
	completed, err := approvalFixture.store.CompleteCurrentNode(approvalFixture.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       approvalStarted.currentNode,
		TransitionID: "done",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("CompleteCurrentNode did not create a pending Approval")
	}

	interruptedFixture := newCurrentNodeViewFixture(t, false)
	interruptedStarted := interruptedFixture.startTask(t, "Interrupted task")
	if err := interruptedFixture.store.InterruptCurrentNode(
		interruptedFixture.ctx,
		interruptedStarted.currentNode,
		workflow.CurrentNodeInterruptionReason("server_restart"),
		workflow.CurrentNodeInterruptionDetail{Code: "restart", Fields: map[string]string{"error": "process stopped"}},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}

	approvalAttention := approvalFixture.attention(t)
	approvals, err := approvalAttention.ListTask(approvalFixture.ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(approvalStarted.task.ID)})
	if err != nil {
		t.Fatalf("Attention.ListTask approval: %v", err)
	}
	if len(approvals.Items) != 1 {
		t.Fatalf("approval attention = %+v, want one approval", approvals.Items)
	}
	approval := approvals.Items[0]
	if approval.Kind != "approval" ||
		approval.ApprovalID == nil ||
		*approval.ApprovalID != completed.PendingApproval.ID.String() ||
		approval.ApprovalSnapshot == nil ||
		approval.CurrentNode != nil {
		t.Fatalf("approval attention item = %+v, want pending Approval identity", approval)
	}

	interruptedAttention := interruptedFixture.attention(t)
	interruptions, err := interruptedAttention.List(interruptedFixture.ctx, serverapi.WorkflowAttentionListRequest{PageSize: 20})
	if err != nil {
		t.Fatalf("Attention.List interrupted: %v", err)
	}
	if len(interruptions.Items) != 1 {
		t.Fatalf("interrupted attention = %+v, want one interrupted Current Node", interruptions.Items)
	}
	interrupted := interruptions.Items[0]
	if interrupted.Kind != "interrupted" ||
		interrupted.TaskID != string(interruptedStarted.task.ID) ||
		interrupted.CurrentNode == nil ||
		interrupted.CurrentNode.NodeID != string(interruptedFixture.agentNodeID) ||
		interrupted.ApprovalID != nil ||
		interrupted.QuestionID != nil {
		t.Fatalf("interrupted attention item = %+v, want Current Node identity", interrupted)
	}
	taskInterruptions, err := interruptedAttention.ListTask(interruptedFixture.ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(interruptedStarted.task.ID)})
	if err != nil {
		t.Fatalf("Attention.ListTask interrupted: %v", err)
	}
	if len(taskInterruptions.Items) != 1 || taskInterruptions.Items[0].ID != interrupted.ID {
		t.Fatalf("task interrupted attention = %+v, want exact Current Node attention", taskInterruptions.Items)
	}
}

type currentNodeViewFixture struct {
	ctx         context.Context
	metadata    *metadata.Store
	store       *workflowstore.Store
	binding     metadata.Binding
	cfg         config.App
	workflowID  workflow.WorkflowID
	agentNodeID workflow.NodeID
	authority   *sessionruntime.Authority
	board       *Board
	detail      *TaskDetail
	tasks       *TaskList
	activity    *Activity
}

type startedCurrentNodeViewTask struct {
	task        workflowstore.TaskRecord
	currentNode workflow.CurrentNodeReference
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
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	projector := NewTaskProjector()
	board, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projector, authority)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	detail, err := NewTaskDetail(metadataStore, definitions, projector, authority)
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
		board:       board,
		detail:      detail,
		tasks:       tasks,
		activity:    activity,
	}
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
	if _, err := f.store.BindSessionToCurrentNode(f.ctx, workflowstore.TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.currentNode,
		AssociatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	return sessionID
}

func (f currentNodeViewFixture) attention(t *testing.T) *Attention {
	t.Helper()
	attention, err := NewAttention(f.metadata, mustDefinitionProjection(t, f.store), f.authority, emptyCurrentNodeViewPrompts{})
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}
	return attention
}

func currentNodeViewWorkflow(t *testing.T, store *workflowstore.Store, requiresApproval bool) workflow.WorkflowID {
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

type emptyCurrentNodeViewPrompts struct{}

func (emptyCurrentNodeViewPrompts) ListPendingPrompts(string) ([]PendingPromptSnapshot, error) {
	return []PendingPromptSnapshot{}, nil
}
