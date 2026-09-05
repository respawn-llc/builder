package app

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestPendingWorkPresentationConsumesServerOrderAndCanonicalInput(t *testing.T) {
	inputs := pendingWorkInputs(runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "queue 1"),
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "steer 1"),
		pendingWorkCompactionForTest(),
		pendingWorkWorktreeForTest(runtimeinput.PendingWorkWorktreeTransitionEnter, "/wt switch feature", "feature"),
		pendingWorkWorktreeForTest(runtimeinput.PendingWorkWorktreeTransitionLeave, "/wt leave", ""),
	}})
	want := []string{"queue 1", "steer 1", "/compact", "/wt switch feature", "/wt leave"}
	for index := range want {
		if inputs[index].Text != want[index] {
			t.Fatalf("item %d = %q, want %q", index, inputs[index].Text, want[index])
		}
	}
}

func pendingWorkMessageForTest(lane runtimeinput.PendingWorkLane, text string) runtimeinput.PendingWorkItem {
	return runtimeinput.PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: lane, Kind: runtimeinput.PendingWorkItemKindMessage,
		State: runtimeinput.PendingWorkItemStatePending, CanonicalInput: text,
		Message: &runtimeinput.PendingWorkMessage{Text: text},
	}
}

func pendingWorkCompactionForTest() runtimeinput.PendingWorkItem {
	return runtimeinput.PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: runtimeinput.PendingWorkLaneSteer,
		Kind: runtimeinput.PendingWorkItemKindManualCompaction, State: runtimeinput.PendingWorkItemStatePending,
		CanonicalInput: "/compact", ManualCompaction: &runtimeinput.PendingWorkManualCompaction{},
	}
}

func pendingWorkWorktreeForTest(transition runtimeinput.PendingWorkWorktreeTransitionKind, canonical, selector string) runtimeinput.PendingWorkItem {
	payload := &runtimeinput.PendingWorkWorktreeTransition{Transition: transition}
	if selector != "" {
		payload.Selector = &selector
	}
	return runtimeinput.PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: runtimeinput.PendingWorkLaneSteer,
		Kind: runtimeinput.PendingWorkItemKindWorktreeTransition, State: runtimeinput.PendingWorkItemStatePending,
		CanonicalInput: canonical, WorktreeTransition: payload,
	}
}
