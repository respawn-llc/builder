package workflowsvc

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestNormalizeTaskObservationErrorClassifiesClosedEventStream(t *testing.T) {
	err := normalizeTaskObservationError(io.EOF)
	if !errors.Is(err, serverapi.ErrStreamFailed) {
		t.Fatalf("normalized error = %v, want stream failure", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("normalized error = %v, must not remain raw EOF", err)
	}
}

type observationApprovalViewStub struct {
	approvals []clientui.PendingApproval
}

type observationTaskDetailStub struct {
	detail serverapi.WorkflowTaskDetail
	nodes  []workflow.CurrentNode
}

func (s observationTaskDetailStub) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return s.detail, nil
}

func (s observationTaskDetailStub) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return s.detail, nil
}

func (s observationTaskDetailStub) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return s.detail, nil
}

func (s observationTaskDetailStub) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return s.nodes, nil
}

type observationTaskDetailState struct {
	mu     sync.Mutex
	detail serverapi.WorkflowTaskDetail
	calls  chan int
	count  int
}

func (s *observationTaskDetailState) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	s.mu.Lock()
	s.count++
	call := s.count
	detail := s.detail
	s.mu.Unlock()
	s.calls <- call
	return detail, nil
}

func (s *observationTaskDetailState) GetTaskByProjectShortID(ctx context.Context, _ string, _ string) (serverapi.WorkflowTaskDetail, error) {
	return s.GetTask(ctx, "")
}

func (s *observationTaskDetailState) GetTaskByShortID(ctx context.Context, _ string) (serverapi.WorkflowTaskDetail, error) {
	return s.GetTask(ctx, "")
}

func (s *observationTaskDetailState) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return nil, nil
}

func (s *observationTaskDetailState) setStatus(status serverapi.WorkflowTaskStatusKind) {
	s.mu.Lock()
	s.detail.Status.Kind = status
	s.mu.Unlock()
}

type observationDefinitionStub struct{}

func (observationDefinitionStub) GetDefinition(context.Context, runtimeids.WorkflowID) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	return serverapi.WorkflowDefinition{}, nil, nil
}

type observationAttentionStub struct{}

func (observationAttentionStub) List(context.Context, serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	return serverapi.WorkflowAttentionListResponse{}, nil
}

func (observationAttentionStub) ListTask(context.Context, serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return serverapi.WorkflowTaskAttentionListResponse{}, nil
}

func (s observationApprovalViewStub) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{Approvals: s.approvals}, nil
}

func newTaskObservationService(taskID, projectID string) (*Service, *observationTaskDetailState) {
	state := &observationTaskDetailState{
		detail: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{
				ID:         taskID,
				ProjectID:  projectID,
				WorkflowID: runtimeids.NewWorkflowID(),
				ShortID:    "T-1",
			},
			Status: serverapi.WorkflowTaskStatus{Kind: serverapi.WorkflowTaskStatusKindRunning},
		},
		calls: make(chan int, 4),
	}
	return &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			Definitions: observationDefinitionStub{},
			TaskDetail:  state,
			Attention:   observationAttentionStub{},
		},
	}, state
}

func publishTaskObservationEvent(service *Service, taskID, projectID string) {
	service.events.publish(serverapi.WorkflowProjectEvent{
		ProjectID:       &projectID,
		Resource:        serverapi.WorkflowProjectEventResourceTask,
		Action:          serverapi.WorkflowProjectEventActionUpdated,
		PrimaryEntityID: taskID,
	})
}

func observeTaskAsync(
	ctx context.Context,
	service *Service,
	req serverapi.WorkflowTaskObservationRequest,
) <-chan struct {
	response serverapi.WorkflowTaskObservationResponse
	err      error
} {
	result := make(chan struct {
		response serverapi.WorkflowTaskObservationResponse
		err      error
	}, 1)
	go func() {
		response, err := service.ObserveWorkflowTask(ctx, req)
		result <- struct {
			response serverapi.WorkflowTaskObservationResponse
			err      error
		}{response: response, err: err}
	}()
	return result
}

