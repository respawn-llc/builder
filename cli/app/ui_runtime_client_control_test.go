package app

import (
	"context"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func TestRuntimeClientInputMakesOneExplicitCall(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, nil).(*sessionRuntimeClient)

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		Input: runtimeinput.Text("hello"),
	}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if controls.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", controls.submitCalls)
	}
}

type chatSettingsDeadlineClient struct {
	readRemaining   time.Duration
	mutateRemaining time.Duration
}

func (c *chatSettingsDeadlineClient) ReadChatSettings(
	ctx context.Context,
	_ serverapi.ChatSettingsReadRequest,
) (serverapi.ChatSettingsReadResponse, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		c.readRemaining = time.Until(deadline)
	}
	return serverapi.ChatSettingsReadResponse{}, nil
}

func (c *chatSettingsDeadlineClient) MutateChatSettings(
	ctx context.Context,
	_ serverapi.ChatSettingsMutationRequest,
) (serverapi.ChatSettingsMutationResponse, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		c.mutateRemaining = time.Until(deadline)
	}
	return serverapi.ChatSettingsMutationResponse{}, nil
}

func TestRuntimeClientUsesRelaxedChatSettingsDeadline(t *testing.T) {
	settings := &chatSettingsDeadlineClient{}
	runtimeClient := &sessionRuntimeClient{
		sessionID:    runtimeids.NewSessionID().String(),
		chatSettings: settings,
	}

	if _, err := runtimeClient.ReadChatSettings(); err != nil {
		t.Fatalf("ReadChatSettings: %v", err)
	}
	questionsEnabled := false
	if _, err := runtimeClient.MutateChatSettings(serverapi.ChatSettingsMutationOperation{
		Kind:    serverapi.ChatSettingsMutationQuestions,
		Enabled: &questionsEnabled,
	}); err != nil {
		t.Fatalf("MutateChatSettings: %v", err)
	}

	const minimumRelaxedDeadline = 20 * time.Second
	if settings.readRemaining < minimumRelaxedDeadline {
		t.Fatalf("Chat settings read deadline = %s, want at least %s", settings.readRemaining, minimumRelaxedDeadline)
	}
	if settings.mutateRemaining < minimumRelaxedDeadline {
		t.Fatalf("Chat settings mutation deadline = %s, want at least %s", settings.mutateRemaining, minimumRelaxedDeadline)
	}
}
