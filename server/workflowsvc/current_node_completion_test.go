package workflowsvc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/scriptedllm"
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
	"core/shared/textutil"
)

func TestCompleteWorkflowTaskReturnsPendingApprovalWithoutReplacingCurrentNode(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-pending-approval", "node-agent")
	approvalID := workflow.NewApprovalID()
	execution := &currentNodeCompletionExecutionStub{
		sessionResult: workflowstore.CurrentNodeCompletionResult{
			PendingApproval: &workflow.PendingApproval{
				ID:     approvalID,
				Source: source,
			},
		},
	}
	service := currentNodeCompletionService(execution)
	sessionID := runtimeids.NewSessionID()

	response, err := service.CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID.String(),
		RunID:          currentNodeCompletionRunID(t),
		StepID:         currentNodeCompletionStepID(t),
		TransitionID:   "done",
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if response.AgentCompletion == nil {
		t.Fatalf("agent completion response = %+v", response)
	}
	completion := response.AgentCompletion
	if completion.TaskID != string(source.TaskID) {
		t.Fatalf("task id = %q, want %q", completion.TaskID, source.TaskID)
	}
	if completion.PendingApprovalID == nil || *completion.PendingApprovalID != approvalID.String() {
		t.Fatalf("pending approval id = %v, want %q", completion.PendingApprovalID, approvalID)
	}
	if len(completion.CurrentNodes) != 0 {
		t.Fatalf("current nodes = %+v, want none while source remains pending approval", completion.CurrentNodes)
	}
	if execution.sessionID != sessionID {
		t.Fatalf("completion dispatch = %+v, want live Session completion", execution)
	}
}

func TestCompleteWorkflowTaskReturnsResultDespitePostCommitDiagnostic(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-accepted-diagnostic", "node-agent")
	publicationErr := errors.New("completion event publication failed")
	execution := &currentNodeCompletionExecutionStub{
		sessionResult: workflowstore.CurrentNodeCompletionResult{
			PendingApproval: &workflow.PendingApproval{
				ID:     workflow.NewApprovalID(),
				Source: source,
			},
		},
		sessionDiagnostic: publicationErr,
	}
	response, err := currentNodeCompletionService(execution).CompleteWorkflowTask(
		context.Background(),
		serverapi.WorkflowTaskCompleteRequest{
			ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
			AgentSessionID: runtimeids.NewSessionID().String(),
			RunID:          currentNodeCompletionRunID(t),
			StepID:         currentNodeCompletionStepID(t),
			TransitionID:   "done",
		},
	)
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if response.AgentCompletion == nil || response.AgentCompletion.TaskID != string(source.TaskID) {
		t.Fatalf("accepted completion response = %+v, want Task %s", response, source.TaskID)
	}
}

