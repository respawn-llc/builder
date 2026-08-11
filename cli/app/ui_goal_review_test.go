package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestGoalSetCommandDeliversQueuedPreviewToTUIWithoutChangingCanonicalGoal(t *testing.T) {
	committed := runtimeGoalFixture(
		"goal-committed",
		"committed objective",
		clientui.RuntimeGoalStatusPaused,
		false,
	)
	if err := committed.Goal.Validate(); err != nil {
		t.Fatalf("committed Goal fixture: %v", err)
	}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{
			Pending: &clientui.GoalPreview{
				Objective: "queued objective",
				Status:    clientui.RuntimeGoalStatusActive,
			},
			Availability: clientui.GoalAvailabilityAvailable,
		},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: committed},
	})
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	command := model.goalRuntimeCommand(goalRuntimeSet, "queued objective")
	if command == nil {
		t.Fatal("goal Set command = nil")
	}
	rawMessage := command()
	message, ok := rawMessage.(goalRuntimeDoneMsg)
	if !ok {
		t.Fatalf("goal Set command message = %T, want goalRuntimeDoneMsg", rawMessage)
	}
	updateUIModel(t, model, message)

	wantPending := &clientui.GoalPreview{
		Objective: "queued objective",
		Status:    clientui.RuntimeGoalStatusActive,
	}
	if !reflect.DeepEqual(model.goal.pending, wantPending) {
		t.Fatalf("TUI pending preview = %+v, want %+v", model.goal.pending, wantPending)
	}
	if model.goal.goal != nil {
		t.Fatalf("TUI durable Goal = %+v, want nil for queued Set", model.goal.goal)
	}
	content := strings.Join(model.layout().goalOverlayContentLines(80), "\n")
	if !strings.Contains(content, "queued objective") || !strings.Contains(content, "active") {
		t.Fatalf("TUI Goal overlay = %q, want queued objective/status", content)
	}
	view := runtimeClient.MainView()
	if !reflect.DeepEqual(view.Status.Goal, committed) {
		t.Fatalf("canonical runtime Goal = %+v, want %+v", view.Status.Goal, committed)
	}

	authoritative := clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal:         transcriptGoalFixture("goal-committed", "committed objective", clientui.RuntimeGoalStatusActive),
		Availability: clientui.GoalAvailabilityAvailable,
	}))
	admission, err := runtimeClient.admitTranscriptMessageState(authoritative)
	if err != nil {
		t.Fatalf("admit authoritative Goal status: %v", err)
	}
	if cmd := model.applyAdmittedTranscriptMessageState(authoritative, admission); cmd != nil {
		t.Fatalf("authoritative Goal status command = %v, want nil", cmd)
	}
	if model.goal.pending != nil {
		t.Fatalf("stale TUI pending preview = %+v, want nil", model.goal.pending)
	}
	if model.goal.goal == nil || model.goal.goal.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("reconciled TUI Goal = %+v, want authoritative active Goal", model.goal.goal)
	}
	if preview := runtimeClient.goalMutationPendingPreview(); preview != nil {
		t.Fatalf("stale runtime-client pending preview = %+v, want nil", preview)
	}
}

