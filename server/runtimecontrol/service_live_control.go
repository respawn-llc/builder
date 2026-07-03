package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	servicecontract "core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var _ servicecontract.RuntimeLiveControlService = (*Service)(nil)

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
		err := s.withRuntimeAccess(ctx, memoReq.SessionID.String(), func(engine *runtime.Engine) error {
			item, accepted, err := engine.QueueUserMessageForActiveRun(ctx, memoReq.Text, clientRequestID, func() error {
				if s == nil || s.promptStore == nil {
					return nil
				}
				record, _, err := s.recordPromptHistory(ctx, memoReq.SessionID.String(), clientRequestID.String(), memoReq.Text)
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
		err := s.withRuntimeAccess(ctx, memoReq.SessionID.String(), func(engine *runtime.Engine) error {
			stopped, err := engine.TryInterruptActiveRun()
			if err != nil {
				return err
			}
			if stopped {
				resp.Status = serverapi.RuntimeLiveStopStatusStopped
			}
			return nil
		})
		if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
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
	err = s.withRuntimeAccess(ctx, sessionID.String(), func(engine *runtime.Engine) error {
		result, err := engine.WaitForActiveRunResult(ctx)
		if errors.Is(err, runtime.ErrNoActiveLiveRun) {
			return serverapi.ErrRuntimeNoActiveRun
		}
		if errors.Is(err, runtime.ErrLiveRunNoFinalAnswer) {
			return serverapi.ErrRuntimeNoFinalAnswer
		}
		if err != nil {
			return err
		}
		sessionName := strings.TrimSpace(engine.SessionName())
		if sessionName == "" {
			sessionName = sessionID.String()
		}
		content := result.AssistantMessage.Content
		resp = serverapi.RuntimeLiveWaitResponse{
			SessionID:      sessionID.String(),
			SessionName:    sessionName,
			Result:         &content,
			DurationMillis: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
			LiveRunGroupID: result.GroupID.String(),
			TerminalRunID:  result.RunID.String(),
			TerminalStepID: result.StepID.String(),
			TerminalStatus: string(result.Status),
			ResultKind:     serverapi.RuntimeLiveResultKindAssistantFinalAnswer,
		}
		return nil
	})
	return resp, err
}
