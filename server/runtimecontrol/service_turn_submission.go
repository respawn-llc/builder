package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/runtimeops"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type userTurnProjection struct {
	ExecutionText string
	HistoryText   string
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
	memoReq := userTurnMemoRequest(req)
	return runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, sameSessionUserTurnMemoRequest, func(ctx context.Context, attempt runtimeops.Attempt) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		projection, err := s.resolveUserTurnInput(ctx, req.SessionID, req.Input)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return serverapi.RuntimeSubmitUserTurnResponse{}, err
		}
		sessionID, err := runtimeids.ParseSessionID(memoReq.SessionID)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return serverapi.RuntimeSubmitUserTurnResponse{}, err
		}
		driver, err := runtimecommand.NewUserTurnDriver(runtimecommand.UserTurnDriverOptions{
			SessionID:                       sessionID,
			ExecutionText:                   projection.ExecutionText,
			HistoryText:                     projection.HistoryText,
			ClientRequestID:                 req.OperationRef.ClientRequestID,
			OperationRef:                    req.OperationRef,
			PreSubmitCompactionOperationRef: req.PreSubmitCompactionOperationRef,
			Operations:                      s.operations,
			AttemptContext:                  attempt.Context(),
			RecordPromptHistory: func(
				historyCtx context.Context,
				engine *runtime.Engine,
				historySessionID runtimeids.SessionID,
				sourceID string,
				text string,
			) {
				if _, _, historyErr := s.recordPromptHistory(historyCtx, historySessionID.String(), sourceID, text); historyErr != nil {
					engine.ReportPromptHistoryPersistError(historyErr.Error())
				}
			},
		})
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return serverapi.RuntimeSubmitUserTurnResponse{}, err
		}
		outcome, runErr := s.runAgentOperation(attempt.Context(), req.SessionID, driver)
		typed, ok := outcome.(runtimecommand.UserTurnOperationOutcome)
		if outcome != nil && !ok {
			return serverapi.RuntimeSubmitUserTurnResponse{}, errors.New("user turn driver returned the wrong outcome")
		}
		return typed.Response, runErr
	})
}
