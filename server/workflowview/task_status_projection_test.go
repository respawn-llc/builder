package workflowview

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

type taskStatusProjectionTestRunner struct{}

type taskStatusProjectionTestAssignmentSteerer struct{}

type taskStatusLiveTarget interface {
	taskStatusLiveTarget()
}

type taskStatusNoLiveTarget struct{}

func (taskStatusNoLiveTarget) taskStatusLiveTarget() {}

type taskStatusAgentTarget struct {
	sessionID runtimeids.SessionID
}

func (taskStatusAgentTarget) taskStatusLiveTarget() {}

type taskStatusScriptTarget struct {
	path string
}

func (taskStatusScriptTarget) taskStatusLiveTarget() {}

type taskStatusExpectedTarget struct {
	nodeID workflow.NodeID
	live   taskStatusLiveTarget
}

type controllerBackedTaskStatusRunner struct {
	authority *sessionruntime.Authority
	mu        sync.Mutex
	configs   map[workflow.TaskID]controllerBackedTaskStatusExecution
}

type controllerBackedTaskStatusExecution struct {
	target           taskStatusLiveTarget
	plan             sessionruntime.AgentRuntimePlan
	request          *tools.AskQuestionRequest
	queued           bool
	admissionRelease <-chan struct{}
	executionRelease <-chan struct{}
	started          chan<- sessionruntime.ExecutionHandle
}

func newControllerBackedTaskStatusRunner(authority *sessionruntime.Authority) *controllerBackedTaskStatusRunner {
	return &controllerBackedTaskStatusRunner{
		authority: authority,
		configs:   make(map[workflow.TaskID]controllerBackedTaskStatusExecution),
	}
}

func (r *controllerBackedTaskStatusRunner) configure(
	taskID workflow.TaskID,
	config controllerBackedTaskStatusExecution,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs[taskID] = config
}

func (r *controllerBackedTaskStatusRunner) StartCurrentNodeWithPreparation(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowexecution.LaunchPreparation,
	_ workflowruntime.TaskPromptDelivery,
	_ workflowexecution.CurrentNodeAssignmentSteer,
	lease sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	r.mu.Lock()
	config, ok := r.configs[reference.TaskID]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("no test execution configured for Task %q", reference.TaskID)
	}
	switch target := config.target.(type) {
	case taskStatusScriptTarget:
		lease.Release()
		handle, err := r.authority.StartScriptExecution(ctx, sessionruntime.ScriptExecutionRequest{
			Workflow: &lease,
			Command: sessionruntime.ScriptCommand{
				Path: target.path,
				Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			},
		})
		if err != nil {
			return err
		}
		config.started <- handle
		return nil
	case taskStatusAgentTarget:
		descriptor, err := session.NewOpenSessionDescriptor(target.sessionID)
		if err != nil {
			return err
		}
		if !config.queued {
			lease.Release()
		}
		handle, err := r.authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
			Descriptor: descriptor,
			Runtime:    &config.plan,
			Workflow:   &lease,
			Resource:   sessionruntime.OpenAgentResource{},
			Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
				if config.request != nil {
					_, err := r.authority.AwaitPromptResponse(ctx, scope.ID(), *config.request)
					return err
				}
				<-config.executionRelease
				return nil
			},
		})
		if err != nil {
			return err
		}
		config.started <- handle
		if config.queued {
			select {
			case <-config.admissionRelease:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		return nil
	default:
		return errors.New("unsupported test execution target")
	}
}

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
	runner     *controllerBackedTaskStatusRunner
	detail     *TaskDetail
	list       *TaskList
	search     *TaskSearch
	board      *Board
}

func newRealTaskStatusSurfaces(t *testing.T, requiresApproval bool) realTaskStatusSurfaces {
	t.Helper()
	fixture := newCurrentNodeViewFixture(t, requiresApproval)
	runner := newControllerBackedTaskStatusRunner(fixture.authority)
	controller, err := workflowexecution.NewCurrentNodeController(
		fixture.store,
		runner,
		fixture.authority,
		workflowexecution.NewMutationPermit(),
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: taskStatusProjectionTestAssignmentSteerer{},
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
	dependencies, err := NewTaskDependencies(fixture.metadata, projection, fixture.dependencyCounter)
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
		runner:     runner,
		detail:     detail,
		list:       list,
		search:     search,
		board:      board,
	}
}

type realTaskStatusExecution struct {
	handle  sessionruntime.ExecutionHandle
	release chan struct{}
	target  taskStatusLiveTarget
}

