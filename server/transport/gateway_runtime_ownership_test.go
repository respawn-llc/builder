package transport

import (
	"testing"

	"core/shared/serverapi"
)

func TestConnectionStateRuntimeOwnershipRemovesOnlyMatchingAttachment(t *testing.T) {
	state := &connectionState{}
	first := serverapi.SessionRuntimeAttachment{SessionID: "session-1", Generation: 1}
	second := serverapi.SessionRuntimeAttachment{SessionID: "session-1", Generation: 2}
	state.recordOwnedRuntime(first)
	state.recordOwnedRuntime(second)
	state.removeOwnedRuntime(first)
	if owned := state.takeOwnedRuntimes(); len(owned) != 1 || owned[0] != second {
		t.Fatalf("mismatched release removed ownership: %+v", owned)
	}

	state.recordOwnedRuntime(second)
	state.removeOwnedRuntime(second)
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
