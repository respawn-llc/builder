package clientui

import (
	"testing"

	"core/shared/textutil"
)

func TestGoalMutationResultRepresentsQueuedSetWithoutAuthoritativeIdentity(t *testing.T) {
	result := GoalMutationResult{
		Pending:      &GoalPreview{Objective: "queued objective", Status: RuntimeGoalStatusActive},
		Availability: textutil.Value(GoalAvailabilityAvailable),
	}

	if err := result.Validate(); err != nil {
		t.Fatalf("validate queued Goal mutation result: %v", err)
	}
	if result.Goal != nil || result.Pending == nil {
		t.Fatalf("queued Goal mutation result = %+v, want pending preview without authoritative Goal", result)
	}
}

func TestGoalMutationResultAllowsOmittedAvailability(t *testing.T) {
	result := GoalMutationResult{
		Pending: &GoalPreview{Objective: "queued objective", Status: RuntimeGoalStatusActive},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("validate Goal mutation result without availability: %v", err)
	}
}
