package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type observationTaskDetails struct {
	detail       serverapi.WorkflowTaskDetail
	currentNodes []workflow.CurrentNode
}

type observationSequenceDetails struct {
	details []serverapi.WorkflowTaskDetail
	errors  []error
	first   chan struct{}
}

func (r *observationSequenceDetails) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	if len(r.details) == 0 {
		if len(r.errors) > 0 {
			err := r.errors[0]
			r.errors = r.errors[1:]
			return serverapi.WorkflowTaskDetail{}, err
		}
		return serverapi.WorkflowTaskDetail{}, context.Canceled
	}
	detail := r.details[0]
	r.details = r.details[1:]
	if r.first != nil {
		close(r.first)
		r.first = nil
	}
	return detail, nil
}

func (r *observationSequenceDetails) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return r.GetTask(context.Background(), "")
}

func (r *observationSequenceDetails) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return r.GetTask(context.Background(), "")
}

func (r *observationTaskDetails) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return r.detail, nil
}
func (r *observationTaskDetails) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return r.detail, nil
}
func (r *observationTaskDetails) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return r.detail, nil
}
func (r *observationTaskDetails) ListTaskCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return append([]workflow.CurrentNode(nil), r.currentNodes...), nil
}

type observationAttention struct {
	items []serverapi.WorkflowAttentionItem
}

func (r *observationAttention) List(context.Context, serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	return serverapi.WorkflowAttentionListResponse{}, nil
}
func (r *observationAttention) ListTask(context.Context, serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return serverapi.WorkflowTaskAttentionListResponse{Items: append([]serverapi.WorkflowAttentionItem(nil), r.items...)}, nil
}

type observationApprovals struct {
	approvals []clientui.PendingApproval
}

type observationDefinitions struct {
	definition serverapi.WorkflowDefinition
}

func (r observationDefinitions) GetDefinition(context.Context, runtimeids.WorkflowID) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	return r.definition, nil, nil
}

func (r observationApprovals) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{Approvals: r.approvals}, nil
}

func observationTaskDetail(kind serverapi.WorkflowTaskStatusKind) serverapi.WorkflowTaskDetail {
	return serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{
			ID:        "task-1",
			ProjectID: "project-1",
			ShortID:   "KNT-1",
		},
		Status: serverapi.WorkflowTaskStatus{Kind: kind},
	}
}

func TestObserveWorkflowTaskReturnsDoneImmediately(t *testing.T) {
	details := &observationTaskDetails{detail: observationTaskDetail(serverapi.WorkflowTaskStatusKindDone)}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			TaskDetail: details,
			Attention:  &observationAttention{},
		},
	}
	response, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
	})
	if err != nil {
		t.Fatalf("ObserveWorkflowTask: %v", err)
	}
	if len(response.Outcomes) != 1 || response.Outcomes[0].Kind != serverapi.RuntimeObservationOutcomeTaskDone {
		t.Fatalf("response = %+v", response)
	}
}

func TestObserveWorkflowTaskWatchProjectsAccessLabelsFromApprovalView(t *testing.T) {
	sessionID := "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	questionID := "approval-1"
	questionKind := serverapi.WorkflowAttentionQuestionKindApproval
	details := &observationTaskDetails{detail: observationTaskDetail(serverapi.WorkflowTaskStatusKindWaitingApproval)}
	attention := &observationAttention{items: []serverapi.WorkflowAttentionItem{{
		Kind:             string(serverapi.WorkflowTaskAttentionKindQuestion),
		TaskID:           "task-1",
		SessionID:        &sessionID,
		ApprovalID:       &questionID,
		QuestionID:       &questionID,
		Message:          observationStringPtr("stale"),
		Question:         &serverapi.WorkflowAttentionQuestionPrompt{Kind: questionKind},
		OccurredAtUnixMs: time.Now().UnixMilli(),
	}}}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			TaskDetail: details,
			Attention:  attention,
			Approvals: observationApprovals{approvals: []clientui.PendingApproval{{
				ApprovalID: questionID,
				SessionID:  sessionID,
				Question:   "authoritative question",
				Options: []clientui.ApprovalOption{
					{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Custom allow"},
					{Decision: clientui.ApprovalDecisionDeny, Label: "Custom deny"},
				},
			}}},
		},
	}
	response, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWatch,
	})
	if err != nil {
		t.Fatalf("ObserveWorkflowTask: %v", err)
	}
	question := response.Outcomes[0].Question
	if question == nil || question.Text != "authoritative question" || question.AccessOptions[0].Label != "Custom allow" {
		t.Fatalf("question = %+v", question)
	}
}

