package app

import (
	"reflect"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestDelayedTranscriptRuntimeTupleCannotRollBackNewerUnaryState(t *testing.T) {
	controls := newUnavailableRuntimeControlService()
	runtimeClient := newTestSessionRuntimeClient(&countingSessionViewClient{}, controls)
	v9 := runtimeTupleTestView(9, runtimeTupleTestIdleActivity())
	runtimeClient.storeMainView(v9)
	m := newProjectedTestUIModel(runtimeClient)
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	if _, _, err := controller.Accept(runtimeTupleTestHydration(9, runtimeTupleTestIdleActivity())); err != nil {
		t.Fatalf("accept initial hydration: %v", err)
	}

	v11 := runtimeTupleTestView(11, runtimeTupleTestIdleActivity())
	canonical := runtimeClient.storeMainView(v11)
	m.applyRuntimeMainViewState(canonical)
	delayed := runtimeTupleTestUpdateMessage(2, 10, runtimeTupleTestRunningActivity())
	if _, cmd, err := controller.Accept(delayed); err != nil {
		t.Fatalf("accept delayed runtime update: %v", err)
	} else if cmd != nil {
		t.Fatal("lower-sequence runtime update scheduled an unexpected refresh")
	}

	assertRuntimeTupleView(t, runtimeClient.MainView(), v11)
	if m.runtimeActivityBusy() || m.runtimeActivityBlocksInput() {
		t.Fatalf("delayed running state blocked input: projection=%+v lifecycle=%+v", m.runtimeActivityProjection, m.runtimeLifecycle.Run)
	}
	if m.currentRunID != "" || m.currentStepID != "" {
		t.Fatalf("delayed running state restored active identity run=%q step=%q", m.currentRunID, m.currentStepID)
	}
}

func TestRuntimeTupleEqualityIncludesReviewerActivity(t *testing.T) {
	inactive := runtimeTupleTestIdleActivity()
	running := inactive
	running.Reviewer = clientui.ReviewerActivityRunning

	if runtimeActivitiesEqual(inactive, running) {
		t.Fatal("runtime activities with different Reviewer state compared equal")
	}
}

func TestRuntimeMainViewRefreshCommitsOnlyWhenReducerHandlesCandidate(t *testing.T) {
	v10 := runtimeTupleTestView(10, runtimeTupleTestIdleActivity())
	v10.Status = clientui.RuntimeStatus{
		ReviewerFrequency: "edits",
		ReviewerEnabled:   true,
		ThinkingLevel:     "high",
	}
	v10.Session.SessionName = "captured unary metadata"
	v10.Session.ConversationFreshness = clientui.ConversationFreshnessEstablished
	v10.Session.ExecutionTarget = clientui.SessionExecutionTarget{
		WorkspaceID:      "workspace-1",
		WorkspaceName:    "workspace",
		WorkspaceRoot:    "/workspace",
		EffectiveWorkdir: "/workspace",
	}
	reads := &countingSessionViewClient{view: v10}
	runtimeClient := newTestSessionRuntimeClient(reads, newUnavailableRuntimeControlService())
	v9 := runtimeTupleTestView(9, runtimeTupleTestIdleActivity())
	runtimeClient.storeMainView(v9)
	m := newProjectedTestUIModel(runtimeClient)
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
	)
	if _, _, err := controller.Accept(runtimeTupleTestHydration(9, v9.Activity)); err != nil {
		t.Fatalf("accept initial hydration: %v", err)
	}

	decision := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseManual))
	if decision.cmd == nil {
		t.Fatal("refresh request did not return a command")
	}
	msg, ok := decision.cmd().(runtimeMainViewRefreshedMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want runtimeMainViewRefreshedMsg", decision.cmd())
	}
	assertRuntimeTupleView(t, runtimeClient.MainView(), v9)
	if m.runtimeActivityProjection != v9.Activity {
		t.Fatalf("UI projection changed before reducer: %+v", m.runtimeActivityProjection)
	}

	v11 := runtimeTupleTestView(11, runtimeTupleTestIdleActivity())
	if _, _, err := controller.Accept(runtimeTupleTestUpdateMessage(2, 11, v11.Activity)); err != nil {
		t.Fatalf("accept newer transcript update: %v", err)
	}
	m.handleRuntimeMainViewRefreshed(msg)

	got := runtimeClient.MainView()
	assertRuntimeTupleView(t, got, v11)
	if got.Status.ReviewerFrequency != "edits" || !got.Status.ReviewerEnabled {
		t.Fatalf("unary status metadata was not projected: %+v", got.Status)
	}
	if got.Session.SessionName != "captured unary metadata" || got.Session.ExecutionTarget.WorkspaceID != "workspace-1" {
		t.Fatalf("unary session metadata was not projected: %+v", got.Session)
	}
	if m.reviewerMode != "edits" || !m.reviewerEnabled || m.sessionName != "captured unary metadata" {
		t.Fatalf("UI metadata was not projected: reviewer=%q enabled=%t session=%q", m.reviewerMode, m.reviewerEnabled, m.sessionName)
	}
}

