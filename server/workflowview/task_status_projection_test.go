package workflowview

import (
	"context"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"

	"github.com/google/uuid"
)

type taskStatusProjectionTestRunner struct{}

type taskStatusProjectionTestAssignmentSteerer struct{}

func (taskStatusProjectionTestAssignmentSteerer) SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return taskStatusProjectionTestAssignmentSteer{}, nil
}

type taskStatusProjectionTestAssignmentSteer struct{}

func (taskStatusProjectionTestAssignmentSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}

type realTaskStatusSurfaces struct {
	fixture    currentNodeViewFixture
	controller *workflowexecution.CurrentNodeController
	detail     *TaskDetail
	list       *TaskList
	search     *TaskSearch
	board      *Board
}

func newRealTaskStatusSurfaces(t *testing.T, requiresApproval bool) realTaskStatusSurfaces {
	t.Helper()
	fixture := newCurrentNodeViewFixture(t, requiresApproval)
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
			t.Errorf("close real status controller: %v", err)
		}
	})
	projection, err := NewTaskStatusProjection(fixture.metadata, fixture.store, NewTaskProjector(), controller)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	definitions := mustDefinitionProjection(t, fixture.store)
	dependencies, err := NewTaskDependencies(fixture.metadata, projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	detail, err := NewTaskDetail(fixture.metadata, projection, dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	list, err := NewTaskList(fixture.metadata, definitions, projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	search, err := NewTaskSearch(fixture.metadata, projection)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	board, err := NewBoard(fixture.metadata, definitions, testsetup.QuestionsEnabled("coder"), projection)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	return realTaskStatusSurfaces{
		fixture:    fixture,
		controller: controller,
		detail:     detail,
		list:       list,
		search:     search,
		board:      board,
	}
}

type realTaskStatusExecution struct {
	handle    sessionruntime.ExecutionHandle
	lease     sessionruntime.WorkflowExecutionLease
	release   chan struct{}
	sessionID string
}

func startRealTaskStatusExecution(
	t *testing.T,
	surfaces realTaskStatusSurfaces,
	task startedCurrentNodeViewTask,
	queued bool,
	request *tools.AskQuestionRequest,
) realTaskStatusExecution {
	t.Helper()
	sessionID := surfaces.fixture.bindCurrentNodeSession(t, task)
	plan := surfaces.fixture.newAgentRuntimePlan(t)
	lease, err := surfaces.fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   surfaces.fixture.binding.ProjectID,
		WorkflowID:  surfaces.fixture.workflowID,
		CurrentNode: task.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	if !queued {
		lease.Release()
	}
	release := make(chan struct{})
	handle, err := surfaces.fixture.authority.StartAgentExecution(t.Context(), sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			if request != nil {
				_, err := surfaces.fixture.authority.AwaitPromptResponse(ctx, scope.ID(), *request)
				return err
			}
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	t.Cleanup(func() {
		lease.Release()
		close(release)
		if err := handle.Stop(context.Background()); err != nil {
			t.Errorf("stop real status execution: %v", err)
		}
	})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, err := surfaces.fixture.authority.CurrentProjectWorkflowTaskExecutionSnapshots(
			surfaces.fixture.binding.ProjectID,
			surfaces.fixture.workflowID,
		)
		if err != nil {
			return false
		}
		executions := snapshots[task.task.ID].Executions
		if len(executions) != 1 {
			return false
		}
		if executions[0].Queued != queued {
			return false
		}
		if request == nil {
			return true
		}
		kind := sessionruntime.PendingPromptKindQuestion
		if request.Approval {
			kind = sessionruntime.PendingPromptKindSessionApproval
		}
		return executions[0].HasPendingPromptKind(kind)
	}, "timed out waiting for stable live Task status execution")
	return realTaskStatusExecution{
		handle:    handle,
		lease:     lease,
		release:   release,
		sessionID: sessionID.String(),
	}
}

func realTaskStatusApprovalRequest() tools.AskQuestionRequest {
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

type staticTaskStatusLiveObservationSource struct {
	observation workflowexecution.WorkflowTaskExecutionObservation
	calls       *int
}

type countingTaskStatusLiveObservationSource struct {
	source TaskStatusLiveObservationSource
	calls  *int
}

func (s countingTaskStatusLiveObservationSource) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	*s.calls++
	return s.source.ObserveWorkflowTaskExecutions(taskIDs)
}

func (s staticTaskStatusLiveObservationSource) ObserveWorkflowTaskExecutions([]workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	if s.calls != nil {
		*s.calls++
	}
	return s.observation, nil
}

func (taskStatusProjectionTestRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return nil
}

func TestTaskStatusProjectionObservationEncodesTypedLiveStateOnce(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
					"task-both": {
						Executions: []sessionruntime.TaskExecution{
							{
								Queued: true,
							},
							{
								Queued: false,
								PendingPrompts: []sessionruntime.PendingPromptReference{{
									ID:   "approval",
									Kind: sessionruntime.PendingPromptKindSessionApproval,
								}},
							},
							{
								Queued: false,
								PendingPrompts: []sessionruntime.PendingPromptReference{{
									ID:   "question",
									Kind: sessionruntime.PendingPromptKindQuestion,
								}},
							},
						},
					},
					"task-empty": {},
					"task-question": {
						Executions: []sessionruntime.TaskExecution{{
							Queued: false,
							PendingPrompts: []sessionruntime.PendingPromptReference{{
								ID:   "question-2",
								Kind: sessionruntime.PendingPromptKindQuestion,
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

	observation, err := projection.Observe([]workflow.TaskID{"task-both", "task-question"})
	if err != nil {
		t.Fatalf("TaskStatusProjection.Observe: %v", err)
	}
	const want = `[{"task_id":"task-both","has_running":true,"has_queued":true,"waiting_question":true,"has_waiting_approval":true},{"task_id":"task-question","has_running":true,"has_queued":false,"waiting_question":true,"has_waiting_approval":false}]`
	if observation.LiveTaskStatesJSON != want {
		t.Fatalf("encoded live task states = %s, want %s", observation.LiveTaskStatesJSON, want)
	}
}

func TestTaskStatusProjectionProjectsRetainedDurableAndLiveFactsInEitherCaptureOrder(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "mixed projection")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	taskID := started.task.ID
	calls := 0
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			calls: &calls,
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
					taskID: {
						Executions: []sessionruntime.TaskExecution{{
							Ref: sessionruntime.WorkflowExecutionRef{
								ProjectID:   fixture.binding.ProjectID,
								WorkflowID:  fixture.workflowID,
								CurrentNode: started.currentNode,
							},
							Agent: &sessionruntime.TaskAgentExecutionTarget{SessionID: sessionID},
							PendingPrompts: []sessionruntime.PendingPromptReference{{
								ID:   "question",
								Kind: sessionruntime.PendingPromptKindQuestion,
							}},
						}},
					},
				},
				Quiescence: map[workflow.TaskID]bool{taskID: false},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	assertProjection := func(t *testing.T, observation TaskStatusObservation, durable *TaskStatusDurableSnapshot) {
		t.Helper()
		results, err := projection.Project(t.Context(), observation, durable, []workflow.TaskID{taskID})
		if err != nil {
			t.Fatalf("TaskStatusProjection.Project: %v", err)
		}
		result, ok := results[taskID]
		if !ok {
			t.Fatalf("projection omitted task %q", taskID)
		}
		if result.Status.Kind != "waiting_question" ||
			result.Actions.CanInterrupt ||
			result.Actions.CanDelete ||
			len(result.LiveSessionIDs) != 1 ||
			result.LiveSessionIDs[0] != sessionID.String() ||
			result.AttentionCount != 1 ||
			len(result.CurrentNodes) != 1 {
			t.Fatalf("mixed projection result = %+v", result)
		}
	}

	t.Run("durable before live", func(t *testing.T) {
		err := projection.WithDurableSnapshot(t.Context(), func(durable *TaskStatusDurableSnapshot) error {
			observation, err := projection.Observe([]workflow.TaskID{taskID})
			if err != nil {
				return err
			}
			assertProjection(t, observation, durable)
			return nil
		})
		if err != nil {
			t.Fatalf("durable-before-live projection: %v", err)
		}
	})
	t.Run("live before durable", func(t *testing.T) {
		observation, err := projection.Observe([]workflow.TaskID{taskID})
		if err != nil {
			t.Fatalf("capture live observation: %v", err)
		}
		err = projection.WithDurableSnapshot(t.Context(), func(durable *TaskStatusDurableSnapshot) error {
			assertProjection(t, observation, durable)
			return nil
		})
		if err != nil {
			t.Fatalf("live-before-durable projection: %v", err)
		}
	})
	if calls != 2 {
		t.Fatalf("live observation calls = %d, want one per request", calls)
	}
}

func TestTaskStatusProjectionDurableSnapshotRetainsOneCurrentNodeGeneration(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "durable snapshot")
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
	projection, err := NewTaskStatusProjection(fixture.metadata, fixture.store, NewTaskProjector(), controller)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	err = projection.WithDurableSnapshot(t.Context(), func(snapshot *TaskStatusDurableSnapshot) error {
		before, err := snapshot.CurrentNodesByTask(t.Context(), []workflow.TaskID{started.task.ID})
		if err != nil {
			return err
		}
		if len(before[started.task.ID]) != 1 || before[started.task.ID][0].Scheduling == nil {
			t.Fatalf("before Current Nodes = %+v", before)
		}
		beforeState := before[started.task.ID][0].Scheduling.State

		if err := fixture.store.InterruptCurrentNode(
			t.Context(),
			started.currentNode,
			"projection_test",
			workflow.CurrentNodeInterruptionDetail{Code: "projection_test"},
		); err != nil {
			return err
		}
		after, err := snapshot.CurrentNodesByTask(t.Context(), []workflow.TaskID{started.task.ID})
		if err != nil {
			return err
		}
		if len(after[started.task.ID]) != 1 || after[started.task.ID][0].Scheduling == nil {
			t.Fatalf("after Current Nodes = %+v", after)
		}
		if got := after[started.task.ID][0].Scheduling.State; got != beforeState {
			t.Fatalf("snapshot escaped transaction: state = %q, want pre-mutation state %q", got, beforeState)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithDurableSnapshot: %v", err)
	}

	var observedState workflow.CurrentNodeSchedulingState
	err = projection.WithDurableSnapshot(t.Context(), func(snapshot *TaskStatusDurableSnapshot) error {
		nodes, err := snapshot.CurrentNodesByTask(t.Context(), []workflow.TaskID{started.task.ID})
		if err != nil {
			return err
		}
		if len(nodes[started.task.ID]) != 1 || nodes[started.task.ID][0].Scheduling == nil {
			t.Fatalf("post-mutation Current Nodes = %+v", nodes)
		}
		observedState = nodes[started.task.ID][0].Scheduling.State
		return nil
	})
	if err != nil {
		t.Fatalf("WithDurableSnapshot post-mutation: %v", err)
	}
	if observedState != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("new snapshot state = %q, want interrupted", observedState)
	}
}

func TestTaskStatusDurableSnapshotCannotEscapeProjectionCallback(t *testing.T) {
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
	projection, err := NewTaskStatusProjection(fixture.metadata, fixture.store, NewTaskProjector(), controller)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	var retained *TaskStatusDurableSnapshot
	if err := projection.WithDurableSnapshot(t.Context(), func(snapshot *TaskStatusDurableSnapshot) error {
		retained = snapshot
		return nil
	}); err != nil {
		t.Fatalf("WithDurableSnapshot: %v", err)
	}
	if retained == nil {
		t.Fatal("projection did not provide a durable snapshot")
	}
	if _, err := retained.CurrentNodesByTask(t.Context(), []workflow.TaskID{"missing"}); err == nil {
		t.Fatal("retained durable snapshot remained usable after callback")
	}
}
