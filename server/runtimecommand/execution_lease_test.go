package runtimecommand

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecutionLeaseRetainsOnePermitThroughGateAndReleasesAtTerminalOwnership(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	lease, err := authority.AcquireExecution(context.Background(), ref)
	if err != nil {
		t.Fatalf("acquire execution lease: %v", err)
	}
	nextReady := make(chan Future[string], 1)
	nextErr := make(chan error, 1)
	go func() {
		next, enqueueErr := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
			return "next", nil
		})
		if enqueueErr != nil {
			nextErr <- enqueueErr
			return
		}
		nextReady <- next
	}()
	select {
	case <-nextReady:
		t.Fatal("execution lease did not retain the command-stage permit")
	case err := <-nextErr:
		t.Fatalf("next command admission: %v", err)
	default:
	}

	if err := lease.Commit(); err != nil {
		t.Fatalf("commit execution lease: %v", err)
	}
	if err := lease.Wait(context.Background()); err != nil {
		t.Fatalf("wait committed execution lease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release execution lease: %v", err)
	}
	next := <-nextReady
	if got, err := next.Await(context.Background()); err != nil || got != "next" {
		t.Fatalf("next result = %q, %v", got, err)
	}
	if err := lease.Release(); !errors.Is(err, ErrTurnExpired) {
		t.Fatalf("duplicate lease release = %v, want ErrTurnExpired", err)
	}
}

func TestExecutionLeaseAbortUnblocksWaiterWithoutStartingOwner(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	lease, err := authority.AcquireExecution(context.Background(), ref)
	if err != nil {
		t.Fatalf("acquire execution lease: %v", err)
	}
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- lease.Wait(context.Background())
	}()
	abortErr := errors.New("owner canceled")
	if err := lease.Abort(abortErr); err != nil {
		t.Fatalf("abort execution lease: %v", err)
	}
	if err := <-waitErr; !errors.Is(err, ErrStartGateAborted) {
		t.Fatalf("aborted lease wait = %v, want ErrStartGateAborted", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release aborted lease: %v", err)
	}
}

func TestExecutionLeaseReusesOneRetainedPermitForRepeatedMutations(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() { _ = authority.Close(context.Background()) })

	lease, err := authority.AcquireExecution(context.Background(), ref)
	if err != nil {
		t.Fatalf("acquire execution lease: %v", err)
	}
	mutationLease, ok := lease.(interface {
		OrderedMutation(context.Context, func(OrderedMutationTurn) error) error
	})
	if !ok {
		t.Fatal("execution lease does not expose its retained ordering continuation")
	}
	if err := lease.Commit(); err != nil {
		t.Fatalf("commit execution lease: %v", err)
	}
	if err := lease.Wait(context.Background()); err != nil {
		t.Fatalf("wait execution lease: %v", err)
	}

	var applied []int
	for index := range 3 {
		index := index
		if err := mutationLease.OrderedMutation(context.Background(), func(turn OrderedMutationTurn) error {
			if turn == nil {
				return errors.New("missing ordered mutation turn")
			}
			applied = append(applied, index)
			return nil
		}); err != nil {
			t.Fatalf("ordered mutation %d: %v", index, err)
		}
	}
	if len(applied) != 3 || applied[0] != 0 || applied[1] != 1 || applied[2] != 2 {
		t.Fatalf("ordered mutation sequence = %v, want [0 1 2]", applied)
	}

	nextReady := make(chan Future[string], 1)
	go func() {
		next, enqueueErr := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
			return "after owner", nil
		})
		if enqueueErr != nil {
			return
		}
		nextReady <- next
	}()
	select {
	case <-nextReady:
		t.Fatal("retained execution permit was released before terminal ownership")
	default:
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release execution lease: %v", err)
	}
	select {
	case next := <-nextReady:
		if got, err := next.Await(context.Background()); err != nil || got != "after owner" {
			t.Fatalf("next command result = %q, %v", got, err)
		}
	case <-time.After(time.Second):
		t.Fatal("next command did not acquire the released execution permit")
	}
}
