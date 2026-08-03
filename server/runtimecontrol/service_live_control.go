package runtimecontrol

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"core/server/runtime"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

var _ servicecontract.RuntimeLiveControlService = (*Service)(nil)

func (s *Service) withLiveExecutionRuntime(ctx context.Context, sessionID runtimeids.SessionID, callback func(context.Context, *runtime.Engine) error) error {
	if s == nil || s.execution == nil {
		return errors.New("session runtime authority is required")
	}
	return s.execution.WithLiveExecutionRuntime(ctx, sessionID, callback)
}

func (s *Service) LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	clientRequestID, err := runtimeids.ParseRuntimeClientRequestID(req.ClientRequestID)
	if err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	memoReq := liveSteerMemoRequest{SessionID: sessionID, Text: strings.TrimSpace(req.Text)}
	return s.liveSteers.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLiveSteerMemoRequest, func(ctx context.Context) (serverapi.RuntimeLiveSteerResponse, error) {
		var resp serverapi.RuntimeLiveSteerResponse
		err := s.withLiveExecutionRuntime(ctx, memoReq.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
			item, accepted, err := engine.QueueUserMessageForActiveRun(callbackCtx, memoReq.Text, clientRequestID, func() error {
				if s == nil || s.promptStore == nil {
					return nil
				}
				record, _, err := s.recordPromptHistory(callbackCtx, memoReq.SessionID.String(), clientRequestID.String(), memoReq.Text)
				if err != nil {
					return err
				}
				memoReq.Text = record.Text
				return nil
			})
			if errors.Is(err, runtime.ErrNoActiveLiveRun) {
				return serverapi.ErrRuntimeNoActiveRun
			}
			if err != nil {
				return err
			}
			if !accepted {
				return serverapi.ErrRuntimeNoActiveRun
			}
			resp = serverapi.RuntimeLiveSteerResponse{QueueItemID: item.ID, Text: item.Text, ClientRequestID: item.ClientRequestID}
			return nil
		})
		return resp, err
	})
}

func (s *Service) LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	memoReq := liveStopMemoRequest{SessionID: sessionID}
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

func (s *Service) LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	var resp serverapi.RuntimeLiveWaitResponse
	var waitHandle *runtime.LiveRunWaitHandle
	var sessionName string
	err = s.withLiveExecutionRuntime(ctx, sessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		handle, err := engine.CaptureActiveRunResult(callbackCtx)
		if errors.Is(err, runtime.ErrNoActiveLiveRun) {
			return serverapi.ErrRuntimeNoActiveRun
		}
		if err != nil {
			return err
		}
		waitHandle = handle
		sessionName = strings.TrimSpace(engine.SessionName())
		return nil
	})
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
		SessionID:      sessionID.String(),
		SessionName:    sessionName,
		Result:         textutil.Pointer(result.AssistantMessage.Content),
		DurationMillis: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
		LiveRunGroupID: result.GroupID.String(),
		TerminalRunID:  result.RunID.String(),
		TerminalStepID: result.StepID.String(),
		TerminalStatus: string(result.Status),
		ResultKind:     serverapi.RuntimeLiveResultKindAssistantFinalAnswer,
	}
	return resp, err
}

