package runtimecommand

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testHandoffOwner struct {
	joined chan struct{}
	once   sync.Once
}

func (o *testHandoffOwner) Join(context.Context) error {
	o.once.Do(func() { close(o.joined) })
	return nil
}

func TestBeginHandoffRegistersOwnerBeforeAtomicMutationAndOpensGateAfterCommit(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	ownerStarted := make(chan struct{})
	owner := &testHandoffOwner{joined: make(chan struct{})}
	handoff, err := BeginHandoff(
		context.Background(),
		authority,
		SessionTarget(ref),
		func(gate *StartGate) (HandoffOwner, error) {
			go func() {
				if err := gate.Wait(context.Background()); err == nil {
					close(ownerStarted)
				}
			}()
			return owner, nil
		},
		func(Turn) (string, error) {
			return "committed", nil
		},
	)
	if err != nil {
		t.Fatalf("begin handoff: %v", err)
	}
	result, continuation, err := handoff.Await(context.Background())
	if err != nil || result != "committed" {
		t.Fatalf("handoff result = %q, continuation=%v, error=%v", result, continuation, err)
	}
	select {
	case <-ownerStarted:
	case <-time.After(time.Second):
		t.Fatal("handoff owner did not start after committed mutation")
	}
	if err := continuation.Release(); err != nil {
		t.Fatalf("release handoff continuation: %v", err)
	}
	if err := owner.Join(context.Background()); err != nil {
		t.Fatalf("join handoff owner: %v", err)
	}
}

func TestBeginHandoffMutationFailureAbortsGateJoinsOwnerAndReleasesPermit(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	ownerStarted := make(chan struct{})
	owner := &testHandoffOwner{joined: make(chan struct{})}
	handoff, err := BeginHandoff(
		context.Background(),
		authority,
		SessionTarget(ref),
		func(gate *StartGate) (HandoffOwner, error) {
			go func() {
				if gate.Wait(context.Background()) == nil {
					close(ownerStarted)
				}
			}()
			return owner, nil
		},
		func(Turn) (string, error) {
			return "", errors.New("mutation failed")
		},
	)
	if err != nil {
		t.Fatalf("begin handoff: %v", err)
	}
	if _, _, err := handoff.Await(context.Background()); !errors.Is(err, errors.New("mutation failed")) {
		if err == nil || err.Error() != "mutation failed" {
			t.Fatalf("handoff mutation error = %v", err)
		}
	}
	select {
	case <-ownerStarted:
		t.Fatal("handoff owner started after mutation failure")
	default:
	}
	select {
	case <-owner.joined:
	case <-time.After(time.Second):
		t.Fatal("failed handoff owner was not joined")
	}

	next, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		return "capacity released", nil
	})
	if err != nil {
		t.Fatalf("enqueue after failed handoff: %v", err)
	}
	if got, err := next.Await(context.Background()); err != nil || got != "capacity released" {
		t.Fatalf("post-failure result = %q, %v", got, err)
	}
}

func TestBeginHandoffOwnerRegistrationFailureDoesNotEnterQueue(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	var mutated bool
	_, err := BeginHandoff(
		context.Background(),
		authority,
		SessionTarget(ref),
		func(*StartGate) (HandoffOwner, error) {
			return nil, errors.New("owner registration failed")
		},
		func(Turn) (string, error) {
			mutated = true
			return "unexpected", nil
		},
	)
	if err == nil || err.Error() != "owner registration failed" {
		t.Fatalf("registration error = %v", err)
	}
	if mutated {
		t.Fatal("mutation ran after owner registration failure")
	}
}