func TestObserveWorkflowTaskProjectsScriptDiagnosticAndNodeKey(t *testing.T) {
	details := observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning)
	nodeKey := "verify"
	scriptPath := "scripts/check.sh"
	attention := &observationAttention{items: []serverapi.WorkflowAttentionItem{{
		ID:          "attention-1",
		Kind:        string(serverapi.WorkflowTaskAttentionKindInterruptedCurrentNode),
		TaskID:      "task-1",
		CurrentNode: &serverapi.WorkflowTaskCurrentNode{NodeID: "node-script"},
		DetailJSON:  observationStringPtr(`{"Code":"script_failed","Fields":{"error":"script exited unsuccessfully"}}`),
	}}}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			Definitions: observationDefinitions{definition: serverapi.WorkflowDefinition{
				Nodes: []serverapi.WorkflowNode{{ID: "node-script", Key: nodeKey, ScriptPath: &scriptPath}},
			}},
			TaskDetail: &observationTaskDetails{detail: details},
			Attention:  attention,
		},
	}
	response, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
	})
	if err != nil {
		t.Fatalf("ObserveWorkflowTask: %v", err)
	}
	if len(response.Outcomes) != 1 {
		t.Fatalf("outcomes = %+v", response.Outcomes)
	}
	outcome := response.Outcomes[0]
	if outcome.Kind != serverapi.RuntimeObservationOutcomeExecutionError ||
		outcome.ScriptPath == nil || *outcome.ScriptPath != "scripts/check.sh" ||
		outcome.NodeKey == nil || *outcome.NodeKey != nodeKey ||
		outcome.ExecutionError == nil || outcome.ExecutionError.Diagnostic == nil ||
		*outcome.ExecutionError.Diagnostic != "script exited unsuccessfully" {
		t.Fatalf("script outcome = %+v", outcome)
	}
}

func TestObserveWorkflowTaskWatchProjectsExcludedCurrentNodeInterruption(t *testing.T) {
	details := observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning)
	reference, err := workflow.NewCurrentNodeReference("task-1", "node-agent", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	currentNode, err := workflow.NewCurrentNode(reference, nil, &workflow.CurrentNodeScheduling{
		State: workflow.CurrentNodeSchedulingInterrupted,
		Interruption: &workflow.CurrentNodeInterruption{
			Reason:     workflow.CurrentNodeInterruptionReasonUserInterrupt,
			Detail:     workflow.CurrentNodeInterruptionDetail{Code: "interrupted"},
			OccurredAt: time.Unix(1, 0).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("NewCurrentNode: %v", err)
	}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			TaskDetail: &observationTaskDetails{detail: details, currentNodes: []workflow.CurrentNode{currentNode}},
			Attention:  &observationAttention{},
		},
	}
	response, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWatch,
	})
	if err != nil {
		t.Fatalf("ObserveWorkflowTask: %v", err)
	}
	if len(response.Outcomes) != 1 ||
		response.Outcomes[0].Kind != serverapi.RuntimeObservationOutcomeInterrupted ||
		response.Outcomes[0].Interrupted == nil ||
		response.Outcomes[0].Interrupted.Reason != "user_interrupt" {
		t.Fatalf("response = %+v", response)
	}
}

