package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/sessionruntime"
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
	memoReq := liveSteerMemoRequest{SessionID: sessionID, Text: strings.TrimSpace(req.Text)}
	return s.liveSteers.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameLiveSteerMemoRequest, func(ctx context.Context) (serverapi.RuntimeLiveSteerResponse, error) {
		var resp serverapi.RuntimeLiveSteerResponse
		queue := func(callbackCtx context.Context, engine *runtime.Engine, accept func(func() error) error) error {
			return accept(func() error {
				if !engine.HasActiveLiveRunGroup() {
					return serverapi.ErrRuntimeNoActiveRun
				}
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
		}
		var err error
		if s.commands == nil {
			err = s.withOrderedRuntime(ctx, memoReq.SessionID.String(), func(callbackCtx context.Context, engine *runtime.Engine) error {
				return queue(callbackCtx, engine, func(apply func() error) error {
					return s.acceptRuntimeInputAtOrderedTurn(callbackCtx, memoReq.SessionID.String(), apply)
				})
			})
		} else {
			handle, active := s.authority.SessionExecution(memoReq.SessionID)
			if !active || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent {
				return resp, serverapi.ErrRuntimeNoActiveRun
			}
			scope := handle.Scope()
			resource, resourceAvailable := scope.Resource()
			if !resourceAvailable {
				return resp, serverapi.ErrRuntimeNoActiveRun
			}
			err = s.commands.DispatchAgent(ctx, scope, func(turn runtime.OrderedMutationTurn) error {
				return s.authority.WithExactExecutionRuntime(context.Background(), scope.ID(), func(callbackCtx context.Context, engine *runtime.Engine) error {
					return turn.Apply(func() error {
						return queue(callbackCtx, engine, func(apply func() error) error {
							acceptance, acceptErr := s.commands.BeginInput(callbackCtx, resource)
							if acceptErr != nil {
								return acceptErr
							}
							if applyErr := apply(); applyErr != nil {
								return errors.Join(applyErr, acceptance.Abort())
							}
							return acceptance.Commit()
						})
					})
				})
			})
			if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
				err = serverapi.ErrRuntimeNoActiveRun
			}
		}
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
