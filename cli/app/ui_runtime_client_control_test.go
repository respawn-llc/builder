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

	_, err := runtimeClient.QueueRuntimeUserMessage(clientui.RuntimeQueueUserMessageRequest{
		OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queue-error"},
		Text:         "queued input",
	})
	if !errors.Is(err, boom) {
		t.Fatalf("QueueUserMessage err = %v, want %v", err, boom)
	}
	if !errors.Is(observed, boom) {
		t.Fatalf("observed connection err = %v, want %v", observed, boom)
	}
}

func TestRuntimeClientInterruptAppliesReturnedActivitySnapshot(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 7}
	controls := &reconnectRetryRuntimeControlClient{
		interruptResp: serverapi.RuntimeInterruptResponse{
			Version: version,
			Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{
				QueueAccepting: true,
			}),
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
		},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
			ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
			RunID:      "run-1",
			StepID:     "step-1",
		}),
	})

	if err := runtimeClient.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != version || view.InputReconciliation.Version != version {
		t.Fatalf("versioned submodels not applied: view=%+v reconciliation=%+v", view.Version, view.InputReconciliation.Version)
	}
	if view.Activity.State != clientui.RuntimeActivityRegisteredIdle || !view.Activity.QueueAccepting {
		t.Fatalf("activity = %+v, want returned idle snapshot", view.Activity)
	}
}

func TestRuntimeClientInputRequestsUseCallerOperationRefAsRequestID(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-boundary-1"}

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{OperationRef: ref, Text: "hello"}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if got := controls.submitRequestIDs(); len(got) != 1 || got[0] != ref.ClientRequestID {
		t.Fatalf("request ids = %+v, want %q", got, ref.ClientRequestID)
	}
	if len(controls.submitRefs) != 1 || controls.submitRefs[0] != ref {
		t.Fatalf("operation refs = %+v, want %+v", controls.submitRefs, ref)
	}
}

func TestRuntimeClientInterruptPassesPendingOperationRefs(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 7}
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	controls := &reconnectRetryRuntimeControlClient{
		interruptResp: serverapi.RuntimeInterruptResponse{
			Version:             version,
			Activity:            clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}),
			InputReconciliation: clientui.NewUnknownRuntimeInputReconciliationSnapshot(version, []clientui.RuntimeOperationRef{ref}),
		},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)

	if err := runtimeClient.InterruptWithPendingRefs([]clientui.RuntimeOperationRef{ref}); err != nil {
		t.Fatalf("InterruptWithPendingRefs: %v", err)
	}
	if len(controls.interruptReq.PendingOperationRefs) != 1 || controls.interruptReq.PendingOperationRefs[0] != ref {
		t.Fatalf("pending refs = %+v, want %+v", controls.interruptReq.PendingOperationRefs, ref)
	}
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if len(view.InputReconciliation.Operations) != 1 || view.InputReconciliation.Operations[0].OperationRef != ref {
		t.Fatalf("cached reconciliation = %+v, want ref %+v", view.InputReconciliation, ref)
	}
}

func TestRuntimeClientInterruptPassesTargetOperationRef(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 8}
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-target"}
	controls := &reconnectRetryRuntimeControlClient{
		interruptResp: serverapi.RuntimeInterruptResponse{
			Version:             version,
			Activity:            clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}),
			InputReconciliation: clientui.NewUnknownRuntimeInputReconciliationSnapshot(version, []clientui.RuntimeOperationRef{ref}),
		},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)

	if err := runtimeClient.InterruptWithTarget(ref, []clientui.RuntimeOperationRef{ref}); err != nil {
		t.Fatalf("InterruptWithTarget: %v", err)
	}
	if controls.interruptReq.TargetOperationRef == nil || *controls.interruptReq.TargetOperationRef != ref {
		t.Fatalf("target ref = %+v, want %+v", controls.interruptReq.TargetOperationRef, ref)
	}
}

func TestRuntimeClientMainViewRefreshPassesPendingOperationRefs(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}}}
	runtimeClient := newUIRuntimeClientWithReads("session-1", reads, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)

	if _, err := runtimeClient.RefreshMainViewWithPendingRefs([]clientui.RuntimeOperationRef{ref}); err != nil {
		t.Fatalf("RefreshMainViewWithPendingRefs: %v", err)
	}
	if len(reads.lastMainViewReq.PendingOperationRefs) != 1 || reads.lastMainViewReq.PendingOperationRefs[0] != ref {
		t.Fatalf("pending refs = %+v, want %+v", reads.lastMainViewReq.PendingOperationRefs, ref)
	}
}

