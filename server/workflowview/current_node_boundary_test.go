package workflowview

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
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

func TestBoardListNodeCardsPaginatesDeterministically(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Board A"),
		fixture.startTask(t, "Board B"),
		fixture.startTask(t, "Board C"),
	}
	for _, task := range started {
		fixture.setTaskUpdatedAt(t, task.task.ID, 1_000)
	}
	want := []string{
		string(started[0].task.ID),
		string(started[1].task.ID),
		string(started[2].task.ID),
	}
	sort.Sort(sort.Reverse(sort.StringSlice(want)))

	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: string(fixture.workflowID),
		NodeID:     string(fixture.agentNodeID),
		PageSize:   1,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	}
	var got []string
	var firstPage serverapi.WorkflowBoardNodeCardsListResponse
	var secondPage serverapi.WorkflowBoardNodeCardsListResponse
	for pageIndex := 0; ; pageIndex++ {
		page, err := fixture.board.ListNodeCards(fixture.ctx, request)
		if err != nil {
			t.Fatalf("Board.ListNodeCards page %d: %v", pageIndex, err)
		}
		if len(page.Cards) != 1 {
			t.Fatalf("board page %d cards = %+v, want one", pageIndex, page.Cards)
		}
		got = append(got, page.Cards[0].TaskID)
		if pageIndex == 0 {
			firstPage = page
			if page.PreviousPageToken != nil || page.NextPageToken == nil {
				t.Fatalf("first board page tokens = previous %v next %v", page.PreviousPageToken, page.NextPageToken)
			}
		}
		if pageIndex == 1 {
			secondPage = page
			if page.PreviousPageToken == nil {
				t.Fatal("second board page has no newer-page token")
			}
		}
		if page.NextPageToken == nil {
			break
		}
		request.PageToken = page.NextPageToken
	}
	if !equalStrings(got, want) {
		t.Fatalf("board pagination order = %v, want %v", got, want)
	}
	request.PageToken = secondPage.PreviousPageToken
	newer, err := fixture.board.ListNodeCards(fixture.ctx, request)
	if err != nil {
		t.Fatalf("Board.ListNodeCards newer: %v", err)
	}
	if len(newer.Cards) != 1 || newer.Cards[0].TaskID != firstPage.Cards[0].TaskID {
		t.Fatalf("newer board page = %+v, want first page task %q", newer.Cards, firstPage.Cards[0].TaskID)
	}
	request.PageToken = firstPage.NextPageToken
	request.LabelFilter = serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindUnlabeled}
	if _, err := fixture.board.ListNodeCards(fixture.ctx, request); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("board token replay with changed filter error = %v, want invalid page token", err)
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

func TestTaskListPaginatesStableSortAndRejectsScopeReplay(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "List A"),
		fixture.startTask(t, "List B"),
		fixture.startTask(t, "List C"),
	}
	for _, task := range started {
		fixture.setTaskUpdatedAt(t, task.task.ID, 2_000)
	}
	want := []string{
		string(started[0].task.ID),
		string(started[1].task.ID),
		string(started[2].task.ID),
	}
	sort.Strings(want)
	projectID := fixture.binding.ProjectID
	workflowID := string(fixture.workflowID)
	request := serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
		Sort: []serverapi.WorkflowTaskListSort{{
			Field:     serverapi.WorkflowTaskListSortFieldUpdated,
			Direction: serverapi.WorkflowTaskListSortDirectionDesc,
		}},
		PageSize: 1,
	}
	var got []string
	var firstToken string
	for pageIndex := 0; ; pageIndex++ {
		page, err := fixture.tasks.List(fixture.ctx, request)
		if err != nil {
			t.Fatalf("TaskList.List page %d: %v", pageIndex, err)
		}
		if len(page.Tasks) != 1 {
			t.Fatalf("task list page %d = %+v, want one task", pageIndex, page.Tasks)
		}
		got = append(got, page.Tasks[0].TaskID)
		if pageIndex == 0 {
			if page.NextPageToken == nil {
				t.Fatal("first task-list page has no continuation token")
			}
			firstToken = *page.NextPageToken
		}
		if page.NextPageToken == nil {
			break
		}
		request.PageToken = *page.NextPageToken
	}
	if !equalStrings(got, want) {
		t.Fatalf("task-list pagination order = %v, want %v", got, want)
	}
	request.PageToken = firstToken
	request.StatusKinds = []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive}
	if _, err := fixture.tasks.List(fixture.ctx, request); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("task-list token replay with changed filter error = %v, want invalid page token", err)
	}
}

