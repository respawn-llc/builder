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
	model.openGoalOverlay(nil, nil)

	message := model.goalRuntimeCommand(goalRuntimeSet, preview.Objective)().(goalRuntimeDoneMsg)
	updateUIModel(t, model, message)

	if message.mutation.Goal != nil || model.goal.goal != nil || model.goal.pending == nil || *model.goal.pending != *preview {
		t.Fatalf("queued Goal result=%+v presentation=%+v, want identity-less preview", message.mutation, model.goal)
	}
}

func TestGoalMutationFeedbackPreservesApprovedStateBoundaries(t *testing.T) {
	goal := runtimeClientTestGoal("goal-1", "accepted objective", clientui.RuntimeGoalStatusActive)
	model := newProjectedTestUIModel(nil, WithUISessionID("session-1"))
	model.openGoalOverlay(&clientui.RuntimeGoal{Goal: goal}, nil)
	model.goalRuntimePending = goalRuntimePendingState{token: 1, inFlight: true, inFlightOperation: goalRuntimeClear}
	updateUIModel(t, model, goalRuntimeDoneMsg{token: 1, sessionID: "session-1", operation: goalRuntimeClear})
	if model.goal.goal == nil || *model.goal.goal != *goal {
		t.Fatalf("Goal presentation=%+v, want authoritative Goal preserved", model.goal.goal)
	}
}
