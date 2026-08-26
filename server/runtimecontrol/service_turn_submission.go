package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
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
				request.SessionID,
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
	if errors.Is(err, sessionruntime.ErrSessionWorkflowActivationActive) && s.reactivator != nil {
		reactivated, reactivateErr := s.reactivator.ReactivateWorkflowSession(attempt.Context(), sessionID)
		if reactivateErr != nil {
			err = reactivateErr
		} else if validateErr := validateReactivatedWorkflowExecution(s.authority, sessionID, reactivated); validateErr != nil {
			err = validateErr
		} else {
			err = executeTurn()
		}
	}
	if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
		err = errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
	}
	if attempt.Accepted() {
		s.recordAcceptedUserTurnHistory(request, projection)
	}
	return response, err
}

func validateReactivatedWorkflowExecution(
	authority *sessionruntime.Authority,
	sessionID runtimeids.SessionID,
	reactivated sessionruntime.ExecutionHandle,
) error {
	if authority == nil {
		return errors.New("session runtime authority is required")
	}
	if reactivated == nil {
		return errors.New("reactivated Workflow execution is absent")
	}
	scope := reactivated.Scope()
	resource, hasResource := scope.Resource()
	workflowRef, workflowScoped := scope.Workflow()
	if scope.Kind() != sessionruntime.ExecutionScopeAgent ||
		!hasResource ||
		resource.SessionID() != sessionID ||
		!workflowScoped {
		return fmt.Errorf(
			"reactivated Workflow Session %s returned mismatched execution scope %s",
			sessionID,
			scope.ID(),
		)
	}
	live, exists := authority.SessionExecution(sessionID)
	if !exists {
		return fmt.Errorf(
			"reactivated Workflow Session %s returned execution scope %s before publication",
			sessionID,
			scope.ID(),
		)
	}
	liveScope := live.Scope()
	liveWorkflowRef, liveWorkflowScoped := liveScope.Workflow()
	if liveScope.ID() != scope.ID() ||
		!liveWorkflowScoped ||
		!liveWorkflowRef.CurrentNode.Equal(workflowRef.CurrentNode) {
		return fmt.Errorf(
			"reactivated Workflow Session %s returned execution scope %s that does not match live scope %s",
			sessionID,
			scope.ID(),
			liveScope.ID(),
		)
	}
	return nil
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
	sessionID string,
	engine *runtime.Engine,
) (bool, error) {
	return runRuntimeCommand(ctx, func(ctx context.Context) (bool, bool, error) {
		attempt := newRuntimeCommandAttempt(ctx)
		defer attempt.Finish()
		_, commandErr := engine.CompactContextForPreSubmitWithAcceptance(attempt.Context(), attempt.Accept)
		accepted := attempt.Accepted()
		return accepted, accepted, commandErr
	})
}
