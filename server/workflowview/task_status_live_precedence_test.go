package workflowview

import (
	"context"
	"fmt"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestTaskListCanonicalStatusDistinguishesStaleDurableFactsAndExactLiveExecutions(t *testing.T) {
	for _, test := range []struct {
		name         string
		prepare      func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord)
		observations func(task workflow.TaskID, started workflowstore.StartTaskResult, claimed workflowstore.RunnableRunRecord) taskStatusTestObservations
		wantStatus   serverapi.WorkflowTaskStatusKind
		wantRunID    bool
		wantQuestion bool
	}{
		{
			name: "stale queued without exact live execution remains active",
			prepare: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
				t.Helper()
				started, err := store.StartTask(ctx, task)
				if err != nil {
					t.Fatalf("StartTask: %v", err)
				}
				return started, workflowstore.RunnableRunRecord{}
			},
			observations: func(workflow.TaskID, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) taskStatusTestObservations {
				return taskStatusTestObservations{}
			},
			wantStatus: serverapi.WorkflowTaskStatusKindActive,
		},
		{
			name:    "stale running without exact live execution remains active",
			prepare: claimTaskStatusTestRun,
			observations: func(workflow.TaskID, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) taskStatusTestObservations {
				return taskStatusTestObservations{}
			},
			wantStatus: serverapi.WorkflowTaskStatusKindActive,
		},
		{
			name: "stale waiting question without exact live execution remains active",
			prepare: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
				t.Helper()
				started, claimed := claimTaskStatusTestRun(t, ctx, store, task)
				if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-stale"); err != nil {
					t.Fatalf("SetRunWaitingAsk: %v", err)
				}
				return started, claimed
			},
			observations: func(workflow.TaskID, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) taskStatusTestObservations {
				return taskStatusTestObservations{}
			},
			wantStatus: serverapi.WorkflowTaskStatusKindActive,
		},
		{
			name:         "exact live running becomes running",
			prepare:      claimTaskStatusTestRun,
			observations: taskStatusTestExactRunningObservations,
			wantStatus:   serverapi.WorkflowTaskStatusKindRunning,
			wantRunID:    true,
		},
		{
			name: "exact live question agreement becomes waiting question",
			prepare: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
				t.Helper()
				started, claimed := claimTaskStatusTestRun(t, ctx, store, task)
				if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-live"); err != nil {
					t.Fatalf("SetRunWaitingAsk: %v", err)
				}
				return started, claimed
			},
			observations: taskStatusTestExactQuestionObservations,
			wantStatus:   serverapi.WorkflowTaskStatusKindWaitingQuestion,
			wantRunID:    true,
			wantQuestion: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
			workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
			if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
				t.Fatalf("LinkWorkflow: %v", err)
			}
			task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
				ProjectID: binding.ProjectID,
				Title:     test.name,
				Body:      "Body",
			})
			if err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			started, claimed := test.prepare(t, ctx, workflowStore, task.ID)
			observations := test.observations(task.ID, started, claimed)
			list, _, _, _ := newTaskStatusTestReadModels(t, metadataStore, workflowStore, observations)
			projectID := binding.ProjectID
			workflowIDValue := string(workflowID)
			response, err := list.List(ctx, serverapi.WorkflowTaskListRequest{
				LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
				ProjectID:   &projectID,
				WorkflowID:  &workflowIDValue,
				StatusKinds: []serverapi.WorkflowTaskStatusKind{test.wantStatus},
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(response.Tasks) != 1 || response.Tasks[0].TaskID != string(task.ID) {
				t.Fatalf("list status %q = %+v, want task %q", test.wantStatus, response.Tasks, task.ID)
			}
			status := response.Tasks[0].Status
			if status.Kind != test.wantStatus {
				t.Fatalf("list status = %+v, want %q", status, test.wantStatus)
			}
			if (len(status.RunIDs) != 0) != test.wantRunID {
				t.Fatalf("list run ids = %+v, want present=%t", status.RunIDs, test.wantRunID)
			}
			if containsTaskStatusAttention(status.AttentionTypes, serverapi.WorkflowTaskAttentionKindQuestion) != test.wantQuestion {
				t.Fatalf("list attention = %+v, want question=%t", status.AttentionTypes, test.wantQuestion)
			}
		})
	}
}

