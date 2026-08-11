package main

import (
	"bytes"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestWriteGoalMutationTextRendersPendingPreview(t *testing.T) {
	var output bytes.Buffer
	writeGoalMutationText(&output, serverapi.RuntimeGoalMutationResponse{
		Pending:      &clientui.GoalPreview{Objective: "ship the queued goal", Status: clientui.RuntimeGoalStatusActive},
		Availability: clientui.GoalAvailabilityAvailable,
	})
	if got, want := output.String(), "Goal: ship the queued goal\nStatus: active\n"; got != want {
		t.Fatalf("pending CLI Goal output = %q, want %q", got, want)
	}
}
