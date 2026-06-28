package transport

import "testing"

func TestConnectionStateRuntimeOwnershipRemovesOnlyMatchingSession(t *testing.T) {
	state := &connectionState{}
	state.recordOwnedRuntime("session-1")
	state.removeOwnedRuntime("session-other")
	if owned := state.takeOwnedRuntimes(); len(owned) != 1 || owned[0] != "session-1" {
		t.Fatalf("mismatched release removed ownership: %+v", owned)
	}

	state.recordOwnedRuntime("session-1")
	state.removeOwnedRuntime("session-1")
	if owned := state.takeOwnedRuntimes(); len(owned) != 0 {
		t.Fatalf("matching explicit release left owned runtimes: %+v", owned)
	}
}

func TestConnectionStateRuntimeOwnershipIgnoresCloseBeforeActivationResponse(t *testing.T) {
	state := &connectionState{}
	if owned := state.takeOwnedRuntimes(); len(owned) != 0 {
		t.Fatalf("empty connection state owned runtimes = %+v, want none", owned)
	}
}