func TestCompleteWorkflowTaskForceDoesNotRecloseTaskInterruptedApproval(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	source := workflowServiceCurrentNodeReference(t, workflow.TaskID(task.Task.ID), started.CurrentNodes[0])

	feed := &approvalCompletionFeed{pending: make(chan struct{}, 1), resolutionStarted: make(chan struct{}, 2), resolutionRelease: make(chan struct{})}
	t.Cleanup(feed.release)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{PersistenceRoot: metadataStore.PersistenceRoot(), StoreOptions: metadataStore.AuthoritativeSessionStoreOptions(), PromptFeed: feed})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		initialBranchControllerRunner{},
		authority,
		service.taskMutations,
		workflowexecution.CurrentNodeControllerConfig{AgentConcurrency: 1, AssignmentSteerer: initialBranchControllerSteerer{}},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	execution := &approvalCompletionExecution{manualMoveExecutionStub: newManualMoveExecutionStub(service), controller: controller}
	service.currentNodeExecution = execution
	t.Cleanup(func() {
		if err := errors.Join(controller.Close(), authority.Close(context.Background())); err != nil {
			t.Errorf("close Approval execution: %v", err)
		}
	})

	sessionID := createPersistedWorkflowServiceSession(t, metadataStore, binding)
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	appCfg, err := config.Load(binding.CanonicalRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	filesystemContext, err := runtimewire.NewFilesystemContext(
		binding.CanonicalRoot,
		binding.CanonicalRoot,
		metadata.ProjectWorkspaceBoundary{ProjectID: binding.ProjectID},
	)
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	settings := appCfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings, FilesystemContext: filesystemContext,
		QuestionsEnabled: textutil.Value(true), AutoCompactionEnabled: textutil.Value(true),
		Client: scriptedllm.NewClient(scriptedllm.Script{}),
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	stepID := *currentNodeCompletionStepID(t)
	request := tools.AskQuestionRequest{
		ToolCallID: "force-complete-pending-approval", StepID: stepID.String(), Question: "Allow access?", Approval: true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}},
	}
	promptDone := make(chan approvalCompletionAsync[tools.AskQuestionResolution], 1)
	handle, err := authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Workflow: &sessionruntime.WorkflowAgentExecution{
			Reference: sessionruntime.WorkflowExecutionRef{ProjectID: binding.ProjectID, WorkflowID: workflowID, CurrentNode: source},
			Config: &workflowruntime.CurrentNodeExecutionConfig{
				Contract:       workflowruntime.CompletionContract{Transitions: []workflowruntime.CompletionTransition{{ID: "next"}}},
				CompletionMode: workflowruntime.CompletionModeTool, Controller: controller,
				Instructions: workflowruntime.TaskInstructions{CurrentNode: source},
			},
		},
		Resource: sessionruntime.OpenAgentResource{},
		Runner: func(runCtx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			resolution, awaitErr := authority.AwaitPromptResolution(runCtx, scope.ID(), request)
			promptDone <- approvalCompletionAsync[tools.AskQuestionResolution]{value: resolution, err: awaitErr}
			return awaitErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	t.Cleanup(func() { _ = handle.Stop(context.Background()) })
	select {
	case <-feed.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for pending Approval")
	}

	completionDone := make(chan approvalCompletionAsync[serverapi.WorkflowTaskCompleteResponse], 1)
	go func() {
		response, completeErr := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
			ActorKind: serverapi.WorkflowTaskCompleteActorUser, Force: true, TaskID: task.Task.ID, TransitionID: "next",
			OutputValues: map[string]string{"prior_summary": "planned"},
		})
		completionDone <- approvalCompletionAsync[serverapi.WorkflowTaskCompleteResponse]{value: response, err: completeErr}
	}()
	select {
	case <-feed.resolutionStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Task Interrupt Approval closure")
	}

	commentary := "This stale Approval must not be accepted."
	answerDone := make(chan approvalCompletionAsync[[]sessionruntime.PromptAnswerResult], 1)
	go func() {
		results, resolveErr := authority.ResolvePromptBatch(context.Background(), sessionID, stepID, []sessionruntime.PromptAnswerCommand{{
			ToolCallID: clientui.ToolCallID(request.ToolCallID),
			Payload: sessionruntime.PromptApprovalAnswerCommand{Answer: tools.AskQuestionApproval{
				Decision: tools.AskQuestionApprovalDecisionAllowOnce, Commentary: &commentary,
			}},
		}})
		answerDone <- approvalCompletionAsync[[]sessionruntime.PromptAnswerResult]{value: results, err: resolveErr}
	}()
	select {
	case answer := <-answerDone:
		t.Fatalf("stale Approval completed before Task Interrupt closure: %+v", answer)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case result := <-completionDone:
		t.Fatalf("forced completion returned before Task Interrupt closure: %+v", result)
	default:
	}
	feed.release()

	var prompt approvalCompletionAsync[tools.AskQuestionResolution]
	select {
	case prompt = <-promptDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Task-Interrupted Approval")
	}
	if !errors.Is(prompt.err, context.Canceled) || prompt.value != nil {
		t.Fatalf("Task Interrupt Approval = (%+v, %v), want cancellation without commentary", prompt.value, prompt.err)
	}
	var answer approvalCompletionAsync[[]sessionruntime.PromptAnswerResult]
	select {
	case answer = <-answerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stale Approval answer")
	}
	if answer.err != nil || len(answer.value) != 1 || answer.value[0].Outcome != sessionruntime.PromptAnswerOutcomeSkipped {
		t.Fatalf("stale Approval answer = (%+v, %v), want Skipped", answer.value, answer.err)
	}
	var completion approvalCompletionAsync[serverapi.WorkflowTaskCompleteResponse]
	select {
	case completion = <-completionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forced completion")
	}
	if completion.err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", completion.err)
	}
	if completion.value.ForcedMove == nil || completion.value.ForcedMove.TaskID != task.Task.ID || completion.value.ForcedMove.TargetNodeID == "" || completion.value.ForcedMove.Outcome.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied || completion.value.ForcedMove.Outcome.Applied == nil || len(completion.value.ForcedMove.Outcome.Applied.CurrentNodes) != 1 {
		t.Fatalf("forced completion response = %+v, want applied move", completion.value.ForcedMove)
	}
	if execution.manualMoveSelections != 1 {
		t.Fatalf("Manual Move selections = %d, want 1", execution.manualMoveSelections)
	}
	select {
	case <-feed.resolutionStarted:
		t.Fatal("Manual Move republished the Task-Interrupted Approval resolution")
	case <-time.After(50 * time.Millisecond):
	}
}