func TestObserveWorkflowTaskWaitReachesDurableDoneAfterTaskEvent(t *testing.T) {
	const taskID = "task-1"
	const projectID = "project-1"
	service, state := newTaskObservationService(taskID, projectID)
	result := observeTaskAsync(t.Context(), service, serverapi.WorkflowTaskObservationRequest{
		TaskID: taskID, ProjectID: projectID, Mode: serverapi.WorkflowTaskObservationWait,
	})
	if call := <-state.calls; call != 1 {
		t.Fatalf("initial Task projection call = %d, want 1", call)
	}

	state.setStatus(serverapi.WorkflowTaskStatusKindDone)
	publishTaskObservationEvent(service, taskID, projectID)

	observed := <-result
	if observed.err != nil {
		t.Fatalf("ObserveWorkflowTask: %v", observed.err)
	}
	if len(observed.response.Outcomes) != 1 ||
		observed.response.Outcomes[0].Kind != serverapi.WorkflowTaskObservationDone {
		t.Fatalf("response = %+v, want durable Done outcome", observed.response)
	}
}

func TestObserveWorkflowTaskWatchReprojectsAfterGenericTaskEvent(t *testing.T) {
	const taskID = "task-1"
	const projectID = "project-1"
	service, state := newTaskObservationService(taskID, projectID)
	ctx, cancel := context.WithCancel(t.Context())
	result := observeTaskAsync(ctx, service, serverapi.WorkflowTaskObservationRequest{
		TaskID: taskID, ProjectID: projectID, Mode: serverapi.WorkflowTaskObservationWatch,
	})
	if call := <-state.calls; call != 1 {
		t.Fatalf("initial Task projection call = %d, want 1", call)
	}

	// The generic event carries no typed observation outcome. Watch therefore
	// reprojects current facts; a transient outcome gone before this projection
	// is not observed. KENT-599 owns a stronger event-captured contract.
	publishTaskObservationEvent(service, taskID, projectID)
	if call := <-state.calls; call != 2 {
		t.Fatalf("event-triggered Task projection call = %d, want 2", call)
	}
	cancel()

	observed := <-result
	if !errors.Is(observed.err, context.Canceled) {
		t.Fatalf("ObserveWorkflowTask error = %v, want cancellation after empty event-triggered projection", observed.err)
	}
}

func TestObserveWorkflowTaskPreservesTypedCancellationAndGapErrors(t *testing.T) {
	const taskID = "task-1"
	const projectID = "project-1"
	tests := []struct {
		name    string
		stop    func(context.CancelFunc, *Service)
		wantErr error
	}{
		{
			name: "cancellation",
			stop: func(cancel context.CancelFunc, _ *Service) {
				cancel()
			},
			wantErr: context.Canceled,
		},
		{
			name: "stream gap",
			stop: func(_ context.CancelFunc, service *Service) {
				service.events.Close(serverapi.ErrStreamGap)
			},
			wantErr: serverapi.ErrStreamGap,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, state := newTaskObservationService(taskID, projectID)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := observeTaskAsync(ctx, service, serverapi.WorkflowTaskObservationRequest{
				TaskID: taskID, ProjectID: projectID, Mode: serverapi.WorkflowTaskObservationWatch,
			})
			if call := <-state.calls; call != 1 {
				t.Fatalf("initial Task projection call = %d, want 1", call)
			}

			test.stop(cancel, service)
			observed := <-result
			if !errors.Is(observed.err, test.wantErr) {
				t.Fatalf("ObserveWorkflowTask error = %v, want %v", observed.err, test.wantErr)
			}
		})
	}
}

