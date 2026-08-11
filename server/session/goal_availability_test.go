package session

import (
	"testing"

	"core/shared/clientui"
	"core/shared/toolspec"
)

func TestGoalAvailabilityResolvesCapabilityAndRejectsMalformed(t *testing.T) {
	store := newSessionTestStore(t)
	assertGoalAvailability(t, store, clientui.GoalAvailabilityAvailable)
	if got, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{string(toolspec.ToolAskQuestion)}}}); err != nil || got != clientui.GoalAvailabilityAvailable {
		t.Fatalf("ask_question availability=%q err=%v", got, err)
	}
	markSessionTestLocked(t, store, LockedContract{EnabledTools: []string{string(toolspec.ToolExecCommand)}})
	assertGoalAvailability(t, store, clientui.GoalAvailabilityAgentCapabilityMissing)
	if err := store.ResetLockedContractForCompactionBoundary(); err != nil {
		t.Fatal(err)
	}
	assertGoalAvailability(t, mustOpenSessionTestStore(t, store), clientui.GoalAvailabilityAvailable)
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{}}); err == nil {
		t.Fatal("missing locked tool snapshot returned availability")
	}
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{"unknown"}}}); err == nil {
		t.Fatal("invalid locked tool returned availability")
	}
	malformed := newSessionTestStore(t)
	malformed.mu.Lock()
	malformed.meta.Locked = &LockedContract{}
	malformed.mu.Unlock()
	if availability := malformed.GoalMutationAvailability(); availability != nil {
		t.Fatalf("malformed locked contract mutation availability = %q, want omitted", *availability)
	}
	goal, receipt, err := malformed.SetGoal("mutation remains accepted", GoalActorUser)
	if err != nil || !receipt.Committed || goal.Objective != "mutation remains accepted" {
		t.Fatalf("Goal mutation after omitted availability = goal %+v receipt %+v err %v", goal, receipt, err)
	}
}
func assertGoalAvailability(t *testing.T, store *Store, want clientui.GoalAvailability) {
	got, err := store.GoalAvailability()
	if err != nil || got != want {
		t.Fatalf("availability=%q err=%v want=%q", got, err, want)
	}
}
