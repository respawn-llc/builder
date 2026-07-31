package runtimecommand

import (
	"context"
	"errors"
	"testing"

	"core/shared/runtimeids"
)

func TestSubmittedTurnMilestoneCompletesBeforePrivateTerminalRelease(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	milestone := NewSubmittedTurnResult[string]()
	continuations := make(chan *Continuation, 1)
	first, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(turn Turn) (string, error) {
		continuation, retainErr := turn.Retain()
		if retainErr != nil {
			return "", retainErr
		}
		continuations <- continuation
		return "submitted", nil
	})
	if err != nil {
		t.Fatalf("enqueue originating command: %v", err)
	}
	if _, err := first.Await(context.Background()); err != nil {
		t.Fatalf("originating result: %v", err)
	}
	continuation := <-continuations

	releaseMilestoneStage := make(chan struct{})
	stage, err := Reenter(context.Background(), continuation, func(Turn) (struct{}, error) {
		if err := milestone.Complete("first response", nil); err != nil {
			return struct{}{}, err
		}
		<-releaseMilestoneStage
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("enqueue submitted-turn milestone: %v", err)
	}

	value, err := milestone.Await(context.Background())
	if err != nil || value != "first response" {
		t.Fatalf("submitted-turn milestone = %q, %v", value, err)
	}
	nextReady := make(chan Future[string], 1)
	nextErr := make(chan error, 1)
	go func() {
		next, err := enqueueNext(authority, ref)
		if err != nil {
			nextErr <- err
			return
		}
		nextReady <- next
	}()
	select {
	case <-nextReady:
		t.Fatal("private continuation permit released at public milestone")
	case err := <-nextErr:
		t.Fatalf("enqueue next command: %v", err)
	default:
	}
	close(releaseMilestoneStage)
	if _, err := stage.Await(context.Background()); err != nil {
		t.Fatalf("milestone stage: %v", err)
	}

	terminal, err := EnqueueTerminal(context.Background(), continuation, func(Turn) (string, error) {
		return "terminal", nil
	})
	if err != nil {
		t.Fatalf("enqueue terminal stage: %v", err)
	}
	if got, err := terminal.Await(context.Background()); err != nil || got != "terminal" {
		t.Fatalf("terminal result = %q, %v", got, err)
	}
	next := <-nextReady
	if got, err := next.Await(context.Background()); err != nil || got != "next" {
		t.Fatalf("post-terminal result = %q, %v", got, err)
	}
}

func TestSubmittedTurnResultCompletesExactlyOnce(t *testing.T) {
	milestone := NewSubmittedTurnResult[string]()
	if err := milestone.Complete("first", nil); err != nil {
		t.Fatalf("complete milestone: %v", err)
	}
	if err := milestone.Complete("second", nil); !errors.Is(err, ErrMilestoneCompleted) {
		t.Fatalf("duplicate completion error = %v, want ErrMilestoneCompleted", err)
	}
	if got, err := milestone.Await(context.Background()); err != nil || got != "first" {
		t.Fatalf("milestone result = %q, %v", got, err)
	}
}

func TestEnqueueSubmittedTurnMilestoneKeepsContinuationOwned(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	continuations := make(chan *Continuation, 1)
	first, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(turn Turn) (string, error) {
		continuation, retainErr := turn.Retain()
		if retainErr != nil {
			return "", retainErr
		}
		continuations <- continuation
		return "first", nil
	})
	if err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	if _, err := first.Await(context.Background()); err != nil {
		t.Fatalf("first result: %v", err)
	}
	continuation := <-continuations
	milestone := NewSubmittedTurnResult[string]()
	stage, err := EnqueueSubmittedTurnMilestone(
		context.Background(),
		continuation,
		milestone,
		func(Turn) (string, error) {
			return "milestone", nil
		},
	)
	if err != nil {
		t.Fatalf("enqueue milestone: %v", err)
	}
	if _, err := stage.Await(context.Background()); err != nil {
		t.Fatalf("milestone stage: %v", err)
	}
	if got, err := milestone.Await(context.Background()); err != nil || got != "milestone" {
		t.Fatalf("milestone result = %q, %v", got, err)
	}

	nextReady := make(chan Future[string], 1)
	go func() {
		next, enqueueErr := enqueueNext(authority, ref)
		if enqueueErr == nil {
			nextReady <- next
		}
	}()
	select {
	case <-nextReady:
		t.Fatal("milestone stage released the continuation permit")
	default:
	}
	terminal, err := EnqueueTerminal(context.Background(), continuation, func(Turn) (string, error) {
		return "terminal", nil
	})
	if err != nil {
		t.Fatalf("enqueue terminal: %v", err)
	}
	if _, err := terminal.Await(context.Background()); err != nil {
		t.Fatalf("terminal stage: %v", err)
	}
	next := <-nextReady
	if got, err := next.Await(context.Background()); err != nil || got != "next" {
		t.Fatalf("next result = %q, %v", got, err)
	}
}

func enqueueNext(authority *Authority, ref runtimeids.SessionResourceRef) (Future[string], error) {
	return Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		return "next", nil
	})
}