func TestTaskReadModelsStayAvailableAcrossHeldLifecycleStates(t *testing.T) {
	t.Run("durable done wins while authority and scheduler cleanup remain observable", func(t *testing.T) {
		ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
		workflowID, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		observations := taskStatusTestExactRunningObservations(task.ID, started, claimed)
		list, board, detail, search := newTaskStatusTestReadModels(t, metadataStore, workflowStore, observations)
		assertTaskStatusTestReadSurfaces(t, ctx, binding.ProjectID, workflowID, task, list, board, detail, search, "done", serverapi.WorkflowTaskStatusKindDone, false, false)
	})

	t.Run("durable canceled wins while authority and scheduler cleanup remain observable", func(t *testing.T) {
		ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
		workflowID, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
		if _, err := workflowStore.CancelTask(ctx, task.ID, "canceled"); err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
		observations := taskStatusTestExactRunningObservations(task.ID, started, claimed)
		list, board, detail, search := newTaskStatusTestReadModels(t, metadataStore, workflowStore, observations)
		assertTaskStatusTestReadSurfaces(t, ctx, binding.ProjectID, workflowID, task, list, board, detail, search, "done", serverapi.WorkflowTaskStatusKindCanceled, false, false)
	})

	t.Run("retired script with durable active and scheduler running stays active", func(t *testing.T) {
		ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
		workflowID, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
		observations := taskStatusTestSchedulerOnlyObservations(task.ID, started, claimed, workflowexecution.SchedulerActiveRunPhaseRunning)
		list, board, detail, search := newTaskStatusTestReadModels(t, metadataStore, workflowStore, observations)
		assertTaskStatusTestReadSurfaces(t, ctx, binding.ProjectID, workflowID, task, list, board, detail, search, "agent", serverapi.WorkflowTaskStatusKindActive, false, false)
	})

	t.Run("durable live question disagreement remains running", func(t *testing.T) {
		ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
		workflowID, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
		if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-disagreement"); err != nil {
			t.Fatalf("SetRunWaitingAsk: %v", err)
		}
		observations := taskStatusTestExactRunningObservations(task.ID, started, claimed)
		list, board, detail, search := newTaskStatusTestReadModels(t, metadataStore, workflowStore, observations)
		assertTaskStatusTestReadSurfaces(t, ctx, binding.ProjectID, workflowID, task, list, board, detail, search, "agent", serverapi.WorkflowTaskStatusKindRunning, true, false)
	})
}

func TestTaskReadModelsStayAvailableDuringIndefiniteSchedulerStartup(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Held startup", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	permit := workflowexecution.NewMutationPermit()
	starter := newTaskStatusStartupBarrier()
	scheduler, err := workflowexecution.NewSchedulerService(workflowStore, starter, permit, workflowexecution.SchedulerConfig{Concurrency: 1})
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })
	snapshots, err := newTaskStatusSnapshotCoordinator(metadataStore.DB(), metadataStore.Queries(), permit, authority, scheduler)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}
	list, board, detail, search := newTaskStatusTestReadModelsWithSnapshots(t, metadataStore, workflowStore, snapshots)

	startDone := make(chan error, 1)
	go func() {
		startDone <- scheduler.StartExplicitRuns(context.Background(), []workflow.RunID{started.RunID})
	}()
	select {
	case <-starter.Entered():
	case <-t.Context().Done():
		t.Fatalf("wait for held scheduler startup: %v", t.Context().Err())
	}
	t.Cleanup(starter.Unblock)
	activeRuns := scheduler.ActiveRunSnapshot()
	if len(activeRuns.ActiveRuns) != 1 || activeRuns.ActiveRuns[0].Phase != workflowexecution.SchedulerActiveRunPhaseStarting {
		t.Fatalf("held startup scheduler observation = %+v, want one starting run", activeRuns)
	}

	assertTaskStatusTestReadSurfaces(t, ctx, binding.ProjectID, workflowID, task, list, board, detail, search, "agent", serverapi.WorkflowTaskStatusKindActive, false, false)
	searchResponse, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       "Body",
		Context:     serverapi.TaskSearchDefaultContext,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search during held startup: %v", err)
	}
	if len(searchResponse.Groups) != 1 ||
		searchResponse.Groups[0].TaskID != string(task.ID) ||
		searchResponse.Groups[0].Status.Kind != serverapi.WorkflowTaskStatusKindActive {
		t.Fatalf("search during held startup = %+v, want task %q active", searchResponse, task.ID)
	}

	starter.Unblock()
	if err := <-startDone; err != nil {
		t.Fatalf("StartExplicitRuns: %v", err)
	}
	scheduler.RuntimeFinished(started.RunID, 1)
}

