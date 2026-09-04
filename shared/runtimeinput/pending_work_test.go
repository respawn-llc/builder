package runtimeinput

import (
	"testing"

	"core/shared/runtimeids"
)

func TestCollectionContract(t *testing.T) {
	t.Parallel()
	guidance := "keep implementation details"
	selector, compactionID := "feature/a", runtimeids.NewCompactionRequestID()
	compactionView, err := runtimeids.ParseQueueItemID(compactionID.String())
	if err != nil {
		t.Fatalf("compaction ID view: %v", err)
	}
	steerMessage := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindMessage,
		State: PendingWorkItemStatePending, CanonicalInput: "keep going", Message: &PendingWorkMessage{Text: "keep going"},
	}
	steerCompaction := PendingWorkItem{
		ID: compactionView, Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindManualCompaction,
		State: PendingWorkItemStatePending, CanonicalInput: "/compact keep implementation details",
		ManualCompaction: &PendingWorkManualCompaction{Guidance: &guidance},
	}
	queueMessage := steerMessage
	queueMessage.ID = runtimeids.NewQueueItemID()
	queueMessage.Lane = PendingWorkLaneQueue
	enter := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindWorktreeTransition,
		State: PendingWorkItemStatePending, CanonicalInput: "/wt switch feature/a",
		WorktreeTransition: &PendingWorkWorktreeTransition{Transition: PendingWorkWorktreeTransitionEnter, Selector: &selector},
	}
	leave := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindWorktreeTransition,
		State: PendingWorkItemStatePending, CanonicalInput: "/wt leave",
		WorktreeTransition: &PendingWorkWorktreeTransition{Transition: PendingWorkWorktreeTransitionLeave},
	}
	if err := (PendingWork{Items: []PendingWorkItem{queueMessage, steerMessage, steerCompaction, enter, leave}}).Validate(); err != nil {
		t.Fatalf("valid Queue-first collection: %v", err)
	}

	twoPayloads := steerMessage
	twoPayloads.ManualCompaction = steerCompaction.ManualCompaction
	badGuidance := steerCompaction
	badGuidance.ManualCompaction = &PendingWorkManualCompaction{Guidance: stringPointer("keep   implementation details")}
	missingSelector := enter
	missingSelector.WorktreeTransition = &PendingWorkWorktreeTransition{Transition: PendingWorkWorktreeTransitionEnter}
	badSelector := enter
	badSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionEnter, Selector: stringPointer(" feature/a "),
	}
	leaveWithSelector := leave
	leaveWithSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionLeave, Selector: &selector,
	}
	for name, collection := range map[string]PendingWork{
		"duplicate id":        {Items: []PendingWorkItem{steerMessage, steerMessage}},
		"Steer before Queue":  {Items: []PendingWorkItem{steerMessage, queueMessage}},
		"two payloads":        {Items: []PendingWorkItem{twoPayloads}},
		"bad guidance":        {Items: []PendingWorkItem{badGuidance}},
		"missing enter value": {Items: []PendingWorkItem{missingSelector}},
		"bad selector":        {Items: []PendingWorkItem{badSelector}},
		"leave selector":      {Items: []PendingWorkItem{leaveWithSelector}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := collection.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
	restoration, err := steerCompaction.Restoration()
	technical, technicalErr := steerCompaction.TechnicalRestoration()
	if err != nil || technicalErr != nil ||
		restoration.Kind != steerCompaction.Kind || restoration.CanonicalInput != steerCompaction.CanonicalInput ||
		technical.ItemID != compactionView || technical.Kind != steerCompaction.Kind ||
		technical.CanonicalInput != steerCompaction.CanonicalInput ||
		compactionView.String() != compactionID.String() {
		t.Fatalf("restoration = %+v; technical = %+v; errors = %v, %v", restoration, technical, err, technicalErr)
	}
	if _, err := runtimeids.ParseCanonicalUUIDv4(technical.ItemID.String(), "Pending Work item ID"); err != nil {
		t.Fatalf("Pending Work identity: %v", err)
	}
}

func stringPointer(value string) *string { return &value }
