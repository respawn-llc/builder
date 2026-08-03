package promptcontrol

import (
	"context"
	"fmt"
	"strings"

	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
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
	items := s.prompts.ListPendingPrompts(strings.TrimSpace(req.SessionID))
	asks := make([]clientui.PendingAsk, 0, len(items))
	for _, item := range items {
		if item.Request.Approval {
			continue
		}
		recommendedOptionIndex, err := DecodeLegacyRecommendedOptionIndex(
			item.Request.RecommendedOptionIndex,
			len(item.Request.Suggestions),
		)
		if err != nil {
			return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf(
				"pending ask %q: %w",
				item.Request.ID,
				err,
			)
		}
		asks = append(asks, clientui.PendingAsk{
			AskID:                  item.Request.ID,
			SessionID:              strings.TrimSpace(req.SessionID),
			Question:               item.Request.Question,
			Suggestions:            append([]string(nil), item.Request.Suggestions...),
			RecommendedOptionIndex: recommendedOptionIndex,
			CreatedAt:              item.CreatedAt,
		})
	}
	return serverapi.AskListPendingBySessionResponse{Asks: asks}, nil
}

var _ servicecontract.AskViewService = (*AskViewService)(nil)