func TestObserveWorkflowTaskPropagatesMalformedInterruptionDetail(t *testing.T) {
	service := &Service{
		readModels: ReadModels{
			TaskDetail: &observationTaskDetails{detail: observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning)},
			Attention: &observationAttention{items: []serverapi.WorkflowAttentionItem{{
				Kind:       string(serverapi.WorkflowTaskAttentionKindInterruptedCurrentNode),
				TaskID:     "task-1",
				DetailJSON: observationStringPtr("{not-json"),
			}}},
		},
	}
	_, _, err := service.observeWorkflowTaskSnapshot(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWatch,
	})
	if err == nil {
		t.Fatal("malformed interruption detail returned nil error")
	}
}

func TestObserveWorkflowTaskNormalizesDeletedTaskReadModelError(t *testing.T) {
	service := &Service{
		readModels: ReadModels{
			TaskDetail: &observationSequenceDetails{errors: []error{sql.ErrNoRows}},
			Attention:  &observationAttention{},
		},
	}
	_, _, err := service.observeWorkflowTaskSnapshot(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWatch,
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskNotFound) {
		t.Fatalf("error = %v, want ErrWorkflowTaskNotFound", err)
	}
}

func TestObserveWorkflowTaskWaitFiltersUserInterruption(t *testing.T) {
	details := observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning)
	attention := &observationAttention{items: []serverapi.WorkflowAttentionItem{{
		ID:         "attention-1",
		Kind:       string(serverapi.WorkflowTaskAttentionKindInterrupted),
		TaskID:     "task-1",
		DetailJSON: observationStringPtr(`{"Code":"user_interrupt","Fields":{}}`),
	}}}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			TaskDetail: &observationTaskDetails{detail: details},
			Attention:  attention,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := service.ObserveWorkflowTask(ctx, serverapi.WorkflowTaskObservationRequest{
			TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
		})
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("wait returned success after filtering interruption and cancellation")
	}
}

func TestObserveWorkflowTaskReportsParallelEligibleErrorsTogether(t *testing.T) {
	firstSession := "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	secondSession := "8b0364cc-5c6c-412e-a4e8-31380661d1e1"
	details := observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning)
	attention := &observationAttention{items: []serverapi.WorkflowAttentionItem{
		{
			ID:          "attention-1",
			Kind:        string(serverapi.WorkflowTaskAttentionKindInterrupted),
			TaskID:      "task-1",
			SessionID:   &firstSession,
			CurrentNode: &serverapi.WorkflowTaskCurrentNode{NodeID: "node-a"},
			DetailJSON:  observationStringPtr(`{"Code":"agent_failed","Fields":{"error":"first"}}`),
		},
		{
			ID:          "attention-2",
			Kind:        string(serverapi.WorkflowTaskAttentionKindInterrupted),
			TaskID:      "task-1",
			SessionID:   &secondSession,
			CurrentNode: &serverapi.WorkflowTaskCurrentNode{NodeID: "node-b"},
			DetailJSON:  observationStringPtr(`{"Code":"agent_failed","Fields":{"error":"second"}}`),
		},
	}}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			Definitions: observationDefinitions{definition: serverapi.WorkflowDefinition{
				Nodes: []serverapi.WorkflowNode{
					{ID: "node-a", Key: "first"},
					{ID: "node-b", Key: "second"},
				},
			}},
			TaskDetail: &observationTaskDetails{detail: details},
			Attention:  attention,
		},
	}
	response, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
	})
	if err != nil {
		t.Fatalf("ObserveWorkflowTask: %v", err)
	}
	if len(response.Outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want two parallel outcomes", response.Outcomes)
	}
}