func TestRuntimeMainViewRefreshPreservesMetadataChangedAfterRequestStarted(t *testing.T) {
	v10 := runtimeTupleTestView(10, runtimeTupleTestIdleActivity())
	v10.Status = clientui.RuntimeStatus{
		ReviewerFrequency: "stale unary reviewer",
		ThinkingLevel:     "stale unary thinking",
	}
	reads := &countingSessionViewClient{view: v10}
	runtimeClient := newTestSessionRuntimeClient(reads, newUnavailableRuntimeControlService())
	v9 := runtimeTupleTestView(9, runtimeTupleTestIdleActivity())
	v9.Status = clientui.RuntimeStatus{ReviewerFrequency: "initial", ThinkingLevel: "initial"}
	runtimeClient.storeMainView(v9)
	m := newProjectedTestUIModel(runtimeClient)

	decision := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseManual))
	msg, ok := decision.cmd().(runtimeMainViewRefreshedMsg)
	if !ok {
		t.Fatal("refresh command returned an unexpected message")
	}
	statusMessage := clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptSessionStatus{
		ReviewerFrequency: "fresh transcript reviewer",
		ThinkingLevel:     "fresh transcript thinking",
		CompactionMode:    "auto",
	}))

	admission, err := runtimeClient.admitTranscriptMessageState(statusMessage)
	if err != nil {
		t.Fatalf("admit transcript status: %v", err)
	}
	m.applyAdmittedTranscriptMessageState(statusMessage, admission)

	m.handleRuntimeMainViewRefreshed(msg)

	got := runtimeClient.MainView()
	assertRuntimeTupleView(t, got, v10)
	if got.Status.ReviewerFrequency != "fresh transcript reviewer" || got.Status.ThinkingLevel != "fresh transcript thinking" {
		t.Fatalf("stale unary response replaced newer metadata: %+v", got.Status)
	}
	if m.reviewerMode != "fresh transcript reviewer" || m.thinkingLevel != "fresh transcript thinking" {
		t.Fatalf("UI projected stale unary metadata: reviewer=%q thinking=%q", m.reviewerMode, m.thinkingLevel)
	}
}

func runtimeTupleTestView(
	sequence uint64,
	activity clientui.RuntimeActivity,
) clientui.RuntimeMainView {
	return clientui.RuntimeMainView{
		Version:  clientui.ReadModelVersion{Epoch: "runtime-tuple-test", Generation: 1, Sequence: sequence},
		Session:  clientui.RuntimeSessionView{SessionID: "session-1"},
		Activity: activity,
	}
}

func runtimeTupleTestHydration(
	sequence uint64,
	activity clientui.RuntimeActivity,
) clientui.TranscriptMessage {
	message := ongoingHydrationMessage(1)
	payload := message.Payload().(clientui.TranscriptHydration)
	payload.RuntimeReadModelUpdate = clientui.RuntimeReadModelUpdate{
		Version:  clientui.ReadModelVersion{Epoch: "runtime-tuple-test", Generation: 1, Sequence: sequence},
		Activity: activity,
	}
	message = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(payload))
	return message
}

func runtimeTupleTestUpdateMessage(
	deliverySequence uint64,
	runtimeSequence uint64,
	activity clientui.RuntimeActivity,
) clientui.TranscriptMessage {
	return clientui.NewTranscriptMessage(deliverySequence, clientui.NewTranscriptEvent(clientui.RuntimeReadModelUpdate{
		Version:  clientui.ReadModelVersion{Epoch: "runtime-tuple-test", Generation: 1, Sequence: runtimeSequence},
		Activity: activity,
	}))

}

func runtimeTupleTestIdleActivity() clientui.RuntimeActivity {
	return clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRegisteredIdle,
		Reviewer:       clientui.ReviewerActivityInactive,
		QueueAccepting: true,
	}
}

func runtimeTupleTestRunningActivity() clientui.RuntimeActivity {
	return clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRunning,
		Reviewer:       clientui.ReviewerActivityInactive,
		QueueAccepting: true,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID:      ongoingTestRunID(),
			StepID:     ongoingTestStepID(),
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		},
	}
}

func assertRuntimeTupleView(t *testing.T, got, want clientui.RuntimeMainView) {
	t.Helper()
	if got.Version != want.Version || !runtimeActivitiesEqual(got.Activity, want.Activity) {
		t.Fatalf(
			"runtime tuple = version=%+v activity=%+v, want version=%+v activity=%+v",
			got.Version,
			got.Activity,
			want.Version,
			want.Activity,
		)
	}
}

func assertRuntimeTupleHydrationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected stale/conflicting hydration developer error")
	}
	developerErr, ok := err.(ongoing.DeveloperError)
	if !ok {
		t.Fatalf("hydration error = %T, want ongoing.DeveloperError", err)
	}
	if _, ok := developerErr.Facts["current_version"]; !ok {
		t.Fatalf("hydration error lacks current version diagnostics: %+v", developerErr)
	}
	if _, ok := developerErr.Facts["incoming_version"]; !ok {
		t.Fatalf("hydration error lacks incoming version diagnostics: %+v", developerErr)
	}
}

func assertUnchanged[T any](t *testing.T, label string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s changed: got %+v, want %+v", label, got, want)
	}
}

var _ = serverapi.RuntimeInterruptResponse{}
