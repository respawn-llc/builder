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
		{Request: askquestion.AskQuestionRequest{ToolCallID: "ask-1", StepID: promptViewStepID, Question: "one?"}, CreatedAt: now},
		{Request: askquestion.AskQuestionRequest{
			ToolCallID: "approval-1",
			StepID:     promptViewStepID,
			Approval:   true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{
				{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
				{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
			},
			AccessTargets: []askquestion.FileAccessTarget{
				{RequestedPath: "alias/first", ResolvedPath: "/outside/target"},
				{RequestedPath: "alias/second", ResolvedPath: "/outside/target"},
			},
		}, CreatedAt: now.Add(time.Second)},
	}})

	resp, err := svc.ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListPendingApprovalsBySession: %v", err)
	}
	if len(resp.Approvals) != 1 {
		t.Fatalf("expected one pending approval, got %+v", resp)
	}
	if resp.Approvals[0].ToolCallID != clientui.ToolCallID("approval-1") ||
		resp.Approvals[0].SessionID.String() != "session-1" ||
		resp.Approvals[0].StepID.String() != promptViewStepID {
		t.Fatalf("unexpected pending approval: %+v", resp.Approvals[0])
	}
	if len(resp.Approvals[0].Options) != 2 || resp.Approvals[0].Options[0].Decision != clientui.ApprovalDecisionAllowOnce {
		t.Fatalf("unexpected approval options: %+v", resp.Approvals[0].Options)
	}
	wantTargets := []clientui.FileAccessTarget{
		{RequestedPath: "alias/first", ResolvedPath: "/outside/target"},
		{RequestedPath: "alias/second", ResolvedPath: "/outside/target"},
	}
	if len(resp.Approvals[0].AccessTargets) != len(wantTargets) ||
		resp.Approvals[0].AccessTargets[0] != wantTargets[0] ||
		resp.Approvals[0].AccessTargets[1] != wantTargets[1] {
		t.Fatalf("approval access targets = %+v, want ordered aliases %+v", resp.Approvals[0].AccessTargets, wantTargets)
	}
}

func TestApprovalViewServiceRejectsMalformedPendingToolCallIdentity(t *testing.T) {
	for name, request := range map[string]askquestion.AskQuestionRequest{
		"prompt": {ToolCallID: " approval-1", StepID: promptViewStepID, Question: "allow?", Approval: true},
		"step":   {ToolCallID: "approval-1", StepID: "step-1", Question: "allow?", Approval: true},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewApprovalViewService(&stubApprovalPendingPromptSource{items: []registry.PendingPromptSnapshot{{Request: request}}})
			if _, err := svc.ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{SessionID: "session-1"}); err == nil {
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