func TestTaskListDefaultSortUsesCurrentStatusBeforeActivity(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	active := fixture.startTask(t, "Active")
	interrupted := fixture.startTask(t, "Interrupted")
	if err := fixture.store.InterruptCurrentNode(
		fixture.ctx,
		interrupted.currentNode,
		workflow.CurrentNodeInterruptionReason("server_restart"),
		workflow.CurrentNodeInterruptionDetail{Code: "restart"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	backlog := fixture.createBacklogTask(t, "Backlog")
	fixture.setTaskUpdatedAt(t, active.task.ID, 3_000)
	fixture.setTaskUpdatedAt(t, interrupted.task.ID, 1_000)
	fixture.setTaskUpdatedAt(t, backlog.ID, 2_000)
	projectID := fixture.binding.ProjectID
	workflowID := string(fixture.workflowID)

	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	got := make([]serverapi.WorkflowTaskStatusKind, 0, len(list.Tasks))
	for _, task := range list.Tasks {
		got = append(got, task.Status.Kind)
	}
	want := []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindInterrupted,
		serverapi.WorkflowTaskStatusKindBacklog,
		serverapi.WorkflowTaskStatusKindActive,
	}
	if !equalStatusKinds(got, want) {
		t.Fatalf("default task-list status order = %v, want %v", got, want)
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

func TestActivityOrdersAndPaginatesCanonicalSources(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Activity pagination")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	older, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Older", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment older: %v", err)
	}
	newer, err := fixture.store.AddComment(fixture.ctx, started.task.ID, "Newer", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment newer: %v", err)
	}
	fixture.setSessionCreatedAt(t, sessionID, 1_000)
	fixture.setCommentUpdatedAt(t, older.ID, 2_000)
	fixture.setCommentUpdatedAt(t, newer.ID, 3_000)

	first, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:   string(started.task.ID),
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("Activity.List first: %v", err)
	}
	if len(first.Items) != 2 ||
		first.Items[0].Comment == nil ||
		first.Items[0].Comment.ID != newer.ID ||
		first.Items[1].Comment == nil ||
		first.Items[1].Comment.ID != older.ID ||
		first.NextPageToken == "" {
		t.Fatalf("first activity page = %+v, token %q", first.Items, first.NextPageToken)
	}
	second, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:    string(started.task.ID),
		PageSize:  2,
		PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("Activity.List second: %v", err)
	}
	if len(second.Items) != 1 ||
		second.Items[0].SessionStarted == nil ||
		second.Items[0].SessionStarted.SessionID != sessionID.String() ||
		second.NextPageToken != "" {
		t.Fatalf("second activity page = %+v, token %q", second.Items, second.NextPageToken)
	}
	if _, err := fixture.activity.List(fixture.ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:    string(started.task.ID),
		PageSize:  2,
		PageToken: "invalid",
	}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("invalid activity page token error = %v", err)
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

func TestAttentionPaginatesDurableCurrentStateAndScopesTaskQuery(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, true)
	approvalTask := fixture.startTask(t, "Approval")
	completed, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       approvalTask.currentNode,
		TransitionID: "done",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("pending Approval is missing")
	}
	firstInterrupted := fixture.startTask(t, "Interrupted first")
	secondInterrupted := fixture.startTask(t, "Interrupted second")
	for _, task := range []startedCurrentNodeViewTask{firstInterrupted, secondInterrupted} {
		if err := fixture.store.InterruptCurrentNode(
			fixture.ctx,
			task.currentNode,
			workflow.CurrentNodeInterruptionReason("server_restart"),
			workflow.CurrentNodeInterruptionDetail{Code: "restart"},
		); err != nil {
			t.Fatalf("InterruptCurrentNode %s: %v", task.task.ID, err)
		}
	}
	fixture.setApprovalCreatedAt(t, completed.PendingApproval.ID.String(), 3_000)
	fixture.setCurrentNodeInterruptedAt(t, firstInterrupted.currentNode, 2_000)
	fixture.setCurrentNodeInterruptedAt(t, secondInterrupted.currentNode, 1_000)
	attention := fixture.attention(t)

	first, err := attention.List(fixture.ctx, serverapi.WorkflowAttentionListRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("Attention.List first: %v", err)
	}
	if len(first.Items) != 2 ||
		first.Items[0].Kind != "approval" ||
		first.Items[1].TaskID != string(firstInterrupted.task.ID) ||
		first.NextPageToken == "" {
		t.Fatalf("first attention page = %+v token %q", first.Items, first.NextPageToken)
	}
	second, err := attention.List(fixture.ctx, serverapi.WorkflowAttentionListRequest{
		PageSize:  2,
		PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("Attention.List second: %v", err)
	}
	if len(second.Items) != 1 ||
		second.Items[0].TaskID != string(secondInterrupted.task.ID) ||
		second.NextPageToken != "" {
		t.Fatalf("second attention page = %+v token %q", second.Items, second.NextPageToken)
	}
	scoped, err := attention.ListTask(fixture.ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(secondInterrupted.task.ID),
	})
	if err != nil {
		t.Fatalf("Attention.ListTask: %v", err)
	}
	if len(scoped.Items) != 1 || scoped.Items[0].TaskID != string(secondInterrupted.task.ID) {
		t.Fatalf("task-scoped attention = %+v", scoped.Items)
	}
	if _, err := attention.List(fixture.ctx, serverapi.WorkflowAttentionListRequest{
		PageSize:  2,
		PageToken: "invalid",
	}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("invalid attention page token error = %v", err)
	}
}

