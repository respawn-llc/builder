package app

import (
	"reflect"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestGoalSetCommandDeliversQueuedPreviewToTUIWithoutChangingCanonicalGoal(t *testing.T) {
	committed := &clientui.RuntimeGoal{
		Goal: &clientui.Goal{
			ID:        "goal-committed",
			Objective: "committed objective",
			Status:    clientui.RuntimeGoalStatusPaused,
		},
		Availability: clientui.GoalAvailabilityAvailable,
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
