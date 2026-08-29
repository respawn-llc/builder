package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
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
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		response, commandErr := s.submitUserTurn(attempt, request, projection, req)
		if commandErr == nil {
			commandErr = response.Validate()
		}
		return response, attempt.Accepted(), commandErr
	})
}

func (s *Service) submitUserTurn(
	attempt *runtimeCommandAttempt,
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
	}
	executeTurn := func() error {
		return s.authority.RunCurrentHumanTurn(
			attempt.Context(),
			descriptor,
			attempt.Accept,
			runTurn,
		)
	}
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
			var workflowState *runtime.WorkflowSessionState
			stateErr := s.withRuntime(attempt.Context(), request.SessionID, func(_ context.Context, engine *runtime.Engine) error {
				var err error
				workflowState, err = engine.WorkflowSessionState()
				return err
			})
			if stateErr != nil {
				err = stateErr
				break
			}
			if workflowState == nil {
				err = errors.New("retained Workflow Session has no Current Node binding")
				break
			}
			handle, reactivateErr := s.reactivator.ReactivateWorkflowSession(attempt.Context(), sessionID)
			if reactivateErr != nil {
				err = reactivateErr
			} else {
				_, err = s.authority.ValidateLiveWorkflowAgentExecution(
					handle,
					sessionID,
					workflowState.CurrentNode,
				)
				if err == nil {
					err = s.withRuntime(attempt.Context(), request.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
						return runTurn(runCtx, engine, attempt.Accept)
					})
				}
			}
		}
	}
	if attempt.Accepted() {
		s.recordAcceptedUserTurnHistory(request, projection)
	}
	return response, err
}

func (s *Service) recordAcceptedUserTurnHistory(
	request sessionUserTurnRequest,
	projection userTurnProjection,
) {
	if _, err := s.recordPromptHistory(context.Background(), request.SessionID, projection.HistoryText); err != nil {
		_ = s.withRuntime(context.Background(), request.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			engine.ReportPromptHistoryPersistError(err.Error())
			return nil
		})
	}
}

func (s *Service) runPreSubmitCompaction(
	ctx context.Context,
	engine *runtime.Engine,
	text string,
) (bool, error) {
	attempt := newRuntimeCommandAttempt(ctx)
	defer attempt.Finish()
	receipt, err := engine.CompactContextForPreSubmitWithAcceptance(attempt.Context(), text, attempt.Accept)
	return receipt.Committed, err
}