func TestAttentionAndDetailProjectLiveQuestionFromExactScope(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Question")
	unrelated := fixture.startTask(t, "Unrelated")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	authority, plan := fixture.newAgentAuthority(t)
	askID := uuid.NewString()
	request := tools.AskQuestionRequest{
		ID:                     askID,
		StepID:                 uuid.NewString(),
		Question:               "Proceed?",
		Suggestions:            []string{"Yes", "No"},
		RecommendedOptionIndex: 1,
	}
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		CurrentNode: started.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	handle, err := authority.StartAgentExecution(fixture.ctx, sessionruntime.AgentExecutionRequest{
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
		_, err := authority.ResolvePendingWorkflowPrompt(started.currentNode.TaskID, askID)
		return err == nil
	}, "timed out waiting for live workflow Question")
	prompts := currentNodeViewPrompts{bySession: map[string][]PendingPromptSnapshot{
		sessionID.String(): {{
			ID:                     askID,
			CreatedAt:              time.UnixMilli(4_000).UTC(),
			Question:               request.Question,
			Suggestions:            request.Suggestions,
			RecommendedOptionIndex: intPointer(request.RecommendedOptionIndex),
		}},
	}}
	attention, err := NewAttention(
		fixture.metadata,
		mustDefinitionProjection(t, fixture.store),
		authority,
		prompts,
	)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}
	taskAttention, err := attention.ListTask(fixture.ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(started.task.ID),
	})
	if err != nil {
		t.Fatalf("Attention.ListTask: %v", err)
	}
	if len(taskAttention.Items) != 1 ||
		taskAttention.Items[0].Kind != "question" ||
		taskAttention.Items[0].QuestionID == nil ||
		*taskAttention.Items[0].QuestionID != askID ||
		taskAttention.Items[0].SessionID == nil ||
		*taskAttention.Items[0].SessionID != sessionID.String() ||
		taskAttention.Items[0].CurrentNode == nil ||
		taskAttention.Items[0].CurrentNode.NodeID != string(fixture.agentNodeID) {
		t.Fatalf("question attention = %+v", taskAttention.Items)
	}
	unrelatedAttention, err := attention.ListTask(fixture.ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(unrelated.task.ID),
	})
	if err != nil {
		t.Fatalf("Attention.ListTask unrelated: %v", err)
	}
	if len(unrelatedAttention.Items) != 0 {
		t.Fatalf("unrelated task attention = %+v, want none", unrelatedAttention.Items)
	}
	detail, err := NewTaskDetail(fixture.metadata, mustDefinitionProjection(t, fixture.store), NewTaskProjector(), authority)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	projected, err := detail.GetTask(fixture.ctx, string(started.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if projected.Status.Kind != serverapi.WorkflowTaskStatusKindWaitingQuestion ||
		projected.AttentionCount != 1 ||
		len(projected.LiveSessionIDs) != 1 ||
		projected.LiveSessionIDs[0] != sessionID.String() {
		t.Fatalf("question task detail = %+v", projected)
	}
	if err := authority.SubmitPromptResponse(sessionID, tools.AskQuestionResponse{
		RequestID: askID,
		Answer:    "Yes",
	}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if _, err := handle.Wait(fixture.ctx); err != nil {
		t.Fatalf("wait Question execution: %v", err)
	}
}

func TestTaskDetailProjectsDurableCurrentStateMatrix(t *testing.T) {
	t.Run("interrupted", func(t *testing.T) {
		fixture := newCurrentNodeViewFixture(t, false)
		started := fixture.startTask(t, "Interrupted")
		if err := fixture.store.InterruptCurrentNode(
			fixture.ctx,
			started.currentNode,
			workflow.CurrentNodeInterruptionReason("server_restart"),
			workflow.CurrentNodeInterruptionDetail{Code: "restart"},
		); err != nil {
			t.Fatalf("InterruptCurrentNode: %v", err)
		}
		detail, err := fixture.detail.GetTask(fixture.ctx, string(started.task.ID))
		if err != nil {
			t.Fatalf("TaskDetail.GetTask: %v", err)
		}
		if detail.Status.Kind != serverapi.WorkflowTaskStatusKindInterrupted ||
			detail.AttentionCount != 1 ||
			!detail.Actions.CanResume {
			t.Fatalf("interrupted detail = %+v", detail)
		}
	})

	t.Run("waiting approval", func(t *testing.T) {
		fixture := newCurrentNodeViewFixture(t, true)
		started := fixture.startTask(t, "Approval")
		completed, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       started.currentNode,
			TransitionID: "done",
		})
		if err != nil {
			t.Fatalf("CompleteCurrentNode: %v", err)
		}
		if completed.PendingApproval == nil {
			t.Fatal("pending Approval is missing")
		}
		detail, err := fixture.detail.GetTask(fixture.ctx, string(started.task.ID))
		if err != nil {
			t.Fatalf("TaskDetail.GetTask: %v", err)
		}
		if detail.Status.Kind != serverapi.WorkflowTaskStatusKindWaitingApproval ||
			detail.AttentionCount != 1 ||
			len(detail.CurrentNodes) != 1 ||
			detail.CurrentNodes[0].NodeID != string(fixture.agentNodeID) {
			t.Fatalf("waiting-Approval detail = %+v", detail)
		}
	})

	t.Run("done", func(t *testing.T) {
		fixture := newCurrentNodeViewFixture(t, false)
		started := fixture.startTask(t, "Done")
		if _, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       started.currentNode,
			TransitionID: "done",
		}); err != nil {
			t.Fatalf("CompleteCurrentNode: %v", err)
		}
		detail, err := fixture.detail.GetTask(fixture.ctx, string(started.task.ID))
		if err != nil {
			t.Fatalf("TaskDetail.GetTask: %v", err)
		}
		if detail.Status.Kind != serverapi.WorkflowTaskStatusKindDone ||
			!detail.Summary.Done ||
			detail.AttentionCount != 0 {
			t.Fatalf("done detail = %+v", detail)
		}
	})
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
	if _, err := f.store.BindSessionToCurrentNode(f.ctx, workflowstore.TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.currentNode,
		AssociatedAt: time.Now().UTC(),
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
	return authority, plan
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
