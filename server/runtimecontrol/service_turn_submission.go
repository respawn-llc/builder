package runtimecontrol

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflowexecution"
	"core/server/workflowstore"
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
	runTurn := func(runCtx context.Context, engine *runtime.Engine, accept runtime.CommandAcceptance) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		var response serverapi.RuntimeSubmitUserTurnResponse
		shouldCompact, err := engine.ShouldCompactBeforeUserMessage(runCtx, projection.ExecutionText)
		if err != nil {
			return response, err
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
					return response, compactErr
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
				return response, errors.Join(acceptedCompactionErr, queueErr)
			}
			response = queuedUserTurnResponse(compacted, queued.ID)
			return response, acceptedCompactionErr
		}
		outcome, queued, err := engine.SubmitUserMessageOrSteerWithAcceptance(
			runCtx,
			projection.ExecutionText,
			accept,
		)
		if err != nil {
			return response, errors.Join(acceptedCompactionErr, err)
		}
		if queued != nil {
			response = queuedUserTurnResponse(compacted, queued.ID)
			return response, acceptedCompactionErr
		}
		response = runtimeControlResponseFromUserTurn(outcome)
		response.Compacted = compacted
		return response, acceptedCompactionErr
	}
	executeTurn := func() (serverapi.RuntimeSubmitUserTurnResponse, error) {
		var response serverapi.RuntimeSubmitUserTurnResponse
		err := s.authority.RunCurrentHumanTurn(
			attempt.Context(),
			descriptor,
			attempt.Accept,
			func(runCtx context.Context, engine *runtime.Engine, accept runtime.CommandAcceptance) error {
				var err error
				response, err = runTurn(runCtx, engine, accept)
				return err
			},
		)
		return response, err
	}
	runLiveWorkflowTurn := func() (bool, serverapi.RuntimeSubmitUserTurnResponse, error) {
		liveExecution, live := s.authority.SessionExecution(sessionID)
		if !live {
			return false, serverapi.RuntimeSubmitUserTurnResponse{}, nil
		}
		if _, workflowScoped := liveExecution.Scope().Workflow(); !workflowScoped {
			return false, serverapi.RuntimeSubmitUserTurnResponse{}, nil
		}
		workflowExecutionRetiring := false
		var response serverapi.RuntimeSubmitUserTurnResponse
		err := s.withRuntime(attempt.Context(), request.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			if s.workflowTasks != nil {
				workflowOwned, ownershipErr := s.workflowTaskSession(runCtx, request.SessionID, nil)
				if ownershipErr != nil {
					return ownershipErr
				}
				if !workflowOwned {
					workflowExecutionRetiring = true
					return nil
				}
			}
			if engine.WorkflowTerminalState().Completed {
				workflowExecutionRetiring = true
				return nil
			}
			var err error
			response, err = runTurn(runCtx, engine, attempt.Accept)
			return err
		})
		if err == nil && workflowExecutionRetiring {
			if waitErr := s.waitForWorkflowExecutionRetirement(attempt.Context(), sessionID); waitErr != nil {
				return true, response, waitErr
			}
			response, err = executeTurn()
		}
		return true, response, err
	}
	executeOrdinaryTurn := func() (serverapi.RuntimeSubmitUserTurnResponse, error) {
		if waitErr := s.waitForWorkflowExecutionRetirement(attempt.Context(), sessionID); waitErr != nil {
			return serverapi.RuntimeSubmitUserTurnResponse{}, waitErr
		}
		return executeTurn()
	}
	workflowHistoryRecorded := false
	workflowHistoryManaged := false
	var historyErr error
	persistedWorkflow, workflowErr := s.workflowTaskSession(attempt.Context(), request.SessionID, nil)
	if workflowErr != nil {
		return response, workflowErr
	}
	liveExecution, live := s.authority.SessionExecution(sessionID)
	liveWorkflow := false
	if live {
		_, liveWorkflow = liveExecution.Scope().Workflow()
	}
	if persistedWorkflow && s.reactivator == nil && !liveWorkflow {
		return response, errors.New("workflow Session reactivator is required")
	}
	if live && liveWorkflow {
		liveWorkflowHandled, liveResponse, liveWorkflowErr := runLiveWorkflowTurn()
		if liveWorkflowHandled {
			response = liveResponse
			err = liveWorkflowErr
		} else if persistedWorkflow {
			err = sessionruntime.ErrSessionWorkflowActivationActive
		} else {
			response, err = executeOrdinaryTurn()
		}
	} else if persistedWorkflow {
		err = sessionruntime.ErrSessionWorkflowActivationActive
	} else {
		response, err = executeOrdinaryTurn()
	}
	if errors.Is(err, sessionruntime.ErrSessionWorkflowActivationActive) {
		preparing := false
		var preparingErr error
		if s.preparations != nil {
			preparing, preparingErr = s.preparations.WorkflowSessionPreparing(attempt.Context(), sessionID)
		}
		switch {
		case preparingErr != nil:
			if errors.Is(preparingErr, workflowstore.ErrSessionNotCurrentWorkflowNode) {
				currentWorkflow, ownershipErr := s.workflowTaskSession(attempt.Context(), request.SessionID, nil)
				switch {
				case ownershipErr != nil:
					err = ownershipErr
				case !currentWorkflow:
					response, err = executeOrdinaryTurn()
				default:
					err = preparingErr
				}
			} else {
				err = preparingErr
			}
		case preparing:
			var release func()
			err = s.withRuntime(attempt.Context(), request.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
				release = engine.HoldQueuedUserAutoDrain()
				queued, queueErr := engine.QueueUserMessageForAutoDrainWithAcceptance(
					runCtx,
					projection.ExecutionText,
					attempt.Accept,
				)
				if queueErr != nil {
					release()
					release = nil
					return queueErr
				}
				response = queuedUserTurnResponse(false, queued.ID)
				return nil
			})
			if err == nil && release != nil {
				go s.releaseQueuedUserAutoDrainWhenWorkflowReady(sessionID, release)
			}
		case s.reactivator != nil:
			liveWorkflowHandled, liveResponse, liveWorkflowErr := runLiveWorkflowTurn()
			if liveWorkflowHandled {
				response = liveResponse
				err = liveWorkflowErr
				break
			}
			continuation, continuationErr := workflowexecution.NewWorkflowSessionContinuationFromInput(
				workflowexecution.WorkflowSessionTextInput{Text: projection.ExecutionText},
			)
			if continuationErr != nil {
				err = continuationErr
				break
			}
			workflowHistoryManaged = true
			type retainedWorkflowResult struct {
				response serverapi.RuntimeSubmitUserTurnResponse
				err      error
			}
			resultCh := make(chan retainedWorkflowResult, 1)
			acceptedCtx := context.WithoutCancel(attempt.Context())
			go func() {
				continuationResult, reactivateErr := s.reactivator.ReactivateWorkflowSessionWithAcceptance(
					attempt.Context(),
					sessionID,
					attempt.Accept,
					acceptedCtx,
					continuation,
				)
				var retainedErr error
				historyRecorded := false
				if continuationResult.Accepted() {
					retainedErr = s.recordAcceptedUserTurnHistory(request, projection)
					historyRecorded = true
				}
				if reactivateErr != nil {
					var conflict *workflowexecution.TaskResumeConflictError
					if errors.As(reactivateErr, &conflict) &&
						conflict.State == workflowexecution.TaskResumeConflictNoResumableCurrentNode {
						liveHandled, liveResponse, liveErr := runLiveWorkflowTurn()
						if liveHandled {
							if attempt.Accepted() && !historyRecorded {
								retainedErr = errors.Join(retainedErr, s.recordAcceptedUserTurnHistory(request, projection))
							}
							resultCh <- retainedWorkflowResult{
								response: liveResponse,
								err:      errors.Join(liveErr, retainedErr),
							}
							return
						}
					}
					resultCh <- retainedWorkflowResult{err: errors.Join(reactivateErr, retainedErr)}
					return
				}
				if _, admissionErr := continuationResult.WaitAdmission(acceptedCtx); admissionErr != nil {
					resultCh <- retainedWorkflowResult{err: errors.Join(admissionErr, retainedErr)}
					return
				}
				turn, turnErr := continuation.WaitTurn(acceptedCtx)
				_, exactErr := continuation.WaitExact(acceptedCtx)
				resultCh <- retainedWorkflowResult{
					response: runtimeControlResponseFromUserTurn(turn),
					err:      errors.Join(exactErr, turnErr, retainedErr),
				}
			}()
			select {
			case result := <-resultCh:
				response = result.response
				err = result.err
			case <-attempt.Caller().Done():
				response = runtimeControlResponseFromUserTurn(runtime.UserTurnResult{})
				err = context.Cause(attempt.Caller())
			}
		}
	}
	if attempt.Accepted() && !workflowHistoryRecorded && !workflowHistoryManaged {
		workflowHistoryRecorded = true
		historyErr = s.recordAcceptedUserTurnHistory(request, projection)
	}
	return response, errors.Join(err, historyErr)
}

