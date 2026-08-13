package promptcontrol

import (
	"context"
	"fmt"

	"core/server/registry"
	askquestion "core/server/tools"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type PendingPromptSource interface {
	ListPendingPrompts(sessionID string) []registry.PendingPromptSnapshot
}

type ApprovalViewService struct {
	prompts PendingPromptSource
}

func NewApprovalViewService(prompts PendingPromptSource) *ApprovalViewService {
	return &ApprovalViewService{prompts: prompts}
}

func (s *ApprovalViewService) ListPendingApprovalsBySession(ctx context.Context, req serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return servicecontract.WithValidated(
		req,
		servicecontract.SemanticValidationRequired,
		func(validated servicecontract.Validated[serverapi.ApprovalListPendingBySessionRequest]) (serverapi.ApprovalListPendingBySessionResponse, error) {
			return s.ListPendingApprovalsBySessionValidated(ctx, validated)
		},
	)
}

func (s *ApprovalViewService) ListPendingApprovalsBySessionValidated(
	_ context.Context,
	validated servicecontract.Validated[serverapi.ApprovalListPendingBySessionRequest],
) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return s.listPendingApprovalsBySession(validated.SessionID(validated.Value().SessionID))
}

func (s *ApprovalViewService) listPendingApprovalsBySession(sessionID runtimeids.SessionID) (serverapi.ApprovalListPendingBySessionResponse, error) {
	if s == nil || s.prompts == nil {
		return serverapi.ApprovalListPendingBySessionResponse{}, fmt.Errorf("pending prompt source is required")
	}
	items := s.prompts.ListPendingPrompts(sessionID.String())
	approvals := make([]clientui.PendingApproval, 0, len(items))
	for _, item := range items {
		if !item.Request.Approval {
			continue
		}
		approvals = append(approvals, clientui.PendingApproval{
			PromptID:  item.PromptID,
			SessionID: sessionID,
			StepID:    item.StepID,
			Question:  item.Request.Question,
			Options:   approvalOptionsFromRequest(item.Request.ApprovalOptions),
			CreatedAt: item.CreatedAt,
		})
	}
	return serverapi.ApprovalListPendingBySessionResponse{Approvals: approvals}, nil
}

func approvalOptionsFromRequest(options []askquestion.AskQuestionApprovalOption) []clientui.ApprovalOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]clientui.ApprovalOption, 0, len(options))
	for _, option := range options {
		out = append(out, clientui.ApprovalOption{
			Decision: clientui.ApprovalDecision(option.Decision),
			Label:    option.Label,
		})
	}
	return out
}

var _ servicecontract.ApprovalViewService = (*ApprovalViewService)(nil)
