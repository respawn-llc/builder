package app

import (
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestGoalSetCommandDeliversQueuedPreviewToTUI(t *testing.T) {
	preview := &clientui.GoalPreview{Objective: "queued objective", Status: clientui.RuntimeGoalStatusActive}
	model := newProjectedTestUIModel(newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{Pending: preview},
	}), WithUISessionID("session-1"))
	message := model.goalRuntimeCommand(goalRuntimeSet, preview.Objective)().(goalRuntimeDoneMsg)
	updateUIModel(t, model, message)
	if !model.goal.open || model.goal.goal != nil || model.goal.pending == nil || *model.goal.pending != *preview {
		t.Fatalf("queued Goal result=%+v presentation=%+v, want identity-less preview", message.mutation, model.goal)
	}
	goal := runtimeClientTestGoal("goal-1", "accepted objective", clientui.RuntimeGoalStatusActive)
	model.openGoalOverlay(&clientui.RuntimeGoal{Goal: goal}, nil)
	model.goalRuntimePending = goalRuntimePendingState{token: 1, inFlight: true, inFlightOperation: goalRuntimeClear}
	updateUIModel(t, model, goalRuntimeDoneMsg{token: 1, sessionID: "session-1", operation: goalRuntimeClear})
	if model.goal.goal == nil || *model.goal.goal != *goal {
		t.Fatalf("Goal presentation=%+v, want authoritative Goal preserved", model.goal.goal)
	}
}