func TestBoardAndDetailActionsUseFinalCapturedStatusObservation(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
	observations := taskStatusTestExactRunningObservations(task.ID, started, claimed)

	detailAuthority := &taskStatusTestAuthoritySource{snapshot: observations.authority, maximumCalls: 2}
	detailSnapshots := newTaskStatusTestSnapshotCoordinator(t, metadataStore, detailAuthority, observations.scheduler)
	detail, err := NewTaskDetail(metadataStore, NewTaskProjector(), detailSnapshots)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	detailValue, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !detailValue.Actions.CanInterrupt || len(detailValue.CurrentScripts) != 1 {
		t.Fatalf("detail actions/targets = %+v, want captured exact live execution", detailValue)
	}
	if detailAuthority.calls != 2 {
		t.Fatalf("detail authority reads = %d, want capture's two stable reads only", detailAuthority.calls)
	}

	boardAuthority := &taskStatusTestAuthoritySource{snapshot: observations.authority, maximumCalls: 2}
	boardSnapshots := newTaskStatusTestSnapshotCoordinator(t, metadataStore, boardAuthority, observations.scheduler)
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	board, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), NewTaskProjector(), boardSnapshots)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	definition, _, err := workflowStore.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentNode := workflowViewNodeByKey(t, definition, "agent")
	cards, err := board.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      string(workflow.NodeIDOf(agentNode)),
	})
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 || !cards.Cards[0].Actions.CanInterrupt {
		t.Fatalf("board actions = %+v, want captured exact live execution", cards.Cards)
	}
	if boardAuthority.calls != 2 {
		t.Fatalf("board authority reads = %d, want capture's two stable reads only", boardAuthority.calls)
	}
}

type taskStatusTestObservations struct {
	authority sessionruntime.AllWorkflowExecutionSnapshot
	scheduler workflowexecution.SchedulerActiveRunSnapshot
}

type taskStatusTestAuthoritySource struct {
	snapshot     sessionruntime.AllWorkflowExecutionSnapshot
	maximumCalls int
	calls        int
}

func (s *taskStatusTestAuthoritySource) AllWorkflowExecutionSnapshot() (sessionruntime.AllWorkflowExecutionSnapshot, error) {
	s.calls++
	if s.maximumCalls > 0 && s.calls > s.maximumCalls {
		return sessionruntime.AllWorkflowExecutionSnapshot{}, fmt.Errorf("authority snapshot read %d exceeds final captured observation", s.calls)
	}
	return s.snapshot, nil
}

type taskStatusTestSchedulerSource struct {
	snapshot workflowexecution.SchedulerActiveRunSnapshot
}

func (s taskStatusTestSchedulerSource) ActiveRunSnapshot() workflowexecution.SchedulerActiveRunSnapshot {
	return s.snapshot
}

func newTaskStatusTestReadModels(
	t *testing.T,
	metadataStore *metadata.Store,
	workflowStore *workflowstore.Store,
	observations taskStatusTestObservations,
) (*TaskList, *Board, *TaskDetail, *TaskSearch) {
	t.Helper()
	snapshots := newTaskStatusTestSnapshotCoordinator(t, metadataStore, &taskStatusTestAuthoritySource{snapshot: observations.authority}, observations.scheduler)
	return newTaskStatusTestReadModelsWithSnapshots(t, metadataStore, workflowStore, snapshots)
}

func newTaskStatusTestReadModelsWithSnapshots(
	t *testing.T,
	metadataStore *metadata.Store,
	workflowStore *workflowstore.Store,
	snapshots *TaskStatusSnapshotCoordinator,
) (*TaskList, *Board, *TaskDetail, *TaskSearch) {
	t.Helper()
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	projector := NewTaskProjector()
	list, err := NewTaskList(metadataStore, definitions, projector, snapshots)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	board, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projector, snapshots)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	detail, err := NewTaskDetail(metadataStore, projector, snapshots)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, projector, snapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	return list, board, detail, search
}