func TestRuntimeClientInterruptIgnoresStaleReturnedActivitySnapshot(t *testing.T) {
	currentVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 9}
	staleVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 7}
	controls := &reconnectRetryRuntimeControlClient{
		interruptResp: serverapi.RuntimeInterruptResponse{
			Version:             staleVersion,
			Activity:            clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true}),
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(staleVersion),
		},
	}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls).(*sessionRuntimeClient)
	runningActivity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-1",
		StepID:     "step-1",
	})
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version:             currentVersion,
		Session:             clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity:            runningActivity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(currentVersion),
	})

	if err := runtimeClient.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != currentVersion || view.Activity != runningActivity {
		t.Fatalf("stale interrupt response was applied: view=%+v", view)
	}
}

func TestRuntimeClientRuntimeActivityEventPatchesCachedMainView(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 7}
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	reconciliation := clientui.NewEmptyRuntimeInputReconciliationSnapshot(version)
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    version,
		RuntimeActivity:     &activity,
		InputReconciliation: &reconciliation,
	})

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != version || view.Activity != activity || view.InputReconciliation.Version != version {
		t.Fatalf("cached runtime activity = %+v, want version %+v activity %+v", view, version, activity)
	}
}

func TestRuntimeClientRuntimeActivityEventAcceptsEmptyReconciliationAsAuthoritative(t *testing.T) {
	currentVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 7}
	nextVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 8}
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version: currentVersion,
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{
			Version: currentVersion,
			Operations: []clientui.RuntimeInputReconciliation{{
				Version:      currentVersion,
				OperationRef: ref,
				State:        clientui.RuntimeInputReconciliationCommitted,
			}},
		},
	})
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{})
	emptyReconciliation := clientui.NewEmptyRuntimeInputReconciliationSnapshot(nextVersion)

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    nextVersion,
		RuntimeActivity:     &activity,
		InputReconciliation: &emptyReconciliation,
	})

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if len(view.InputReconciliation.Operations) != 0 {
		t.Fatalf("empty authoritative reconciliation was not applied: %+v", view.InputReconciliation)
	}
	if view.InputReconciliation.Version != nextVersion {
		t.Fatalf("reconciliation version = %+v, want %+v", view.InputReconciliation.Version, nextVersion)
	}
}

func TestApplyRuntimeEventStatusPatchesCachedMainViewFromRuntimeActivityEvent(t *testing.T) {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 7}
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	reconciliation := clientui.NewEmptyRuntimeInputReconciliationSnapshot(version)
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)
	m := &uiModel{uiRuntimeFeatureState: uiRuntimeFeatureState{engine: runtimeClient}}

	m.applyRuntimeEventStatus(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    version,
		RuntimeActivity:     &activity,
		InputReconciliation: &reconciliation,
	})

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != version || view.Activity != activity {
		t.Fatalf("cached runtime activity = %+v, want version %+v activity %+v", view, version, activity)
	}
}

func TestRuntimeClientRejectsNewGenerationRuntimeActivitySnapshotUntilHydration(t *testing.T) {
	currentVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 9}
	nextVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 2, Sequence: 1}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version: currentVersion,
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
			ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
			RunID:      "run-1",
			StepID:     "step-1",
		}),
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(currentVersion),
	})
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	reconciliation := clientui.NewEmptyRuntimeInputReconciliationSnapshot(nextVersion)

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    nextVersion,
		RuntimeActivity:     &activity,
		InputReconciliation: &reconciliation,
	})

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != currentVersion || view.Activity == activity {
		t.Fatalf("new generation activity was applied before hydration: %+v", view)
	}
}

func TestRuntimeClientRejectsIncomparableEpochRuntimeActivitySnapshot(t *testing.T) {
	currentVersion := clientui.ReadModelVersion{Epoch: "epoch-2", Generation: 1, Sequence: 9}
	oldEpochVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 99, Sequence: 99}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)
	runningActivity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-1",
		StepID:     "step-1",
	})
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version:             currentVersion,
		Session:             clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity:            runningActivity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(currentVersion),
	})
	incoming := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	reconciliation := clientui.NewEmptyRuntimeInputReconciliationSnapshot(oldEpochVersion)

	runtimeClient.observeRuntimeEventStatus(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    oldEpochVersion,
		RuntimeActivity:     &incoming,
		InputReconciliation: &reconciliation,
	})

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != currentVersion || view.Activity != runningActivity {
		t.Fatalf("incomparable epoch activity was applied: %+v", view)
	}
}

func TestRuntimeClientStoreMainViewRejectsStaleSameGenerationHydration(t *testing.T) {
	currentVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 9}
	staleVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 8}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)
	idle := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	running := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-stale",
		StepID:     "step-stale",
	})
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version:  currentVersion,
		Session:  clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity: idle,
	})

	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Version:  staleVersion,
		Session:  clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity: running,
	})

	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if view.Version != currentVersion || view.Activity != idle {
		t.Fatalf("stale main view replaced cache: %+v", view)
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