func startRealTaskStatusExecution(
	t *testing.T,
	surfaces realTaskStatusSurfaces,
	backlog startedCurrentNodeViewTask,
	queued bool,
	request *tools.AskQuestionRequest,
) (startedCurrentNodeViewTask, realTaskStatusExecution) {
	sessionID := surfaces.fixture.newCurrentNodeViewSession(t)
	return startControllerBackedTaskStatusExecution(t, surfaces, backlog, controllerBackedTaskStatusExecution{
		target:  taskStatusAgentTarget{sessionID: sessionID},
		queued:  queued,
		request: request,
	})
}

func startRealTaskStatusScriptExecution(
	t *testing.T,
	surfaces realTaskStatusSurfaces,
	backlog startedCurrentNodeViewTask,
	scriptPath string,
) (startedCurrentNodeViewTask, realTaskStatusExecution) {
	t.Helper()
	return startControllerBackedTaskStatusExecution(t, surfaces, backlog, controllerBackedTaskStatusExecution{
		target: taskStatusScriptTarget{path: scriptPath},
	})
}

func startControllerBackedTaskStatusExecution(
	t *testing.T,
	surfaces realTaskStatusSurfaces,
	backlog startedCurrentNodeViewTask,
	options controllerBackedTaskStatusExecution,
) (startedCurrentNodeViewTask, realTaskStatusExecution) {
	t.Helper()
	release := make(chan struct{})
	admissionRelease := make(chan struct{})
	startedHandle := make(chan sessionruntime.ExecutionHandle, 1)
	options.plan = surfaces.fixture.newAgentRuntimePlan(t)
	options.admissionRelease = admissionRelease
	options.executionRelease = release
	options.started = startedHandle
	surfaces.runner.configure(backlog.task.ID, options)
	startedResult, err := surfaces.controller.StartTaskWithPreparation(t.Context(), backlog.task.ID, workflowexecution.EstablishedRootLaunchPreparation())
	if err != nil {
		t.Fatalf("StartTaskWithPreparation: %v", err)
	}
	if len(startedResult.Mutation.Created) != 1 {
		t.Fatalf("StartTaskWithExecutionTarget mutation = %+v, want one Current Node", startedResult.Mutation)
	}
	task := startedCurrentNodeViewTask{
		task:        backlog.task,
		currentNode: startedResult.Mutation.Created[0].Reference,
	}
	agentTarget, isAgent := options.target.(taskStatusAgentTarget)
	if isAgent {
		if _, err := surfaces.fixture.store.BindSessionToCurrentNode(t.Context(), workflowstore.CurrentNodeSessionBindingRequest{
			Association: workflowstore.TaskSessionAssociationRequest{
				SessionID:    agentTarget.sessionID,
				CurrentNode:  task.currentNode,
				AssociatedAt: time.Now().UTC(),
			},
		}); err != nil {
			t.Fatalf("BindSessionToCurrentNode: %v", err)
		}
	}
	var handle sessionruntime.ExecutionHandle
	select {
	case handle = <-startedHandle:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Controller-admitted execution")
	}
	t.Cleanup(func() {
		close(admissionRelease)
		close(release)
		if err := handle.Stop(context.Background()); err != nil {
			t.Errorf("stop real status execution: %v", err)
		}
	})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		observation, err := surfaces.controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{task.task.ID})
		if err != nil {
			return false
		}
		if observation.Quiescence[task.task.ID] {
			return false
		}
		executions := observation.Executions[task.task.ID].Executions
		if len(executions) != 1 {
			return false
		}
		if executions[0].Queued != options.queued {
			return false
		}
		if scriptTarget, ok := options.target.(taskStatusScriptTarget); ok {
			return executions[0].Script != nil && executions[0].Script.Path == scriptTarget.path
		}
		if options.request == nil {
			return true
		}
		kind := sessionruntime.PendingPromptKindQuestion
		if options.request.Approval {
			kind = sessionruntime.PendingPromptKindSessionApproval
		}
		return executions[0].HasPendingPromptKind(kind)
	}, "timed out waiting for stable live Task status execution")
	return task, realTaskStatusExecution{
		handle:  handle,
		release: release,
		target:  options.target,
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

func (taskStatusProjectionTestRunner) StartCurrentNodeWithPreparation(
	context.Context,
	workflow.CurrentNodeReference,
	workflowexecution.LaunchPreparation,
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
			AgentConcurrency:  1,
			AssignmentSteerer: taskStatusProjectionTestAssignmentSteerer{},
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
			AgentConcurrency:  1,
			AssignmentSteerer: taskStatusProjectionTestAssignmentSteerer{},
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