type approvalCompletionAsync[T any] struct {
	value T
	err   error
}

type approvalCompletionExecution struct {
	*manualMoveExecutionStub
	controller           *workflowexecution.CurrentNodeController
	manualMoveSelections int
}

func (e *approvalCompletionExecution) Interrupt(ctx context.Context, selector workflowexecution.InterruptSelector) error {
	return e.controller.Interrupt(ctx, selector)
}

func (e *approvalCompletionExecution) InterruptForManualMove(ctx context.Context, taskID workflow.TaskID, beforeSelection func() error) error {
	e.manualMoveSelections++
	return e.controller.InterruptForManualMove(ctx, taskID, beforeSelection)
}

type approvalCompletionFeed struct {
	pending, resolutionStarted chan struct{}
	resolutionRelease          chan struct{}
	releaseOnce                sync.Once
}

func (f *approvalCompletionFeed) PromptPendingScope(sessionruntime.ExecutionScope, tools.AskQuestionRequest, time.Time) error {
	f.pending <- struct{}{}
	return nil
}

func (f *approvalCompletionFeed) PromptResolvedScope(sessionruntime.ExecutionScope, string) error {
	f.resolutionStarted <- struct{}{}
	<-f.resolutionRelease
	return nil
}

func (f *approvalCompletionFeed) release() {
	f.releaseOnce.Do(func() { close(f.resolutionRelease) })
}

func TestCompleteWorkflowTaskForceReturnsDependencyConfirmationManualMoveOutcome(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	blocker := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	blocked := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	startWorkflowServiceTask(t, ctx, service, blocked.Task.ID)
	execution.calls = nil
	if _, err := service.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: blocker.Task.ID,
		BlockedTaskID: blocked.Task.ID,
	}); err != nil {
		t.Fatalf("AddWorkflowTaskDependency: %v", err)
	}

	response, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
		Force:        true,
		TaskID:       blocked.Task.ID,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "planned"},
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if response.ForcedMove == nil ||
		response.ForcedMove.TaskID != blocked.Task.ID ||
		response.ForcedMove.TargetNodeID == "" ||
		response.ForcedMove.Outcome.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired ||
		response.ForcedMove.Outcome.UnsatisfiedDependencyCount == nil ||
		*response.ForcedMove.Outcome.UnsatisfiedDependencyCount != 1 {
		t.Fatalf("forced completion response = %+v", response)
	}
	if !reflect.DeepEqual(execution.calls, []string{"interrupt"}) {
		t.Fatalf("forced completion operations = %v, want Interrupt before dependency outcome", execution.calls)
	}
}

func TestCompleteWorkflowTaskMapsMissingLiveSourceFailure(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	execution := &currentNodeCompletionExecutionStub{sessionErr: sessionruntime.ErrExecutionNoLongerLive}
	_, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID.String(),
		RunID:          currentNodeCompletionRunID(t),
		StepID:         currentNodeCompletionStepID(t),
		TransitionID:   "done",
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		t.Fatalf("live completion error = %v, want target-not-found", err)
	}
}

