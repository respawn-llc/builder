package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeops"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func (s *Service) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	memoReq := turnSubmitMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Text: req.Text}
	return runtimeops.Do(s.operations, ctx, memoReq.SessionID, req.OperationRef, memoReq, sameTurnSubmitMemoRequest, func(ctx context.Context, attempt runtimeops.Attempt) (serverapi.RuntimeSubmitUserTurnResponse, error) {
		start, err := s.beginRunStart(req.SessionID)
		if err != nil {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
			return serverapi.RuntimeSubmitUserTurnResponse{}, err
		}
		defer start.Release()
		stopStartCancel := releaseRunStartOnContextDone(start, attempt.Context())
		defer stopStartCancel()
		runCtx, stopRunCtx := mergeOperationContexts(attempt.Context(), start.Context())
		defer stopRunCtx()
		var resp serverapi.RuntimeSubmitUserTurnResponse
		var recordEngine *runtime.Engine
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
		err = s.withRuntimeAccess(runCtx, req.SessionID, func(engine *runtime.Engine) error {
			recordEngine = engine
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
				queued := engine.QueueUserMessageForAutoDrain(memoReq.Text, strings.TrimSpace(req.ClientRequestID))
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
			resp = serverapi.RuntimeSubmitUserTurnResponse{Message: msg.Content, Compacted: compacted}
			return nil
		})
		if err == nil {
			s.recordPromptHistoryAsync(recordEngine, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text)
		} else if inputAccepted {
			s.recordPromptHistoryAsync(recordEngine, memoReq.SessionID, strings.TrimSpace(req.ClientRequestID), memoReq.Text)
		} else {
			s.recordRuntimeAccessFailureOrCancellation(memoReq.SessionID, req.OperationRef, err, attempt)
		}
		return resp, err
	})
}

func (s *Service) runPreSubmitCompaction(ctx context.Context, sessionID string, ref clientui.RuntimeOperationRef, engine *runtime.Engine) error {
	if ref.Validate() != nil {
		return engine.CompactContextForPreSubmit(ctx)
	}
	_, err := runtimeops.Do(s.operations, ctx, sessionID, ref, sessionOnlyMemoRequest{SessionID: strings.TrimSpace(sessionID)}, func(a sessionOnlyMemoRequest, b sessionOnlyMemoRequest) bool {
		return a.SessionID == b.SessionID
	}, func(ctx context.Context, attempt runtimeops.Attempt) (struct{}, error) {
		runCtx, stopRunCtx := mergeOperationContexts(ctx, attempt.Context())
		defer stopRunCtx()
		compactErr := engine.CompactContextForPreSubmitWithActiveHook(runCtx, func() {
			s.operations.MarkOperationActive(sessionID, ref)
		})
		if s.operationAttemptCanceled(compactErr, attempt) {
			s.operations.RecordCanceledNotCommitted(sessionID, ref)
		} else {
			s.operations.RecordCompactCompletion(sessionID, ref, compactErr)
		}
		return struct{}{}, compactErr
	})
	return err
}

func (s *Service) recordPromptHistoryAsync(engine *runtime.Engine, sessionID string, sourceID string, text string) {
	if s == nil || s.promptStore == nil {
		return
	}
	go func() {
		if _, _, err := s.recordPromptHistory(context.Background(), sessionID, sourceID, text); err != nil {
			engine.ReportPromptHistoryPersistError(err.Error())
		}
	}()
}
