package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type userTurnProjection struct {
	ExecutionText string
	HistoryText   string
}

func queuedUserTurnResponse(compacted bool, queueItemID string) serverapi.RuntimeSubmitUserTurnResponse {
	return serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, ResultKind: clientui.UserTurnResultKindQueued, Steered: true, QueueItemID: queueItemID}
}

func userTurnMemoRequest(req serverapi.RuntimeSubmitUserTurnRequest) sessionUserTurnMemoRequest {
	memo := sessionUserTurnMemoRequest{
		SessionID: strings.TrimSpace(req.SessionID),
		Kind:      req.Input.Kind,
	}
	if req.Input.Text != nil {
		memo.Text = *req.Input.Text
	}
	if req.Input.PromptCommand != nil {
		memo.Name = strings.TrimSpace(req.Input.PromptCommand.Name)
		memo.Arguments = req.Input.PromptCommand.Arguments
	}
	return memo
}

func sameSessionUserTurnMemoRequest(left, right sessionUserTurnMemoRequest) bool {
	return left.SessionID == right.SessionID &&
		left.Kind == right.Kind &&
		left.Text == right.Text &&
		left.Name == right.Name &&
		left.Arguments == right.Arguments
}

func (s *Service) resolveUserTurnInput(ctx context.Context, sessionID string, input serverapi.RuntimeUserTurnInput) (userTurnProjection, error) {
	if input.Kind == runtimeinput.KindPromptCommand && (s == nil || s.promptCommands == nil) {
		return userTurnProjection{}, errors.New("prompt command resolver is required")
	}
	execution, err := input.ExecutionText(func(command runtimeinput.PromptCommand) (string, error) {
		return s.promptCommands.ResolvePromptCommand(ctx, sessionID, command.Name, command.Arguments)
	})
	if err != nil {
		return userTurnProjection{}, err
	}
	history, err := input.CanonicalHistoryText()
	if err != nil {
		return userTurnProjection{}, err
	}
	return userTurnProjection{ExecutionText: execution, HistoryText: history}, nil
}

func (s *Service) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	clientRequestID, err := runtimeids.ParseRuntimeClientRequestID(req.ClientRequestID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	memoReq := userTurnMemoRequest(req)
	return memoizedRuntimeCommand(ctx, clientRequestID.String(), memoReq, s.userTurns, sameSessionUserTurnMemoRequest, func(ctx context.Context) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
		projection, err := s.resolveUserTurnInput(ctx, req.SessionID, req.Input)
		if err != nil {
			return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
		}
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		response, commandErr := s.submitUserTurn(attempt, clientRequestID, memoReq, projection, req)
		if commandErr == nil {
			commandErr = response.Validate()
		}
		return response, attempt.Accepted(), commandErr
	})
}

