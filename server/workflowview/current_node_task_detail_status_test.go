package workflowview

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestCurrentNodeStatusProjectionCrossSurfaceStableQuestion(t *testing.T) {
	surfaces := newRealTaskStatusSurfaces(t, false)
	fixture := surfaces.fixture
	started := fixture.startTask(t, "Cross surface question")
	question := fixture.startCurrentNodeQuestionOnAuthority(t, started, fixture.authority, fixture.newAgentRuntimePlan(t))
	defer question.resolve(t, fixture.ctx)

	detail, err := surfaces.detail.GetTask(fixture.ctx, string(started.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 20
	listed, err := surfaces.list.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("TaskList tasks = %+v, want one", listed.Tasks)
	}
	cards, err := surfaces.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   projectID,
		WorkflowID:  workflowID,
		NodeID:      string(fixture.agentNodeID),
		PageSize:    20,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 {
		t.Fatalf("Board cards = %+v, want one", cards.Cards)
	}
	searchResponse, err := surfaces.search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       "Cross surface question",
		Context:     serverapi.TaskSearchDefaultContext,
		ProjectIDs:  []string{projectID},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("TaskSearch.Search: %v", err)
	}
	if len(searchResponse.Groups) != 1 {
		t.Fatalf("TaskSearch groups = %+v, want one", searchResponse.Groups)
	}
	if !reflect.DeepEqual(detail.Status, listed.Tasks[0].Status) ||
		!reflect.DeepEqual(detail.Status, cards.Cards[0].Status) ||
		!reflect.DeepEqual(detail.Status, searchResponse.Groups[0].Status) {
		t.Fatalf("cross-surface status mismatch: detail=%+v list=%+v board=%+v search=%+v",
			detail.Status, listed.Tasks[0].Status, cards.Cards[0].Status, searchResponse.Groups[0].Status)
	}
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindWaitingQuestion ||
		detail.AttentionCount != 1 ||
		detail.Actions.CanInterrupt ||
		len(detail.LiveSessionIDs) != 1 ||
		detail.LiveSessionIDs[0] != question.sessionID.String() {
		t.Fatalf("cross-surface detail = %+v", detail)
	}
	if !reflect.DeepEqual(detail.Actions, cards.Cards[0].Actions) ||
		cards.Cards[0].TaskID != string(started.task.ID) ||
		listed.Tasks[0].TaskID != string(started.task.ID) ||
		searchResponse.Groups[0].TaskID != string(started.task.ID) {
		t.Fatalf("cross-surface lifecycle projection mismatch: detail=%+v board=%+v list=%+v search=%+v",
			detail.Actions, cards.Cards[0].Actions, listed.Tasks[0], searchResponse.Groups[0])
	}
}

