package session

import (
	"testing"

	"core/shared/toolspec"
)

func TestGoalAvailabilityResolvesCapabilityAndRejectsMalformed(t *testing.T) {
	store := newSessionTestStore(t)
	if got, err := store.GoalAvailability(); err != nil || got != GoalAvailable {
		t.Fatalf("unlocked availability=%q err=%v", got, err)
	}
	if got, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{string(toolspec.ToolAskQuestion)}}}); err != nil || got != GoalAvailable {
		t.Fatalf("ask_question availability=%q err=%v", got, err)
	}
	markSessionTestLocked(t, store, LockedContract{EnabledTools: []string{string(toolspec.ToolExecCommand)}})
	if got, err := mustOpenSessionTestStore(t, store).GoalAvailability(); err != nil || got != GoalAgentCapabilityMissing {
		t.Fatalf("reopened availability=%q err=%v", got, err)
	}
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{}}); err == nil {
		t.Fatal("missing locked tool snapshot returned availability")
	}
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{"unknown"}}}); err == nil {
		t.Fatal("invalid locked tool returned availability")
	}
}
