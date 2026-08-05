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
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

var _ servicecontract.RuntimeLiveControlService = (*Service)(nil)

func (s *Service) withLiveExecutionRuntime(ctx context.Context, id runtimeids.SessionID, fn func(context.Context, *runtime.Engine) error) error {
	return s.withRuntime(ctx, id.String(), fn)
}

func (s *Service) LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	id, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	clientRequestID, err := runtimeids.ParseRuntimeClientRequestID(req.ClientRequestID)
	if err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	memoReq := liveSteerMemoRequest{SessionID: id, Text: strings.TrimSpace(req.Text)}
	return s.liveSteers.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLiveSteerMemoRequest, func(ctx context.Context) (serverapi.RuntimeLiveSteerResponse, error) {
		var response serverapi.RuntimeLiveSteerResponse
		err := s.withLiveExecutionRuntime(ctx, id, func(callbackCtx context.Context, engine *runtime.Engine) error {
			item, accepted, err := engine.QueueUserMessageForActiveRun(callbackCtx, memoReq.Text, clientRequestID, func() error {
				if s.promptStore == nil {
					return nil
				}
				record, _, err := s.recordPromptHistory(callbackCtx, id.String(), clientRequestID.String(), memoReq.Text)
				if err == nil {
					memoReq.Text = record.Text
				}
				return err
			})
			if err != nil {
				return err
			}
			if !accepted {
				return serverapi.ErrRuntimeNoActiveRun
			}
			response = serverapi.RuntimeLiveSteerResponse{QueueItemID: item.ID, Text: item.Text, ClientRequestID: item.ClientRequestID}
			return nil
		})
		if err != nil {
			if errors.Is(err, runtime.ErrNoActiveLiveRun) {
				return response, serverapi.ErrRuntimeNoActiveRun
			}
			return response, err
		}
		return response, nil
	})
}

func (s *Service) LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	id, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	response := serverapi.RuntimeLiveStopResponse{Status: serverapi.RuntimeLiveStopStatusIdle}
	err = s.withRuntime(ctx, id.String(), func(_ context.Context, engine *runtime.Engine) error {
		stopped, err := engine.TryInterruptActiveRun()
		if stopped {
			response.Status = serverapi.RuntimeLiveStopStatusStopped
		}
		return err
	})
	if errors.Is(err, runtime.ErrNoActiveLiveRun) || errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		return response, nil
	}
	return response, err
}

func (s *Service) captureLiveRun(ctx context.Context, id runtimeids.SessionID) (*runtime.LiveRunWaitHandle, string, error) {
	var handle *runtime.LiveRunWaitHandle
	var name string
	err := s.withRuntime(ctx, id.String(), func(callbackCtx context.Context, engine *runtime.Engine) error {
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

func (s *Service) waitLiveRun(ctx context.Context, id runtimeids.SessionID) (runtime.LiveRunResult, string, error) {
	handle, name, err := s.captureLiveRun(ctx, id)
	if err != nil {
		return runtime.LiveRunResult{}, "", err
	}
	result, err := handle.Wait()
	return result, name, err
}

func (s *Service) LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	id, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	result, name, err := s.waitLiveRun(ctx, id)
	terminal := classifyLiveRun(result, err)
	if terminal.noFinal {
		return serverapi.RuntimeLiveWaitResponse{}, serverapi.ErrRuntimeNoFinalAnswer
	}
	if terminal.err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, terminal.err
	}
	if name == "" {
		name = id.String()
	}
	return serverapi.RuntimeLiveWaitResponse{
		SessionID: id.String(), SessionName: name,
		Result:         textutil.Pointer(result.AssistantMessage.Content),
		DurationMillis: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
		LiveRunGroupID: result.GroupID.String(), TerminalRunID: result.RunID.String(),
		TerminalStepID: result.StepID.String(), TerminalStatus: string(result.Status),
		ResultKind: serverapi.RuntimeLiveResultKindAssistantFinalAnswer,
	}, nil
}

type liveWatchPrompt struct {
	at time.Time
	id string
	q  serverapi.ObservationQuestion
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
	prompts := make([]liveWatchPrompt, 0, len(asks.Asks)+len(approvals.Approvals))
	for index := range asks.Asks {
		ask := asks.Asks[index]
		prompts = append(prompts, liveWatchPrompt{at: ask.CreatedAt, id: ask.AskID, q: serverapi.ObservationQuestion{Ask: &ask}})
	}
	for index := range approvals.Approvals {
		approval := approvals.Approvals[index]
		prompts = append(prompts, liveWatchPrompt{at: approval.CreatedAt, id: approval.ApprovalID, q: serverapi.ObservationQuestion{Approval: &approval}})
	}
	slices.SortStableFunc(prompts, func(left, right liveWatchPrompt) int {
		if left.at.Before(right.at) {
			return -1
		}
		if left.at.After(right.at) {
			return 1
		}
		return strings.Compare(left.id, right.id)
	})
	if len(prompts) == 0 {
		return nil, nil
	}
	return &prompts[0].q, nil
}

func (s *Service) LiveWatch(ctx context.Context, req serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	id, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
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
					attentionErrCh <- serverapi.NormalizeStreamError(err)
				}
				return
			}
			question, err := s.pendingWatchQuestion(watchCtx, id.String())
			if err != nil {
				if watchCtx.Err() == nil {
					attentionErrCh <- serverapi.NormalizeStreamError(err)
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

type liveRunTerminal struct {
	result        runtime.LiveRunResult
	err           error
	noFinal       bool
	noFinalReason runtime.LiveRunNoFinalAnswerReason
	status        runtime.RunStatus
	diagnostic    *string
}

func classifyLiveRun(result runtime.LiveRunResult, err error) liveRunTerminal {
	terminal := liveRunTerminal{
		result: result,
		err:    err,
		status: result.Status,
	}
	if errors.Is(err, runtime.ErrLiveRunNoFinalAnswer) {
		terminal.noFinal = true
		terminal.noFinalReason = result.NoFinalReason
		return terminal
	}
	if result.Error != nil {
		value := result.Error.Error()
		terminal.diagnostic = &value
	}
	return terminal
}

func liveWatchResult(id runtimeids.SessionID, name string, result runtime.LiveRunResult, err error) (serverapi.RuntimeLiveWatchResponse, error) {
	terminal := classifyLiveRun(result, err)
	if name == "" {
		name = id.String()
	}
	if terminal.noFinal {
		reason := strings.TrimSpace(string(terminal.noFinalReason))
		if reason == "" {
			reason = string(runtime.LiveRunNoFinalAnswerReasonUnknown)
		}
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind:    serverapi.RuntimeLiveWatchNoFinalResult,
			Failure: &serverapi.RuntimeLiveWatchFailure{Reason: reason},
		}}, nil
	}
	if terminal.err != nil {
		kind := serverapi.RuntimeLiveWatchExecutionError
		reason := "execution error"
		if terminal.status == runtime.RunStatusInterrupted {
			kind, reason = serverapi.RuntimeLiveWatchInterrupted, "interrupted"
		}
		return serverapi.RuntimeLiveWatchResponse{SessionID: id.String(), Outcome: serverapi.RuntimeLiveWatchOutcome{
			Kind: kind, Failure: &serverapi.RuntimeLiveWatchFailure{Reason: reason, Diagnostic: terminal.diagnostic},
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
