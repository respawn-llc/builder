package session

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/toolspec"
)

func TestGoalAvailabilityLifecycleAndCapability(t *testing.T) {
	store := newSessionTestStore(t)
	assertGoalAvailability(t, store, clientui.GoalAvailabilityAvailable)
	if got, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{string(toolspec.ToolAskQuestion)}}}); err != nil || got != clientui.GoalAvailabilityAvailable { t.Fatalf("locked ask_question availability = %q err=%v", got, err) }
	markSessionTestLocked(t, store, LockedContract{EnabledTools: []string{string(toolspec.ToolExecCommand)}})
	assertGoalAvailability(t, store, clientui.GoalAvailabilityAgentCapabilityMissing)
	if err := store.ResetLockedContractForCompactionBoundary(); err != nil {
		t.Fatal(err)
	}
	assertGoalAvailability(t, mustOpenSessionTestStore(t, store), clientui.GoalAvailabilityAvailable)
}

func TestGoalAvailabilityRejectsMalformedLockedSnapshot(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	if _, err := GoalAvailabilityFromMeta(Meta{Locked: &LockedContract{}}); err == nil { t.Fatal("missing locked tool snapshot returned availability") }
	_, err := GoalAvailabilityFromMeta(Meta{SessionID: "session-1", PromptCacheLineageGeneration: 7, Locked: &LockedContract{HasEnabledTools: true, EnabledTools: []string{string(toolspec.ToolAskQuestion), "unknown"}}})
	if err == nil || !strings.Contains(err.Error(), "generation 7") { t.Fatalf("malformed availability error = %v", err) }
}
func assertGoalAvailability(t *testing.T, store *Store, want clientui.GoalAvailability) {
	got, err := store.GoalAvailability()
	if err != nil || got != want {
		t.Fatalf("availability = %q err=%v, want %q", got, err, want)
	}
}