func (s *Service) LiveWatch(ctx context.Context, req serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var handle *runtime.LiveRunWaitHandle
	var sessionName string
	err = s.withLiveExecutionRuntime(watchCtx, sessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		captured, captureErr := engine.CaptureActiveRunResult(callbackCtx)
		if captureErr != nil {
			return captureErr
		}
		handle = captured
		sessionName = strings.TrimSpace(engine.SessionName())
		return nil
	})
	if errors.Is(err, runtime.ErrNoActiveLiveRun) || errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		question, questionErr := s.pendingObservationQuestion(ctx, sessionID.String())
		if questionErr != nil {
			return serverapi.RuntimeLiveWatchResponse{}, questionErr
		}
		if question == nil {
			return serverapi.RuntimeLiveWatchResponse{}, serverapi.ErrRuntimeNoActiveRun
		}
		return observationQuestionResponse(sessionID.String(), question), nil
	}
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}

	if sessionName == "" {
		sessionName = sessionID.String()
	}
	subscription, err := s.subscribeLiveWatchAttention(ctx, sessionID.String())
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	defer func() { _ = subscription.Close() }()

	terminalResults := make(chan liveWatchTerminalResult, 1)
	attentionResults := make(chan liveWatchAttentionResult, 1)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		result, waitErr := handle.Wait()
		terminalResults <- liveWatchTerminalResult{result: result, err: waitErr}
	}()
	go func() {
		defer waitGroup.Done()
		for {
			event, nextErr := subscription.Next(watchCtx)
			select {
			case attentionResults <- liveWatchAttentionResult{event: event, err: nextErr}:
			case <-watchCtx.Done():
				return
			}
			if nextErr != nil {
				return
			}
		}
	}()

	if question, questionErr := s.pendingObservationQuestion(ctx, sessionID.String()); questionErr != nil {
		cancel()
		waitGroup.Wait()
		return serverapi.RuntimeLiveWatchResponse{}, questionErr
	} else if question != nil {
		cancel()
		waitGroup.Wait()
		return observationQuestionResponse(sessionID.String(), question), nil
	}

	for {
		select {
		case terminal := <-terminalResults:
			if ctx.Err() != nil {
				cancel()
				waitGroup.Wait()
				return serverapi.RuntimeLiveWatchResponse{}, ctx.Err()
			}
			cancel()
			waitGroup.Wait()
			return observationLiveRunResponse(sessionID.String(), sessionName, terminal.result, terminal.err)
		case attention := <-attentionResults:
			if attention.err != nil {
				if errors.Is(attention.err, context.Canceled) && watchCtx.Err() != nil {
					waitGroup.Wait()
					return serverapi.RuntimeLiveWatchResponse{}, watchCtx.Err()
				}
				cancel()
				waitGroup.Wait()
				return serverapi.RuntimeLiveWatchResponse{}, attention.err
			}
			question, questionErr := s.pendingObservationQuestion(ctx, sessionID.String())
			if questionErr != nil {
				cancel()
				waitGroup.Wait()
				return serverapi.RuntimeLiveWatchResponse{}, questionErr
			}
			if question == nil {
				continue
			}
			cancel()
			waitGroup.Wait()
			return observationQuestionResponse(sessionID.String(), question), nil
		}
	}
}

type liveWatchTerminalResult struct {
	result runtime.LiveRunResult
	err    error
}

type liveWatchAttentionResult struct {
	event clientui.AttentionNotificationEvent
	err   error
}

func (s *Service) subscribeLiveWatchAttention(ctx context.Context, sessionID string) (serverapi.AttentionNotificationSubscription, error) {
	if s == nil || s.attention == nil {
		return nil, errors.New("attention notification service is required")
	}
	return s.attention.SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{
		SessionID:                    sessionID,
		IncludePendingPromptSnapshot: true,
	})
}

