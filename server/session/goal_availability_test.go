package session

import (
	"testing"

	"core/shared/clientui"
	"core/shared/toolspec"
)

func TestGoalAvailabilityLifecycleAndCapability(t *testing.T) {
	store := newSessionTestStore(t)
	assertGoalAvailability(t, store, clientui.GoalAvailabilityAvailable)
	markSessionTestLocked(t, store, LockedContract{EnabledTools: []string{string(toolspec.ToolExecCommand)}})
	assertGoalAvailability(t, store, clientui.GoalAvailabilityAgentCapabilityMissing)
	if err := store.ResetLockedContractForCompactionBoundary(); err != nil {
		t.Fatal(err)
	}
	assertGoalAvailability(t, mustOpenSessionTestStore(t, store), clientui.GoalAvailabilityAvailable)
}
func assertGoalAvailability(t *testing.T, store *Store, want clientui.GoalAvailability) {
	got, err := store.GoalAvailability()
	if err != nil || got != want {
		t.Fatalf("availability = %q err=%v, want %q", got, err, want)
	}
}
