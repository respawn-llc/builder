package workflowsvc

import (
	"context"
	"errors"
	"io"
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
			ToolCallID: clientui.ToolCallID(questionID),
			SessionID:  sessionID,
			StepID:     stepID,
			Question:   message,
			Options:    []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}},
			CreatedAt:  createdAt,
		}}},
	}}
	item := serverapi.WorkflowAttentionItem{
		Kind:    string(serverapi.WorkflowTaskAttentionKindQuestion),
		Message: &message,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{
			SessionID: sessionID, StepID: stepID, ToolCallID: clientui.ToolCallID(questionID),
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
