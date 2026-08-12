package app

import (
	"testing"
	"time"

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
	now := time.Now()
	goal := &clientui.Goal{ID: "goal-1", Objective: "accepted objective", Status: clientui.RuntimeGoalStatusActive, CreatedAt: now, UpdatedAt: now}
	tests := []struct {
		name      string
		operation goalRuntimeOperation
		mutation  clientui.GoalMutationResult
	}{
		{name: "authoritative Goal without availability", operation: goalRuntimeSet, mutation: clientui.GoalMutationResult{Goal: goal}},
		{name: "Clear acceptance waits for authoritative update", operation: goalRuntimeClear},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newProjectedTestUIModel(nil, WithUISessionID("session-1"))
			model.openGoalOverlay(&clientui.RuntimeGoal{Goal: goal}, nil)
			model.goalRuntimePending = goalRuntimePendingState{token: 1, inFlight: true, inFlightOperation: test.operation}

			updateUIModel(t, model, goalRuntimeDoneMsg{token: 1, sessionID: "session-1", operation: test.operation, mutation: test.mutation})

			if model.goal.goal == nil || *model.goal.goal != *goal {
				t.Fatalf("Goal presentation=%+v, want authoritative Goal preserved", model.goal.goal)
			}
		})
	}
}
