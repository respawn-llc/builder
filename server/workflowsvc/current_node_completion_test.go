package workflowsvc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/server/requestmemo"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
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
		TransitionID:   "done",
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if response.TaskID != string(source.TaskID) {
		t.Fatalf("task id = %q, want %q", response.TaskID, source.TaskID)
	}
	if response.PendingApprovalID == nil || *response.PendingApprovalID != approvalID.String() {
		t.Fatalf("pending approval id = %v, want %q", response.PendingApprovalID, approvalID)
	}
	if len(response.CurrentNodes) != 0 {
		t.Fatalf("current nodes = %+v, want none while source remains pending approval", response.CurrentNodes)
	}
	if execution.sessionID != sessionID || execution.idleSelector.TaskID != nil || execution.idleSelector.SessionID != nil {
		t.Fatalf("completion dispatch = %+v, want live Session completion", execution)
	}
}

func TestCompleteWorkflowTaskUsesOnlyTaskOrSessionForcedSelector(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-forced", "node-agent")
	result := workflowstore.CurrentNodeCompletionResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{source},
			Created: []workflow.CurrentNode{{
				Reference: currentNodeCompletionReference(t, "task-forced", "node-done"),
			}},
		},
	}
	tests := []struct {
		name    string
		request serverapi.WorkflowTaskCompleteRequest
		assert  func(*testing.T, workflowstore.IdleCurrentNodeSelector)
	}{
		{
			name: "task",
			request: serverapi.WorkflowTaskCompleteRequest{
				ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
				Force:        true,
				TaskID:       string(source.TaskID),
				TransitionID: "done",
			},
			assert: func(t *testing.T, selector workflowstore.IdleCurrentNodeSelector) {
				t.Helper()
				if selector.TaskID == nil || *selector.TaskID != source.TaskID || selector.SessionID != nil {
					t.Fatalf("idle selector = %+v, want task selector", selector)
				}
			},
		},
		{
			name: "session",
			request: serverapi.WorkflowTaskCompleteRequest{
				ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
				Force:        true,
				SessionID:    runtimeids.NewSessionID().String(),
				TransitionID: "done",
			},
			assert: func(t *testing.T, selector workflowstore.IdleCurrentNodeSelector) {
				t.Helper()
				if selector.TaskID != nil || selector.SessionID == nil {
					t.Fatalf("idle selector = %+v, want Session selector", selector)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := &currentNodeCompletionExecutionStub{idleResult: result}
			response, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), test.request)
			if err != nil {
				t.Fatalf("CompleteWorkflowTask: %v", err)
			}
			test.assert(t, execution.idleSelector)
			if response.TaskID != string(source.TaskID) || len(response.CurrentNodes) != 1 || response.CurrentNodes[0].NodeID != "node-done" {
				t.Fatalf("response = %+v, want task and replacement Current Node", response)
			}
		})
	}
}

func TestCompleteWorkflowTaskMapsAmbiguousAndPendingSourceFailures(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-completion-errors", "node-agent")
	sessionID := runtimeids.NewSessionID()
	tests := []struct {
		name      string
		resultErr error
		want      error
	}{
		{
			name:      "ambiguous",
			resultErr: workflowstore.ErrCurrentNodeCompletionSelectorAmbiguous,
			want:      serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous,
		},
		{
			name:      "pending approval source",
			resultErr: workflowstore.ErrCurrentNodePendingApproval,
			want:      workflowstore.ErrCurrentNodePendingApproval,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := &currentNodeCompletionExecutionStub{idleErr: test.resultErr}
			_, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
				ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
				Force:        true,
				TaskID:       string(source.TaskID),
				TransitionID: "done",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("completion error = %v, want %v", err, test.want)
			}
		})
	}

	execution := &currentNodeCompletionExecutionStub{sessionErr: sessionruntime.ErrExecutionNoLongerLive}
	_, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID.String(),
		TransitionID:   "done",
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		t.Fatalf("live completion error = %v, want target-not-found", err)
	}
}

func TestWorkflowTaskCompleteContractHasNoRunOrPlacementFields(t *testing.T) {
	for _, contract := range []reflect.Type{
		reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{}),
		reflect.TypeOf(serverapi.WorkflowTaskCompleteResponse{}),
	} {
		for _, removed := range []string{"RunID", "RunIDs", "PlacementID", "PlacementIDs", "ProjectID", "ShortID"} {
			if _, exists := contract.FieldByName(removed); exists {
				t.Fatalf("%s still exposes removed completion field %s", contract.Name(), removed)
			}
		}
	}
}

func TestAnswerWorkflowTaskQuestionRoutesOnlyTaskAndAskToCurrentNodeExecution(t *testing.T) {
	execution := &currentNodeCompletionExecutionStub{}
	service := currentNodeCompletionService(execution)
	request := serverapi.WorkflowTaskQuestionAnswerRequest{
		ClientRequestID: "question-request-1",
		TaskID:          "task-question",
		AskID:           "ask-question",
		Answer:          "continue",
	}

	if err := service.AnswerWorkflowTaskQuestion(context.Background(), request); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion: %v", err)
	}
	if execution.questionTaskID != workflow.TaskID(request.TaskID) ||
		execution.questionAskID != request.AskID ||
		execution.questionResponse.RequestID != request.AskID ||
		execution.questionResponse.Answer != request.Answer ||
		execution.questionSubmitErr != nil {
		t.Fatalf("question dispatch = %+v, want task-scoped exact answer", execution)
	}
}

