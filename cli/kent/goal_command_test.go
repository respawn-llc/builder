package main

import (
	"bytes"
	"core/shared/clientui"
	"core/shared/serverapi"
	"testing"
)

func TestWriteGoalMutationTextRendersPendingPreview(t *testing.T) {
	var output bytes.Buffer
	writeGoalMutationText(&output, serverapi.RuntimeGoalMutationResponse{Pending: &clientui.GoalPreview{Objective: "ship the queued goal", Status: clientui.RuntimeGoalStatusActive}})
	if got := output.String(); got != "Goal: ship the queued goal\nStatus: active\n" {
		t.Fatal(got)
	}
}
