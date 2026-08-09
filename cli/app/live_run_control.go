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
	QueueItemID     string
	Text            string
	ClientRequestID string
}

type RunLiveStopResult struct {
	Status serverapi.RuntimeLiveStopStatus
}

type RunLiveWatchResult struct {
	Response serverapi.RuntimeLiveWatchResponse
	Warnings []string
}

func RunLiveWatch(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) (serverapi.RuntimeLiveWatchResponse, error) {
	result, err := runLiveWatch(ctx, opts, targetSessionID, false)
	return result.Response, err
}

func RunLiveWatchWithCleanup(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) (RunLiveWatchResult, error) {
	return runLiveWatch(ctx, opts, targetSessionID, true)
}

func runLiveWatch(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID, includeCloseWarnings bool) (RunLiveWatchResult, error) {
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		return RunLiveWatchResult{}, err
	}
	response, err := liveClient.LiveWatch(ctx, serverapi.RuntimeLiveWatchRequest{SessionID: targetSessionID.String()})
	if err == nil {
		err = response.Validate()
	}
	closeErr := closeFn()
	result := RunLiveWatchResult{Response: response}
	if includeCloseWarnings && closeErr != nil {
		result.Warnings = []string{closeErr.Error()}
	}
	if err != nil {
		return result, err
	}
	return result, nil
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
	return RunLiveSteerResult{QueueItemID: resp.QueueItemID, Text: resp.Text, ClientRequestID: resp.ClientRequestID}, nil
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

func RunLiveWait(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) (RunPromptResult, error) {
	return runLiveWait(ctx, opts, targetSessionID, false)
}

func RunLiveWaitWithCleanup(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID) (RunPromptResult, error) {
	return runLiveWait(ctx, opts, targetSessionID, true)
}

func runLiveWait(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID, includeCloseWarnings bool) (RunPromptResult, error) {
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		return RunPromptResult{}, err
	}
	resp, err := liveClient.LiveWait(ctx, serverapi.RuntimeLiveWaitRequest{SessionID: targetSessionID.String()})
	result := runtimeLiveWaitResult(targetSessionID, resp)
	closeErr := closeFn()
	if includeCloseWarnings && closeErr != nil {
		result.CleanupWarnings = []string{closeErr.Error()}
	}
	if err != nil {
		return result, err
	}
	return result, nil
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