func TestWorkflowTaskCompleteContractHasExactAgentProvenanceAndNoPlacementFields(t *testing.T) {
	for _, contract := range []reflect.Type{
		reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{}),
		reflect.TypeOf(serverapi.WorkflowTaskCompleteResponse{}),
	} {
		for _, removed := range []string{"RunIDs", "PlacementID", "PlacementIDs", "ProjectID", "ShortID"} {
			if _, exists := contract.FieldByName(removed); exists {
				t.Fatalf("%s still exposes removed completion field %s", contract.Name(), removed)
			}
		}
	}
	request := reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{})
	for _, required := range []string{"RunID", "StepID"} {
		if _, exists := request.FieldByName(required); !exists {
			t.Fatalf("WorkflowTaskCompleteRequest lacks %s provenance", required)
		}
	}
}

func currentNodeCompletionService(execution *currentNodeCompletionExecutionStub) *Service {
	return &Service{
		readModels:           ReadModels{TaskDetail: currentNodeCompletionUnavailableTaskDetail{}},
		currentNodeExecution: execution,
	}
}

func currentNodeCompletionRunID(t *testing.T) *runtimeids.RunID {
	t.Helper()
	value, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Run ID: %v", err)
	}
	return &value
}

func currentNodeCompletionStepID(t *testing.T) *runtimeids.StepID {
	t.Helper()
	value, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse Step ID: %v", err)
	}
	return &value
}

type currentNodeCompletionUnavailableTaskDetail struct{}

func (currentNodeCompletionUnavailableTaskDetail) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return nil, errors.New("current nodes unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func currentNodeCompletionReference(t *testing.T, taskID, nodeID string) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), workflow.NodeID(nodeID), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return reference
}

type currentNodeCompletionExecutionStub struct {
	store                  *workflowstore.Store
	promoted               []workflow.CurrentNode
	promotionHandled       bool
	promotionErr           error
	resumePreflight        workflowexecution.TaskResumePreflight
	resumeEligibilityErr   error
	resumeEligibilityCalls int
	startPreparations      chan<- workflowexecution.TaskStartPreparation
	startFinalizers        chan<- workflowexecution.TaskPreparationFinalizer
	sessionID              runtimeids.SessionID
	sessionResult          workflowstore.CurrentNodeCompletionResult
	sessionDiagnostic      error
	sessionErr             error
	manualMoveAssignments  workflowstore.ManualMoveTargetAssignmentPreparer
}

func (s *currentNodeCompletionExecutionStub) configuredResumePreflight(
	taskID workflow.TaskID,
) (workflowexecution.TaskResumePreflight, bool) {
	if s.resumePreflight.Outcome == "" && s.resumePreflight.CurrentNodes == nil {
		return workflowexecution.TaskResumePreflight{}, false
	}
	preflight := s.resumePreflight
	preflight.CurrentNodes = append([]workflow.CurrentNode(nil), preflight.CurrentNodes...)
	for index := range preflight.CurrentNodes {
		preflight.CurrentNodes[index].Reference.TaskID = taskID
	}
	return preflight, true
}

func (s *currentNodeCompletionExecutionStub) PromoteConcurrencyQueuedTask(
	context.Context,
	workflow.TaskID,
) ([]workflow.CurrentNode, bool, error) {
	return append([]workflow.CurrentNode(nil), s.promoted...), s.promotionHandled, s.promotionErr
}

func (s *currentNodeCompletionExecutionStub) PreflightTaskResume(
	ctx context.Context,
	taskID workflow.TaskID,
) (workflowexecution.TaskResumePreflight, error) {
	s.resumeEligibilityCalls++
	if s.resumeEligibilityErr != nil {
		return workflowexecution.TaskResumePreflight{}, s.resumeEligibilityErr
	}
	if preflight, configured := s.configuredResumePreflight(taskID); configured {
		return preflight, nil
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumePreflight{}, err
	}
	if len(selected) != 0 {
		return workflowexecution.TaskResumePreflight{
			Outcome:      workflowexecution.TaskResumePreflightResumable,
			CurrentNodes: selected,
		}, nil
	}
	currentNodes, err := s.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumePreflight{}, err
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil {
			continue
		}
		switch currentNode.Scheduling.State {
		case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
			return workflowexecution.TaskResumePreflight{
				Outcome:      workflowexecution.TaskResumePreflightNoOp,
				CurrentNodes: currentNodes,
			}, nil
		}
	}
	return workflowexecution.TaskResumePreflight{}, &workflowexecution.TaskResumeConflictError{TaskID: taskID}
}

