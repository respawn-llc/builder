package workflowsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type observationApprovalViewStub struct {
	approvals []clientui.PendingApproval
}

func (s observationApprovalViewStub) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{Approvals: s.approvals}, nil
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
	questionID := "access-1"
	message := "Allow access?"
	createdAt := time.UnixMilli(42).UTC()
	service := &Service{readModels: ReadModels{
		Approvals: observationApprovalViewStub{approvals: []clientui.PendingApproval{{
			ApprovalID: questionID,
			SessionID:  sessionID.String(),
			Question:   message,
			Options:    []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"}},
			CreatedAt:  createdAt,
		}}},
	}}
	item := serverapi.WorkflowAttentionItem{
		Kind:             string(serverapi.WorkflowTaskAttentionKindQuestion),
		SessionID:        &[]string{sessionID.String()}[0],
		QuestionID:       &questionID,
		Message:          &message,
		Question:         &serverapi.WorkflowAttentionQuestionPrompt{Kind: serverapi.WorkflowAttentionQuestionKindApproval},
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