func newTaskStatusTestSnapshotCoordinator(
	t *testing.T,
	metadataStore *metadata.Store,
	authority workflowExecutionObservationSource,
	scheduler workflowexecution.SchedulerActiveRunSnapshot,
) *TaskStatusSnapshotCoordinator {
	t.Helper()
	coordinator, err := newTaskStatusSnapshotCoordinator(
		metadataStore.DB(),
		metadataStore.Queries(),
		workflowexecution.NewMutationPermit(),
		authority,
		taskStatusTestSchedulerSource{snapshot: scheduler},
	)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}
	return coordinator
}

func newClaimedTaskStatusTestFixture(
	t *testing.T,
	ctx context.Context,
	workflowStore *workflowstore.Store,
	projectID string,
) (workflow.WorkflowID, workflowstore.TaskRecord, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
	t.Helper()
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: projectID, Title: "Held lifecycle", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, claimed := claimTaskStatusTestRun(t, ctx, workflowStore, task.ID)
	return workflowID, task, started, claimed
}

func claimTaskStatusTestRun(
	t *testing.T,
	ctx context.Context,
	store *workflowstore.Store,
	taskID workflow.TaskID,
) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
	t.Helper()
	started, err := store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	return started, claimed
}

func taskStatusTestExactRunningObservations(
	taskID workflow.TaskID,
	started workflowstore.StartTaskResult,
	claimed workflowstore.RunnableRunRecord,
) taskStatusTestObservations {
	return taskStatusTestExactObservations(taskID, started, claimed, false)
}

func taskStatusTestExactQuestionObservations(
	taskID workflow.TaskID,
	started workflowstore.StartTaskResult,
	claimed workflowstore.RunnableRunRecord,
) taskStatusTestObservations {
	return taskStatusTestExactObservations(taskID, started, claimed, true)
}

func taskStatusTestSchedulerOnlyObservations(
	taskID workflow.TaskID,
	started workflowstore.StartTaskResult,
	claimed workflowstore.RunnableRunRecord,
	phase workflowexecution.SchedulerActiveRunPhase,
) taskStatusTestObservations {
	return taskStatusTestObservations{
		authority: sessionruntime.AllWorkflowExecutionSnapshot{Executions: []sessionruntime.WorkflowExecutionObservation{}},
		scheduler: workflowexecution.SchedulerActiveRunSnapshot{
			Revision: 1,
			ActiveRuns: []workflowexecution.SchedulerActiveRunObservation{{
				RunID:       started.RunID,
				TaskID:      taskID,
				PlacementID: claimed.PlacementID,
				NodeID:      claimed.NodeID,
				Generation:  claimed.Generation,
				Phase:       phase,
			}},
		},
	}
}

func taskStatusTestExactObservations(
	taskID workflow.TaskID,
	started workflowstore.StartTaskResult,
	claimed workflowstore.RunnableRunRecord,
	waitingQuestion bool,
) taskStatusTestObservations {
	schedulerOnly := taskStatusTestSchedulerOnlyObservations(taskID, started, claimed, workflowexecution.SchedulerActiveRunPhaseRunning)
	schedulerOnly.authority = sessionruntime.AllWorkflowExecutionSnapshot{
		ExecutionMapRevision: 1,
	}
	execution := sessionruntime.TaskExecution{
		Ref: sessionruntime.WorkflowExecutionRef{
			TaskID:     taskID,
			RunID:      started.RunID,
			Generation: claimed.Generation,
		},
		WaitingQuestion: waitingQuestion,
	}
	if waitingQuestion {
		execution.Agent = &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()}
	} else {
		execution.Script = &sessionruntime.TaskScriptExecutionTarget{Path: "/bin/true"}
	}
	schedulerOnly.authority.Executions = []sessionruntime.WorkflowExecutionObservation{{Execution: execution}}
	return schedulerOnly
}