func (s *Service) pendingObservationQuestion(ctx context.Context, sessionID string) (*serverapi.RuntimeObservationQuestion, error) {
	if s == nil || s.askViews == nil || s.approvalViews == nil {
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

	type pendingPrompt struct {
		createdAt time.Time
		id        string
		question  serverapi.RuntimeObservationQuestion
	}
	prompts := make([]pendingPrompt, 0, len(asks.Asks)+len(approvals.Approvals))
	for _, ask := range asks.Asks {
		prompts = append(prompts, pendingPrompt{
			createdAt: ask.CreatedAt,
			id:        ask.AskID,
			question: serverapi.RuntimeObservationQuestion{
				QuestionID:             ask.AskID,
				Text:                   ask.Question,
				Kind:                   serverapi.RuntimeObservationQuestionOrdinary,
				Suggestions:            append([]string(nil), ask.Suggestions...),
				RecommendedOptionIndex: ask.RecommendedOptionIndex,
			},
		})
	}
	for _, approval := range approvals.Approvals {
		prompts = append(prompts, pendingPrompt{
			createdAt: approval.CreatedAt,
			id:        approval.ApprovalID,
			question: serverapi.RuntimeObservationQuestion{
				QuestionID:    approval.ApprovalID,
				Text:          approval.Question,
				Kind:          serverapi.RuntimeObservationQuestionAccessRequest,
				AccessOptions: append([]clientui.ApprovalOption(nil), approval.Options...),
			},
		})
	}
	slices.SortStableFunc(prompts, func(left, right pendingPrompt) int {
		if left.createdAt.Before(right.createdAt) {
			return -1
		}
		if left.createdAt.After(right.createdAt) {
			return 1
		}
		return strings.Compare(left.id, right.id)
	})
	if len(prompts) == 0 {
		return nil, nil
	}
	return &prompts[0].question, nil
}

func observationQuestionResponse(sessionID string, question *serverapi.RuntimeObservationQuestion) serverapi.RuntimeLiveWatchResponse {
	return serverapi.RuntimeLiveWatchResponse{
		Target: serverapi.NewRuntimeObservationSessionTarget(sessionID),
		Outcomes: []serverapi.RuntimeObservationOutcome{{
			Kind:      serverapi.RuntimeObservationOutcomeQuestion,
			SessionID: textutil.OptionalExactString(sessionID),
			Question:  question,
		}},
	}
}

func observationLiveRunResponse(sessionID string, sessionName string, result runtime.LiveRunResult, waitErr error) (serverapi.RuntimeLiveWatchResponse, error) {
	if errors.Is(waitErr, runtime.ErrLiveRunNoFinalAnswer) {
		reason := strings.TrimSpace(string(result.NoFinalReason))
		if reason == "" {
			reason = string(runtime.LiveRunNoFinalAnswerReasonUnknown)
		}
		return serverapi.RuntimeLiveWatchResponse{
			Target: serverapi.NewRuntimeObservationSessionTarget(sessionID),
			Outcomes: []serverapi.RuntimeObservationOutcome{{
				Kind:      serverapi.RuntimeObservationOutcomeExecutionError,
				SessionID: textutil.OptionalExactString(sessionID),
				ExecutionError: &serverapi.RuntimeObservationExecutionError{
					Reason:     reason,
					Diagnostic: textutil.OptionalExactString(waitErr.Error()),
				},
			}},
		}, nil
	}
	target := serverapi.NewRuntimeObservationSessionTarget(sessionID)
	if waitErr != nil {
		payload := serverapi.RuntimeObservationExecutionError{
			Reason: waitErr.Error(),
		}
		kind := serverapi.RuntimeObservationOutcomeExecutionError
		if result.Status == runtime.RunStatusInterrupted || errors.Is(waitErr, context.Canceled) {
			kind = serverapi.RuntimeObservationOutcomeInterrupted
			return serverapi.RuntimeLiveWatchResponse{
				Target: target,
				Outcomes: []serverapi.RuntimeObservationOutcome{{
					Kind:        kind,
					SessionID:   textutil.OptionalExactString(sessionID),
					Interrupted: &serverapi.RuntimeObservationInterrupted{Reason: payload.Reason},
				}},
			}, nil
		}
		return serverapi.RuntimeLiveWatchResponse{
			Target: target,
			Outcomes: []serverapi.RuntimeObservationOutcome{{
				Kind:           kind,
				SessionID:      textutil.OptionalExactString(sessionID),
				ExecutionError: &payload,
			}},
		}, nil
	}
	if sessionName == "" {
		sessionName = sessionID
	}
	return serverapi.RuntimeLiveWatchResponse{
		Target: target,
		Outcomes: []serverapi.RuntimeObservationOutcome{{
			Kind:      serverapi.RuntimeObservationOutcomeFinalAnswer,
			SessionID: textutil.OptionalExactString(sessionID),
			FinalAnswer: &serverapi.RuntimeObservationFinalAnswer{
				Result:         textutil.Pointer(result.AssistantMessage.Content),
				SessionName:    sessionName,
				DurationMillis: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
			},
		}},
	}, nil
}
