package metadata

import "testing"

func TestSessionIDSetsEqualIgnoresOrderAndRejectsDuplicates(t *testing.T) {
	if !SessionIDSetsEqual([]string{"session-a", "session-b"}, []string{"session-b", "session-a"}) {
		t.Fatal("equal Session ID sets with different order compared unequal")
	}
	if SessionIDSetsEqual([]string{"session-a", "session-a"}, []string{"session-a", "session-a"}) {
		t.Fatal("duplicate Session IDs compared as a valid set")
	}
	if SessionIDSetsEqual([]string{"session-a", "session-b"}, []string{"session-a", "session-a"}) {
		t.Fatal("one-sided duplicate Session IDs compared as a valid set")
	}
	if SessionIDSetsEqual([]string{"session-a"}, []string{"session-b"}) {
		t.Fatal("different Session ID sets compared equal")
	}
}
