package app

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestPendingWorkPresentationPutsQueueAboveSteerWithoutReorderingLanes(t *testing.T) {
	inputs := pendingWorkInputs(runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "steer 1"),
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "steer 2"),
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "queue 1"),
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "queue 2"),
	}})
	want := []string{"queue 1", "queue 2", "steer 1", "steer 2"}
	for index := range want {
		if inputs[index].Text != want[index] {
			t.Fatalf("item %d = %q, want %q", index, inputs[index].Text, want[index])
		}
	}
}

func pendingWorkMessageForTest(lane runtimeinput.PendingWorkLane, text string) runtimeinput.PendingWorkItem {
	return runtimeinput.PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: lane, Kind: runtimeinput.PendingWorkItemKindMessage,
		State: runtimeinput.PendingWorkItemStatePending, Message: &runtimeinput.PendingWorkMessage{Text: text},
	}
}
