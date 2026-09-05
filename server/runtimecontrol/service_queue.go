package runtimecontrol

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (s *Service) AdmitChatQueuedUserInput(
	ctx context.Context,
	req serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.ChatInputAdmissionResult, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ChatInputAdmissionResult{}, err
	}
	projection, err := s.resolveUserTurnInput(ctx, req.SessionID, req.Input)
	if err != nil {
		return serverapi.ChatInputAdmissionResult{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.ChatInputAdmissionResult{}, err
	}
	if s == nil || s.authority == nil {
		return serverapi.ChatInputAdmissionResult{}, errors.New("session runtime authority is required")
	}
	attempt := newRuntimeCommandAttempt(ctx)
	defer attempt.Finish()
	var queued runtime.QueuedUserMessage
	err = s.authority.WithCurrentRuntime(attempt.Context(), sessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		var queueErr error
		queued, queueErr = engine.QueueUserInputWithAcceptance(
			runCtx,
			projection.queuedInput(),
			attempt.Accept,
		)
		return queueErr
	})
	if !attempt.Accepted() {
		return serverapi.ChatInputAdmissionResult{}, err
	}
	queueItemID, parseErr := runtimeids.ParseQueueItemID(queued.ID)
	if parseErr != nil {
		return serverapi.ChatInputAdmissionResult{Accepted: true}, errors.Join(err, parseErr)
	}
	historyErr := s.recordAcceptedUserTurnHistory(canonicalUserTurnRequest(req), projection)
	return serverapi.ChatInputAdmissionResult{
		QueueItemID:          queueItemID,
		Accepted:             true,
		PromptHistoryFailure: historyErr,
	}, err
}
