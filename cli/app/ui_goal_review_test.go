package app

import (
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func TestGoalSetCommandDeliversQueuedPreviewToTUI(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{
			Pending:      &clientui.GoalPreview{Objective: "queued objective", Status: clientui.RuntimeGoalStatusActive},
			Availability: textutil.Value(clientui.GoalAvailabilityAvailable),
		},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	message := model.goalRuntimeCommand(goalRuntimeSet, "queued objective")().(goalRuntimeDoneMsg)
	if message.mutation.Goal != nil ||
		message.mutation.Pending == nil ||
		message.mutation.Pending.Objective != "queued objective" ||
		message.mutation.Pending.Status != clientui.RuntimeGoalStatusActive {
		t.Fatalf("Goal command result = %+v, want identity-less queued preview", message.mutation)
	}
	updateUIModel(t, model, message)

	if model.goal.pending == nil ||
		model.goal.pending.Objective != message.mutation.Pending.Objective ||
		model.goal.pending.Status != message.mutation.Pending.Status ||
		model.goal.goal != nil {
		t.Fatalf("TUI queued Goal = pending %+v goal %+v, want typed preview only", model.goal.pending, model.goal.goal)
	}
}

func TestGoalMutationWithoutAvailabilityPreservesAcceptedGoalCore(t *testing.T) {
	now := time.Now()
	goal := &clientui.Goal{
		ID:        "goal-1",
		Objective: "accepted objective",
		Status:    clientui.RuntimeGoalStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	model := newProjectedTestUIModel(nil, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)
	model.goalRuntimePending = goalRuntimePendingState{token: 1, inFlight: true, inFlightOperation: goalRuntimeSet}

	updateUIModel(t, model, goalRuntimeDoneMsg{
		token:     1,
		sessionID: "session-1",
		operation: goalRuntimeSet,
		mutation:  clientui.GoalMutationResult{Goal: goal},
	})

	if model.goal.goal == nil || *model.goal.goal != *goal {
		t.Fatalf("TUI Goal = %+v, want accepted Goal core %+v", model.goal.goal, goal)
	}
}

func TestGoalClearAcceptanceWaitsForAuthoritativeUpdate(t *testing.T) {
	now := time.Now()
	goal := &clientui.Goal{ID: "goal-1", Objective: "current objective", Status: clientui.RuntimeGoalStatusActive, CreatedAt: now, UpdatedAt: now}
	model := newProjectedTestUIModel(nil, WithUISessionID("session-1"))
	model.openGoalOverlay(&clientui.RuntimeGoal{Goal: goal}, nil)
	model.goalRuntimePending = goalRuntimePendingState{token: 1, inFlight: true, inFlightOperation: goalRuntimeClear}

	updateUIModel(t, model, goalRuntimeDoneMsg{
		token:     1,
		sessionID: "session-1",
		operation: goalRuntimeClear,
		mutation:  clientui.GoalMutationResult{},
	})

	if model.goal.goal == nil || *model.goal.goal != *goal {
		t.Fatalf("TUI Goal = %+v, want current authoritative Goal preserved", model.goal.goal)
	}
}