func TestTaskStatusProjectionCrossSurfaceStableLifecycleMatrix(t *testing.T) {
	tests := []struct {
		name             string
		requiresApproval bool
		wantStatus       serverapi.WorkflowTaskStatusKind
		wantAttention    int
		setup            func(*testing.T, realTaskStatusSurfaces, startedCurrentNodeViewTask) (string, string)
	}{
		{
			name:       "queued",
			wantStatus: serverapi.WorkflowTaskStatusKindQueued,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				execution := startRealTaskStatusExecution(t, surfaces, task, true, nil)
				return string(surfaces.fixture.agentNodeID), execution.sessionID
			},
		},
		{
			name:       "running",
			wantStatus: serverapi.WorkflowTaskStatusKindRunning,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				execution := startRealTaskStatusExecution(t, surfaces, task, false, nil)
				return string(surfaces.fixture.agentNodeID), execution.sessionID
			},
		},
		{
			name:          "ordinary question",
			wantStatus:    serverapi.WorkflowTaskStatusKindWaitingQuestion,
			wantAttention: 1,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				question := surfaces.fixture.startCurrentNodeQuestionOnAuthority(
					t,
					task,
					surfaces.fixture.authority,
					surfaces.fixture.newAgentRuntimePlan(t),
				)
				t.Cleanup(func() {
					_ = question.authority.SubmitPromptResponse(question.sessionID, tools.AskQuestionResponse{
						RequestID: question.request.ID,
						Answer:    "Yes",
					}, nil)
					_, _ = question.handle.Wait(context.Background())
				})
				return string(surfaces.fixture.agentNodeID), question.sessionID.String()
			},
		},
		{
			name:          "live session approval",
			wantStatus:    serverapi.WorkflowTaskStatusKindWaitingApproval,
			wantAttention: 1,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				execution := startRealTaskStatusExecution(t, surfaces, task, false, func() *tools.AskQuestionRequest {
					request := realTaskStatusApprovalRequest()
					return &request
				}())
				return string(surfaces.fixture.agentNodeID), execution.sessionID
			},
		},
		{
			name:             "durable transition approval",
			requiresApproval: true,
			wantStatus:       serverapi.WorkflowTaskStatusKindWaitingApproval,
			wantAttention:    1,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				completed, err := surfaces.fixture.store.CompleteCurrentNode(surfaces.fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
					Source:       task.currentNode,
					TransitionID: "done",
				})
				if err != nil || completed.PendingApproval == nil {
					t.Fatalf("CompleteCurrentNode durable approval: result=%+v err=%v", completed, err)
				}
				return string(surfaces.fixture.agentNodeID), ""
			},
		},
		{
			name:          "interrupted",
			wantStatus:    serverapi.WorkflowTaskStatusKindInterrupted,
			wantAttention: 1,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				if err := surfaces.fixture.store.InterruptCurrentNode(
					surfaces.fixture.ctx,
					task.currentNode,
					workflow.CurrentNodeInterruptionReason("server_restart"),
					workflow.CurrentNodeInterruptionDetail{Code: "restart"},
				); err != nil {
					t.Fatalf("InterruptCurrentNode: %v", err)
				}
				return string(surfaces.fixture.agentNodeID), ""
			},
		},
		{
			name:       "completed",
			wantStatus: serverapi.WorkflowTaskStatusKindDone,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) (string, string) {
				if _, err := surfaces.fixture.store.CompleteCurrentNode(surfaces.fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
					Source:       task.currentNode,
					TransitionID: "done",
				}); err != nil {
					t.Fatalf("CompleteCurrentNode: %v", err)
				}
				definition, _, err := surfaces.fixture.store.GetDefinition(surfaces.fixture.ctx, surfaces.fixture.workflowID)
				if err != nil {
					t.Fatalf("GetDefinition: %v", err)
				}
				return string(currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal)), ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surfaces := newRealTaskStatusSurfaces(t, test.requiresApproval)
			task := surfaces.fixture.startTask(t, "Lifecycle "+test.name)
			nodeID, sessionID := test.setup(t, surfaces, task)
			assertRealTaskStatusAcrossSurfaces(t, surfaces, task, nodeID, sessionID, test.wantStatus, test.wantAttention)
		})
	}
}

