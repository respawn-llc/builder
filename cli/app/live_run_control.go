package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type RunLiveSteerResult struct {
	QueueItemID     string
	Text            string
	ClientRequestID string
}

type RunLiveStopResult struct {
	Status serverapi.RuntimeLiveStopStatus
}

func RunLiveSteer(ctx context.Context, opts Options, targetSessionID runtimeids.SessionID, text string) (RunLiveSteerResult, error) {
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return RunLiveSteerResult{}, errors.New("text is required")
	}
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		return RunLiveSteerResult{}, err
	}
	defer func() { _ = closeFn() }()
	resp, err := liveClient.LiveSteer(ctx, serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       targetSessionID.String(),
		Text:            trimmedText,
	})
	if err != nil {
		return RunLiveSteerResult{}, err
	}
	return RunLiveSteerResult{QueueItemID: resp.QueueItemID, Text: resp.Text, ClientRequestID: resp.ClientRequestID}, nil
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
	liveClient, closeFn, err := startRuntimeLiveControlClient(ctx, opts)
	if err != nil {
		return RunPromptResult{}, err
	}
	defer func() { _ = closeFn() }()
	resp, err := liveClient.LiveWait(ctx, serverapi.RuntimeLiveWaitRequest{SessionID: targetSessionID.String()})
	result := runtimeLiveWaitResult(targetSessionID, resp)
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