func assertTaskStatusTestReadSurfaces(
	t *testing.T,
	ctx context.Context,
	projectID string,
	workflowID workflow.WorkflowID,
	task workflowstore.TaskRecord,
	list *TaskList,
	board *Board,
	detail *TaskDetail,
	search *TaskSearch,
	nodeKey string,
	wantStatus serverapi.WorkflowTaskStatusKind,
	wantInterrupt bool,
	wantQuestion bool,
) {
	t.Helper()
	workflowIDValue := string(workflowID)
	listResponse, err := list.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &workflowIDValue,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{wantStatus},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listResponse.Tasks) != 1 || listResponse.Tasks[0].TaskID != string(task.ID) || listResponse.Tasks[0].Status.Kind != wantStatus {
		t.Fatalf("list = %+v, want task %q status %q", listResponse.Tasks, task.ID, wantStatus)
	}

	detailValue, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detailValue.Status.Kind != wantStatus || detailValue.Actions.CanInterrupt != wantInterrupt {
		t.Fatalf("detail = %+v, want status %q interrupt=%t", detailValue, wantStatus, wantInterrupt)
	}
	if containsTaskStatusAttention(detailValue.Status.AttentionTypes, serverapi.WorkflowTaskAttentionKindQuestion) != wantQuestion {
		t.Fatalf("detail attention = %+v, want question=%t", detailValue.Status.AttentionTypes, wantQuestion)
	}

	boardResponse, err := board.Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   projectID,
		WorkflowID:  &workflowIDValue,
	})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	column := workflowViewColumnByKey(t, boardResponse, nodeKey)
	cards, err := board.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   projectID,
		WorkflowID:  workflowIDValue,
		NodeID:      column.Node.NodeID,
	})
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 || cards.Cards[0].TaskID != string(task.ID) ||
		cards.Cards[0].Status.Kind != wantStatus ||
		cards.Cards[0].Actions.CanInterrupt != wantInterrupt {
		t.Fatalf("board cards = %+v, want task %q status %q interrupt=%t", cards.Cards, task.ID, wantStatus, wantInterrupt)
	}
	if containsTaskStatusAttention(cards.Cards[0].Status.AttentionTypes, serverapi.WorkflowTaskAttentionKindQuestion) != wantQuestion {
		t.Fatalf("board attention = %+v, want question=%t", cards.Cards[0].Status.AttentionTypes, wantQuestion)
	}

	searchResponse, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       "Body",
		Context:     serverapi.TaskSearchDefaultContext,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{wantStatus},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(searchResponse.Groups) != 1 ||
		searchResponse.Groups[0].TaskID != string(task.ID) ||
		searchResponse.Groups[0].Status.Kind != wantStatus {
		t.Fatalf("search = %+v, want task %q status %q", searchResponse, task.ID, wantStatus)
	}
}

func containsTaskStatusAttention(values []serverapi.WorkflowTaskAttentionKind, want serverapi.WorkflowTaskAttentionKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type taskStatusStartupBarrier struct {
	*testsetup.StartBarrier
}

func newTaskStatusStartupBarrier() *taskStatusStartupBarrier {
	return &taskStatusStartupBarrier{
		StartBarrier: testsetup.NewStartBarrier(),
	}
}

func (s *taskStatusStartupBarrier) PrepareWorkflowRun(context.Context, workflowexecution.SchedulerPrepareRunRequest) (workflowexecution.PreparedWorkflowRun, error) {
	return taskStatusStartupBarrierPreparedRun{barrier: s.StartBarrier}, nil
}

type taskStatusStartupBarrierPreparedRun struct {
	barrier *testsetup.StartBarrier
}

func (taskStatusStartupBarrierPreparedRun) Admission() workflowexecution.RunAdmission {
	return workflowexecution.RunAdmission{}
}

func (taskStatusStartupBarrierPreparedRun) Commit() error {
	return nil
}

func (p taskStatusStartupBarrierPreparedRun) Activate() {
	if err := p.barrier.ArriveAndWait(context.Background()); err != nil {
		panic(fmt.Sprintf("held scheduler startup barrier: %v", err))
	}
}

func (taskStatusStartupBarrierPreparedRun) Abort(context.Context) error {
	return nil
}

var _ workflowExecutionObservationSource = (*taskStatusTestAuthoritySource)(nil)
var _ schedulerActiveRunObservationSource = taskStatusTestSchedulerSource{}
var _ workflowexecution.SchedulerRuntimeStarter = (*taskStatusStartupBarrier)(nil)
