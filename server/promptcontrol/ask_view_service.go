package promptcontrol

import (
	"context"
	"fmt"
	"strings"

	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type AskViewService struct {
	prompts PendingPromptSource
}

func NewAskViewService(prompts PendingPromptSource) *AskViewService {
	return &AskViewService{prompts: prompts}
}

func (s *AskViewService) ListPendingAsksBySession(_ context.Context, req serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.AskListPendingBySessionResponse{}, err
	}
	if s == nil || s.prompts == nil {
		return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf("pending prompt source is required")
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf("pending ask session identity: %w", err)
	}
	items := s.prompts.ListPendingPrompts(sessionID.String())
	asks := make([]clientui.PendingAsk, 0, len(items))
	for _, item := range items {
		if item.Request.Approval {
			continue
		}
		toolCallID, stepID, err := pendingToolCallIdentity(item.Request.ToolCallID, item.Request.StepID)
		if err != nil {
			return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf("pending ask identity: %w", err)
		}
		recommendedOptionIndex, err := DecodeLegacyRecommendedOptionIndex(
			item.Request.RecommendedOptionIndex,
			len(item.Request.Suggestions),
		)
		if err != nil {
			return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf(
				"pending ask %q: %w",
				item.Request.ToolCallID,
				err,
			)
		}
		asks = append(asks, clientui.PendingAsk{
			ToolCallID:             toolCallID,
			SessionID:              sessionID,
			StepID:                 stepID,
			Question:               item.Request.Question,
			Suggestions:            append([]string(nil), item.Request.Suggestions...),
			RecommendedOptionIndex: recommendedOptionIndex,
			CreatedAt:              item.CreatedAt,
		})
	}
	return serverapi.AskListPendingBySessionResponse{Asks: asks}, nil
}

var _ servicecontract.AskViewService = (*AskViewService)(nil)
