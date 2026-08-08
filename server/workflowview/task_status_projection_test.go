package workflowview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type taskStatusProjectionTestRunner struct{}

type taskStatusProjectionTestAssignmentEnsurer struct{}

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
	nodeID       workflow.NodeID
	live         taskStatusLiveTarget
	runtimeOwned bool
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

func (r *controllerBackedTaskStatusRunner) StartCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ workflowexecution.CurrentNodeAssignmentEnsure,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
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
			Descriptor:         descriptor,
			Runtime:            &config.plan,
			Workflow:           &lease,
			Resource:           sessionruntime.OpenAgentResource{},
			PromptFeed:         currentNodeExecutionPromptFeedForTest(controller),
			RunningPublication: currentNodeRunningPublicationForTest(controller),
			Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
				if config.request != nil {
					_, err := r.authority.AwaitPromptResponse(ctx, scope.ID(), *config.request)
					return err
				}
				select {
				case <-config.executionRelease:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
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

func currentNodeExecutionPromptFeedForTest(
	controller workflowruntime.Controller,
) sessionruntime.ExecutionPromptFeed {
	feed, _ := controller.(sessionruntime.ExecutionPromptFeed)
	return feed
}

func currentNodeRunningPublicationForTest(
	controller workflowruntime.Controller,
) sessionruntime.WorkflowRunningPublication {
	publication, _ := controller.(sessionruntime.WorkflowRunningPublication)
	return publication
}

func (taskStatusProjectionTestAssignmentEnsurer) EnsureCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
) (workflowexecution.CurrentNodeAssignmentEnsure, error) {
	return taskStatusProjectionTestAssignmentEnsure{}, nil
}

type taskStatusProjectionTestAssignmentEnsure struct{}

func (taskStatusProjectionTestAssignmentEnsure) Wait(context.Context) (session.CommitReceipt, error) {
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
	projection *TaskStatusProjection
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
			AssignmentEnsurer: taskStatusProjectionTestAssignmentEnsurer{},
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
		runner:     runner,
		detail:     detail,
		list:       list,
		search:     search,
		board:      board,
		projection: projection,
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
	startedResult, err := surfaces.controller.StartTask(t.Context(), backlog.task.ID, func(ctx context.Context) error {
		return surfaces.fixture.store.LockTaskExecutionTarget(ctx, backlog.task.ID, &workflowstore.ExecutionTargetCandidate{
			Snapshot: workflowstore.ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: workflowstore.ExecutionTargetProvenanceResolved,
			},
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   surfaces.fixture.binding.WorkspaceID,
				SourceWorkspaceRoot: surfaces.fixture.binding.CanonicalRoot,
			},
		})
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(startedResult.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one Current Node", startedResult.Mutation)
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
		handle.RequestStop()
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
		lifecycle, exists := observation.Lifecycle[task.task.ID]
		if options.queued {
			return exists &&
				len(executions) == 0 &&
				len(lifecycle.QueuedCurrentNodes) == 1 &&
				lifecycle.QueuedCurrentNodes[0].Equal(task.currentNode) &&
				len(lifecycle.ExactExecutions) == 0
		}
		if !exists ||
			len(lifecycle.ExactExecutions) != 1 ||
			!lifecycle.ExactExecutions[0].CurrentNode.Equal(task.currentNode) ||
			lifecycle.ExactExecutions[0].ScopeID != handle.Scope().ID() {
			return false
		}
		if len(executions) != 1 {
			return false
		}
		if executions[0].Queued {
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
	store         *workflowstore.Store
	observation   workflowexecution.WorkflowTaskExecutionObservation
	calls         *int
	snapshotCalls *int
	queryCalls    *int
}

type countingTaskStatusLiveObservationSource struct {
	source TaskStatusCaptureSource
	calls  *int
}

func (s countingTaskStatusLiveObservationSource) CaptureWorkflowTaskExecutions(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(workflowexecution.WorkflowTaskExecutionObservation, *sqlitegen.Queries) error,
) error {
	*s.calls++
	return s.source.CaptureWorkflowTaskExecutions(ctx, taskIDs, operation)
}

func (s countingTaskStatusLiveObservationSource) CaptureWorkflowTaskLifecycleQuery(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries) error,
) error {
	*s.calls++
	source, ok := s.source.(TaskStatusLifecycleQuerySource)
	if !ok {
		return errors.New("counted task status source has no lifecycle query capture")
	}
	return source.CaptureWorkflowTaskLifecycleQuery(ctx, operation)
}

func (s countingTaskStatusLiveObservationSource) CaptureWorkflowTaskBoundedLifecycleRead(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries, workflowexecution.WorkflowTaskLifecycleReader) error,
) error {
	*s.calls++
	source, ok := s.source.(TaskStatusBoundedLifecycleSource)
	if !ok {
		return errors.New("counted task status source has no bounded lifecycle capture")
	}
	return source.CaptureWorkflowTaskBoundedLifecycleRead(ctx, operation)
}

