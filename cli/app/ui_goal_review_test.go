package app

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestGoalSetCommandDeliversQueuedPreviewToTUI(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{
			Pending:      &clientui.GoalPreview{Objective: "queued objective", Status: clientui.RuntimeGoalStatusActive},
			Availability: clientui.GoalAvailabilityAvailable,
		},
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	model := newProjectedTestUIModel(runtimeClient, WithUISessionID("session-1"))
	model.openGoalOverlay(nil, nil)

	message := model.goalRuntimeCommand(goalRuntimeSet, "queued objective")().(goalRuntimeDoneMsg)
	updateUIModel(t, model, message)

	content := strings.Join(model.layout().goalOverlayContentLines(80), "\n")
	if model.goal.pending == nil || model.goal.goal != nil || !strings.Contains(content, "queued objective") || strings.Contains(content, "ID:") {
		t.Fatalf("TUI queued Goal = pending %+v goal %+v content %q", model.goal.pending, model.goal.goal, content)
	}
}
