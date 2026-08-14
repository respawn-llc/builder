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

func (s *AskViewService) ListPendingAsksBySession(ctx context.Context, req serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.AskListPendingBySessionRequest]) (serverapi.AskListPendingBySessionResponse, error) {
		sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
		if err != nil {
			return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf("pending ask session identity: %w", err)
		}
		return s.ListPendingAsksBySessionValidated(ctx, validated, sessionID)
	})
}

func (s *AskViewService) ListPendingAsksBySessionValidated(_ context.Context, _ servicecontract.Validated[serverapi.AskListPendingBySessionRequest], sessionID runtimeids.SessionID) (serverapi.AskListPendingBySessionResponse, error) {
	if s == nil || s.prompts == nil {
		return serverapi.AskListPendingBySessionResponse{}, fmt.Errorf("pending prompt source is required")
	}
	items := s.prompts.ListPendingPrompts(sessionID.String())
	asks := make([]clientui.PendingAsk, 0, len(items))
	for _, item := range items {
		if item.Request.Approval {
			continue
		}
		promptID, stepID, err := pendingPromptIdentity(item.Request.ID, item.Request.StepID)
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
				item.Request.ID,
				err,
			)
		}
		asks = append(asks, clientui.PendingAsk{
			PromptID:               promptID,
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
