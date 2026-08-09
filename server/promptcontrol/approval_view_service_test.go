package promptcontrol

import (
	"context"
	"testing"
	"time"

	"core/server/registry"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type stubApprovalPendingPromptSource struct {
	items []registry.PendingPromptSnapshot
}

func (s *stubApprovalPendingPromptSource) ListPendingPrompts(string) []registry.PendingPromptSnapshot {
	return append([]registry.PendingPromptSnapshot(nil), s.items...)
}

func TestServiceListsPendingApprovalsBySession(t *testing.T) {
	now := time.Now().UTC()
	stepID := promptViewStepID(t)
	svc := NewApprovalViewService(&stubApprovalPendingPromptSource{items: []registry.PendingPromptSnapshot{
		{Request: askquestion.AskQuestionRequest{ID: "ask-1", StepID: stepID.String(), Question: "one?"}, CreatedAt: now},
		{Request: askquestion.AskQuestionRequest{ID: "approval-1", StepID: stepID.String(), Question: "allow?", Approval: true, ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"}}}, CreatedAt: now.Add(time.Second)},
	}})

	resp, err := svc.ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListPendingApprovalsBySession: %v", err)
	}
	if len(resp.Approvals) != 1 {
		t.Fatalf("expected one pending approval, got %+v", resp)
	}
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if resp.Approvals[0].PromptID != clientui.PromptID("approval-1") ||
		resp.Approvals[0].SessionID != sessionID ||
		resp.Approvals[0].StepID != stepID {
		t.Fatalf("unexpected pending approval: %+v", resp.Approvals[0])
	}
	if len(resp.Approvals[0].Options) != 2 || resp.Approvals[0].Options[0].Decision != clientui.ApprovalDecisionAllowOnce {
		t.Fatalf("unexpected approval options: %+v", resp.Approvals[0].Options)
	}
}

func TestApprovalViewServiceRejectsMalformedPendingPromptIdentity(t *testing.T) {
	for name, request := range map[string]askquestion.AskQuestionRequest{
		"prompt": {ID: " approval-1", StepID: promptViewStepID(t).String(), Question: "allow?", Approval: true},
		"step":   {ID: "approval-1", StepID: "step-1", Question: "allow?", Approval: true},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewApprovalViewService(&stubApprovalPendingPromptSource{items: []registry.PendingPromptSnapshot{{Request: request}}})
			if _, err := svc.ListPendingApprovalsBySession(
				context.Background(),
				serverapi.ApprovalListPendingBySessionRequest{SessionID: "session-1"},
			); err == nil {
				t.Fatal("accepted malformed pending prompt identity")
			}
		})
	}
}

func TestApprovalViewServiceRequiresSessionID(t *testing.T) {
	if _, err := NewApprovalViewService(&stubApprovalPendingPromptSource{}).ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{}); err == nil {
		t.Fatal("expected validation error")
	}
}
