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
	get    func() serverapi.WorkflowTaskDetail
}

func (s observationTaskDetailStub) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	if s.get != nil {
		return s.get(), nil
	}
	return s.detail, nil
}

func (s observationTaskDetailStub) GetTaskByProjectShortID(ctx context.Context, _, _ string) (serverapi.WorkflowTaskDetail, error) {
	return s.GetTask(ctx, "")
}

func (s observationTaskDetailStub) GetTaskByShortID(ctx context.Context, _ string) (serverapi.WorkflowTaskDetail, error) {
	return s.GetTask(ctx, "")
}

func (s observationTaskDetailStub) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return s.nodes, nil
}

type taskObservationState struct {
	sync.Mutex
	status serverapi.WorkflowTaskStatusKind
	calls  chan struct{}
}

func (s *taskObservationState) detail() serverapi.WorkflowTaskDetail {
	s.Lock()
	defer s.Unlock()
	status := s.status
	s.calls <- struct{}{}
	return serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{
			ID: "task-1", ProjectID: "project-1", WorkflowID: runtimeids.NewWorkflowID(), ShortID: "T-1",
		},
		Status: serverapi.WorkflowTaskStatus{Kind: status},
	}
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

type taskObservationResult struct {
	response serverapi.WorkflowTaskObservationResponse
	err      error
}

func newTaskObservationTest() (*Service, *taskObservationState) {
	state := &taskObservationState{status: serverapi.WorkflowTaskStatusKindRunning, calls: make(chan struct{}, 4)}
	return &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			Definitions: observationDefinitionStub{},
			TaskDetail:  observationTaskDetailStub{get: state.detail},
			Attention:   observationAttentionStub{},
		},
	}, state
}

func observeTaskForTest(ctx context.Context, service *Service, mode serverapi.WorkflowTaskObservationMode) <-chan taskObservationResult {
	result := make(chan taskObservationResult, 1)
	go func() {
		response, err := service.ObserveWorkflowTask(ctx, serverapi.WorkflowTaskObservationRequest{
			TaskID: "task-1", ProjectID: "project-1", Mode: mode,
		})
		result <- taskObservationResult{response, err}
	}()
	return result
}

func publishTaskObservationEvent(service *Service) {
	projectID := "project-1"
	service.events.publish(serverapi.WorkflowProjectEvent{
		ProjectID: &projectID, Resource: serverapi.WorkflowProjectEventResourceTask,
		Action: serverapi.WorkflowProjectEventActionUpdated, PrimaryEntityID: "task-1",
	})
}

func TestObserveWorkflowTaskWaitReachesDurableDoneAfterTaskEvent(t *testing.T) {
	service, state := newTaskObservationTest()
	result := observeTaskForTest(t.Context(), service, serverapi.WorkflowTaskObservationWait)
	<-state.calls
	state.Lock()
	state.status = serverapi.WorkflowTaskStatusKindDone
	state.Unlock()
	publishTaskObservationEvent(service)
	got := <-result
	if got.err != nil || len(got.response.Outcomes) != 1 ||
		got.response.Outcomes[0].Kind != serverapi.WorkflowTaskObservationDone {
		t.Fatalf("observation = %+v, err=%v", got.response, got.err)
	}
}

func TestObserveWorkflowTaskWatchReprojectsAfterGenericTaskEvent(t *testing.T) {
	service, state := newTaskObservationTest()
	ctx, cancel := context.WithCancel(t.Context())
	result := observeTaskForTest(ctx, service, serverapi.WorkflowTaskObservationWatch)
	<-state.calls
	publishTaskObservationEvent(service)
	<-state.calls
	cancel()
	if err := (<-result).err; !errors.Is(err, context.Canceled) {
		t.Fatalf("observation error = %v, want cancellation after reprojection", err)
	}
}

func TestObserveWorkflowTaskPreservesTypedCancellationAndGapErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		gap     bool
		wantErr error
	}{
		{"cancellation", false, context.Canceled},
		{"stream gap", true, serverapi.ErrStreamGap},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, state := newTaskObservationTest()
			ctx, cancel := context.WithCancel(t.Context())
			result := observeTaskForTest(ctx, service, serverapi.WorkflowTaskObservationWatch)
			<-state.calls
			if test.gap {
				service.events.Close(serverapi.ErrStreamGap)
			} else {
				cancel()
			}
			if err := (<-result).err; !errors.Is(err, test.wantErr) {
				t.Fatalf("observation error = %v, want %v", err, test.wantErr)
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