func (s *Service) submitUserTurn(
	attempt *runtimeCommandAttempt,
	clientRequestID runtimeids.RuntimeClientRequestID,
	memoReq sessionUserTurnMemoRequest,
	projection userTurnProjection,
	req serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	var response serverapi.RuntimeSubmitUserTurnResponse
	err := s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		defer func() {
			if !attempt.Accepted() {
				return
			}
			if _, _, err := s.recordPromptHistory(context.Background(), memoReq.SessionID, clientRequestID.String(), projection.HistoryText); err != nil {
				engine.ReportPromptHistoryPersistError(err.Error())
			}
		}()
		shouldCompact, err := engine.ShouldCompactBeforeUserMessage(runCtx, projection.ExecutionText)
		if err != nil {
			return err
		}
		compacted := false
		compactionBusy := false
		var acceptedCompactionErr error
		if shouldCompact {
			compactionAccepted, compactErr := s.runPreSubmitCompaction(
				attempt.Context(),
				clientRequestID.String(),
				memoReq.SessionID,
				engine,
			)
			if compactionAccepted {
				compacted = true
				acceptedCompactionErr = compactErr
			} else if compactErr != nil {
				if !errors.Is(compactErr, runtime.ErrAgentBusy) {
					return compactErr
				}
				compactionBusy = true
			}
		}
		if compactionBusy {
			queued, queueErr := engine.QueueUserMessageForAutoDrainWithAcceptance(
				projection.ExecutionText,
				clientRequestID.String(),
				attempt.Accept,
			)
			if queueErr != nil {
				return errors.Join(acceptedCompactionErr, queueErr)
			}
			response = queuedUserTurnResponse(compacted, queued.ID)
			return acceptedCompactionErr
		}
		outcome, queued, err := engine.SubmitUserMessageOrSteerWithAcceptance(
			runCtx,
			projection.ExecutionText,
			clientRequestID.String(),
			attempt.Accept,
		)
		if err != nil {
			return errors.Join(acceptedCompactionErr, err)
		}
		if queued != nil {
			response = queuedUserTurnResponse(compacted, queued.ID)
			return acceptedCompactionErr
		}
		response = serverapi.RuntimeSubmitUserTurnResponse{
			Compacted:  compacted,
			ResultKind: clientui.UserTurnResultKindNoFinal,
		}
		switch outcome.Kind {
		case runtime.UserTurnResultAssistantFinal:
			response.ResultKind = clientui.UserTurnResultKindAssistantFinal
			if outcome.FinalAnswer != nil && outcome.FinalAnswer.Content != nil {
				response.Message = outcome.FinalAnswer.Content
			}
		case runtime.UserTurnResultSilentFinal:
			response.ResultKind = clientui.UserTurnResultKindSilentFinal
			response.Message = textutil.Value("")
		}
		return acceptedCompactionErr
	})
	if err == nil || attempt.Accepted() || !errors.Is(err, serverapi.ErrSessionRunStarting) {
		return response, err
	}
	activeResponse, steered, activeErr := s.trySubmitUserTurnAsActiveExecution(
		attempt,
		clientRequestID,
		memoReq,
		projection,
		req,
	)
	if activeErr != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, activeErr
	}
	if steered {
		return activeResponse, nil
	}
	return serverapi.RuntimeSubmitUserTurnResponse{}, err
}

func (s *Service) trySubmitUserTurnAsActiveExecution(
	attempt *runtimeCommandAttempt,
	clientRequestID runtimeids.RuntimeClientRequestID,
	memoReq sessionUserTurnMemoRequest,
	projection userTurnProjection,
	req serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
	var response serverapi.RuntimeSubmitUserTurnResponse
	steered := false
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, errors.New("session runtime authority is required")
	}
	err = s.withLiveExecutionRuntime(attempt.Context(), sessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		item, accepted, err := engine.QueueUserMessageForActiveRunWithAcceptance(
			callbackCtx,
			projection.ExecutionText,
			clientRequestID,
			attempt.Accept,
		)
		if errors.Is(err, runtime.ErrNoActiveLiveRun) {
			if !activeExecutionAllowsUserTurnAutoDrain(runtimeactivity.ActiveStepFromProvider(engine)) {
				return serverapi.ErrSessionRunStarting
			}
			item, err = engine.QueueUserMessageForAutoDrainWithAcceptance(
				projection.ExecutionText,
				clientRequestID.String(),
				attempt.Accept,
			)
			accepted = err == nil
		}
		if err != nil {
			return err
		}
		if !accepted {
			return serverapi.ErrSessionRunStarting
		}
		response = queuedUserTurnResponse(false, item.ID)
		steered = true
		if _, _, err := s.recordPromptHistory(context.Background(), memoReq.SessionID, clientRequestID.String(), projection.HistoryText); err != nil {
			engine.ReportPromptHistoryPersistError(err.Error())
		}
		return nil
	})
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, steered, err
	}
	return response, steered, nil
}

func activeExecutionAllowsUserTurnAutoDrain(snapshot *runtimeactivity.ActiveStepSnapshot) bool {
	if snapshot == nil {
		return false
	}
	switch snapshot.ActiveKind {
	case clientui.RuntimeActivityActiveKindCompaction, clientui.RuntimeActivityActiveKindPreSubmitCompaction:
		return true
	default:
		return false
	}
}

func (s *Service) runPreSubmitCompaction(
	ctx context.Context,
	requestID string,
	sessionID string,
	engine *runtime.Engine,
) (bool, error) {
	memoReq := sessionOnlyMemoRequest{SessionID: strings.TrimSpace(sessionID)}
	return memoizedRuntimeCommand(ctx, requestID, memoReq, s.preSubmitCompactions, sameComparable[sessionOnlyMemoRequest], func(ctx context.Context) (bool, bool, error) {
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		_, commandErr := engine.CompactContextForPreSubmitWithAcceptance(attempt.Context(), attempt.Accept)
		accepted := attempt.Accepted()
		return accepted, accepted, commandErr
	})
}