func TestStaleQueuedGoalSetResultCannotReinstallPreviewAfterAuthoritativeGoalStatus(t *testing.T) {
	committed := runtimeGoalFixture(
		"goal-committed",
		"committed objective",
		clientui.RuntimeGoalStatusPaused,
		false,
	)
	if err := committed.Goal.Validate(); err != nil {
		t.Fatalf("committed Goal fixture: %v", err)
	}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{
			Pending: &clientui.GoalPreview{
				Objective: "queued objective",
				Status:    clientui.RuntimeGoalStatusActive,
			},
			Availability: clientui.GoalAvailabilityAvailable,
		},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: committed},
	})
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	command := model.goalRuntimeCommand(goalRuntimeSet, "queued objective")
	rawMessage := command()
	message, ok := rawMessage.(goalRuntimeDoneMsg)
	if !ok {
		t.Fatalf("goal Set command message = %T, want goalRuntimeDoneMsg", rawMessage)
	}

	authoritative := clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal:         transcriptGoalFixture("goal-committed", "committed objective", clientui.RuntimeGoalStatusActive),
		Availability: clientui.GoalAvailabilityAvailable,
	}))
	admission, err := runtimeClient.admitTranscriptMessageState(authoritative)
	if err != nil {
		t.Fatalf("admit authoritative Goal status: %v", err)
	}
	if cmd := model.applyAdmittedTranscriptMessageState(authoritative, admission); cmd != nil {
		t.Fatalf("authoritative Goal status command = %v, want nil", cmd)
	}
	if model.goal.pending != nil {
		t.Fatalf("authoritative Goal status left pending preview = %+v", model.goal.pending)
	}

	updateUIModel(t, model, message)
	if model.goal.pending != nil {
		t.Fatalf("stale queued Set reinstalled preview = %+v", model.goal.pending)
	}
	if model.goal.goal == nil || model.goal.goal.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("stale queued Set replaced authoritative Goal = %+v", model.goal.goal)
	}
	if !runtimeGoalsEqual(runtimeClient.MainView().Status.Goal, model.goal.goal) {
		t.Fatalf("TUI Goal = %+v, want canonical Goal %+v", model.goal.goal, runtimeClient.MainView().Status.Goal)
	}
}

func TestAuthoritativeMainViewRefreshReconcilesQueuedGoalPreview(t *testing.T) {
	committed := runtimeGoalFixture(
		"goal-committed",
		"committed objective",
		clientui.RuntimeGoalStatusPaused,
		false,
	)
	authoritative := runtimeGoalFixture(
		"goal-committed",
		"committed objective",
		clientui.RuntimeGoalStatusActive,
		false,
	)
	for name, goal := range map[string]*clientui.RuntimeGoal{"committed": committed, "authoritative": authoritative} {
		if err := goal.Goal.Validate(); err != nil {
			t.Fatalf("%s Goal fixture: %v", name, err)
		}
	}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{
			Pending: &clientui.GoalPreview{
				Objective: "queued objective",
				Status:    clientui.RuntimeGoalStatusActive,
			},
			Availability: clientui.GoalAvailabilityAvailable,
		},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: committed},
	})
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	rawMessage := model.goalRuntimeCommand(goalRuntimeSet, "queued objective")()
	message, ok := rawMessage.(goalRuntimeDoneMsg)
	if !ok {
		t.Fatalf("goal Set command message = %T, want goalRuntimeDoneMsg", rawMessage)
	}
	updateUIModel(t, model, message)
	if model.goal.pending == nil {
		t.Fatal("queued Set did not install TUI preview before refresh")
	}

	model.runtimeMainViewToken = 1
	model.handleRuntimeMainViewRefreshed(runtimeMainViewRefreshedMsg{
		token: 1,
		view: clientui.RuntimeMainView{
			Session: clientui.RuntimeSessionView{SessionID: "session-1"},
			Status:  clientui.RuntimeStatus{Goal: authoritative},
		},
	})

	if model.goal.pending != nil {
		t.Fatalf("main-view refresh left stale TUI preview = %+v", model.goal.pending)
	}
	if model.goal.goal == nil || model.goal.goal.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("main-view refresh left TUI Goal = %+v, want authoritative active Goal", model.goal.goal)
	}
	if runtimeClient.goalMutationPendingPreview() != nil {
		t.Fatal("main-view refresh left stale runtime-client pending preview")
	}
	if !runtimeGoalsEqual(runtimeClient.MainView().Status.Goal, authoritative) {
		t.Fatalf("canonical Goal = %+v, want %+v", runtimeClient.MainView().Status.Goal, authoritative)
	}
}

