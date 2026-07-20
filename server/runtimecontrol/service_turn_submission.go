package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeops"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (s *Service) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	memoReq := sessionTextMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text}
	return runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, sameSessionTextMemoRequest, func(ctx context.Context, attempt runtimeops.Attempt) (serverapi.RuntimeSubmitUserTurnResponse, error) {
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
		err := s.runAgentExecution(attempt.Context(), req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
			defer func() {
				if !inputAccepted {
					return
				}
				if _, _, err := s.recordPromptHistory(context.Background(), memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text); err != nil {
					engine.ReportPromptHistoryPersistError(err.Error())
				}
			}()
			shouldCompact, err := engine.ShouldCompactBeforeUserMessage(runCtx, memoReq.Text)
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
				queued, err := engine.QueueUserMessageForAutoDrain(
					memoReq.Text,
					strings.TrimSpace(req.ClientRequestID),
				)
				if err != nil {
					return err
				}
				recordAccepted(true)
				resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
				return nil
			}
			msg, queued, err := engine.SubmitUserMessageOrSteerWithHooks(runCtx, memoReq.Text, strings.TrimSpace(req.ClientRequestID), func() {
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
			if msg.Content != nil {
				resp.Message = *msg.Content
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, serverapi.ErrSessionRunStarting) {
				resp, steered, steerErr := s.trySubmitUserTurnAsActiveLiveSteer(ctx, attempt, memoReq, req)
				if steerErr != nil {
					if errors.Is(steerErr, runtime.ErrNoActiveLiveRun) || errors.Is(steerErr, serverapi.ErrRuntimeNoActiveRun) {
						s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
						return serverapi.RuntimeSubmitUserTurnResponse{}, err
					}
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

func (s *Service) trySubmitUserTurnAsActiveLiveSteer(ctx context.Context, attempt runtimeops.Attempt, memoReq sessionTextMemoRequest, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
	var resp serverapi.RuntimeSubmitUserTurnResponse
	steered := false
	runCtx, stopRunCtx := mergeOperationContexts(ctx, attempt.Context())
	defer stopRunCtx()
	liveClientRequestID := runtimeids.NewRuntimeClientRequestID()
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, errors.New("session runtime authority is required")
	}
	err = s.withLiveExecutionRuntime(runCtx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		committed, err := s.operations.TryCommitOperationMutation(memoReq.SessionID, req.OperationRef, func() error {
			item, accepted, err := engine.QueueUserMessageForActiveRun(runCtx, memoReq.Text, liveClientRequestID, nil)
			if err != nil {
				return err
			}
			if !accepted {
				return runtime.ErrNoActiveLiveRun
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
		if _, _, err := s.recordPromptHistory(context.Background(), memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text); err != nil {
			engine.ReportPromptHistoryPersistError(err.Error())
		}
		return nil
	})
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, steered, err
	}
	return resp, steered, nil
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
