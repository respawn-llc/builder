package app

import (
	"context"
	"errors"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type runtimeControlStatusPatchClient struct {
	reconnectRetryRuntimeControlClient
	fastModeResp       serverapi.RuntimeSetFastModeEnabledResponse
	reviewerResp       serverapi.RuntimeSetReviewerEnabledResponse
	autoCompactionResp serverapi.RuntimeSetAutoCompactionEnabledResponse
	queueErr           error
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

func (c *runtimeControlStatusPatchClient) QueueUserMessage(context.Context, serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	if c.queueErr != nil {
		return serverapi.RuntimeQueueUserMessageResponse{}, c.queueErr
	}
	return serverapi.RuntimeQueueUserMessageResponse{QueueItemID: "queued-1", Text: "queued input"}, nil
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
	if view.Session.SessionName != "renamed" {
		t.Fatalf("cached session name = %q, want renamed", view.Session.SessionName)
	}
	if view.Status.ThinkingLevel != "high" {
		t.Fatalf("cached thinking level = %q, want high", view.Status.ThinkingLevel)
	}
	if !view.Status.FastModeEnabled {
		t.Fatal("cached fast mode = false, want true")
	}
	if !view.Status.ReviewerEnabled || view.Status.ReviewerFrequency != "edits" {
		t.Fatalf("cached reviewer status = enabled %v frequency %q, want edits", view.Status.ReviewerEnabled, view.Status.ReviewerFrequency)
	}
	if !view.Status.AutoCompactionEnabled {
		t.Fatal("cached auto-compaction = false, want true")
	}
}

func TestRuntimeClientQueueUserMessageErrorNotifiesConnectionObserver(t *testing.T) {
	boom := errors.New("queue failed")
	controls := &runtimeControlStatusPatchClient{queueErr: boom}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	var observed error
	runtimeClient.SetConnectionStateObserver(func(err error) { observed = err })

	_, err := runtimeClient.QueueUserMessage("queued input")
	if !errors.Is(err, boom) {
		t.Fatalf("QueueUserMessage err = %v, want %v", err, boom)
	}
	if !errors.Is(observed, boom) {
		t.Fatalf("observed connection err = %v, want %v", observed, boom)
	}
}

func TestRuntimeClientSetGoalCachesGoal(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalShowResponse{Goal: &serverapi.RuntimeGoal{ID: "goal-1", Objective: "ship", Status: "active"}},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)

	goal, err := runtimeClient.SetGoal("ship")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if goal == nil || goal.ID != "goal-1" || view.Status.Goal == nil || view.Status.Goal.ID != "goal-1" {
		t.Fatalf("goal = %+v cached = %+v, want goal-1", goal, view.Status.Goal)
	}
}
