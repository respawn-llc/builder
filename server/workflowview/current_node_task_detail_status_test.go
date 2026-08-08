package workflowview

import (
	"context"
	"core/internal/testharness/workflowtest"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestCurrentNodeStatusProjectionCrossSurfaceStableQuestion(t *testing.T) {
	surfaces := newRealTaskStatusSurfaces(t, false)
	fixture := surfaces.fixture
	backlog := startedCurrentNodeViewTask{task: fixture.createBacklogTask(t, "Cross surface question")}
	request := tools.AskQuestionRequest{
		ID:                     uuid.NewString(),
		StepID:                 uuid.NewString(),
		Question:               "Proceed?",
		Suggestions:            []string{"Yes", "No"},
		RecommendedOptionIndex: 1,
	}
	started, execution := startRealTaskStatusExecution(t, surfaces, backlog, false, &request)
	agentTarget, ok := execution.target.(taskStatusAgentTarget)
	if !ok {
		t.Fatalf("question execution target = %T, want agent", execution.target)
	}

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
		detail.Actions.CanDelete ||
		len(detail.LiveSessionIDs) != 1 ||
		detail.LiveSessionIDs[0] != agentTarget.sessionID.String() {
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
		startsExisting   bool
		wantStatus       serverapi.WorkflowTaskStatusKind
		wantAttention    int
		wantCanInterrupt bool
		wantCanDelete    bool
		setup            func(*testing.T, realTaskStatusSurfaces, startedCurrentNodeViewTask) taskStatusExpectedTarget
	}{
		{
			name:          "queued",
			wantStatus:    serverapi.WorkflowTaskStatusKindQueued,
			wantCanDelete: false,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				_, _ = startRealTaskStatusExecution(t, surfaces, task, true, nil)
				return taskStatusExpectedTarget{
					nodeID:       surfaces.fixture.agentNodeID,
					live:         taskStatusNoLiveTarget{},
					runtimeOwned: true,
				}
			},
		},
		{
			name:             "running",
			wantStatus:       serverapi.WorkflowTaskStatusKindRunning,
			wantCanInterrupt: true,
			wantCanDelete:    false,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				_, execution := startRealTaskStatusExecution(t, surfaces, task, false, nil)
				return taskStatusExpectedTarget{nodeID: surfaces.fixture.agentNodeID, live: execution.target}
			},
		},
		{
			name:             "live script",
			wantStatus:       serverapi.WorkflowTaskStatusKindRunning,
			wantCanInterrupt: true,
			wantCanDelete:    false,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				shellPath, err := exec.LookPath("sh")
				if err != nil {
					t.Skipf("sh executable unavailable: %v", err)
				}
				_, execution := startRealTaskStatusScriptExecution(t, surfaces, task, shellPath)
				return taskStatusExpectedTarget{nodeID: surfaces.fixture.agentNodeID, live: execution.target}
			},
		},
		{
			name:          "ordinary question",
			wantStatus:    serverapi.WorkflowTaskStatusKindWaitingQuestion,
			wantAttention: 1,
			wantCanDelete: false,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				request := tools.AskQuestionRequest{
					ID:       uuid.NewString(),
					StepID:   uuid.NewString(),
					Question: "Proceed?",
				}
				_, execution := startRealTaskStatusExecution(t, surfaces, task, false, &request)
				return taskStatusExpectedTarget{nodeID: surfaces.fixture.agentNodeID, live: execution.target}
			},
		},
		{
			name:          "live session approval",
			wantStatus:    serverapi.WorkflowTaskStatusKindWaitingApproval,
			wantAttention: 1,
			wantCanDelete: false,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				_, execution := startRealTaskStatusExecution(t, surfaces, task, false, func() *tools.AskQuestionRequest {
					request := realTaskStatusApprovalRequest()
					return &request
				}())
				return taskStatusExpectedTarget{nodeID: surfaces.fixture.agentNodeID, live: execution.target}
			},
		},
		{
			name:             "durable transition approval",
			requiresApproval: true,
			startsExisting:   true,
			wantStatus:       serverapi.WorkflowTaskStatusKindWaitingApproval,
			wantAttention:    1,
			wantCanDelete:    true,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				completed, err := workflowtest.CompleteCurrentNode(surfaces.fixture.store, surfaces.fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
					Source:       task.currentNode,
					TransitionID: "done",
				})
				if err != nil || completed.PendingApproval == nil {
					t.Fatalf("CompleteCurrentNode durable approval: result=%+v err=%v", completed, err)
				}
				return taskStatusExpectedTarget{nodeID: surfaces.fixture.agentNodeID, live: taskStatusNoLiveTarget{}}
			},
		},
		{
			name:           "interrupted",
			startsExisting: true,
			wantStatus:     serverapi.WorkflowTaskStatusKindInterrupted,
			wantAttention:  1,
			wantCanDelete:  true,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				if err := surfaces.fixture.store.InterruptCurrentNode(
					surfaces.fixture.ctx,
					task.currentNode,
					workflow.CurrentNodeInterruptionReason("server_restart"),
					workflow.CurrentNodeInterruptionDetail{Code: "restart"},
				); err != nil {
					t.Fatalf("InterruptCurrentNode: %v", err)
				}
				return taskStatusExpectedTarget{nodeID: surfaces.fixture.agentNodeID, live: taskStatusNoLiveTarget{}}
			},
		},
		{
			name:           "completed",
			startsExisting: true,
			wantStatus:     serverapi.WorkflowTaskStatusKindDone,
			wantCanDelete:  true,
			setup: func(t *testing.T, surfaces realTaskStatusSurfaces, task startedCurrentNodeViewTask) taskStatusExpectedTarget {
				if _, err := workflowtest.CompleteCurrentNode(surfaces.fixture.store, surfaces.fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
					Source:       task.currentNode,
					TransitionID: "done",
				}); err != nil {
					t.Fatalf("CompleteCurrentNode: %v", err)
				}
				definition, _, err := surfaces.fixture.store.GetDefinition(surfaces.fixture.ctx, surfaces.fixture.workflowID)
				if err != nil {
					t.Fatalf("GetDefinition: %v", err)
				}
				return taskStatusExpectedTarget{
					nodeID: currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal),
					live:   taskStatusNoLiveTarget{},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surfaces := newRealTaskStatusSurfaces(t, test.requiresApproval)
			task := startedCurrentNodeViewTask{
				task: surfaces.fixture.createBacklogTask(t, "Lifecycle "+test.name),
			}
			if test.startsExisting {
				task = surfaces.fixture.startExistingTask(t, task.task)
			}
			expected := test.setup(t, surfaces, task)
			assertRealTaskStatusAcrossSurfaces(t, surfaces, task, expected, test.wantStatus, test.wantAttention, test.wantCanInterrupt, test.wantCanDelete)
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
			AgentConcurrency:  1,
			AssignmentEnsurer: taskStatusProjectionTestAssignmentEnsurer{},
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

func TestLifecycleSensitiveReadSurfacesUseOneCapturePerRequest(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	controller, err := workflowexecution.NewCurrentNodeController(
		fixture.store,
		taskStatusProjectionTestRunner{},
		fixture.authority,
		workflowexecution.NewMutationPermit(),
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentEnsurer: taskStatusProjectionTestAssignmentEnsurer{},
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
	captures := 0
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		countingTaskStatusLiveObservationSource{source: controller, calls: &captures},
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
	definitions := mustDefinitionProjection(t, fixture.store)
	list, err := NewTaskList(fixture.metadata, definitions, projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	search, err := NewTaskSearch(fixture.metadata, projection)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	board, err := NewBoard(
		fixture.metadata,
		definitions,
		testsetup.QuestionsEnabled("coder"),
		projection,
	)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	started := fixture.startTask(t, "Captured surface")
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	requireOneCapture := func(t *testing.T, operation func() error) {
		t.Helper()
		captures = 0
		if err := operation(); err != nil {
			t.Fatalf("read surface: %v", err)
		}
		if captures != 1 {
			t.Fatalf("lifecycle captures = %d, want one", captures)
		}
	}
	t.Run("detail", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := detail.GetTask(t.Context(), string(started.task.ID))
			return err
		})
	})
	t.Run("current nodes", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := detail.ListCurrentNodes(t.Context(), string(started.task.ID))
			return err
		})
	})
	t.Run("list", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := list.List(t.Context(), serverapi.WorkflowTaskListRequest{
				ProjectID:  &projectID,
				WorkflowID: &workflowID,
				Limit:      intPointer(20),
				LabelFilter: serverapi.WorkflowTaskLabelFilter{
					Kind: serverapi.WorkflowTaskLabelFilterKindNone,
				},
			})
			return err
		})
	})
	t.Run("search", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := search.Search(t.Context(), taskSearchRequest("Captured surface"))
			return err
		})
	})
	t.Run("board counts", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := board.Get(t.Context(), serverapi.WorkflowBoardRequest{
				ProjectID:  projectID,
				WorkflowID: &workflowID,
				LabelFilter: serverapi.WorkflowTaskLabelFilter{
					Kind: serverapi.WorkflowTaskLabelFilterKindNone,
				},
			})
			return err
		})
	})
	t.Run("board cards", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := board.ListNodeCards(t.Context(), serverapi.WorkflowBoardNodeCardsListRequest{
				ProjectID:  projectID,
				WorkflowID: workflowID,
				NodeID:     string(fixture.agentNodeID),
				PageSize:   20,
				LabelFilter: serverapi.WorkflowTaskLabelFilter{
					Kind: serverapi.WorkflowTaskLabelFilterKindNone,
				},
			})
			return err
		})
	})
	t.Run("dependencies", func(t *testing.T) {
		requireOneCapture(t, func() error {
			_, err := dependencies.GetTaskDependencies(t.Context(), string(started.task.ID))
			return err
		})
	})
}

