package promptcontrol

import (
	"context"
	"testing"
	"time"

	"core/server/registry"
	askquestion "core/server/tools"
	"core/shared/clientui"
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
	svc := NewApprovalViewService(&stubApprovalPendingPromptSource{items: []registry.PendingPromptSnapshot{
		{Request: askquestion.AskQuestionRequest{ID: "ask-1", Question: "one?"}, CreatedAt: now},
		{Request: askquestion.AskQuestionRequest{ID: "approval-1", Question: "allow?", Approval: true, ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"}}}, CreatedAt: now.Add(time.Second)},
	}})

	resp, err := svc.ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListPendingApprovalsBySession: %v", err)
	}
	if len(resp.Approvals) != 1 {
		t.Fatalf("expected one pending approval, got %+v", resp)
	}
	if resp.Approvals[0].ApprovalID != "approval-1" {
		t.Fatalf("unexpected pending approval: %+v", resp.Approvals[0])
	}
	if len(resp.Approvals[0].Options) != 2 || resp.Approvals[0].Options[0].Decision != clientui.ApprovalDecisionAllowOnce {
		t.Fatalf("unexpected approval options: %+v", resp.Approvals[0].Options)
	}
}

func TestApprovalViewServiceRequiresSessionID(t *testing.T) {
	if _, err := NewApprovalViewService(&stubApprovalPendingPromptSource{}).ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{}); err == nil {
		t.Fatal("expected validation error")
	}
}
