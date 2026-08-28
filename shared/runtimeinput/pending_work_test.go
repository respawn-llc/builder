package runtimeinput

import (
	"testing"

	"core/shared/runtimeids"
)

func TestPendingWorkClosedContract(t *testing.T) {
	guidance := "keep details"
	selector := "feature/a"
	queue := pendingWorkTestMessage(PendingWorkLaneQueue, "queued")
	steer := pendingWorkTestMessage(PendingWorkLaneSteer, "steer")
	compaction := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer,
		Kind: PendingWorkItemKindManualCompaction, State: PendingWorkItemStatePending,
		CanonicalInput:   "/compact keep details",
		ManualCompaction: &PendingWorkManualCompaction{Guidance: &guidance},
	}
	enter := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer,
		Kind: PendingWorkItemKindWorktreeTransition, State: PendingWorkItemStatePending,
		CanonicalInput: "/wt switch feature/a",
		WorktreeTransition: &PendingWorkWorktreeTransition{
			Transition: PendingWorkWorktreeTransitionEnter,
			Selector:   &selector,
		},
	}
	leave := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer,
		Kind: PendingWorkItemKindWorktreeTransition, State: PendingWorkItemStatePending,
		CanonicalInput: "/wt leave",
		WorktreeTransition: &PendingWorkWorktreeTransition{
			Transition: PendingWorkWorktreeTransitionLeave,
		},
	}
	if err := (PendingWork{Items: []PendingWorkItem{queue, steer, compaction, enter, leave}}).Validate(); err != nil {
		t.Fatalf("valid Queue-first collection: %v", err)
	}

	twoPayloads := steer
	twoPayloads.ManualCompaction = compaction.ManualCompaction
	queueCompaction := compaction
	queueCompaction.Lane = PendingWorkLaneQueue
	badGuidance := compaction
	badGuidance.ManualCompaction = &PendingWorkManualCompaction{Guidance: textPointer("keep   details")}
	badCompactionCanonical := compaction
	badCompactionCanonical.CanonicalInput = "/compact   keep details"
	missingSelector := enter
	missingSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionEnter,
	}
	badSelector := enter
	badSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionEnter,
		Selector:   textPointer(" feature/a "),
	}
	leaveWithSelector := leave
	leaveWithSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionLeave,
		Selector:   &selector,
	}
	for name, collection := range map[string]PendingWork{
		"Steer before Queue":     {Items: []PendingWorkItem{steer, queue}},
		"duplicate identity":     {Items: []PendingWorkItem{queue, queue}},
		"multiple payloads":      {Items: []PendingWorkItem{twoPayloads}},
		"Queue compaction":       {Items: []PendingWorkItem{queueCompaction}},
		"unnormalized guidance":  {Items: []PendingWorkItem{badGuidance}},
		"wrong canonical input":  {Items: []PendingWorkItem{badCompactionCanonical}},
		"missing enter selector": {Items: []PendingWorkItem{missingSelector}},
		"unnormalized selector":  {Items: []PendingWorkItem{badSelector}},
		"leave with selector":    {Items: []PendingWorkItem{leaveWithSelector}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := collection.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}

	restoration, err := compaction.Restoration()
	if err != nil || restoration.CanonicalInput != compaction.CanonicalInput {
		t.Fatalf("restoration = %+v, %v", restoration, err)
	}
	technical, err := compaction.TechnicalRestoration()
	if err != nil || technical.ItemID != compaction.ID || technical.CanonicalInput != compaction.CanonicalInput {
		t.Fatalf("technical restoration = %+v, %v", technical, err)
	}
}

func TestNormalizePendingWorkArgument(t *testing.T) {
	if got := NormalizePendingWorkArgument(" \tfeature   with\nspaces "); got != "feature with spaces" {
		t.Fatalf("normalized Pending Work argument = %q", got)
	}
}

func pendingWorkTestMessage(lane PendingWorkLane, text string) PendingWorkItem {
	return PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: lane, Kind: PendingWorkItemKindMessage,
		State: PendingWorkItemStatePending, CanonicalInput: text,
		Message: &PendingWorkMessage{Text: text},
	}
}

func textPointer(value string) *string {
	return &value
}
