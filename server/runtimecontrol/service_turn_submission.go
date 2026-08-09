package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
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
		textMemo := sessionTextMemoRequest{SessionID: memoReq.SessionID, Text: projection.ExecutionText}
		var resp serverapi.RuntimeSubmitUserTurnResponse
		inputAccepted := false
		recordAccepted := func(queued bool) {
			if inputAccepted {
				return
			}
			inputAccepted = true
			if queued {
				s.operations.RecordQueuedMessageSubmitted(memoReq.SessionID, req.OperationRef)
			} else {
				s.operations.RecordUserMessageFlushed(memoReq.SessionID, req.OperationRef)
			}
		}
		err = s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			defer func() {
				if !inputAccepted {
					return
				}
				if _, _, err := s.recordPromptHistory(context.Background(), memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), projection.HistoryText); err != nil {
					engine.ReportPromptHistoryPersistError(err.Error())
				}
			}()
			shouldCompact, err := engine.ShouldCompactBeforeUserMessage(runCtx, projection.ExecutionText)
			if err != nil {
				return err
			}
			compacted := false
			compactionBusy := false
			if shouldCompact {
				compactErr := s.runPreSubmitCompaction(runCtx, memoReq.SessionID, req.PreSubmitCompactionOperationRef, engine)
				if compactErr != nil {
					if !errors.Is(compactErr, runtime.ErrAgentBusy) {
						return compactErr
					}
					compactionBusy = true
				} else {
					compacted = true
				}
			}
			if compactionBusy {
				queued, queueErr := engine.QueueUserMessageForAutoDrain(
					projection.ExecutionText,
					strings.TrimSpace(req.ClientRequestID),
				)
				if queueErr != nil {
					return queueErr
				}
				recordAccepted(true)
				resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
				return nil
			}
			outcome, queued, err := engine.SubmitUserMessageOrSteerWithOutcomeHooks(runCtx, projection.ExecutionText, strings.TrimSpace(req.ClientRequestID), func() {
				s.operations.MarkOperationActive(memoReq.SessionID, req.OperationRef)
			}, recordAccepted)
			if err != nil {
				return err
			}
			if queued != nil {
				resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
				return nil
			}
			resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted}
			switch outcome.Kind {
			case runtime.UserTurnResultAssistantFinal:
				if outcome.FinalAnswer != nil && outcome.FinalAnswer.Content != nil {
					resp.Message = outcome.FinalAnswer.Content
				}
			case runtime.UserTurnResultSilentFinal:
				resp.Message = textutil.Value("")
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, serverapi.ErrSessionRunStarting) {
				resp, steered, steerErr := s.trySubmitUserTurnAsActiveExecution(ctx, attempt, textMemo, projection, req)
				if steerErr != nil {
					s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, steerErr, attempt)
					return serverapi.RuntimeSubmitUserTurnResponse{}, steerErr
				}
				if steered {
					s.operations.RecordQueuedMessageSubmitted(memoReq.SessionID, req.OperationRef)
					return resp, nil
				}
			}
		}
		if err != nil && !inputAccepted {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return serverapi.RuntimeSubmitUserTurnResponse{}, err
		}
		return resp, err
	})
}

func (s *Service) trySubmitUserTurnAsActiveExecution(ctx context.Context, attempt runtimeops.Attempt, memoReq sessionTextMemoRequest, projection userTurnProjection, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
	var resp serverapi.RuntimeSubmitUserTurnResponse
	steered := false
	runCtx, stopRunCtx := mergeOperationContexts(ctx, attempt.Context())
	defer stopRunCtx()
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, errors.New("session runtime authority is required")
	}
	err = s.withLiveExecutionRuntime(runCtx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		committed, err := s.operations.TryCommitOperationMutation(memoReq.SessionID, req.OperationRef, func() error {
			item, accepted, err := engine.QueueUserMessageForActiveRun(runCtx, projection.ExecutionText, req.OperationRef.ClientRequestID, nil)
			if errors.Is(err, runtime.ErrNoActiveLiveRun) {
				if !activeExecutionAllowsUserTurnAutoDrain(runtimeactivity.ActiveStepFromProvider(engine)) {
					return serverapi.ErrSessionRunStarting
				}
				item, err = engine.QueueUserMessageForAutoDrain(projection.ExecutionText, req.OperationRef.ClientRequestID.String())
				if err != nil {
					return err
				}
				accepted = true
			} else if err != nil {
				return err
			}
			if !accepted {
				return serverapi.ErrSessionRunStarting
			}
			resp = serverapi.RuntimeSubmitUserTurnResponse{Steered: true, QueueItemID: item.ID}
			steered = true
			return nil
		})
		if err != nil {
			return err
		}
		if !committed {
			return runtimeops.ErrOperationCanceled
		}
		if _, _, err := s.recordPromptHistory(context.Background(), memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), projection.HistoryText); err != nil {
			engine.ReportPromptHistoryPersistError(err.Error())
		}
		return nil
	})
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, steered, err
	}
	return resp, steered, nil
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

func (s *Service) runPreSubmitCompaction(ctx context.Context, sessionID string, ref clientui.RuntimeOperationRef, engine *runtime.Engine) error {
	_, err := runtimeops.Do(s.operations, ctx, sessionID, ref, sessionOnlyMemoRequest{SessionID: strings.TrimSpace(sessionID)}, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool {
		return a.SessionID == b.SessionID
	}, func(ctx context.Context, attempt runtimeops.Attempt) (struct{}, error) {
		runCtx, stopRunCtx := mergeOperationContexts(ctx, attempt.Context())
		defer stopRunCtx()
		receipt, compactErr := engine.CompactContextForPreSubmitWithActiveHook(runCtx, func() {
			s.operations.MarkOperationActive(sessionID, ref)
		})
		s.recordOperationCompletion(sessionID, ref, receipt, compactErr, attempt, s.operations.RecordCompactCompletion)
		return struct{}{}, compactErr
	})
	return err
}
