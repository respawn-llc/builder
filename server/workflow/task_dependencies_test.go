package workflow

import "testing"

func TestTaskDependencyPolicyAvailabilityUsesTheDirectLimit(t *testing.T) {
	policy := TaskDependencyPolicy{}

	available, err := policy.AddAvailability(49)
	if err != nil {
		t.Fatalf("AddAvailability(49): %v", err)
	}
	if available.Kind != TaskDependencyAddAvailable || available.RemainingCapacity == nil || *available.RemainingCapacity != 1 {
		t.Fatalf("availability at 49 = %+v, want available with remaining capacity 1", available)
	}

	limitReached, err := policy.AddAvailability(MaxTaskDependencies)
	if err != nil {
		t.Fatalf("AddAvailability(limit): %v", err)
	}
	if limitReached.Kind != TaskDependencyAddLimitReached || limitReached.RemainingCapacity != nil {
		t.Fatalf("availability at limit = %+v, want limit_reached without remaining capacity", limitReached)
	}
}
