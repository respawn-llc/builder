package session

import (
	"testing"

	"core/shared/toolspec"
)

func TestGoalAvailabilityResolvesCapabilityAndRejectsMalformed(t *testing.T) {
	store := newSessionTestStore(t)
	assertGoalAvailability(t, store, GoalAvailable)
	if got, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{string(toolspec.ToolAskQuestion)}}}); err != nil || got != GoalAvailable {
		t.Fatalf("ask_question availability=%q err=%v", got, err)
	}
	markSessionTestLocked(t, store, LockedContract{EnabledTools: []string{string(toolspec.ToolExecCommand)}})
	assertGoalAvailability(t, store, GoalAgentCapabilityMissing)
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{}}); err == nil { t.Fatal("missing locked tool snapshot returned availability") }
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{"unknown"}}}); err == nil { t.Fatal("invalid locked tool returned availability") }
}
func assertGoalAvailability(t *testing.T, store *Store, want GoalAvailability) {
	got, err := store.GoalAvailability()
	if err != nil || got != want {
		t.Fatalf("availability=%q err=%v want=%q", got, err, want)
	}
}