func TestObserveWorkflowTaskWaitReturnsInterruptedOutcome(t *testing.T) {
	taskID := "task-1"
	projectID := "project-1"
	workflowID := runtimeids.NewWorkflowID()
	currentNode, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), "node-1", nil)
	if err != nil {
		t.Fatalf("current node reference: %v", err)
	}
	service := &Service{readModels: ReadModels{
		Definitions: observationDefinitionStub{},
		TaskDetail: observationTaskDetailStub{
			detail: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{
				ID: taskID, ProjectID: projectID, WorkflowID: workflowID, ShortID: "T-1",
			}},
			nodes: []workflow.CurrentNode{{
				Reference: currentNode,
				Scheduling: &workflow.CurrentNodeScheduling{
					State: workflow.CurrentNodeSchedulingInterrupted,
					Interruption: &workflow.CurrentNodeInterruption{
						Reason: workflow.CurrentNodeInterruptionReasonUserInterrupt,
						Detail: workflow.NewCurrentNodeInterruptionDetail("user_interrupt", nil),
					},
				},
			}},
		},
		Attention: observationAttentionStub{},
	}}
	response, ready, err := service.observeWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: taskID, ProjectID: projectID, Mode: serverapi.WorkflowTaskObservationWait,
	})
	if err != nil {
		t.Fatalf("observe workflow task: %v", err)
	}
	if !ready || len(response.Outcomes) != 1 {
		t.Fatalf("response = %+v, ready=%v; want one ready interruption", response, ready)
	}
	if response.Outcomes[0].Kind != serverapi.WorkflowTaskObservationInterrupted {
		t.Fatalf("outcome kind = %q, want interruption", response.Outcomes[0].Kind)
	}
}

func TestTaskCurrentNodeFailureUsesDefinitionIdentityAndDiagnostic(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	currentNode, err := workflow.NewCurrentNodeReference("task-1", "node-script", nil)
	if err != nil {
		t.Fatalf("current node reference: %v", err)
	}
	node := workflow.CurrentNode{
		Reference: currentNode,
		SessionID: &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
			Interruption: &workflow.CurrentNodeInterruption{
				Reason: workflow.CurrentNodeInterruptionReason("script_failed"),
				Detail: workflow.NewCurrentNodeInterruptionDetail("script_failed", errors.New("script stopped")),
			},
		},
	}
	scriptPath := "scripts/check.sh"
	outcome, err := taskCurrentNodeFailure(
		node,
		map[string]serverapi.WorkflowNode{"node-script": {ID: "node-script", Key: "check", ScriptPath: &scriptPath}},
		map[string]string{"node-script": "check"},
	)
	if err != nil {
		t.Fatalf("task current node failure: %v", err)
	}
	if outcome.Kind != serverapi.WorkflowTaskObservationExecutionError ||
		outcome.SessionID != nil ||
		outcome.ScriptPath == nil || *outcome.ScriptPath != scriptPath ||
		outcome.NodeKey == nil || *outcome.NodeKey != "check" ||
		outcome.Failure == nil || outcome.Failure.Diagnostic == nil || *outcome.Failure.Diagnostic != "script stopped" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestTaskQuestionResolvesLiveAccessThroughAuthoritativeApprovalView(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	stepID, err := runtimeids.ParseStepID("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	questionID := "access-1"
	message := "Allow access?"
	createdAt := time.UnixMilli(42).UTC()
	service := &Service{readModels: ReadModels{
		Approvals: observationApprovalViewStub{approvals: []clientui.PendingApproval{{
			PromptID:  clientui.PromptID(questionID),
			SessionID: sessionID,
			StepID:    stepID,
			Question:  message,
			Options:   []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}},
			CreatedAt: createdAt,
		}}},
	}}
	item := serverapi.WorkflowAttentionItem{
		Kind:    string(serverapi.WorkflowTaskAttentionKindQuestion),
		Message: &message,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{
			SessionID: sessionID, StepID: stepID, PromptID: clientui.PromptID(questionID),
			Kind:              serverapi.WorkflowAttentionQuestionKindApproval,
			ApprovalDecisions: []clientui.ApprovalDecision{clientui.ApprovalDecisionAllowOnce},
		},
		OccurredAtUnixMs: createdAt.UnixMilli(),
	}
	outcome, ok, err := service.taskQuestion(context.Background(), item, nil, map[string][]clientui.PendingApproval{})
	if err != nil || !ok {
		t.Fatalf("task question = %+v, ok=%v, err=%v", outcome, ok, err)
	}
	if outcome.Question == nil || outcome.Question.Approval == nil ||
		outcome.Question.Approval.Options[0].Label != "Allow once" {
		t.Fatalf("question = %+v", outcome.Question)
	}
}
