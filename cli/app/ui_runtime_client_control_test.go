package app

import (
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type runtimeControlStatusPatchClient struct {
	reconnectRetryRuntimeControlClient
	settings serverapi.ChatSettings
}

func (c *runtimeControlStatusPatchClient) ReadChatSettings(context.Context, serverapi.ChatSettingsReadRequest) (serverapi.ChatSettingsReadResponse, error) {
	return serverapi.ChatSettingsReadResponse{Settings: c.settings}, nil
}

func (c *runtimeControlStatusPatchClient) MutateChatSettings(context.Context, serverapi.ChatSettingsMutationRequest) (serverapi.ChatSettingsMutationResponse, error) {
	return serverapi.ChatSettingsMutationResponse{
		Result:   serverapi.ChatSettingsMutationResult{Kind: serverapi.ChatSettingsMutationApplied, Changed: true},
		Settings: c.settings,
	}, nil
}

var _ apicontract.ChatSettingsService = (*runtimeControlStatusPatchClient)(nil)

func TestRuntimeClientControlMutationsPatchCachedSessionStatus(t *testing.T) {
	controls := &runtimeControlStatusPatchClient{
		settings: serverapi.ChatSettings{
			SelectedAgent: serverapi.ChatSettingsAgentSummary{Role: "default", Thinking: "high"},
			Supervisor:    serverapi.ChatSettingsSupervisor{Value: serverapi.ChatSettingsSupervisorAfterEdits},
			Fast:          &serverapi.ChatSettingsFast{Value: true},
			Questions:     serverapi.ChatSettingsQuestions{Enabled: false},
			AutoCompaction: serverapi.ChatSettingsAutoCompaction{
				Stored: true,
			},
		},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, controls).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})

	if err := runtimeClient.SetSessionName("renamed"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}
	thinking := "high"
	if response, err := runtimeClient.MutateChatSettings(serverapi.ChatSettingsMutationOperation{
		Kind:  serverapi.ChatSettingsMutationThinking,
		Value: &thinking,
	}); err != nil || response.Result.Kind != serverapi.ChatSettingsMutationApplied {
		t.Fatalf("Thinking mutation response = %+v, err=%v", response, err)
	}

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Session.SessionName != "renamed" ||
		view.Status.ThinkingLevel != "high" ||
		!view.Status.FastModeEnabled ||
		!view.Status.ReviewerEnabled ||
		view.Status.ReviewerFrequency != "edits" ||
		!view.Status.AutoCompactionEnabled {
		t.Fatalf("cached session status was not patched: %+v", view)
	}
}

func TestRuntimeClientInputRequestUsesCallerRequestIdentity(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, unavailableChatSettingsService{}).(*sessionRuntimeClient)
	requestID := runtimeids.NewRuntimeClientRequestID()

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		ClientRequestID: requestID,
		Input:           runtimeinput.Text("hello"),
	}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if got := controls.submitRequestIDs(); len(got) != 1 || got[0] != requestID.String() {
		t.Fatalf("request ids = %+v, want %q", got, requestID.String())
	}
}
