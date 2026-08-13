package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/runtime"
	servicecontract "core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

var _ servicecontract.RuntimeLiveControlService = (*Service)(nil)

func (s *Service) withLiveExecutionRuntime(ctx context.Context, id runtimeids.SessionID, fn func(context.Context, *runtime.Engine) error) error {
	if s == nil || s.execution == nil {
		return errors.New("session runtime authority is required")
	}
	return s.execution.WithLiveExecutionRuntime(ctx, id, fn)
}

func (s *Service) LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeLiveSteerRequest]) (serverapi.RuntimeLiveSteerResponse, error) {
		return s.LiveSteerValidated(ctx, validated, runtimeLiveSteerIdentity(validated))
	})
}

func (s *Service) LiveSteerValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeLiveSteerRequest], identity servicecontract.RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveSteerResponse, error) {
	req := validated.Value()
	memoReq := liveSteerMemoRequest{
		SessionID:       identity.SessionID,
		CallerSessionID: serverapi.CanonicalOptionalString(req.CallerSessionID),
		Text:            strings.TrimSpace(req.Text),
	}
	return s.liveSteers.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLiveSteerMemoRequest, func(ctx context.Context) (serverapi.RuntimeLiveSteerResponse, error) {
		var resp serverapi.RuntimeLiveSteerResponse
		err := s.withLiveExecutionRuntime(ctx, memoReq.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
			queueText := memoReq.Text
			var agentSteer runtime.AgentSteer
			var err error
			if memoReq.CallerSessionID.Present {
				agentSteer, err = runtime.NewAgentSteer(*identity.CallerSessionID, memoReq.Text)
				if err != nil {
					return err
				}
				queueText = *agentSteer.Message().Content
			}
			queueBefore := func() error {
				if s == nil || s.promptStore == nil {
					return nil
				}
				_, _, err := s.recordPromptHistory(callbackCtx, memoReq.SessionID.String(), identity.ClientRequestID.String(), queueText)
				if err != nil {
					return err
				}
				return nil
			}
			var item runtime.QueuedUserMessage
			var accepted bool
			if memoReq.CallerSessionID.Present {
				item, accepted, err = engine.QueueAgentSteerForActiveRun(callbackCtx, agentSteer, identity.ClientRequestID, queueBefore)
			} else {
				item, accepted, err = engine.QueueUserMessageForActiveRun(callbackCtx, queueText, identity.ClientRequestID, queueBefore)
			}
			if errors.Is(err, runtime.ErrNoActiveLiveRun) {
				return serverapi.ErrRuntimeNoActiveRun
			}
			if err != nil {
				return err
			}
			if !accepted {
				return serverapi.ErrRuntimeNoActiveRun
			}
			displayText, displayErr := item.DisplayText()
			if displayErr != nil {
				return displayErr
			}
			resp = serverapi.RuntimeLiveSteerResponse{QueueItemID: item.ID, Text: displayText, ClientRequestID: item.ClientRequestID}
			return nil
		})
		return resp, err
	})
}

func (s *Service) LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeLiveStopRequest]) (serverapi.RuntimeLiveStopResponse, error) {
		return s.LiveStopValidated(ctx, validated, servicecontract.RuntimeLiveRequestIdentity{SessionID: validated.SessionID(req.SessionID)})
	})
}

func (s *Service) LiveStopValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeLiveStopRequest], identity servicecontract.RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveStopResponse, error) {
	req := validated.Value()
	memoReq := liveStopMemoRequest{SessionID: identity.SessionID}
	return s.liveStops.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLiveStopMemoRequest, func(ctx context.Context) (serverapi.RuntimeLiveStopResponse, error) {
		resp := serverapi.RuntimeLiveStopResponse{Status: serverapi.RuntimeLiveStopStatusIdle}
		err := s.withLiveExecutionRuntime(ctx, memoReq.SessionID, func(_ context.Context, engine *runtime.Engine) error {
			stopped, err := engine.TryInterruptActiveRun()
			if err != nil {
				return err
			}
			if stopped {
				resp.Status = serverapi.RuntimeLiveStopStatusStopped
			}
			return nil
		})
		if errors.Is(err, serverapi.ErrRuntimeUnavailable) || errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
			return resp, nil
		}
		return resp, err
	})
}