func TestTaskDetailDependenciesUseOneStatusObservation(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	controller, err := workflowexecution.NewCurrentNodeController(
		fixture.store,
		taskStatusProjectionTestRunner{},
		fixture.authority,
		workflowexecution.NewMutationPermit(),
		workflowexecution.CurrentNodeControllerConfig{
			AutomaticConcurrency: 1,
			AssignmentSteerer:    taskStatusProjectionTestAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
	})
	calls := 0
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		countingTaskStatusLiveObservationSource{source: controller, calls: &calls},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	dependencies, err := NewTaskDependencies(fixture.metadata, projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	detail, err := NewTaskDetail(fixture.metadata, projection, dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	blocker := createViewTask(t, fixture, "Blocker")
	blocked := createViewTask(t, fixture, "Blocked")
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	}); err != nil {
		t.Fatalf("AddTaskDependency: %v", err)
	}
	projected, err := detail.GetTask(fixture.ctx, string(blocked.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if calls != 1 {
		t.Fatalf("live observations for one Task detail request = %d, want 1", calls)
	}
	if projected.Dependencies.BlockerCount != 1 ||
		projected.Dependencies.UnsatisfiedBlockerCount != 1 ||
		len(projected.Dependencies.Directions) != 2 {
		t.Fatalf("detail dependencies = %+v", projected.Dependencies)
	}
}

func assertRealTaskStatusAcrossSurfaces(
	t *testing.T,
	surfaces realTaskStatusSurfaces,
	task startedCurrentNodeViewTask,
	nodeID string,
	sessionID string,
	wantStatus serverapi.WorkflowTaskStatusKind,
	wantAttention int,
) {
	t.Helper()
	detail, err := surfaces.detail.GetTask(surfaces.fixture.ctx, string(task.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	projectID := surfaces.fixture.binding.ProjectID
	workflowID := surfaces.fixture.workflowID
	limit := 20
	listed, err := surfaces.list.List(surfaces.fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{wantStatus},
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("TaskList tasks = %+v, want one", listed.Tasks)
	}
	cards, err := surfaces.board.ListNodeCards(surfaces.fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   projectID,
		WorkflowID:  workflowID,
		NodeID:      nodeID,
		PageSize:    20,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 {
		t.Fatalf("Board cards = %+v, want one", cards.Cards)
	}
	searchResponse, err := surfaces.search.Search(surfaces.fixture.ctx, serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       "Lifecycle " + strings.TrimSpace(strings.TrimPrefix(task.task.Title, "Lifecycle ")),
		Context:     serverapi.TaskSearchDefaultContext,
		ProjectIDs:  []string{projectID},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{wantStatus},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("TaskSearch.Search: %v", err)
	}
	if len(searchResponse.Groups) != 1 {
		t.Fatalf("TaskSearch groups = %+v, want one", searchResponse.Groups)
	}
	statuses := []serverapi.WorkflowTaskStatus{
		detail.Status,
		listed.Tasks[0].Status,
		cards.Cards[0].Status,
		searchResponse.Groups[0].Status,
	}
	for index, status := range statuses[1:] {
		if !reflect.DeepEqual(statuses[0], status) {
			t.Fatalf("surface status %d = %+v, want %s", index+1, status, statuses[0].Kind)
		}
	}
	if detail.Status.Kind != wantStatus || detail.AttentionCount != wantAttention {
		t.Fatalf("detail status/attention = %s/%d, want %s/%d", detail.Status.Kind, detail.AttentionCount, wantStatus, wantAttention)
	}
	if !reflect.DeepEqual(detail.Actions, cards.Cards[0].Actions) {
		t.Fatalf("detail/board actions differ: detail=%+v board=%+v", detail.Actions, cards.Cards[0].Actions)
	}
	activeNodeIDs := make([]string, 0, len(detail.CurrentNodes))
	for _, currentNode := range detail.CurrentNodes {
		activeNodeIDs = append(activeNodeIDs, currentNode.NodeID)
	}
	if !reflect.DeepEqual(activeNodeIDs, cards.Cards[0].ActiveNodeIDs) {
		t.Fatalf("detail/board current nodes differ: detail=%v board=%v", activeNodeIDs, cards.Cards[0].ActiveNodeIDs)
	}
	if sessionID == "" {
		if len(detail.LiveSessionIDs) != 0 {
			t.Fatalf("detail live sessions = %v, want none", detail.LiveSessionIDs)
		}
	} else if !reflect.DeepEqual(detail.LiveSessionIDs, []string{sessionID}) {
		t.Fatalf("detail live sessions = %v, want %q", detail.LiveSessionIDs, sessionID)
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

func TestTaskDeleteActionUsesWorkflowExecutionQuiescence(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Delete hint")
	taskID := started.task.ID
	fixture.quiescence.blocked[taskID] = true

	detail, err := fixture.detail.GetTask(fixture.ctx, string(taskID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask blocked: %v", err)
	}
	if detail.Actions.CanDelete {
		t.Fatalf("blocked task actions = %+v, want can_delete false", detail.Actions)
	}
	cards, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards blocked: %v", err)
	}
	if len(cards.Cards) != 1 || cards.Cards[0].Actions.CanDelete {
		t.Fatalf("blocked board cards = %+v, want can_delete false", cards.Cards)
	}

	delete(fixture.quiescence.blocked, taskID)
	detail, err = fixture.detail.GetTask(fixture.ctx, string(taskID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask quiescent: %v", err)
	}
	if !detail.Actions.CanDelete {
		t.Fatalf("quiescent task actions = %+v, want can_delete true", detail.Actions)
	}
	cards, err = fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards quiescent: %v", err)
	}
	if len(cards.Cards) != 1 || !cards.Cards[0].Actions.CanDelete {
		t.Fatalf("quiescent board cards = %+v, want can_delete true", cards.Cards)
	}
}

func TestTaskListProjectsCurrentNodeStatusAndColumn(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "List task")
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 20

	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		ColumnKeys:  []string{"agent"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
		Limit: &limit,
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
	workflowID := fixture.workflowID
	limit := 1
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
		Limit: &limit,
	}
	var got []string
	var nextOffset *int
	for pageIndex := 0; ; pageIndex++ {
		page, err := fixture.tasks.List(fixture.ctx, request)
		if err != nil {
			t.Fatalf("TaskList.List page %d: %v", pageIndex, err)
		}
		if len(page.Tasks) != 1 {
			t.Fatalf("task list page %d = %+v, want one task", pageIndex, page.Tasks)
		}
		got = append(got, page.Tasks[0].TaskID)
		if page.NextOffset == nil {
			break
		}
		nextOffset = page.NextOffset
		request.Offset = nextOffset
	}
	if !equalStrings(got, want) {
		t.Fatalf("task-list pagination order = %v, want %v", got, want)
	}
	request.Offset = nextOffset
	request.StatusKinds = []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive}
	if _, err := fixture.tasks.List(fixture.ctx, request); err != nil {
		t.Fatalf("task-list offset with changed filter error = %v", err)
	}
}

func TestProjectWideTaskListBeyondEndRetainsMatchingWorkflowCardinality(t *testing.T) {
	t.Run("one", func(t *testing.T) {
		fixture := newCurrentNodeViewFixture(t, false)
		fixture.createBacklogTask(t, "Only workflow task")
		projectID := fixture.binding.ProjectID
		offset := 1
		limit := 1

		page, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
			ProjectID:   &projectID,
			LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
			Offset:      &offset,
			Limit:       &limit,
		})
		if err != nil {
			t.Fatalf("TaskList.List at end: %v", err)
		}
		if len(page.Tasks) != 0 ||
			page.NextOffset != nil ||
			page.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne {
			t.Fatalf("project-wide task-list page at end = %+v", page)
		}
	})

	t.Run("multiple", func(t *testing.T) {
		fixture := newCurrentNodeViewFixture(t, false)
		fixture.createBacklogTask(t, "First workflow task")
		secondWorkflowID := currentNodeViewWorkflow(t, fixture.store, false)
		if _, err := fixture.store.LinkWorkflow(fixture.ctx, fixture.binding.ProjectID, secondWorkflowID, false); err != nil {
			t.Fatalf("LinkWorkflow second workflow: %v", err)
		}
		if _, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
			ProjectID:  fixture.binding.ProjectID,
			WorkflowID: &secondWorkflowID,
			Title:      "Second workflow task",
		}); err != nil {
			t.Fatalf("CreateTask second workflow: %v", err)
		}
		projectID := fixture.binding.ProjectID
		offset := 3
		limit := 1

		page, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
			ProjectID:   &projectID,
			LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
			Offset:      &offset,
			Limit:       &limit,
		})
		if err != nil {
			t.Fatalf("TaskList.List beyond end: %v", err)
		}
		if len(page.Tasks) != 0 ||
			page.NextOffset != nil ||
			page.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple {
			t.Fatalf("project-wide task-list page beyond end = %+v", page)
		}
	})
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
	workflowID := fixture.workflowID
	limit := 20

	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
		Limit: &limit,
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

