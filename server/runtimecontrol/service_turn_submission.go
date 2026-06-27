package runtimecontrol

import (
	"context"
	"strings"

	"core/server/runtime"
	"core/shared/serverapi"
)

func (s *Service) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	memoReq := turnSubmitMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text, PromptHistoryRecorded: req.PromptHistoryRecorded}
	return s.turnSubmits.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameTurnSubmitMemoRequest, func(ctx context.Context) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		runCtx := context.Background()
		if ctx != nil {
			runCtx = context.WithoutCancel(ctx)
		}
		var resp serverapi.RuntimeSubmitUserTurnResponse
		err := s.withRuntimeAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			shouldCompact, err := engine.ShouldCompactBeforeUserMessage(runCtx, memoReq.Text)
			if err != nil {
				return err
			}
			compacted := false
			if shouldCompact {
				if err := engine.CompactContextForPreSubmit(runCtx); err != nil {
					if !runtime.IsAgentBusyError(err) {
						return err
					}
				} else {
					compacted = true
				}
			}
			if !req.PromptHistoryRecorded {
				if _, _, err := s.recordPromptHistory(runCtx, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text); err != nil {
					return err
				}
			}
			msg, queued, err := engine.SubmitUserMessageOrSteer(runCtx, memoReq.Text, strings.TrimSpace(req.ClientRequestID))
			if err != nil {
				return err
			}
			if queued != nil {
				resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
				return nil
			}
			resp = serverapi.RuntimeSubmitUserTurnResponse{Message: msg.Content, Compacted: compacted}
			return nil
		})
		return resp, err
	})
}
