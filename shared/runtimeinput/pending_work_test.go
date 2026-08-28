package runtimeinput

import (
	"testing"

	"core/shared/runtimeids"
)

func TestNormalizePendingWorkArgument(t *testing.T) {
	t.Parallel()

	if got := NormalizePendingWorkArgument(" \tfeature   with\nspaces "); got != "feature with spaces" {
		t.Fatalf("normalized Pending Work argument = %q", got)
	}
}

func TestPendingWorkClosedContract(t *testing.T) {
	t.Parallel()

	guidance := "keep implementation details"
	selector := "feature/a"
	queueMessage := pendingWorkTestMessage(PendingWorkLaneQueue, "run tests")
	steerMessage := pendingWorkTestMessage(PendingWorkLaneSteer, "keep going")
	compaction := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindManualCompaction,
		State: PendingWorkItemStatePending, CanonicalInput: "/compact keep implementation details",
		ManualCompaction: &PendingWorkManualCompaction{Guidance: &guidance},
	}
	enter := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindWorktreeTransition,
		State: PendingWorkItemStatePending, CanonicalInput: "/wt switch feature/a",
		WorktreeTransition: &PendingWorkWorktreeTransition{
			Transition: PendingWorkWorktreeTransitionEnter,
			Selector:   &selector,
		},
	}
	leave := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindWorktreeTransition,
		State: PendingWorkItemStatePending, CanonicalInput: "/wt leave",
		WorktreeTransition: &PendingWorkWorktreeTransition{
			Transition: PendingWorkWorktreeTransitionLeave,
		},
	}

	if err := (PendingWork{Items: []PendingWorkItem{queueMessage, steerMessage, compaction, enter, leave}}).Validate(); err != nil {
		t.Fatalf("valid Queue-first collection: %v", err)
	}

	blank := " "
	unnormalizedGuidance := "keep   implementation details"
	unnormalizedSelector := " feature/a "
	wrongCompactionInput := compaction
	wrongCompactionInput.CanonicalInput = "/compact   keep implementation details"
	wrongEnterInput := enter
	wrongEnterInput.CanonicalInput = "/worktree switch feature/a"
	twoPayloads := steerMessage
	twoPayloads.ManualCompaction = compaction.ManualCompaction
	queueCompaction := compaction
	queueCompaction.Lane = PendingWorkLaneQueue
	blankGuidance := compaction
	blankGuidance.ManualCompaction = &PendingWorkManualCompaction{Guidance: &blank}
	nonNormalizedGuidance := compaction
	nonNormalizedGuidance.ManualCompaction = &PendingWorkManualCompaction{Guidance: &unnormalizedGuidance}
	enterWithoutSelector := enter
	enterWithoutSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionEnter,
	}
	enterWithUnnormalizedSelector := enter
	enterWithUnnormalizedSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionEnter,
		Selector:   &unnormalizedSelector,
	}
	leaveWithSelector := leave
	leaveWithSelector.WorktreeTransition = &PendingWorkWorktreeTransition{
		Transition: PendingWorkWorktreeTransitionLeave,
		Selector:   &selector,
	}

	for name, collection := range map[string]PendingWork{
		"Steer before Queue":            {Items: []PendingWorkItem{steerMessage, queueMessage}},
		"duplicate id":                  {Items: []PendingWorkItem{queueMessage, queueMessage}},
		"two payloads":                  {Items: []PendingWorkItem{twoPayloads}},
		"Queue compaction":              {Items: []PendingWorkItem{queueCompaction}},
		"blank guidance":                {Items: []PendingWorkItem{blankGuidance}},
		"non-normalized guidance":       {Items: []PendingWorkItem{nonNormalizedGuidance}},
		"wrong compaction canonical":    {Items: []PendingWorkItem{wrongCompactionInput}},
		"enter without selector":        {Items: []PendingWorkItem{enterWithoutSelector}},
		"non-normalized enter selector": {Items: []PendingWorkItem{enterWithUnnormalizedSelector}},
		"wrong enter canonical":         {Items: []PendingWorkItem{wrongEnterInput}},
		"leave with selector":           {Items: []PendingWorkItem{leaveWithSelector}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := collection.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}

	overCapacity := PendingWork{Items: make([]PendingWorkItem, PendingWorkCapacity+1)}
	for index := range overCapacity.Items {
		overCapacity.Items[index] = pendingWorkTestMessage(PendingWorkLaneQueue, "queued")
	}
	if err := overCapacity.Validate(); err != nil {
		t.Fatalf("approved over-capacity collection: %v", err)
	}
}

func TestPendingWorkRestorationUsesServerCanonicalInput(t *testing.T) {
	t.Parallel()

	guidance := "keep details"
	item := PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: PendingWorkLaneSteer, Kind: PendingWorkItemKindManualCompaction,
		State: PendingWorkItemStatePending, CanonicalInput: "/compact keep details",
		ManualCompaction: &PendingWorkManualCompaction{Guidance: &guidance},
	}
	restoration, err := item.Restoration()
	if err != nil {
		t.Fatalf("Restoration: %v", err)
	}
	if restoration.Kind != PendingWorkItemKindManualCompaction ||
		restoration.CanonicalInput != item.CanonicalInput {
		t.Fatalf("restoration = %#v", restoration)
	}
	if err := restoration.Validate(); err != nil {
		t.Fatalf("Validate restoration: %v", err)
	}
	technical, err := item.TechnicalRestoration()
	if err != nil {
		t.Fatalf("TechnicalRestoration: %v", err)
	}
	if technical.ItemID != item.ID ||
		technical.Kind != item.Kind ||
		technical.CanonicalInput != item.CanonicalInput {
		t.Fatalf("technical restoration = %#v", technical)
	}
	if err := technical.Validate(); err != nil {
		t.Fatalf("Validate technical restoration: %v", err)
	}
	technical.ItemID = runtimeids.QueueItemID{}
	if err := technical.Validate(); err == nil {
		t.Fatal("technical restoration accepted an absent item id")
	}
}

func pendingWorkTestMessage(lane PendingWorkLane, text string) PendingWorkItem {
	return PendingWorkItem{
		ID: runtimeids.NewQueueItemID(), Lane: lane, Kind: PendingWorkItemKindMessage,
		State: PendingWorkItemStatePending, CanonicalInput: text,
		Message: &PendingWorkMessage{Text: text},
	}
}