func TestTaskListProjectsLiveSessionApprovalThroughCanonicalStatus(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Live approval")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
					started.task.ID: {
						Executions: []sessionruntime.TaskExecution{{
							Queued: false,
							Agent:  &sessionruntime.TaskAgentExecutionTarget{SessionID: sessionID},
							PendingPrompts: []sessionruntime.PendingPromptReference{{
								ID:   "approval",
								Kind: sessionruntime.PendingPromptKindSessionApproval,
							}},
						}},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	taskList, err := NewTaskList(fixture.metadata, mustDefinitionProjection(t, fixture.store), projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	projectID := fixture.binding.ProjectID
	limit := 20
	page, err := taskList.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:      &projectID,
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
		LabelFilter:    serverapi.WorkflowTaskLabelFilterNone(),
		Limit:          &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(page.Tasks) != 1 ||
		page.Tasks[0].TaskID != string(started.task.ID) ||
		page.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindWaitingApproval ||
		len(page.Tasks[0].Status.AttentionTypes) != 1 ||
		page.Tasks[0].Status.AttentionTypes[0] != serverapi.WorkflowTaskAttentionKindApproval {
		t.Fatalf("live approval Task List page = %+v", page)
	}
}

func TestWorkflowTaskReadModelsKeepDurableDoneOverLiveExactScope(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	done := fixture.startTask(t, "Done while source execution is live")
	sessionID := fixture.bindCurrentNodeSession(t, done)
	plan := fixture.newAgentRuntimePlan(t)
	lease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		CurrentNode: done.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	releaseExecution := make(chan struct{})
	handle, err := fixture.authority.StartAgentExecution(fixture.ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
			<-releaseExecution
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	t.Cleanup(func() {
		close(releaseExecution)
		if _, waitErr := handle.Wait(context.Background()); waitErr != nil {
			t.Errorf("wait terminal source execution: %v", waitErr)
		}
	})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, snapshotErr := fixture.authority.CurrentProjectWorkflowTaskExecutionSnapshots(
			fixture.binding.ProjectID,
			fixture.workflowID,
		)
		if snapshotErr != nil {
			return false
		}
		executions := snapshots[done.task.ID].Executions
		return len(executions) == 1 && !executions[0].Queued
	}, "timed out waiting for source Exact Execution Scope")

	if _, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       done.currentNode,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	snapshots, err := fixture.authority.CurrentProjectWorkflowTaskExecutionSnapshots(
		fixture.binding.ProjectID,
		fixture.workflowID,
	)
	if err != nil {
		t.Fatalf("CurrentProjectWorkflowTaskExecutionSnapshots: %v", err)
	}
	if executions := snapshots[done.task.ID].Executions; len(executions) != 1 || executions[0].Queued {
		t.Fatalf("source Exact Execution Scope retired before ExecutionFinalized: %+v", executions)
	}

	detail, err := fixture.detail.GetTask(fixture.ctx, string(done.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindDone || !detail.Summary.Done {
		t.Fatalf("task detail = %+v, want durable done", detail)
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	terminalNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal)
	board, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(terminalNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	if len(board.Cards) != 1 ||
		board.Cards[0].TaskID != string(done.task.ID) ||
		board.Cards[0].Status.Kind != serverapi.WorkflowTaskStatusKindDone {
		t.Fatalf("terminal board cards = %+v, want durable done", board.Cards)
	}

	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	doneLimit := 20
	doneOnly, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindDone},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Limit:       &doneLimit,
	})
	if err != nil {
		t.Fatalf("TaskList.List done filter: %v", err)
	}
	if len(doneOnly.Tasks) != 1 ||
		doneOnly.Tasks[0].TaskID != string(done.task.ID) ||
		doneOnly.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindDone {
		t.Fatalf("done task list filter = %+v, want durable done", doneOnly.Tasks)
	}

	active := fixture.startTask(t, "Active after done")
	statusLimit := 1
	statusPage := serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindDone,
			serverapi.WorkflowTaskStatusKindActive,
		},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Sort: []serverapi.WorkflowTaskListSort{{
			Field:     serverapi.WorkflowTaskListSortFieldStatus,
			Direction: serverapi.WorkflowTaskListSortDirectionAsc,
		}},
		Limit: &statusLimit,
	}
	firstPage, err := fixture.tasks.List(fixture.ctx, statusPage)
	if err != nil {
		t.Fatalf("TaskList.List first status page: %v", err)
	}
	if len(firstPage.Tasks) != 1 ||
		firstPage.Tasks[0].TaskID != string(done.task.ID) ||
		firstPage.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindDone ||
		firstPage.NextOffset == nil {
		t.Fatalf("first status page = %+v, want done and a cursor", firstPage)
	}
	statusPage.Offset = firstPage.NextOffset
	secondPage, err := fixture.tasks.List(fixture.ctx, statusPage)
	if err != nil {
		t.Fatalf("TaskList.List second status page: %v", err)
	}
	if len(secondPage.Tasks) != 1 ||
		secondPage.Tasks[0].TaskID != string(active.task.ID) ||
		secondPage.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		secondPage.NextOffset != nil {
		t.Fatalf("second status page = %+v, want active with no cursor", secondPage)
	}
}