func TestObserveWorkflowTaskOmitsAccessPromptAfterApprovalViewRace(t *testing.T) {
	sessionID := "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	approvalID := "approval-raced"
	service := &Service{
		readModels: ReadModels{
			TaskDetail: &observationTaskDetails{detail: observationTaskDetail(serverapi.WorkflowTaskStatusKindWaitingApproval)},
			Attention: &observationAttention{items: []serverapi.WorkflowAttentionItem{{
				Kind:       string(serverapi.WorkflowTaskAttentionKindQuestion),
				TaskID:     "task-1",
				SessionID:  &sessionID,
				ApprovalID: &approvalID,
				QuestionID: &approvalID,
				Question:   &serverapi.WorkflowAttentionQuestionPrompt{Kind: serverapi.WorkflowAttentionQuestionKindApproval},
			}}},
			Approvals: observationApprovals{},
		},
	}
	response, ready, err := service.observeWorkflowTaskSnapshot(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWatch,
	})
	if err != nil {
		t.Fatalf("observeWorkflowTaskSnapshot: %v", err)
	}
	if ready || len(response.Outcomes) != 0 {
		t.Fatalf("response = %+v ready=%t, want omitted resolved prompt", response, ready)
	}
}

func TestObserveWorkflowTaskReevaluatesAfterMatchingTaskEvent(t *testing.T) {
	details := &observationSequenceDetails{
		details: []serverapi.WorkflowTaskDetail{
			observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning),
			observationTaskDetail(serverapi.WorkflowTaskStatusKindDone),
		},
		first: make(chan struct{}),
	}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			TaskDetail: details,
			Attention:  &observationAttention{},
		},
	}
	result := make(chan serverapi.WorkflowTaskObservationResponse, 1)
	errs := make(chan error, 1)
	go func() {
		response, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
			TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
		})
		result <- response
		errs <- err
	}()
	<-details.first
	projectID := "project-1"
	service.events.publish(serverapi.WorkflowProjectEvent{
		ProjectID:       &projectID,
		Resource:        serverapi.WorkflowProjectEventResourceTask,
		PrimaryEntityID: "task-1",
	})
	select {
	case response := <-result:
		if err := <-errs; err != nil {
			t.Fatalf("ObserveWorkflowTask: %v", err)
		}
		if len(response.Outcomes) != 1 || response.Outcomes[0].Kind != serverapi.RuntimeObservationOutcomeTaskDone {
			t.Fatalf("response = %+v", response)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for matching task event reevaluation")
	}
}

func TestObserveWorkflowTaskPropagatesClosedProjectStream(t *testing.T) {
	events := newWorkflowProjectEventBroker()
	events.Close(io.EOF)
	service := &Service{
		events: events,
		readModels: ReadModels{
			TaskDetail: &observationTaskDetails{detail: observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning)},
			Attention:  &observationAttention{},
		},
	}
	_, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
	})
	if err == nil {
		t.Fatal("ObserveWorkflowTask returned nil after stream closure")
	}
}

func TestObserveWorkflowTaskPropagatesDeletionOnReevaluation(t *testing.T) {
	details := &observationSequenceDetails{
		details: []serverapi.WorkflowTaskDetail{
			observationTaskDetail(serverapi.WorkflowTaskStatusKindRunning),
		},
		errors: []error{serverapi.ErrWorkflowTaskNotFound},
		first:  make(chan struct{}),
	}
	service := &Service{
		events: newWorkflowProjectEventBroker(),
		readModels: ReadModels{
			TaskDetail: details,
			Attention:  &observationAttention{},
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
			TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationModeWait,
		})
		result <- err
	}()
	<-details.first
	projectID := "project-1"
	service.events.publish(serverapi.WorkflowProjectEvent{
		ProjectID:       &projectID,
		Resource:        serverapi.WorkflowProjectEventResourceTask,
		PrimaryEntityID: "task-1",
	})
	select {
	case err := <-result:
		if err != serverapi.ErrWorkflowTaskNotFound {
			t.Fatalf("error = %v, want task not found", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for deletion reevaluation")
	}
}

func observationStringPtr(value string) *string {
	return &value
}
