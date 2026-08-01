package runtime

import "testing"

func TestQueuedUserMessageClaimRestoresOnNonCommitAndRemovesOnlyCommittedItems(t *testing.T) {
	store := newQueuedUserMessageStore()
	first := store.Queue("first")
	second := store.Queue("second")

	claim := store.ClaimAll()
	if got := len(claim.Items()); got != 2 {
		t.Fatalf("claimed items = %d, want 2", got)
	}
	claim.Restore()
	if got := store.Snapshot(); len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("restored queue = %#v, want original FIFO", got)
	}

	claim = store.ClaimAll()
	claimItems := claim.Items()
	claim.Commit([]string{claimItems[1].message.ID})
	if got := store.Snapshot(); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("partially committed queue = %#v, want first item", got)
	}
}

func TestQueuedUserMessageDrainLeavesClaimedItemsForTheirClaim(t *testing.T) {
	store := newQueuedUserMessageStore()
	first := store.Queue("first")
	second := store.Queue("second")
	claim := store.ClaimByID(map[string]struct{}{first.ID: {}})

	if drained := store.Drain(); len(drained) != 1 || drained[0].message.ID != second.ID {
		t.Fatalf("drained items = %#v, want only unclaimed second item", drained)
	}
	if got := claim.Items(); len(got) != 1 || got[0].message.ID != first.ID {
		t.Fatalf("claimed items = %#v, want first item retained", got)
	}
	claim.Restore()
	if got := store.Snapshot(); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("restored queue = %#v, want first item", got)
	}
}
