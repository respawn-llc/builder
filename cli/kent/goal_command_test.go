package main

import (
	"bytes"
	"strings"
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
	if got := output.String(); !strings.Contains(got, "ship the queued goal") || !strings.Contains(got, "active") || strings.Contains(got, "No goal") {
		t.Fatalf("pending CLI Goal output = %q", got)
	}
}
