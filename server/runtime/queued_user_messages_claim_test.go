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