func (s *Service) captureLiveRun(ctx context.Context, id runtimeids.SessionID) (*runtime.LiveRunWaitHandle, string, error) {
	var handle *runtime.LiveRunWaitHandle
	var name string
	err := s.withLiveExecutionRuntime(ctx, id, func(callbackCtx context.Context, engine *runtime.Engine) error {
		var err error
		handle, err = engine.CaptureActiveRunResult(callbackCtx)
		if err == nil {
			name = strings.TrimSpace(engine.SessionName())
		}
		return err
	})
	if errors.Is(err, runtime.ErrNoActiveLiveRun) {
		return nil, "", serverapi.ErrRuntimeNoActiveRun
	}
	return handle, name, err
}

func (s *Service) LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeLiveWaitRequest]) (serverapi.RuntimeLiveWaitResponse, error) {
		return s.LiveWaitValidated(ctx, validated, servicecontract.RuntimeLiveRequestIdentity{SessionID: validated.SessionID(req.SessionID)})
	})
}

func (s *Service) LiveWaitValidated(ctx context.Context, _ servicecontract.Validated[serverapi.RuntimeLiveWaitRequest], identity servicecontract.RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveWaitResponse, error) {
	sessionID := identity.SessionID
	var resp serverapi.RuntimeLiveWaitResponse
	waitHandle, sessionName, err := s.captureLiveRun(ctx, sessionID)
	if err != nil {
		return resp, err
	}
	result, err := waitHandle.Wait()
	if errors.Is(err, runtime.ErrLiveRunNoFinalAnswer) {
		return resp, serverapi.ErrRuntimeNoFinalAnswer
	}
	if err != nil {
		return resp, err
	}
	if sessionName == "" {
		sessionName = sessionID.String()
	}
	resp = serverapi.RuntimeLiveWaitResponse{
		SessionID: sessionID.String(), SessionName: sessionName,
		Result:         textutil.Pointer(result.AssistantMessage.Content),
		DurationMillis: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
		LiveRunGroupID: result.GroupID.String(), TerminalRunID: result.RunID.String(),
		TerminalStepID: result.StepID.String(), TerminalStatus: string(result.Status),
		ResultKind: serverapi.RuntimeLiveResultKindAssistantFinalAnswer,
	}
	return resp, err
}

func (s *Service) pendingWatchQuestion(ctx context.Context, sessionID string) (*serverapi.ObservationQuestion, error) {
	if s.askViews == nil || s.approvalViews == nil {
		return nil, nil
	}
	asks, err := s.askViews.ListPendingAsksBySession(ctx, serverapi.AskListPendingBySessionRequest{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	approvals, err := s.approvalViews.ListPendingApprovalsBySession(ctx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	prompt, ok := serverapi.FirstPendingPromptObservation(asks.Asks, approvals.Approvals)
	if !ok {
		return nil, nil
	}
	return &prompt.Question, nil
}

func (s *Service) LiveWatch(ctx context.Context, req serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeLiveWatchRequest]) (serverapi.RuntimeLiveWatchResponse, error) {
		return s.LiveWatchValidated(ctx, validated, servicecontract.RuntimeLiveRequestIdentity{SessionID: validated.SessionID(req.SessionID)})
	})
}

