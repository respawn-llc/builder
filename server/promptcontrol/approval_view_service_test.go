package promptcontrol

import (
	"context"
	"testing"

	"core/server/registry"
	askquestion "core/server/tools"
	"core/shared/serverapi"
)

type stubApprovalPendingPromptSource struct {
	items []registry.PendingPromptSnapshot
}

func (s *stubApprovalPendingPromptSource) ListPendingPrompts(string) []registry.PendingPromptSnapshot {
	return append([]registry.PendingPromptSnapshot(nil), s.items...)
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