func (s *currentNodeCompletionExecutionStub) StartTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation workflowexecution.TaskStartPreparation,
	finalizer workflowexecution.TaskPreparationFinalizer,
) (workflowstore.StartTaskResult, error) {
	if s.store == nil {
		return workflowstore.StartTaskResult{}, errors.New("workflow store is required")
	}
	started, err := s.store.StartTask(ctx, taskID)
	if err != nil {
		return started, err
	}
	if s.startPreparations != nil {
		s.startPreparations <- preparation
		if s.startFinalizers != nil {
			s.startFinalizers <- finalizer
		}
		return started, nil
	}
	if err := preparation.Prepare(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return started, err
	}
	if err := preparation.Commit(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return started, err
	}
	finalizer(workflowexecution.TaskPreparationFinalization{Kind: workflowexecution.TaskPreparationHandedOff})
	return started, nil
}

func (s *currentNodeCompletionExecutionStub) ResumeTask(ctx context.Context, taskID workflow.TaskID) (workflowexecution.TaskResumeResult, error) {
	if s.store == nil {
		return workflowexecution.TaskResumeResult{}, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumeResult{}, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return workflowexecution.TaskResumeResult{}, err
		}
	}
	return workflowexecution.TaskResumeResult{
		Outcome:      workflowexecution.TaskResumeApplied,
		CurrentNodes: selected,
	}, nil
}

func (s *currentNodeCompletionExecutionStub) ResumeTaskWithPreparation(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation workflowexecution.TaskStartPreparation,
	finalizer workflowexecution.TaskPreparationFinalizer,
) (workflowexecution.TaskResumeResult, error) {
	if s.store == nil {
		return workflowexecution.TaskResumeResult{}, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumeResult{}, err
	}
	if err := preparation.Prepare(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return workflowexecution.TaskResumeResult{}, err
	}
	if err := preparation.Commit(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return workflowexecution.TaskResumeResult{}, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return workflowexecution.TaskResumeResult{}, err
		}
	}
	finalizer(workflowexecution.TaskPreparationFinalization{Kind: workflowexecution.TaskPreparationHandedOff})
	return workflowexecution.TaskResumeResult{
		Outcome:      workflowexecution.TaskResumeApplied,
		CurrentNodes: selected,
	}, nil
}

func (s *currentNodeCompletionExecutionStub) ApplyPendingApproval(ctx context.Context, approvalID workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error) {
	if s.store == nil {
		return workflowstore.PendingApprovalApplyResult{}, errors.New("workflow store is required")
	}
	return s.store.ApplyPendingApproval(ctx, approvalID)
}

func (s *currentNodeCompletionExecutionStub) ApplyManualMove(
	ctx context.Context,
	prepared workflowstore.ManualMovePreparation,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.ManualMoveResult, error) {
	if s.store == nil {
		return workflowstore.ManualMoveResult{}, errors.New("workflow store is required")
	}
	if s.manualMoveAssignments != nil {
		return s.store.ApplyManualMoveWithTargetAssignments(ctx, prepared, candidate, s.manualMoveAssignments)
	}
	return s.store.ApplyManualMove(ctx, prepared, candidate)
}

func (s *currentNodeCompletionExecutionStub) Interrupt(context.Context, workflowexecution.InterruptSelector) error {
	return nil
}

func (*currentNodeCompletionExecutionStub) InterruptForManualMove(context.Context, workflow.TaskID, func() error) error {
	return nil
}

func (*currentNodeCompletionExecutionStub) EnsureTaskQuiescent(workflow.TaskID) error {
	return nil
}

func (s *currentNodeCompletionExecutionStub) CompleteSessionCurrentNode(
	_ context.Context,
	sessionID runtimeids.SessionID,
	_ runtimeids.RunID,
	_ runtimeids.StepID,
	_ string,
	_ map[string]string,
	_ string,
) (workflowruntime.CompletionResult, error) {
	s.sessionID = sessionID
	if s.sessionErr != nil {
		return workflowruntime.CompletionResult{}, s.sessionErr
	}
	return workflowruntime.CompletionResult{
		State:           workflowruntime.CompletionStateApplied,
		CommittedResult: s.sessionResult,
		Diagnostic:      s.sessionDiagnostic,
	}, nil
}
