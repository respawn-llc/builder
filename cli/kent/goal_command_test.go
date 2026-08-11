package main

import (
	"bytes"
	"testing"

	"core/shared/serverapi"
)

func TestQueuedGoalStatusOutputUsesOperationContext(t *testing.T) {
	var pauseOutput, completeOutput, clearOutput bytes.Buffer
	response := serverapi.RuntimeGoalMutationResponse{}

	writeGoalStatusMutationText(&pauseOutput, "pause", response)
	writeGoalStatusMutationText(&completeOutput, "complete", response)
	writeGoalMutationText(&clearOutput, response)

	if pauseOutput.Len() == 0 || completeOutput.Len() == 0 {
		t.Fatal("queued Goal status output is empty")
	}
	if pauseOutput.String() == completeOutput.String() {
		t.Fatal("queued Goal status output does not reflect its operation")
	}
	if pauseOutput.String() == clearOutput.String() || completeOutput.String() == clearOutput.String() {
		t.Fatal("queued Goal status output uses the Clear/no-Goal presentation")
	}
}
