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
	if s == nil || s.authority == nil {
		return errors.New("session runtime authority is required")
	}
	return s.authority.WithLiveExecutionRuntime(ctx, id, fn)
}

func (s *Service) LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveSteerResponse{}, err
	}
	callerSessionID := serverapi.CanonicalOptionalString(req.CallerSessionID)
	text := strings.TrimSpace(req.Text)
	var resp serverapi.RuntimeLiveSteerResponse
	err = s.withLiveExecutionRuntime(ctx, sessionID, func(callbackCtx context.Context, engine *runtime.Engine) error {
		queueText := text
		var agentSteer runtime.AgentSteer
		if callerSessionID.Present {
			callerID, parseErr := runtimeids.ParseSessionID(callerSessionID.Value)
			if parseErr != nil {
				return parseErr
			}
			agentSteer, err = runtime.NewAgentSteer(callerID, text)
			if err != nil {
				return err
			}
			queueText = *agentSteer.Message().Content
		}
		var item runtime.QueuedUserMessage
		var accepted bool
		if callerSessionID.Present {
			item, accepted, err = engine.QueueAgentSteerForActiveRun(callbackCtx, agentSteer, nil)
		} else {
			item, accepted, err = engine.QueueUserMessageForActiveRun(callbackCtx, queueText, nil)
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
		resp = serverapi.RuntimeLiveSteerResponse{QueueItemID: item.ID, Text: displayText}
		if s != nil && s.promptStore != nil {
			if _, err := s.recordPromptHistory(callbackCtx, sessionID.String(), queueText); err != nil {
				engine.ReportPromptHistoryPersistError(err.Error())
			}
		}
		return nil
	})
	return resp, err
}

func (s *Service) LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeLiveStopResponse{}, errors.New("session runtime authority is required")
	}
	resp := serverapi.RuntimeLiveStopResponse{Status: serverapi.RuntimeLiveStopStatusIdle}
	stopped, err := s.authority.InterruptCurrentLiveRun(ctx, sessionID)
	if err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	if stopped {
		resp.Status = serverapi.RuntimeLiveStopStatusStopped
	}
	return resp, nil
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
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
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
		if ctx.Err() == nil {
			select {
			case terminal := <-terminalCh:
				cancel()
				wg.Wait()
				return liveWatchResult(id, name, terminal.result, terminal.err)
			default:
			}
		}
		cancel()
		wg.Wait()
		return serverapi.RuntimeLiveWatchResponse{}, err
	case <-ctx.Done():
		cancel()
		wg.Wait()
		return serverapi.RuntimeLiveWatchResponse{}, ctx.Err()
	}
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

type liveRunTerminal struct {
	result        runtime.LiveRunResult
	err           error
	noFinal       bool
	noFinalReason runtime.LiveRunNoFinalAnswerReason
	status        runtime.RunStatus
	reason        *string
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
	if terminal.err == nil {
		terminal.err = result.Error
	}
	if terminal.err != nil {
		if terminal.status == runtime.RunStatusInterrupted {
			reason := strings.TrimSpace(string(terminal.status))
			terminal.reason = &reason
			diagnosticErr := result.Error
			if diagnosticErr == nil {
				diagnosticErr = terminal.err
			}
			diagnostic := strings.TrimSpace(diagnosticErr.Error())
			if diagnostic != "" && diagnostic != reason {
				terminal.diagnostic = &diagnostic
			}
			return terminal
		}
		reason := strings.TrimSpace(terminal.err.Error())
		if reason == "" {
			reason = strings.TrimSpace(string(result.Status))
		}
		if reason == "" {
			reason = "execution error"
		}
		terminal.reason = &reason
	}
	if result.Error != nil && (terminal.reason == nil || result.Error.Error() != *terminal.reason) {
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
		if terminal.reason != nil {
			reason = *terminal.reason
		}
		if terminal.status == runtime.RunStatusInterrupted {
			kind = serverapi.RuntimeLiveWatchInterrupted
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