func (s *Service) waitForWorkflowExecutionRetirement(ctx context.Context, sessionID runtimeids.SessionID) error {
	return sessionruntime.WaitForWorkflowExecutionRetirement(ctx, s.authority, sessionID)
}

func (s *Service) releaseQueuedUserAutoDrainWhenWorkflowReady(
	sessionID runtimeids.SessionID,
	release func(),
) {
	if release == nil {
		return
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if handle, live := s.authority.SessionExecution(sessionID); live {
			if _, workflowScoped := handle.Scope().Workflow(); workflowScoped {
				workflowTurnStarted := false
				runtimeErr := s.withRuntime(context.Background(), sessionID.String(), func(_ context.Context, engine *runtime.Engine) error {
					active := engine.ActiveRun()
					workflowTurnStarted = active != nil && active.ActiveKind == runtime.ActiveKindWorkflowTurn
					return nil
				})
				if runtimeErr == nil && workflowTurnStarted {
					release()
					return
				}
			}
		}
		if s.preparations != nil {
			preparing, err := s.preparations.WorkflowSessionPreparing(context.Background(), sessionID)
			_, live := s.authority.SessionExecution(sessionID)
			if err != nil || (!preparing && !live) {
				release()
				return
			}
		}
		<-ticker.C
	}
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