func TestBeginHandoffDrainWinsBeforeStageAdmission(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	first, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		close(started)
		<-release
		return "first", nil
	})
	if err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	<-started

	ownerStarted := make(chan struct{})
	owner := &testHandoffOwner{joined: make(chan struct{})}
	mutationRan := make(chan struct{}, 1)
	handoffErr := make(chan error, 1)
	close(release)
	if _, err := first.Await(context.Background()); err != nil {
		t.Fatalf("first result: %v", err)
	}
	if err := authority.CloseResource(context.Background(), ref); err != nil {
		t.Fatalf("close resource: %v", err)
	}
	go func() {
		_, err := BeginHandoff(
			context.Background(),
			authority,
			SessionTarget(ref),
			func(gate *StartGate) (HandoffOwner, error) {
				go func() {
					if gate.Wait(context.Background()) == nil {
						close(ownerStarted)
					}
				}()
				return owner, nil
			},
			func(Turn) (string, error) {
				mutationRan <- struct{}{}
				return "unexpected", nil
			},
		)
		handoffErr <- err
	}()
	if err := <-handoffErr; !errors.Is(err, ErrResourceUnavailable) {
		t.Fatalf("drained handoff error = %v, want ErrResourceUnavailable", err)
	}
	select {
	case <-mutationRan:
		t.Fatal("drained handoff mutated the session")
	default:
	}
	select {
	case <-ownerStarted:
		t.Fatal("drained handoff owner started")
	default:
	}
	select {
	case <-owner.joined:
	case <-time.After(time.Second):
		t.Fatal("drained handoff owner was not joined")
	}
}

func TestBeginHandoffCancellationBeforeAdmissionAbortsOwner(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	started := make(chan struct{})
	release := make(chan struct{})
	first, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		close(started)
		<-release
		return "first", nil
	})
	if err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	ownerStarted := make(chan struct{})
	ownerRegistered := make(chan struct{})
	owner := &testHandoffOwner{joined: make(chan struct{})}
	mutationRan := make(chan struct{}, 1)
	handoffErr := make(chan error, 1)
	go func() {
		_, err := BeginHandoff(
			ctx,
			authority,
			SessionTarget(ref),
			func(gate *StartGate) (HandoffOwner, error) {
				close(ownerRegistered)
				go func() {
					if gate.Wait(context.Background()) == nil {
						close(ownerStarted)
					}
				}()
				return owner, nil
			},
			func(Turn) (string, error) {
				mutationRan <- struct{}{}
				return "unexpected", nil
			},
		)
		handoffErr <- err
	}()
	<-ownerRegistered
	cancel()
	err = <-handoffErr
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled handoff error = %v, want context canceled", err)
	}
	select {
	case <-mutationRan:
		t.Fatal("canceled handoff mutated the session")
	default:
	}
	select {
	case <-ownerStarted:
		t.Fatal("canceled handoff owner started")
	default:
	}
	select {
	case <-owner.joined:
	case <-time.After(time.Second):
		t.Fatal("canceled handoff owner was not joined")
	}
	close(release)
	if _, err := first.Await(context.Background()); err != nil {
		t.Fatalf("first result: %v", err)
	}
}

func TestBeginHandoffReservationWinsAgainstDrainDuringMutation(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	owner := &testHandoffOwner{joined: make(chan struct{})}
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	handoff, err := BeginHandoff(
		context.Background(),
		authority,
		SessionTarget(ref),
		func(gate *StartGate) (HandoffOwner, error) {
			return owner, nil
		},
		func(Turn) (string, error) {
			close(mutationStarted)
			<-releaseMutation
			return "committed", nil
		},
	)
	if err != nil {
		t.Fatalf("begin handoff: %v", err)
	}
	<-mutationStarted
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- authority.CloseResource(context.Background(), ref)
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("drain completed during reserved mutation: %v", err)
	default:
	}
	close(releaseMutation)
	if _, continuation, err := handoff.Await(context.Background()); err != nil {
		t.Fatalf("reserved handoff result: %v", err)
	} else {
		if err := continuation.Release(); err != nil {
			t.Fatalf("release reserved handoff: %v", err)
		}
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("drain after reserved handoff: %v", err)
	}
}
