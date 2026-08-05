package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	servicecontract "core/shared/apicontract"
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
	memoReq := liveSteerMemoRequest{
		SessionID:       sessionID,
		CallerSessionID: serverapi.CanonicalOptionalString(req.CallerSessionID),
		Text:            strings.TrimSpace(req.Text),
	}
	return s.liveSteers.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLiveSteerMemoRequest, func(ctx context.Context) (serverapi.RuntimeLiveSteerResponse, error) {
		var resp serverapi.RuntimeLiveSteerResponse
		err := s.withLiveExecutionRuntime(ctx, memoReq.SessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
			queueText := memoReq.Text
			var agentSteer runtime.AgentSteer
			if memoReq.CallerSessionID.Present {
				callerID, parseErr := runtimeids.ParseSessionID(memoReq.CallerSessionID.Value)
				if parseErr != nil {
					return parseErr
				}
				agentSteer, err = runtime.NewAgentSteer(callerID, memoReq.Text)
				if err != nil {
					return err
				}
				queueText = *agentSteer.Message().Content
			}
			queueBefore := func() error {
				if s == nil || s.promptStore == nil {
					return nil
				}
				_, _, err := s.recordPromptHistory(callbackCtx, memoReq.SessionID.String(), clientRequestID.String(), queueText)
				if err != nil {
					return err
				}
				return nil
			}
			var item runtime.QueuedUserMessage
			var accepted bool
			if memoReq.CallerSessionID.Present {
				item, accepted, err = engine.QueueAgentSteerForActiveRun(callbackCtx, agentSteer, clientRequestID, queueBefore)
			} else {
				item, accepted, err = engine.QueueUserMessageForActiveRun(callbackCtx, queueText, clientRequestID, queueBefore)
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