func (s staticTaskStatusLiveObservationSource) CaptureWorkflowTaskExecutions(
	ctx context.Context,
	_ []workflow.TaskID,
	operation func(workflowexecution.WorkflowTaskExecutionObservation, *sqlitegen.Queries) error,
) error {
	if s.calls != nil {
		*s.calls++
	}
	if s.snapshotCalls != nil {
		*s.snapshotCalls++
	}
	return captureWorkflowViewTestObservation(ctx, s.store, s.observation, operation)
}

func (s staticTaskStatusLiveObservationSource) CaptureWorkflowTaskLifecycleQuery(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries) error,
) error {
	if s.queryCalls != nil {
		*s.queryCalls++
	}
	token, release, err := sqlitegen.RegisterLifecycleTaskStateResolver(func(taskID string) (sqlitegen.LifecycleTaskQueryState, error) {
		return testLifecycleTaskState(s.observation, workflow.TaskID(taskID))
	})
	if err != nil {
		return err
	}
	defer release()
	return captureWorkflowViewTestObservation(
		ctx,
		s.store,
		s.observation,
		func(_ workflowexecution.WorkflowTaskExecutionObservation, queries *sqlitegen.Queries) error {
			return operation(token, queries)
		},
	)
}

func (s staticTaskStatusLiveObservationSource) CaptureWorkflowTaskBoundedLifecycleRead(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries, workflowexecution.WorkflowTaskLifecycleReader) error,
) error {
	if s.queryCalls != nil {
		*s.queryCalls++
	}
	return captureWorkflowViewTestBoundedLifecycle(ctx, s.store, s.observation, operation)
}

type workflowViewTestLifecycleReader struct {
	observation workflowexecution.WorkflowTaskExecutionObservation
}

func (r workflowViewTestLifecycleReader) ObserveSelected(
	_ context.Context,
	taskIDs []workflow.TaskID,
) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	selected := workflowexecution.WorkflowTaskExecutionObservation{
		Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{},
		Quiescence: map[workflow.TaskID]bool{},
		Lifecycle:  map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{},
	}
	for _, taskID := range taskIDs {
		if execution, exists := r.observation.Executions[taskID]; exists {
			selected.Executions[taskID] = execution
		}
		if quiescent, exists := r.observation.Quiescence[taskID]; exists {
			selected.Quiescence[taskID] = quiescent
		} else {
			_, owned := r.observation.Lifecycle[taskID]
			selected.Quiescence[taskID] = !owned
		}
		if lifecycle, exists := r.observation.Lifecycle[taskID]; exists {
			selected.Lifecycle[taskID] = lifecycle
		}
	}
	return selected, nil
}