func TestStaleAcceptedGoalSetResultCannotOverwriteAuthoritativeGoal(t *testing.T) {
	committed := runtimeGoalFixture(
		"goal-committed",
		"committed objective",
		clientui.RuntimeGoalStatusPaused,
		false,
	)
	accepted := &serverapi.RuntimeGoal{
		ID:        "goal-set",
		Objective: "set objective",
		Status:    clientui.RuntimeGoalStatusActive,
		CreatedAt: time.Unix(2, 0),
		UpdatedAt: time.Unix(2, 0),
	}
	authoritative := runtimeGoalFixture(
		"goal-authoritative",
		"authoritative objective",
		clientui.RuntimeGoalStatusActive,
		false,
	)
	if err := committed.Goal.Validate(); err != nil {
		t.Fatalf("committed Goal fixture: %v", err)
	}
	if err := accepted.Validate(); err != nil {
		t.Fatalf("accepted Goal fixture: %v", err)
	}
	if err := authoritative.Goal.Validate(); err != nil {
		t.Fatalf("authoritative Goal fixture: %v", err)
	}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: goalResponseFixture(accepted),
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: committed},
	})
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	rawMessage := model.goalRuntimeCommand(goalRuntimeSet, accepted.Objective)()
	message, ok := rawMessage.(goalRuntimeDoneMsg)
	if !ok {
		t.Fatalf("goal Set command message = %T, want goalRuntimeDoneMsg", rawMessage)
	}

	authoritativeMessage := clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal:         &clientui.TranscriptGoal{Goal: authoritative.Goal},
		Availability: clientui.GoalAvailabilityAvailable,
	}))
	admission, err := runtimeClient.admitTranscriptMessageState(authoritativeMessage)
	if err != nil {
		t.Fatalf("admit authoritative Goal status: %v", err)
	}
	if cmd := model.applyAdmittedTranscriptMessageState(authoritativeMessage, admission); cmd != nil {
		t.Fatalf("authoritative Goal status command = %v, want nil", cmd)
	}

	updateUIModel(t, model, message)
	if model.goal.pending != nil {
		t.Fatalf("stale accepted Set installed pending preview = %+v", model.goal.pending)
	}
	if model.goal.goal == nil || model.goal.goal.ID != authoritative.ID {
		t.Fatalf("stale accepted Set replaced authoritative Goal = %+v", model.goal.goal)
	}
	if !runtimeGoalsEqual(runtimeClient.MainView().Status.Goal, model.goal.goal) {
		t.Fatalf("TUI Goal = %+v, want canonical Goal %+v", model.goal.goal, runtimeClient.MainView().Status.Goal)
	}
}

func TestEmptyGoalSetResultCannotClearAuthoritativeGoal(t *testing.T) {
	committed := runtimeGoalFixture(
		"goal-committed",
		"committed objective",
		clientui.RuntimeGoalStatusPaused,
		false,
	)
	authoritative := runtimeGoalFixture(
		"goal-authoritative",
		"authoritative objective",
		clientui.RuntimeGoalStatusActive,
		false,
	)
	if err := committed.Goal.Validate(); err != nil {
		t.Fatalf("committed Goal fixture: %v", err)
	}
	if err := authoritative.Goal.Validate(); err != nil {
		t.Fatalf("authoritative Goal fixture: %v", err)
	}
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{
			Availability: clientui.GoalAvailabilityAvailable,
		},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	runtimeClient.storeMainView(clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{Goal: committed},
	})
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	rawMessage := model.goalRuntimeCommand(goalRuntimeSet, "queued objective")()
	message, ok := rawMessage.(goalRuntimeDoneMsg)
	if !ok {
		t.Fatalf("goal Set command message = %T, want goalRuntimeDoneMsg", rawMessage)
	}
	if message.goal != nil || message.pending != nil {
		t.Fatalf("empty Goal Set result = %+v, want no projection", message)
	}

	authoritativeMessage := clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
		Goal:         &clientui.TranscriptGoal{Goal: authoritative.Goal},
		Availability: clientui.GoalAvailabilityAvailable,
	}))
	admission, err := runtimeClient.admitTranscriptMessageState(authoritativeMessage)
	if err != nil {
		t.Fatalf("admit authoritative Goal status: %v", err)
	}
	if cmd := model.applyAdmittedTranscriptMessageState(authoritativeMessage, admission); cmd != nil {
		t.Fatalf("authoritative Goal status command = %v, want nil", cmd)
	}

	updateUIModel(t, model, message)
	if model.goal.goal == nil || model.goal.goal.ID != authoritative.ID {
		t.Fatalf("empty Set result cleared authoritative Goal = %+v", model.goal.goal)
	}
}