func TestAnswerWorkflowTaskQuestionMapsVolatileQuestionFailures(t *testing.T) {
	request := serverapi.WorkflowTaskQuestionAnswerRequest{
		ClientRequestID: "question-request-2",
		TaskID:          "task-question",
		AskID:           "ask-question",
		Answer:          "continue",
	}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "ambiguous",
			err:  sessionruntime.ErrWorkflowPromptAmbiguous,
			want: serverapi.ErrWorkflowTaskQuestionSelectorAmbiguous,
		},
		{
			name: "stale",
			err:  serverapi.ErrPromptNotFound,
			want: serverapi.ErrPromptNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := currentNodeCompletionService(&currentNodeCompletionExecutionStub{questionErr: test.err})
			err := service.AnswerWorkflowTaskQuestion(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("AnswerWorkflowTaskQuestion error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAnswerWorkflowTaskQuestionRejectsConflictingIdempotentPayload(t *testing.T) {
	execution := &currentNodeCompletionExecutionStub{}
	service := currentNodeCompletionService(execution)
	request := serverapi.WorkflowTaskQuestionAnswerRequest{
		ClientRequestID: "question-request-3",
		TaskID:          "task-question",
		AskID:           "ask-question",
		Answer:          "continue",
	}
	if err := service.AnswerWorkflowTaskQuestion(context.Background(), request); err != nil {
		t.Fatalf("first AnswerWorkflowTaskQuestion: %v", err)
	}
	request.Answer = "stop"
	if err := service.AnswerWorkflowTaskQuestion(context.Background(), request); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("conflicting idempotent answer error = %v, want client request id reuse", err)
	}
}

func TestWorkflowTaskQuestionAnswerContractHasNoRunFields(t *testing.T) {
	for _, contract := range []reflect.Type{
		reflect.TypeOf(serverapi.WorkflowTaskQuestionAnswerRequest{}),
		reflect.TypeOf(taskQuestionAnswerMemoRequest{}),
	} {
		for _, removed := range []string{"RunID", "RunIDs", "PlacementID", "PlacementIDs"} {
			if _, exists := contract.FieldByName(removed); exists {
				t.Fatalf("%s still exposes removed question selector field %s", contract.Name(), removed)
			}
		}
	}
}

func currentNodeCompletionService(execution *currentNodeCompletionExecutionStub) *Service {
	return &Service{
		readModels:           ReadModels{TaskDetail: currentNodeCompletionUnavailableTaskDetail{}},
		currentNodeExecution: execution,
		questionMemo:         requestmemo.New[taskQuestionAnswerMemoRequest, struct{}](),
	}
}

type currentNodeCompletionUnavailableTaskDetail struct{}

func (currentNodeCompletionUnavailableTaskDetail) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
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
	store             *workflowstore.Store
	sessionID         runtimeids.SessionID
	sessionResult     workflowstore.CurrentNodeCompletionResult
	sessionErr        error
	idleSelector      workflowstore.IdleCurrentNodeSelector
	idleResult        workflowstore.CurrentNodeCompletionResult
	idleErr           error
	questionTaskID    workflow.TaskID
	questionAskID     string
	questionResponse  askquestion.AskQuestionResponse
	questionSubmitErr error
	questionErr       error
}

func (s *currentNodeCompletionExecutionStub) StartTaskWithExecutionTarget(
	ctx context.Context,
	taskID workflow.TaskID,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.StartTaskResult, error) {
	if s.store == nil {
		return workflowstore.StartTaskResult{}, errors.New("workflow store is required")
	}
	return s.store.StartTaskWithExecutionTarget(ctx, taskID, candidate)
}

func (s *currentNodeCompletionExecutionStub) ResumeTask(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	if s.store == nil {
		return nil, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return nil, err
		}
	}
	return selected, nil
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
	return s.store.ApplyManualMove(ctx, prepared, candidate)
}

func (*currentNodeCompletionExecutionStub) Interrupt(context.Context, workflowexecution.InterruptSelector) error {
	return nil
}

func (*currentNodeCompletionExecutionStub) EnsureTaskQuiescent(workflow.TaskID) error {
	return nil
}

func (s *currentNodeCompletionExecutionStub) AnswerWorkflowQuestion(_ context.Context, taskID workflow.TaskID, askID string, response askquestion.AskQuestionResponse, submitErr error) error {
	s.questionTaskID = taskID
	s.questionAskID = askID
	s.questionResponse = response
	s.questionSubmitErr = submitErr
	return s.questionErr
}

func (s *currentNodeCompletionExecutionStub) CompleteSessionCurrentNode(_ context.Context, sessionID runtimeids.SessionID, _ string, _ map[string]string, _ string) (workflowstore.CurrentNodeCompletionResult, error) {
	s.sessionID = sessionID
	return s.sessionResult, s.sessionErr
}

func (s *currentNodeCompletionExecutionStub) CompleteIdleCurrentNode(_ context.Context, selector workflowstore.IdleCurrentNodeSelector, _ string, _ map[string]string, _ string) (workflowstore.CurrentNodeCompletionResult, error) {
	s.idleSelector = selector
	return s.idleResult, s.idleErr
}
