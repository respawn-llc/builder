package promptcontrol

import (
	"context"
	"fmt"
	"strings"

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

func (s *ApprovalViewService) ListPendingApprovalsBySession(_ context.Context, req serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ApprovalListPendingBySessionResponse{}, err
	}
	if s == nil || s.prompts == nil {
		return serverapi.ApprovalListPendingBySessionResponse{}, fmt.Errorf("pending prompt source is required")
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.ApprovalListPendingBySessionResponse{}, fmt.Errorf("pending approval session identity: %w", err)
	}
	items := s.prompts.ListPendingPrompts(sessionID.String())
	approvals := make([]clientui.PendingApproval, 0, len(items))
	for _, item := range items {
		if !item.Request.Approval {
			continue
		}
		toolCallID, stepID, err := pendingToolCallIdentity(item.Request.ToolCallID, item.Request.StepID)
		if err != nil {
			return serverapi.ApprovalListPendingBySessionResponse{}, fmt.Errorf("pending approval identity: %w", err)
		}
		approvals = append(approvals, clientui.PendingApproval{
			ToolCallID:    toolCallID,
			SessionID:     sessionID,
			StepID:        stepID,
			Question:      item.Request.Question,
			Options:       approvalOptionsFromRequest(item.Request.ApprovalOptions),
			AccessTargets: append([]clientui.FileAccessTarget(nil), item.Request.AccessTargets...),
			CreatedAt:     item.CreatedAt,
		})
	}
	return serverapi.ApprovalListPendingBySessionResponse{Approvals: approvals}, nil
}

func pendingToolCallIdentity(rawToolCallID, rawStepID string) (clientui.ToolCallID, runtimeids.StepID, error) {
	toolCallID := clientui.ToolCallID(rawToolCallID)
	if err := toolCallID.Validate(); err != nil {
		return "", runtimeids.StepID{}, err
	}
	stepID, err := runtimeids.ParseStepID(rawStepID)
	if err != nil {
		return "", runtimeids.StepID{}, err
	}
	return toolCallID, stepID, nil
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