func (r workflowViewTestLifecycleReader) PendingQuestions(
	cursor workflowstore.LifecycleQuestionCursor,
	limit int,
) ([]workflowstore.LifecyclePendingQuestion, error) {
	questions := lifecyclePendingQuestions(r.observation, nil)
	type indexedQuestion struct {
		question workflowstore.LifecyclePendingQuestion
		itemID   string
	}
	indexed := make([]indexedQuestion, 0, len(questions))
	for _, question := range questions {
		itemID, err := workflowstore.LifecycleQuestionItemID(question.SessionID, question.Prompt.ID)
		if err != nil {
			return nil, err
		}
		indexed = append(indexed, indexedQuestion{question: question, itemID: itemID})
	}
	sort.Slice(indexed, func(i, j int) bool {
		leftTime := indexed[i].question.Prompt.CreatedAt.UnixMilli()
		rightTime := indexed[j].question.Prompt.CreatedAt.UnixMilli()
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return indexed[i].itemID > indexed[j].itemID
	})
	out := make([]workflowstore.LifecyclePendingQuestion, 0, min(limit, len(indexed)))
	for _, candidate := range indexed {
		question := candidate.question
		itemID := candidate.itemID
		occurredAt := question.Prompt.CreatedAt.UnixMilli()
		if cursor.HasValue && (occurredAt > cursor.OccurredAtUnixMs ||
			(occurredAt == cursor.OccurredAtUnixMs && itemID >= cursor.ItemID)) {
			continue
		}
		out = append(out, question)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func captureWorkflowViewTestBoundedLifecycle(
	ctx context.Context,
	store *workflowstore.Store,
	observation workflowexecution.WorkflowTaskExecutionObservation,
	operation func(string, *sqlitegen.Queries, workflowexecution.WorkflowTaskLifecycleReader) error,
) error {
	token, release, err := sqlitegen.RegisterLifecycleTaskStateResolver(func(taskID string) (sqlitegen.LifecycleTaskQueryState, error) {
		return testLifecycleTaskState(observation, workflow.TaskID(taskID))
	})
	if err != nil {
		return err
	}
	defer release()
	return captureWorkflowViewTestObservation(
		ctx,
		store,
		observation,
		func(_ workflowexecution.WorkflowTaskExecutionObservation, queries *sqlitegen.Queries) error {
			return operation(token, queries, workflowViewTestLifecycleReader{observation: observation})
		},
	)
}

func testLifecycleTaskState(
	observation workflowexecution.WorkflowTaskExecutionObservation,
	taskID workflow.TaskID,
) (sqlitegen.LifecycleTaskQueryState, error) {
	lifecycle, owned := observation.Lifecycle[taskID]
	if !owned {
		return sqlitegen.LifecycleTaskQueryState{}, nil
	}
	executions := make([]workflowstore.LifecycleTaskExecutionStatus, 0, len(lifecycle.ExactExecutions))
	for _, exact := range lifecycle.ExactExecutions {
		execution := workflowstore.LifecycleTaskExecutionStatus{CurrentNode: exact.CurrentNode}
		for _, prompt := range exact.PendingPrompts {
			switch prompt.Kind {
			case workflowstore.LifecyclePendingPromptQuestion:
				execution.WaitingQuestion = true
			case workflowstore.LifecyclePendingPromptSessionApproval:
				execution.WaitingApproval = true
			default:
				return sqlitegen.LifecycleTaskQueryState{}, fmt.Errorf("invalid test lifecycle prompt kind %d", prompt.Kind)
			}
		}
		executions = append(executions, execution)
	}
	for index := range executions {
		for _, prompt := range observation.Executions[taskID].Executions[index].PendingPrompts {
			switch prompt.Kind {
			case sessionruntime.PendingPromptKindQuestion:
				executions[index].WaitingQuestion = true
			case sessionruntime.PendingPromptKindSessionApproval:
				executions[index].WaitingApproval = true
			default:
				return sqlitegen.LifecycleTaskQueryState{}, fmt.Errorf("invalid test pending prompt kind %q", prompt.Kind)
			}
		}
	}
	status, err := workflowstore.DeriveLifecycleTaskStatus(taskID, lifecycle.QueuedCurrentNodes, executions)
	if err != nil {
		return sqlitegen.LifecycleTaskQueryState{}, err
	}
	flags := sqlitegen.LifecycleTaskStateOwned
	if status.HasRunning {
		flags |= sqlitegen.LifecycleTaskStateRunning
	}
	if status.HasQueued {
		flags |= sqlitegen.LifecycleTaskStateQueued
	}
	if status.WaitingQuestion {
		flags |= sqlitegen.LifecycleTaskStateWaitingQuestion
	}
	if status.WaitingApproval {
		flags |= sqlitegen.LifecycleTaskStateWaitingApproval
	}
	return sqlitegen.LifecycleTaskQueryState{Flags: flags}, nil
}

func (taskStatusProjectionTestRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentEnsure,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return nil
}

func workflowTaskExecutionObservationForTest(
	t testing.TB,
	currentNodes map[workflow.TaskID][]workflow.CurrentNode,
	executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot,
	quiescence map[workflow.TaskID]bool,
) workflowexecution.WorkflowTaskExecutionObservation {
	t.Helper()
	lifecycle := make(map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot, len(currentNodes))
	for taskID, nodes := range currentNodes {
		snapshot := workflowexecution.WorkflowTaskLifecycleSnapshot{
			CurrentNodes: append([]workflow.CurrentNode(nil), nodes...),
		}
		queued := make(map[workflow.CurrentNodeReferenceKey]struct{})
		for _, execution := range executions[taskID].Executions {
			key, err := execution.Ref.CurrentNode.Key()
			if err != nil {
				t.Fatalf("Current Node key: %v", err)
			}
			if _, exists := queued[key]; !exists {
				queued[key] = struct{}{}
				snapshot.QueuedCurrentNodes = append(snapshot.QueuedCurrentNodes, execution.Ref.CurrentNode)
			}
			if !execution.Queued {
				snapshot.ExactExecutions = append(snapshot.ExactExecutions, workflowstore.LifecycleExactExecution{
					CurrentNode: execution.Ref.CurrentNode,
					ScopeID:     execution.ScopeID,
				})
			}
		}
		lifecycle[taskID] = snapshot
	}
	return workflowexecution.WorkflowTaskExecutionObservation{
		Executions: executions,
		Quiescence: quiescence,
		Lifecycle:  lifecycle,
	}
}

func TestTaskStatusProjectionObservationEncodesTypedLiveStateOnce(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	reference := func(taskID workflow.TaskID, nodeID workflow.NodeID) workflow.CurrentNodeReference {
		ref, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
		if err != nil {
			t.Fatalf("NewCurrentNodeReference: %v", err)
		}
		return ref
	}
	bothTaskID := workflow.TaskID("task-both")
	queuedRef := reference(bothTaskID, "node-queued")
	approvalRef := reference(bothTaskID, "node-approval")
	questionRef := reference(bothTaskID, "node-question")
	questionTaskID := workflow.TaskID("task-question")
	questionOnlyRef := reference(questionTaskID, "node-question-only")
	executions := map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
		bothTaskID: {
			Executions: []sessionruntime.TaskExecution{
				{
					Ref: sessionruntime.WorkflowExecutionRef{
						ProjectID:   fixture.binding.ProjectID,
						WorkflowID:  fixture.workflowID,
						CurrentNode: queuedRef,
					},
					ScopeID: runtimeids.NewExecutionScopeID(),
					Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
					Queued:  true,
				},
				{
					Ref: sessionruntime.WorkflowExecutionRef{
						ProjectID:   fixture.binding.ProjectID,
						WorkflowID:  fixture.workflowID,
						CurrentNode: approvalRef,
					},
					ScopeID: runtimeids.NewExecutionScopeID(),
					Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
					PendingPrompts: []sessionruntime.PendingPromptReference{{
						ID:   "approval",
						Kind: sessionruntime.PendingPromptKindSessionApproval,
					}},
				},
				{
					Ref: sessionruntime.WorkflowExecutionRef{
						ProjectID:   fixture.binding.ProjectID,
						WorkflowID:  fixture.workflowID,
						CurrentNode: questionRef,
					},
					ScopeID: runtimeids.NewExecutionScopeID(),
					Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
					PendingPrompts: []sessionruntime.PendingPromptReference{{
						ID:   "question",
						Kind: sessionruntime.PendingPromptKindQuestion,
					}},
				},
			},
		},
		"task-empty": {},
		questionTaskID: {
			Executions: []sessionruntime.TaskExecution{{
				Ref: sessionruntime.WorkflowExecutionRef{
					ProjectID:   fixture.binding.ProjectID,
					WorkflowID:  fixture.workflowID,
					CurrentNode: questionOnlyRef,
				},
				ScopeID: runtimeids.NewExecutionScopeID(),
				Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
				PendingPrompts: []sessionruntime.PendingPromptReference{{
					ID:   "question-2",
					Kind: sessionruntime.PendingPromptKindQuestion,
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
					bothTaskID: {
						{Reference: queuedRef},
						{Reference: approvalRef},
						{Reference: questionRef},
					},
					questionTaskID: {{Reference: questionOnlyRef}},
				},
				executions,
				nil,
			),
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	observation := captureTaskStatusObservation(t, projection, []workflow.TaskID{"task-both", "task-question"})
	const want = `[{"task_id":"task-both","has_lifecycle_override":true,"current_node_ids":["node-approval","node-question","node-queued"],"has_running":true,"has_queued":true,"waiting_question":true,"has_waiting_approval":true},{"task_id":"task-question","has_lifecycle_override":true,"current_node_ids":["node-question-only"],"has_running":true,"has_queued":false,"waiting_question":true,"has_waiting_approval":false}]`
	if observation.LiveTaskStatesJSON != want {
		t.Fatalf("encoded live task states = %s, want %s", observation.LiveTaskStatesJSON, want)
	}
}

func TestTaskStatusProjectionObservationEncodesPinnedLifecycleOverridesAndExactFacts(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	queuedReference, err := workflow.NewCurrentNodeReference("task-queued-root", "node-queued", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference queued: %v", err)
	}
	runningReference, err := workflow.NewCurrentNodeReference("task-running-root", "node-running", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference running: %v", err)
	}
	runningScopeID := runtimeids.NewExecutionScopeID()
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			store: fixture.store,
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
					runningReference.TaskID: {
						Executions: []sessionruntime.TaskExecution{{
							Ref: sessionruntime.WorkflowExecutionRef{
								ProjectID:   fixture.binding.ProjectID,
								WorkflowID:  fixture.workflowID,
								CurrentNode: runningReference,
							},
							ScopeID: runningScopeID,
							Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
							PendingPrompts: []sessionruntime.PendingPromptReference{{
								ID:   "question",
								Kind: sessionruntime.PendingPromptKindQuestion,
							}},
						}},
					},
				},
				Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
					queuedReference.TaskID: {
						CurrentNodes:       []workflow.CurrentNode{{Reference: queuedReference}},
						QueuedCurrentNodes: []workflow.CurrentNodeReference{queuedReference},
					},
					runningReference.TaskID: {
						CurrentNodes:       []workflow.CurrentNode{{Reference: runningReference}},
						QueuedCurrentNodes: []workflow.CurrentNodeReference{runningReference},
						ExactExecutions: []workflowstore.LifecycleExactExecution{{
							CurrentNode: runningReference,
							ScopeID:     runningScopeID,
						}},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	observation := captureTaskStatusObservation(t, projection, []workflow.TaskID{queuedReference.TaskID, runningReference.TaskID})
	const want = `[{"task_id":"task-queued-root","has_lifecycle_override":true,"current_node_ids":["node-queued"],"has_running":false,"has_queued":true,"waiting_question":false,"has_waiting_approval":false},{"task_id":"task-running-root","has_lifecycle_override":true,"current_node_ids":["node-running"],"has_running":true,"has_queued":false,"waiting_question":true,"has_waiting_approval":false}]`
	if observation.LiveTaskStatesJSON != want {
		t.Fatalf("encoded pinned lifecycle states = %s, want %s", observation.LiveTaskStatesJSON, want)
	}
}

func TestTaskStatusDurableSnapshotUsesPinnedCurrentNodeOverrideBeforeProjection(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Pinned override")
	overrideNodeID := "node-pinned-override"
	liveJSON := fmt.Sprintf(
		`[{"task_id":%q,"has_lifecycle_override":true,"current_node_ids":[%q],"has_running":false,"has_queued":true,"waiting_question":false,"has_waiting_approval":false}]`,
		started.task.ID,
		overrideNodeID,
	)

	err := fixture.projection.WithSnapshot(t.Context(), nil, func(_ TaskStatusObservation, snapshot *TaskStatusDurableSnapshot) error {
		statuses, err := snapshot.ProjectedStatuses(t.Context(), []workflow.TaskID{started.task.ID}, liveJSON)
		if err != nil {
			return err
		}
		status := statuses[started.task.ID]
		if status.Status.Kind != serverapi.WorkflowTaskStatusKindQueued {
			t.Fatalf("status kind = %q, want queued", status.Status.Kind)
		}
		if !slices.Equal(status.Status.NodeIDs, []string{overrideNodeID}) {
			t.Fatalf("status Node IDs = %v, want pinned override %q", status.Status.NodeIDs, overrideNodeID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithSnapshot: %v", err)
	}
}

func TestTaskStatusProjectionProjectsRetainedDurableAndLiveFactsFromOneCapture(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "mixed projection")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	taskID := started.task.ID
	scopeID := runtimeids.NewExecutionScopeID()
	executions := map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
		taskID: {
			Executions: []sessionruntime.TaskExecution{{
				Ref: sessionruntime.WorkflowExecutionRef{
					ProjectID:   fixture.binding.ProjectID,
					WorkflowID:  fixture.workflowID,
					CurrentNode: started.currentNode,
				},
				ScopeID: scopeID,
				Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: sessionID},
				PendingPrompts: []sessionruntime.PendingPromptReference{{
					ID:   "question",
					Kind: sessionruntime.PendingPromptKindQuestion,
				}},
			}},
		},
	}
	calls := 0
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			store: fixture.store,
			calls: &calls,
			observation: workflowTaskExecutionObservationForTest(
				t,
				map[workflow.TaskID][]workflow.CurrentNode{
					taskID: {{Reference: started.currentNode}},
				},
				executions,
				map[workflow.TaskID]bool{taskID: false},
			),
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

	err = projection.WithSnapshot(t.Context(), []workflow.TaskID{taskID}, func(
		observation TaskStatusObservation,
		durable *TaskStatusDurableSnapshot,
	) error {
		assertProjection(t, observation, durable)
		return nil
	})
	if err != nil {
		t.Fatalf("TaskStatusProjection.WithSnapshot: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lifecycle captures = %d, want one per request", calls)
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
	projection, err := NewTaskStatusProjection(fixture.metadata, fixture.store, NewTaskProjector(), controller)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	err = projection.WithSnapshot(t.Context(), nil, func(_ TaskStatusObservation, snapshot *TaskStatusDurableSnapshot) error {
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
		t.Fatalf("WithSnapshot: %v", err)
	}

	var observedState workflow.CurrentNodeSchedulingState
	err = projection.WithSnapshot(t.Context(), nil, func(_ TaskStatusObservation, snapshot *TaskStatusDurableSnapshot) error {
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
		t.Fatalf("WithSnapshot post-mutation: %v", err)
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
	projection, err := NewTaskStatusProjection(fixture.metadata, fixture.store, NewTaskProjector(), controller)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	var retained *TaskStatusDurableSnapshot
	if err := projection.WithSnapshot(t.Context(), nil, func(_ TaskStatusObservation, snapshot *TaskStatusDurableSnapshot) error {
		retained = snapshot
		return nil
	}); err != nil {
		t.Fatalf("WithSnapshot: %v", err)
	}
	if retained == nil {
		t.Fatal("projection did not provide a durable snapshot")
	}
	if _, err := retained.CurrentNodesByTask(t.Context(), []workflow.TaskID{"missing"}); err == nil {
		t.Fatal("retained durable snapshot remained usable after callback")
	}
}

func captureTaskStatusObservation(
	t *testing.T,
	projection *TaskStatusProjection,
	taskIDs []workflow.TaskID,
) TaskStatusObservation {
	t.Helper()
	var observation TaskStatusObservation
	if err := projection.WithSnapshot(t.Context(), taskIDs, func(
		captured TaskStatusObservation,
		_ *TaskStatusDurableSnapshot,
	) error {
		observation = captured
		return nil
	}); err != nil {
		t.Fatalf("TaskStatusProjection.WithSnapshot: %v", err)
	}
	return observation
}
