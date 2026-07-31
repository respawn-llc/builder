package runtimecommand

import (
	"context"
	"testing"
)

func TestOrderedAcceptanceReleasesStagePermitAfterPendingWorkAppend(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	command := OrderedAcceptance[int]{Target: SessionTarget(ref), Payload: 7}
	future, err := EnqueueAcceptance(context.Background(), authority, command, func(Turn, int) (string, error) {
		return "accepted", nil
	})
	if err != nil {
		t.Fatalf("enqueue acceptance: %v", err)
	}
	if got, err := future.Await(context.Background()); err != nil || got != "accepted" {
		t.Fatalf("acceptance result = %q, %v", got, err)
	}
	next, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		return "next", nil
	})
	if err != nil {
		t.Fatalf("enqueue next command: %v", err)
	}
	if got, err := next.Await(context.Background()); err != nil || got != "next" {
		t.Fatalf("next result = %q, %v", got, err)
	}
}