func TestWorkflowTaskReadModelsProjectQueuedAndRunningExactScopes(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	queued := fixture.startTask(t, "Queued")
	running := fixture.startTask(t, "Running")
	queuedSessionID := fixture.bindCurrentNodeSession(t, queued)
	runningSessionID := fixture.bindCurrentNodeSession(t, running)
	plan := fixture.newAgentRuntimePlan(t)

	queuedLease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		CurrentNode: queued.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease queued: %v", err)
	}
	queuedHandle, err := fixture.authority.StartAgentExecution(fixture.ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, queuedSessionID),
		Runtime:    &plan,
		Workflow:   &queuedLease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution queued: %v", err)
	}
	runningLease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		CurrentNode: running.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease running: %v", err)
	}
	runningLease.Release()
	runningHandle, err := fixture.authority.StartAgentExecution(fixture.ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, runningSessionID),
		Runtime:    &plan,
		Workflow:   &runningLease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution running: %v", err)
	}
	t.Cleanup(func() {
		queuedHandle.RequestStop()
		runningHandle.RequestStop()
	})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, snapshotErr := fixture.authority.CurrentProjectWorkflowTaskExecutionSnapshots(
			fixture.binding.ProjectID,
			fixture.workflowID,
		)
		if snapshotErr != nil {
			return false
		}
		queuedExecutions := snapshots[queued.task.ID].Executions
		runningExecutions := snapshots[running.task.ID].Executions
		return len(queuedExecutions) == 1 && queuedExecutions[0].Queued &&
			len(runningExecutions) == 1 && !runningExecutions[0].Queued
	}, "timed out waiting for queued and running Exact Execution Scope snapshots")

	queuedDetail, err := fixture.detail.GetTask(fixture.ctx, string(queued.task.ID))
	if err != nil {
		t.Fatalf("GetTask queued: %v", err)
	}
	if queuedDetail.Status.Kind != serverapi.WorkflowTaskStatusKindQueued ||
		queuedDetail.Actions.CanInterrupt {
		t.Fatalf("queued detail = %+v, want queued and not interruptible", queuedDetail)
	}
	queuedCards, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	cardByTaskID := make(map[string]serverapi.WorkflowBoardTaskCard, len(queuedCards.Cards))
	for _, card := range queuedCards.Cards {
		cardByTaskID[card.TaskID] = card
	}
	if card := cardByTaskID[string(queued.task.ID)]; card.Status.Kind != serverapi.WorkflowTaskStatusKindQueued || card.Actions.CanInterrupt {
		t.Fatalf("queued card = %+v, want queued and not interruptible", card)
	}
	if card := cardByTaskID[string(running.task.ID)]; card.Status.Kind != serverapi.WorkflowTaskStatusKindRunning || !card.Actions.CanInterrupt {
		t.Fatalf("running card = %+v, want running and interruptible", card)
	}

	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	listLimit := 20
	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindQueued,
			serverapi.WorkflowTaskStatusKindRunning,
		},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Limit:       &listLimit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(list.Tasks) != 2 ||
		list.Tasks[0].TaskID != string(running.task.ID) ||
		list.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindRunning ||
		list.Tasks[1].TaskID != string(queued.task.ID) ||
		list.Tasks[1].Status.Kind != serverapi.WorkflowTaskStatusKindQueued {
		t.Fatalf("task list status filter and sort = %+v, want running then queued", list.Tasks)
	}
	pageLimit := 1
	pageRequest := serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindQueued,
			serverapi.WorkflowTaskStatusKindRunning,
		},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Limit:       &pageLimit,
	}
	firstPage, err := fixture.tasks.List(fixture.ctx, pageRequest)
	if err != nil {
		t.Fatalf("TaskList.List first cursor page: %v", err)
	}
	if len(firstPage.Tasks) != 1 ||
		firstPage.Tasks[0].TaskID != string(running.task.ID) ||
		firstPage.NextOffset == nil {
		t.Fatalf("first status cursor page = %+v, want running and a cursor", firstPage)
	}
	pageRequest.Offset = firstPage.NextOffset
	secondPage, err := fixture.tasks.List(fixture.ctx, pageRequest)
	if err != nil {
		t.Fatalf("TaskList.List second cursor page: %v", err)
	}
	if len(secondPage.Tasks) != 1 ||
		secondPage.Tasks[0].TaskID != string(queued.task.ID) ||
		secondPage.NextOffset != nil {
		t.Fatalf("second status cursor page = %+v, want queued and no cursor", secondPage)
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
