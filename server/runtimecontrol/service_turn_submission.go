package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflowexecution"
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

func runtimeControlResponseFromUserTurn(result runtime.UserTurnResult) serverapi.RuntimeSubmitUserTurnResponse {
	response := serverapi.RuntimeSubmitUserTurnResponse{
		ResultKind: clientui.UserTurnResultKindNoFinal,
	}
	switch result.Kind {
	case runtime.UserTurnResultAssistantFinal:
		response.ResultKind = clientui.UserTurnResultKindAssistantFinal
		if result.FinalAnswer != nil && result.FinalAnswer.Content != nil {
			response.Message = result.FinalAnswer.Content
		}
	case runtime.UserTurnResultSilentFinal:
		response.ResultKind = clientui.UserTurnResultKindSilentFinal
		response.Message = textutil.Value("")
	}
	return response
}

func queuedUserTurnResponse(compacted bool, queueItemID string) serverapi.RuntimeSubmitUserTurnResponse {
	return serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, ResultKind: clientui.UserTurnResultKindQueued, Steered: true, QueueItemID: queueItemID}
}

func canonicalUserTurnRequest(req serverapi.RuntimeSubmitUserTurnRequest) sessionUserTurnRequest {
	request := sessionUserTurnRequest{
		SessionID: strings.TrimSpace(req.SessionID),
		Kind:      req.Input.Kind,
	}
	if req.Input.Text != nil {
		request.Text = *req.Input.Text
	}
	if req.Input.PromptCommand != nil {
		request.Name = strings.TrimSpace(req.Input.PromptCommand.Name)
		request.Arguments = req.Input.PromptCommand.Arguments
	}
	return request
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
	request := canonicalUserTurnRequest(req)
	return runRuntimeCommand(ctx, func(ctx context.Context) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
		projection, err := s.resolveUserTurnInput(ctx, req.SessionID, req.Input)
		if err != nil {
			return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
		}
		attempt := runtime.NewCommandAttempt(ctx)
		defer attempt.Finish()
		response, commandErr := s.submitUserTurn(attempt, request, projection, req)
		if commandErr == nil {
			commandErr = response.Validate()
		}
		return response, attempt.Accepted(), commandErr
	})
}

func (s *Service) submitUserTurn(
	attempt *runtime.CommandAttempt,
	request sessionUserTurnRequest,
	projection userTurnProjection,
	req serverapi.RuntimeSubmitUserTurnRequest,
) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	var response serverapi.RuntimeSubmitUserTurnResponse
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, errors.New("session runtime authority is required")
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	runTurn := func(runCtx context.Context, engine *runtime.Engine, accept runtime.CommandAcceptance) error {
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
				engine,
				projection.ExecutionText,
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
				runCtx,
				projection.ExecutionText,
				accept,
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
			accept,
		)
		if err != nil {
			return errors.Join(acceptedCompactionErr, err)
		}
		if queued != nil {
			response = queuedUserTurnResponse(compacted, queued.ID)
			return acceptedCompactionErr
		}
		response = runtimeControlResponseFromUserTurn(outcome)
		response.Compacted = compacted
		return acceptedCompactionErr
	}
	executeTurn := func() error {
		return s.authority.RunCurrentHumanTurn(
			attempt.Context(),
			descriptor,
			attempt.Accept,
			runTurn,
		)
	}
	workflowHistoryRecorded := false
	var historyErr error
	err = executeTurn()
	if errors.Is(err, sessionruntime.ErrSessionWorkflowActivationActive) {
		preparing := false
		var preparingErr error
		if s.preparations != nil {
			preparing, preparingErr = s.preparations.WorkflowSessionPreparing(attempt.Context(), sessionID)
		}
		switch {
		case preparingErr != nil:
			err = preparingErr
		case preparing:
			err = s.withRuntime(attempt.Context(), request.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
				return runTurn(runCtx, engine, attempt.Accept)
			})
		case s.reactivator != nil:
			var reactivateErr error
			continuation, continuationErr := workflowexecution.NewWorkflowSessionContinuation(projection.ExecutionText, nil)
			if continuationErr != nil {
				err = continuationErr
				break
			}
			var continuationResult workflowexecution.WorkflowSessionContinuationResult
			continuationResult, reactivateErr = s.reactivator.ReactivateWorkflowSessionWithAcceptance(
				attempt.Caller(),
				sessionID,
				attempt.Accept,
				attempt.Context(),
				continuation,
			)
			if reactivateErr == nil {
				if attempt.Accepted() {
					workflowHistoryRecorded = true
					historyErr = s.recordAcceptedUserTurnHistory(request, projection)
				}
				_, admissionErr := continuationResult.WaitAdmission(attempt.Caller())
				if admissionErr != nil {
					err = admissionErr
					break
				}
				turn, turnErr := continuation.WaitTurn(attempt.Caller())
				_, exactErr := continuation.WaitExact(attempt.Caller())
				response = runtimeControlResponseFromUserTurn(turn)
				err = exactErr
				if err == nil {
					err = turnErr
				}
			}
			if reactivateErr != nil {
				err = reactivateErr
			}
		}
	}
	if attempt.Accepted() && !workflowHistoryRecorded {
		workflowHistoryRecorded = true
		historyErr = s.recordAcceptedUserTurnHistory(request, projection)
	}
	return response, errors.Join(err, historyErr)
}

func (s *Service) recordAcceptedUserTurnHistory(
	request sessionUserTurnRequest,
	projection userTurnProjection,
) error {
	if _, err := s.recordPromptHistory(context.Background(), request.SessionID, projection.HistoryText); err != nil {
		reportErr := s.withRuntime(context.Background(), request.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			engine.ReportPromptHistoryPersistError(err.Error())
			return nil
		})
		if reportErr != nil {
			return errors.Join(err, reportErr)
		}
	}
	return nil
}

func (s *Service) runPreSubmitCompaction(
	ctx context.Context,
	engine *runtime.Engine,
	text string,
) (bool, error) {
	attempt := runtime.NewCommandAttempt(ctx)
	defer attempt.Finish()
	receipt, err := engine.CompactContextForPreSubmitWithAcceptance(attempt.Context(), text, attempt.Accept)
	return receipt.Committed, err
}
