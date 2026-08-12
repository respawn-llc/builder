package app

import (
	"core/shared/clientui"
	"core/shared/serverapi"
	"testing"
)

func TestGoalSetCommandDeliversQueuedPreviewToTUI(t *testing.T) {
	preview := &clientui.GoalPreview{Objective: "queued objective", Status: clientui.RuntimeGoalStatusActive}
	model := newProjectedTestUIModel(newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{
		setGoalResp: serverapi.RuntimeGoalMutationResponse{Pending: preview},
	}), WithUISessionID("session-1"))
	message := model.goalRuntimeCommand(goalRuntimeSet, preview.Objective)().(goalRuntimeDoneMsg)
	goal := runtimeClientTestGoal("goal-1", "accepted objective", clientui.RuntimeGoalStatusActive)
	model.goal.open = true
	model.applyAdmittedTranscriptMessageState(ongoingTranscriptMessage(1, clientui.TranscriptMessageGoalStatus), runtimeTupleMergeResult{view: clientui.RuntimeMainView{Status: clientui.RuntimeStatus{Goal: &clientui.RuntimeGoal{Goal: goal}}}})
	updateUIModel(t, model, message)
	if model.goal.pending != nil || *model.goal.goal != *goal {
		t.Fatal("authoritative Goal was not preserved")
	}
}