func (s *Service) LiveWatchValidated(ctx context.Context, _ servicecontract.Validated[serverapi.RuntimeLiveWatchRequest], identity servicecontract.RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveWatchResponse, error) {
	id := identity.SessionID
	if s.attention == nil {
		return serverapi.RuntimeLiveWatchResponse{}, errors.New("attention notification service is required")
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub, err := s.attention.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{
		SessionID: id.String(), IncludePendingPromptSnapshot: true,
	})
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	defer func() { _ = sub.Close() }()
	handle, name, captureErr := s.captureLiveRun(watchCtx, id)
	if errors.Is(captureErr, serverapi.ErrRuntimeNoActiveRun) {
		question, err := s.pendingWatchQuestion(ctx, id.String())
		if err != nil {
			return serverapi.RuntimeLiveWatchResponse{}, err
		}
		if question == nil {
			return serverapi.RuntimeLiveWatchResponse{}, serverapi.ErrRuntimeNoActiveRun
		}
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchQuestion, Question: question,
		}}, nil
	}
	if captureErr != nil {
		return serverapi.RuntimeLiveWatchResponse{}, captureErr
	}
	if question, err := s.pendingWatchQuestion(ctx, id.String()); err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	} else if question != nil {
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchQuestion, Question: question,
		}}, nil
	}
	type terminal struct {
		result runtime.LiveRunResult
		err    error
	}
	terminalCh := make(chan terminal, 1)
	questionCh := make(chan *serverapi.ObservationQuestion, 1)
	attentionErrCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); result, err := handle.Wait(); terminalCh <- terminal{result, err} }()
	go func() {
		defer wg.Done()
		for {
			if _, err := sub.Next(watchCtx); err != nil {
				if watchCtx.Err() == nil {
					attentionErrCh <- liveWatchAttentionStreamError(err)
				}
				return
			}
			question, err := s.pendingWatchQuestion(watchCtx, id.String())
			if err != nil {
				if watchCtx.Err() == nil {
					attentionErrCh <- liveWatchAttentionStreamError(err)
				}
				return
			}
			if question != nil {
				questionCh <- question
				return
			}
		}
	}()
	select {
	case question := <-questionCh:
		cancel()
		wg.Wait()
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchQuestion, Question: question,
		}}, nil
	case terminal := <-terminalCh:
		cancel()
		wg.Wait()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return serverapi.RuntimeLiveWatchResponse{}, ctxErr
		}
		return liveWatchResult(id, name, terminal.result, terminal.err)
	case err := <-attentionErrCh:
		cancel()
		wg.Wait()
		return serverapi.RuntimeLiveWatchResponse{}, err
	case <-ctx.Done():
		cancel()
		wg.Wait()
		return serverapi.RuntimeLiveWatchResponse{}, ctx.Err()
	}
}

func runtimeLiveSteerIdentity(validated servicecontract.Validated[serverapi.RuntimeLiveSteerRequest]) servicecontract.RuntimeLiveRequestIdentity {
	req := validated.Value()
	identity := servicecontract.RuntimeLiveRequestIdentity{
		SessionID:       validated.SessionID(req.SessionID),
		ClientRequestID: validated.RuntimeClientRequestID(req.ClientRequestID),
	}
	if req.CallerSessionID != nil {
		callerID := validated.SessionID(*req.CallerSessionID)
		identity.CallerSessionID = &callerID
	}
	return identity
}

type liveWatchAttentionStreamFailure struct {
	cause error
}

func (e *liveWatchAttentionStreamFailure) Error() string {
	return fmt.Sprintf("%s: %v", serverapi.ErrStreamFailed, e.cause)
}

func (e *liveWatchAttentionStreamFailure) Unwrap() error {
	return serverapi.ErrStreamFailed
}

func liveWatchAttentionStreamError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &liveWatchAttentionStreamFailure{cause: err}
	}
	return serverapi.NormalizeStreamError(err)
}

func liveWatchResult(id runtimeids.SessionID, name string, result runtime.LiveRunResult, err error) (serverapi.RuntimeLiveWatchResponse, error) {
	if name == "" {
		name = id.String()
	}
	if errors.Is(err, runtime.ErrLiveRunNoFinalAnswer) {
		if result.NoFinalReason == "" {
			return serverapi.RuntimeLiveWatchResponse{}, errors.New("live run completed without a typed no-final-answer reason")
		}
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: serverapi.RuntimeLiveWatchNoFinalResult,
			Failure: &serverapi.RuntimeLiveWatchFailure{
				Reason: string(result.NoFinalReason),
			},
		}}, nil
	}
	if err == nil {
		err = result.Error
	}
	if err != nil {
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			return serverapi.RuntimeLiveWatchResponse{}, errors.New("live run failed without a diagnostic")
		}
		kind := serverapi.RuntimeLiveWatchExecutionError
		if result.Status == runtime.RunStatusInterrupted {
			kind = serverapi.RuntimeLiveWatchInterrupted
		}
		var diagnostic *string
		if result.Error != nil && result.Error.Error() != reason {
			value := result.Error.Error()
			diagnostic = &value
		}
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: kind, Failure: &serverapi.RuntimeLiveWatchFailure{Reason: reason, Diagnostic: diagnostic},
		}}, nil
	}
	return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
		Kind: serverapi.RuntimeLiveWatchFinalAnswer,
		FinalAnswer: &serverapi.RuntimeLiveWatchFinal{
			Result: textutil.Pointer(result.AssistantMessage.Content), SessionName: name,
			DurationMillis: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
		},
	}}, nil
}
