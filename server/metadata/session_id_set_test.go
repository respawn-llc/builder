package metadata

import "testing"

func TestStringSetsEqualIgnoresOrderAndRejectsDuplicates(t *testing.T) {
	if !StringSetsEqual([]string{"session-a", "session-b"}, []string{"session-b", "session-a"}) {
		t.Fatal("equal string sets with different order compared unequal")
	}
	if StringSetsEqual([]string{"session-a", "session-a"}, []string{"session-a", "session-a"}) {
		t.Fatal("duplicate strings compared as a valid set")
	}
	if StringSetsEqual([]string{"session-a", "session-b"}, []string{"session-a", "session-a"}) {
		t.Fatal("one-sided duplicate strings compared as a valid set")
	}
	if StringSetsEqual([]string{"session-a"}, []string{"session-b"}) {
		t.Fatal("different string sets compared equal")
	}
}
