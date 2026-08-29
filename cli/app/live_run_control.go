package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type RunLiveSteerResult struct {
	QueueItemID string
	Text        string
}

type RunLiveStopResult struct {
	Status serverapi.RuntimeLiveStopStatus
}

type RunLiveWatchResult struct {
	Response serverapi.RuntimeLiveWatchResponse
	Error    error
	Close    func() error
}

func RunLiveWatchWithCleanup(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) RunLiveWatchResult {
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		return RunLiveWatchResult{Error: err, Close: closeFn}
	}
	response, err := liveClient.LiveWatch(ctx, serverapi.RuntimeLiveWatchRequest{SessionID: targetSessionID.String()})
	return RunLiveWatchResult{Response: response, Error: err, Close: closeFn}
}

func RunLiveSteer(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID, text string) (RunLiveSteerResult, error) {
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return RunLiveSteerResult{}, errors.New("text is required")
	}
	callerSessionID, err := LiveSteerCallerSessionID()
	if err != nil {
		return RunLiveSteerResult{}, err
	}
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return RunLiveSteerResult{}, err
	}
	defer func() { _ = closeFn() }()
	resp, err := liveClient.LiveSteer(ctx, serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       targetSessionID.String(),
		CallerSessionID: callerSessionID,
		Text:            trimmedText,
	})
	if err != nil {
		return RunLiveSteerResult{}, err
	}
	return RunLiveSteerResult{QueueItemID: resp.QueueItemID, Text: resp.Text}, nil
}

func LiveSteerCallerSessionID() (*string, error) {
	raw, ok := sessionenv.LookupSessionID(os.LookupEnv)
	if !ok {
		return nil, nil
	}
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid invoking Session ID %q: %w", raw, err)
	}
	if !sessionID.IsCanonicalUUIDv4() {
		return nil, fmt.Errorf("invalid invoking Session ID %q: canonical UUIDv4 required", raw)
	}
	value := sessionID.String()
	return &value, nil
}

func RunLiveStop(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) (RunLiveStopResult, error) {
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return RunLiveStopResult{}, err
	}
	defer func() { _ = closeFn() }()
	resp, err := liveClient.LiveStop(ctx, serverapi.RuntimeLiveStopRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       targetSessionID.String(),
	})
	if err != nil {
		return RunLiveStopResult{}, err
	}
	return RunLiveStopResult{Status: resp.Status}, nil
}

type RunLiveWaitResult struct {
	Result RunPromptResult
	Error  error
	Close  func() error
}

func RunLiveWait(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) (RunPromptResult, error) {
	result := RunLiveWaitWithCleanup(ctx, opts, targetSessionID)
	if result.Close != nil {
		_ = result.Close()
	}
	return result.Result, result.Error
}

func RunLiveWaitWithCleanup(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) RunLiveWaitResult {
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		return RunLiveWaitResult{Error: err, Close: closeFn}
	}
	resp, err := liveClient.LiveWait(ctx, serverapi.RuntimeLiveWaitRequest{SessionID: targetSessionID.String()})
	return RunLiveWaitResult{
		Result: runtimeLiveWaitResult(targetSessionID, resp),
		Error:  err,
		Close:  closeFn,
	}
}

func runtimeLiveWaitResult(targetSessionID runtimeids.SessionID, resp serverapi.RuntimeLiveWaitResponse) RunPromptResult {
	resultText := ""
	if resp.Result != nil {
		resultText = *resp.Result
	}
	sessionID := targetSessionID.String()
	if strings.TrimSpace(resp.SessionID) != "" {
		sessionID = resp.SessionID
	}
	result := RunPromptResult{
		SessionID:   sessionID,
		SessionName: resp.SessionName,
		Result:      resultText,
		Duration:    time.Duration(resp.DurationMillis) * time.Millisecond,
		Warnings:    nil,
	}
	return result
}
