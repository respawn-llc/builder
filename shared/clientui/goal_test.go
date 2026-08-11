package clientui

import "testing"

func TestGoalMutationResultRepresentsQueuedSetWithoutAuthoritativeIdentity(t *testing.T) {
	result := GoalMutationResult{
		Pending:      &GoalPreview{Objective: "queued objective", Status: RuntimeGoalStatusActive},
		Availability: GoalAvailabilityAvailable,
	}

	if err := result.Validate(); err != nil {
		t.Fatalf("validate queued Goal mutation result: %v", err)
	}
	if result.Goal != nil || result.Pending == nil {
		t.Fatalf("queued Goal mutation result = %+v, want pending preview without authoritative Goal", result)
	}
}