func assertRealTaskStatusAcrossSurfaces(
	t *testing.T,
	surfaces realTaskStatusSurfaces,
	task startedCurrentNodeViewTask,
	expected taskStatusExpectedTarget,
	wantStatus serverapi.WorkflowTaskStatusKind,
	wantAttention int,
	wantCanInterrupt bool,
	wantCanDelete bool,
) {
	t.Helper()
	observation, err := surfaces.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{task.task.ID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	wantQuiescent := false
	switch expected.live.(type) {
	case taskStatusNoLiveTarget:
		wantQuiescent = !expected.runtimeOwned
	case taskStatusAgentTarget, taskStatusScriptTarget:
	default:
		t.Fatalf("unsupported expected live target %T", expected.live)
	}
	if got := observation.Quiescence[task.task.ID]; got != wantQuiescent {
		t.Fatalf("Controller quiescence = %t, want %t", got, wantQuiescent)
	}
	detail, err := surfaces.detail.GetTask(surfaces.fixture.ctx, string(task.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if err := (serverapi.WorkflowTaskGetResponse{Task: detail}).Validate(); err != nil {
		t.Fatalf("Task Detail API response validation: %v", err)
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
		NodeID:      string(expected.nodeID),
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
		Query:       task.task.Title,
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
	if detail.Actions.CanInterrupt != wantCanInterrupt || detail.Actions.CanDelete != wantCanDelete {
		t.Fatalf("detail actions = %+v, want can_interrupt=%t can_delete=%t", detail.Actions, wantCanInterrupt, wantCanDelete)
	}
	if !reflect.DeepEqual(detail.Actions, cards.Cards[0].Actions) {
		t.Fatalf("detail/board actions differ: detail=%+v board=%+v", detail.Actions, cards.Cards[0].Actions)
	}
	if len(detail.CurrentNodes) != 0 && (len(detail.CurrentNodes) != 1 || detail.CurrentNodes[0].NodeID != string(expected.nodeID)) {
		t.Fatalf("detail current nodes = %+v, want node %q", detail.CurrentNodes, expected.nodeID)
	}
	activeNodeIDs := make([]string, 0, len(detail.CurrentNodes))
	for _, currentNode := range detail.CurrentNodes {
		activeNodeIDs = append(activeNodeIDs, currentNode.NodeID)
	}
	if !reflect.DeepEqual(activeNodeIDs, cards.Cards[0].ActiveNodeIDs) {
		t.Fatalf("detail/board current nodes differ: detail=%v board=%v", activeNodeIDs, cards.Cards[0].ActiveNodeIDs)
	}
	switch target := expected.live.(type) {
	case taskStatusNoLiveTarget:
		if len(detail.LiveSessionIDs) != 0 || len(detail.CurrentScripts) != 0 {
			t.Fatalf("durable detail live targets = sessions %v scripts %+v, want none", detail.LiveSessionIDs, detail.CurrentScripts)
		}
	case taskStatusAgentTarget:
		if !reflect.DeepEqual(detail.LiveSessionIDs, []string{target.sessionID.String()}) || len(detail.CurrentScripts) != 0 {
			t.Fatalf("agent detail live targets = sessions %v scripts %+v", detail.LiveSessionIDs, detail.CurrentScripts)
		}
	case taskStatusScriptTarget:
		if len(detail.LiveSessionIDs) != 0 ||
			len(detail.CurrentScripts) != 1 ||
			detail.CurrentScripts[0].Path != target.path ||
			detail.CurrentScripts[0].CurrentNode.NodeID != string(expected.nodeID) {
			t.Fatalf("script detail live targets = sessions %v scripts %+v", detail.LiveSessionIDs, detail.CurrentScripts)
		}
		if len(cards.Cards[0].ActiveNodeIDs) != 1 || cards.Cards[0].ActiveNodeIDs[0] != string(expected.nodeID) {
			t.Fatalf("script board active nodes = %v, want node %q", cards.Cards[0].ActiveNodeIDs, expected.nodeID)
		}
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

func TestTaskListAppliesPinnedLifecycleOverrideBeforeColumnAndStatusFilters(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Pinned List task")
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	overrideNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal)
	overrideNodeKey := ""
	for _, node := range definition.Nodes {
		if workflow.NodeIDOf(node) == overrideNodeID {
			overrideNodeKey = string(workflow.NodeKey(node))
			break
		}
	}
	if overrideNodeKey == "" {
		t.Fatalf("override node %q has no key", overrideNodeID)
	}
	overrideReference, err := workflow.NewCurrentNodeReference(started.task.ID, overrideNodeID, nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference override: %v", err)
	}
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			store: fixture.store,
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
					started.task.ID: {
						CurrentNodes:       []workflow.CurrentNode{{Reference: overrideReference}},
						QueuedCurrentNodes: []workflow.CurrentNodeReference{overrideReference},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	taskList, err := NewTaskList(
		fixture.metadata,
		mustDefinitionProjection(t, fixture.store),
		projection,
	)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 1
	page, err := taskList.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		ColumnKeys:  []string{overrideNodeKey},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindQueued},
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Limit:       &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(page.Tasks) != 1 ||
		page.Tasks[0].TaskID != string(started.task.ID) ||
		page.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindQueued ||
		page.Tasks[0].ColumnKeys == nil ||
		!slices.Equal(*page.Tasks[0].ColumnKeys, []string{overrideNodeKey}) {
		t.Fatalf("Task List pinned lifecycle page = %+v", page.Tasks)
	}
	if page.NextOffset != nil {
		t.Fatalf("Task List pinned lifecycle next offset = %v, want none", page.NextOffset)
	}
}

func TestTaskListDependencyFilterUsesPinnedBlockerLifecycleBeforePagination(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	blocker := fixture.startTask(t, "Pinned blocker")
	blocked := fixture.startTask(t, "Pinned blocked")
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: blocker.task.ID,
		BlockedTaskID: blocked.task.ID,
	}); err != nil {
		t.Fatalf("AddTaskDependency: %v", err)
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := workflowtest.ManualMoveTask(fixture.store, fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       blocker.task.ID,
		TargetNodeID: currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal),
	}); err != nil {
		t.Fatalf("move blocker to done: %v", err)
	}
	overrideReference, err := workflow.NewCurrentNodeReference(
		blocker.task.ID,
		fixture.agentNodeID,
		nil,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference override: %v", err)
	}
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			store: fixture.store,
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
					blocker.task.ID: {
						CurrentNodes:       []workflow.CurrentNode{{Reference: overrideReference}},
						QueuedCurrentNodes: []workflow.CurrentNodeReference{overrideReference},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	taskList, err := NewTaskList(
		fixture.metadata,
		mustDefinitionProjection(t, fixture.store),
		projection,
	)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 1
	unblocked := true
	request := serverapi.WorkflowTaskListRequest{
		ProjectID:        &projectID,
		WorkflowID:       &workflowID,
		ColumnKeys:       []string{"agent"},
		StatusKinds:      []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive},
		DependencyFilter: &unblocked,
		LabelFilter:      serverapi.WorkflowTaskLabelFilterNone(),
		Limit:            &limit,
	}
	page, err := taskList.List(fixture.ctx, request)
	if err != nil {
		t.Fatalf("TaskList.List unblocked: %v", err)
	}
	if len(page.Tasks) != 0 {
		t.Fatalf("unblocked page = %+v, want pinned queued blocker to exclude blocked Task", page.Tasks)
	}

	blockedFilter := false
	request.DependencyFilter = &blockedFilter
	page, err = taskList.List(fixture.ctx, request)
	if err != nil {
		t.Fatalf("TaskList.List blocked: %v", err)
	}
	if len(page.Tasks) != 1 || page.Tasks[0].TaskID != string(blocked.task.ID) {
		t.Fatalf("blocked page = %+v, want Task %q", page.Tasks, blocked.task.ID)
	}
	if page.NextOffset != nil {
		t.Fatalf("blocked page next offset = %v, want none", page.NextOffset)
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

func TestTaskListSortsByNumericShortIDBeforePagination(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	tasks := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Short ID first"),
		fixture.startTask(t, "Short ID second"),
		fixture.startTask(t, "Short ID third"),
	}
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 1
	request := serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Sort: []serverapi.WorkflowTaskListSort{{
			Field:     serverapi.WorkflowTaskListSortFieldShortID,
			Direction: serverapi.WorkflowTaskListSortDirectionAsc,
		}},
		Limit: &limit,
	}
	var got []string
	for {
		page, err := fixture.tasks.List(fixture.ctx, request)
		if err != nil {
			t.Fatalf("TaskList.List: %v", err)
		}
		if len(page.Tasks) > 1 {
			t.Fatalf("page has %d tasks, want at most one", len(page.Tasks))
		}
		if len(page.Tasks) == 1 {
			got = append(got, page.Tasks[0].TaskID)
		}
		if page.NextOffset == nil {
			break
		}
		request.Offset = page.NextOffset
	}
	want := []string{string(tasks[0].task.ID), string(tasks[1].task.ID), string(tasks[2].task.ID)}
	if !slices.Equal(got, want) {
		t.Fatalf("short ID pagination order = %v, want %v", got, want)
	}
}

func TestTaskListSortsShortIDDescendingAndAcceptsSevenSelectors(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	tasks := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Short ID first"),
		fixture.startTask(t, "Short ID second"),
	}
	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 20
	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Sort: []serverapi.WorkflowTaskListSort{
			{Field: serverapi.WorkflowTaskListSortFieldShortID, Direction: serverapi.WorkflowTaskListSortDirectionDesc},
			{Field: serverapi.WorkflowTaskListSortFieldLabels, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
			{Field: serverapi.WorkflowTaskListSortFieldCreated, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
			{Field: serverapi.WorkflowTaskListSortFieldUpdated, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
			{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
			{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
			{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
		},
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	if len(list.Tasks) < len(tasks) || list.Tasks[0].TaskID != string(tasks[1].task.ID) {
		t.Fatalf("descending seven-field list = %+v, want highest Short ID first", list.Tasks)
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
	scopeID := runtimeids.NewExecutionScopeID()
	executions := map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
		started.task.ID: {
			Executions: []sessionruntime.TaskExecution{{
				Ref: sessionruntime.WorkflowExecutionRef{
					ProjectID:   fixture.binding.ProjectID,
					WorkflowID:  fixture.workflowID,
					CurrentNode: started.currentNode,
				},
				ScopeID: scopeID,
				Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: sessionID},
				PendingPrompts: []sessionruntime.PendingPromptReference{{
					ID:   "approval",
					Kind: sessionruntime.PendingPromptKindSessionApproval,
				}},
			}},
		},
	}
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			store: fixture.store,
			observation: workflowTaskExecutionObservationForTest(
				t,
				map[workflow.TaskID][]workflow.CurrentNode{
					started.task.ID: {{Reference: started.currentNode}},
				},
				executions,
				nil,
			),
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

	if _, err := workflowtest.CompleteCurrentNode(fixture.store, fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
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
		completed, err := workflowtest.CompleteCurrentNode(fixture.store, fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
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
		if _, err := workflowtest.CompleteCurrentNode(fixture.store, fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
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
