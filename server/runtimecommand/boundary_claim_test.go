package runtimecommand

import (
	"context"
	"testing"
)

func TestBoundaryClaimCarriesOnlySelectionPolicyAndUsesItsFIFOPosition(t *testing.T) {
	authority := NewAuthority(2)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	var applied []string
	appendInput := func(label string) Future[string] {
		future, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
			applied = append(applied, label)
			return label, nil
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", label, err)
		}
		return future
	}

	first := appendInput("accepted-before-claim")
	claim := BoundaryClaim{
		Target:   SessionTarget(ref),
		Boundary: AgentStepBoundary,
		Selection: BoundarySelection{
			Kind: BoundarySelectionEligiblePrefix,
		},
	}
	claimResult, err := EnqueueBoundaryClaim(context.Background(), authority, claim, func(Turn, BoundaryClaim) (string, error) {
		applied = append(applied, "claim")
		return "claim", nil
	})
	if err != nil {
		t.Fatalf("enqueue boundary claim: %v", err)
	}
	last := appendInput("accepted-after-claim")

	for name, future := range map[string]Future[string]{
		"first": first,
		"claim": claimResult,
		"last":  last,
	} {
		if _, err := future.Await(context.Background()); err != nil {
			t.Fatalf("%s result: %v", name, err)
		}
	}
	want := []string{"accepted-before-claim", "claim", "accepted-after-claim"}
	if len(applied) != len(want) {
		t.Fatalf("applied sequence = %v, want %v", applied, want)
	}
	for index, value := range want {
		if applied[index] != value {
			t.Fatalf("applied sequence = %v, want %v", applied, want)
		}
	}
}
