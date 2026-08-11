package app

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type runtimeControlStatusPatchClient struct {
	reconnectRetryRuntimeControlClient
	fastModeResp       serverapi.RuntimeSetFastModeEnabledResponse
	reviewerResp       serverapi.RuntimeSetReviewerEnabledResponse
	autoCompactionResp serverapi.RuntimeSetAutoCompactionEnabledResponse
}

func (c *runtimeControlStatusPatchClient) SetFastModeEnabled(context.Context, serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return c.fastModeResp, nil
}

func (c *runtimeControlStatusPatchClient) SetReviewerEnabled(context.Context, serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return c.reviewerResp, nil
}

func (c *runtimeControlStatusPatchClient) SetAutoCompactionEnabled(context.Context, serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return c.autoCompactionResp, nil
}

func (c *runtimeControlStatusPatchClient) SetQuestionsEnabled(context.Context, serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return serverapi.RuntimeSetQuestionsEnabledResponse{}, nil
}

func TestRuntimeClientControlMutationsPatchCachedSessionStatus(t *testing.T) {
	controls := &runtimeControlStatusPatchClient{
		fastModeResp:       serverapi.RuntimeSetFastModeEnabledResponse{Changed: true},
		reviewerResp:       serverapi.RuntimeSetReviewerEnabledResponse{Changed: true, Mode: "edits"},
		autoCompactionResp: serverapi.RuntimeSetAutoCompactionEnabledResponse{Changed: true, Enabled: true},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})

	if err := runtimeClient.SetSessionName("renamed"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}
	if err := runtimeClient.SetThinkingLevel("high"); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}
	if changed, err := runtimeClient.SetFastModeEnabled(true); err != nil || !changed {
		t.Fatalf("SetFastModeEnabled changed=%v err=%v, want changed", changed, err)
	}
	if changed, mode, err := runtimeClient.SetReviewerEnabled(true); err != nil || !changed || mode != "edits" {
		t.Fatalf("SetReviewerEnabled changed=%v mode=%q err=%v, want edits", changed, mode, err)
	}
	if changed, enabled, err := runtimeClient.SetAutoCompactionEnabled(true); err != nil || !changed || !enabled {
		t.Fatalf("SetAutoCompactionEnabled changed=%v enabled=%v err=%v, want enabled", changed, enabled, err)
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
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
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
