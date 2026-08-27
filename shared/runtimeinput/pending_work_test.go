package runtimeinput

import (
	"testing"

	"core/shared/runtimeids"
)

func TestCollectionContract(t *testing.T) {
	t.Parallel()
	guidance := "keep implementation details"
	steerMessage := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindMessage,
		State: PendingWorkItemStatePending, Message: &PendingWorkMessage{Text: "keep going"},
	}
	steerCompaction := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindManualCompaction,
		State: PendingWorkItemStatePending, ManualCompaction: &PendingWorkManualCompaction{
			Guidance: &guidance, RestorationInput: "/compact   keep implementation details"},
	}
	queueMessage := steerMessage
	queueMessage.ID = runtimeids.NewQueueItemID()
	queueMessage.Lane = PendingWorkLaneQueue
	noGuidance := steerCompaction
	noGuidance.ID = runtimeids.NewQueueItemID()
	noGuidance.ManualCompaction = &PendingWorkManualCompaction{RestorationInput: "/compact"}
	if err := (PendingWork{Items: []PendingWorkItem{steerMessage, steerCompaction, noGuidance, queueMessage}}).Validate(); err != nil {
		t.Fatalf("valid collection: %v", err)
	}
	queueCompaction := steerCompaction
	queueCompaction.Lane = PendingWorkLaneQueue
	twoPayloads := steerMessage
	twoPayloads.ManualCompaction = steerCompaction.ManualCompaction
	blank := " "
	blankGuidance := steerCompaction
	blankGuidance.ManualCompaction = &PendingWorkManualCompaction{Guidance: &blank, RestorationInput: "/compact"}
	missingRestoration := steerCompaction
	missingRestoration.ManualCompaction = &PendingWorkManualCompaction{}
	for name, collection := range map[string]PendingWork{
		"duplicate id":        {Items: []PendingWorkItem{steerMessage, steerMessage}},
		"Queue before Steer":  {Items: []PendingWorkItem{queueMessage, steerMessage}},
		"Queue compaction":    {Items: []PendingWorkItem{queueCompaction}},
		"two payloads":        {Items: []PendingWorkItem{twoPayloads}},
		"blank guidance":      {Items: []PendingWorkItem{blankGuidance}},
		"missing restoration": {Items: []PendingWorkItem{missingRestoration}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := collection.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
	overCapacity := PendingWork{Items: make([]PendingWorkItem, PendingWorkCapacity+1)}
	for index := range overCapacity.Items {
		overCapacity.Items[index] = steerMessage
		overCapacity.Items[index].ID = runtimeids.NewQueueItemID()
		overCapacity.Items[index].Lane = PendingWorkLaneQueue
	}
	if err := overCapacity.Validate(); err != nil {
		t.Fatalf("approved over-capacity collection: %v", err)
	}
}
