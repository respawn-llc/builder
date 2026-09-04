package runtimecontrol

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (s *Service) AdmitQueuedUserInput(
	ctx context.Context,
	req serverapi.RuntimeSubmitUserTurnRequest,
) (runtimeids.QueueItemID, bool, error) {
	if err := req.Validate(); err != nil {
		return runtimeids.QueueItemID{}, false, err
	}
	projection, err := s.resolveUserTurnInput(ctx, req.SessionID, req.Input)
	if err != nil {
		return runtimeids.QueueItemID{}, false, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return runtimeids.QueueItemID{}, false, err
	}
	if s == nil || s.authority == nil {
		return runtimeids.QueueItemID{}, false, errors.New("session runtime authority is required")
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
		return runtimeids.QueueItemID{}, false, err
	}
	queueItemID, parseErr := runtimeids.ParseQueueItemID(queued.ID)
	if parseErr != nil {
		return runtimeids.QueueItemID{}, true, errors.Join(err, parseErr)
	}
	historyErr := s.recordAcceptedUserTurnHistory(canonicalUserTurnRequest(req), projection)
	err = errors.Join(err, historyErr)
	return queueItemID, true, err
}
