package runtimecommand

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/shared/runtimeids"
)

func TestAuthorityEnqueuesOneSessionFIFOAndKeepsSessionsIndependent(t *testing.T) {
	authority := NewAuthority(2)
	firstRef := testResourceRef(t)
	secondRef := testResourceRef(t)
	if err := authority.Admit(firstRef); err != nil {
		t.Fatalf("admit first session: %v", err)
	}
	if err := authority.Admit(secondRef); err != nil {
		t.Fatalf("admit second session: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	var order []string

	first, err := Enqueue(context.Background(), authority, SessionTarget(firstRef), func(Turn) (string, error) {
		close(firstStarted)
		<-releaseFirst
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		return "first", nil
	})
	if err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	<-firstStarted

	second, err := Enqueue(context.Background(), authority, SessionTarget(firstRef), func(Turn) (string, error) {
		mu.Lock()
		order = append(order, "second")
		mu.Unlock()
		return "second", nil
	})
	if err != nil {
		t.Fatalf("enqueue second command: %v", err)
	}

	independent, err := Enqueue(context.Background(), authority, SessionTarget(secondRef), func(Turn) (string, error) {
		mu.Lock()
		order = append(order, "independent")
		mu.Unlock()
		return "independent", nil
	})
	if err != nil {
		t.Fatalf("enqueue independent command: %v", err)
	}
	if got, err := independent.Await(context.Background()); err != nil || got != "independent" {
		t.Fatalf("independent result = %q, %v", got, err)
	}

	select {
	case <-second.Done():
		t.Fatal("same-session FIFO advanced before the first command completed")
	default:
	}

	close(releaseFirst)
	if got, err := first.Await(context.Background()); err != nil || got != "first" {
		t.Fatalf("first result = %q, %v", got, err)
	}
	if got, err := second.Await(context.Background()); err != nil || got != "second" {
		t.Fatalf("second result = %q, %v", got, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "independent" || order[1] != "first" || order[2] != "second" {
		t.Fatalf("application order = %v, want independent then same-session FIFO", order)
	}
}

func TestAuthorityStageCapacityWaitsBeforeAdmissionAndAcceptedWorkOutlivesCaller(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
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

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	secondStarted := make(chan struct{})
	secondErr := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, err := Enqueue(admissionCtx, authority, SessionTarget(ref), func(Turn) (string, error) {
			return "second", nil
		})
		secondErr <- err
	}()
	<-secondStarted
	cancelAdmission()
	select {
	case err := <-secondErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("capacity wait error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("capacity wait did not honor cancellation")
	}

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	close(release)
	if got, err := first.Await(context.Background()); err != nil || got != "first" {
		t.Fatalf("first result = %q, %v", got, err)
	}
	thirdStarted := make(chan struct{})
	releaseThird := make(chan struct{})
	third, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		close(thirdStarted)
		<-releaseThird
		return "third", nil
	})
	if err != nil {
		t.Fatalf("enqueue third command: %v", err)
	}
	<-thirdStarted
	cancelCaller()
	if _, err := third.Await(callerCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller await error = %v, want context canceled", err)
	}
	close(releaseThird)
	if got, err := third.Await(context.Background()); err != nil || got != "third" {
		t.Fatalf("accepted work result = %q, %v", got, err)
	}
}

func TestAuthorityRejectsStaleTargetsAndExpiredTurns(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	otherRef := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	if err := authority.Admit(otherRef); err != nil {
		t.Fatalf("admit other session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	var turn Turn
	result, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(current Turn) (string, error) {
		turn = current
		if err := current.CheckTarget(SessionTarget(ref)); err != nil {
			return "", err
		}
		if err := current.CheckTarget(SessionTarget(otherRef)); !errors.Is(err, ErrCrossResourceTurn) {
			return "", errors.Join(ErrCrossResourceTurn, err)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("enqueue command: %v", err)
	}
	if got, err := result.Await(context.Background()); err != nil || got != "ok" {
		t.Fatalf("result = %q, %v", got, err)
	}
	if err := turn.CheckTarget(SessionTarget(ref)); !errors.Is(err, ErrTurnExpired) {
		t.Fatalf("expired turn error = %v, want ErrTurnExpired", err)
	}

	if err := authority.CloseResource(context.Background(), ref); err != nil {
		t.Fatalf("close resource: %v", err)
	}
	if _, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		return "stale", nil
	}); !errors.Is(err, ErrResourceUnavailable) {
		t.Fatalf("stale target admission error = %v, want ErrResourceUnavailable", err)
	}
}

func TestAuthorityContinuationReentersAtTailWithoutAnotherPermit(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	continuationReady := make(chan *Continuation, 1)
	first, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(turn Turn) (string, error) {
		continuation, retainErr := turn.Retain()
		if retainErr != nil {
			return "", retainErr
		}
		continuationReady <- continuation
		return "first", nil
	})
	if err != nil {
		t.Fatalf("enqueue first command: %v", err)
	}
	if got, err := first.Await(context.Background()); err != nil || got != "first" {
		t.Fatalf("first result = %q, %v", got, err)
	}
	continuation := <-continuationReady

	secondStarted := make(chan struct{})
	secondResult := make(chan Future[string], 1)
	go func() {
		close(secondStarted)
		future, enqueueErr := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
			return "after continuation", nil
		})
		if enqueueErr == nil {
			secondResult <- future
			return
		}
		secondResult <- Future[string]{}
	}()
	<-secondStarted
	select {
	case <-secondResult:
		t.Fatal("a retained continuation released its only stage permit")
	default:
	}

	reentered, err := Reenter(context.Background(), continuation, func(Turn) (string, error) {
		return "intermediate", nil
	})
	if err != nil {
		t.Fatalf("reenter continuation: %v", err)
	}
	if got, err := reentered.Await(context.Background()); err != nil || got != "intermediate" {
		t.Fatalf("reentered result = %q, %v", got, err)
	}
	if err := continuation.Release(); err != nil {
		t.Fatalf("release continuation: %v", err)
	}

	select {
	case future := <-secondResult:
		if got, err := future.Await(context.Background()); err != nil || got != "after continuation" {
			t.Fatalf("post-continuation result = %q, %v", got, err)
		}
	case <-time.After(time.Second):
		t.Fatal("post-continuation command did not acquire the released permit")
	}
}

func TestAuthorityContinuationReentersWithoutAnotherPermit(t *testing.T) {
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
	if got, err := first.Await(context.Background()); err != nil || got != "first" {
		t.Fatalf("first result = %q, %v", got, err)
	}
	continuation := <-continuations

	secondResult := make(chan Future[string], 1)
	go func() {
		future, enqueueErr := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
			return "second", nil
		})
		if enqueueErr == nil {
			secondResult <- future
			return
		}
		secondResult <- Future[string]{}
	}()
	select {
	case <-secondResult:
		t.Fatal("a retained continuation released its only stage permit")
	default:
	}

	reentered, err := Reenter(context.Background(), continuation, func(Turn) (string, error) {
		return "reentered", nil
	})
	if err != nil {
		t.Fatalf("reenter continuation: %v", err)
	}
	if got, err := reentered.Await(context.Background()); err != nil || got != "reentered" {
		t.Fatalf("reentered result = %q, %v", got, err)
	}
	if err := continuation.Release(); err != nil {
		t.Fatalf("release continuation: %v", err)
	}

	select {
	case future := <-secondResult:
		if got, err := future.Await(context.Background()); err != nil || got != "second" {
			t.Fatalf("second result = %q, %v", got, err)
		}
	case <-time.After(time.Second):
		t.Fatal("second command did not acquire the released permit")
	}
}

func TestAuthorityContinuationReentersAtCurrentTail(t *testing.T) {
	authority := NewAuthority(2)
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

	externalStarted := make(chan struct{})
	releaseExternal := make(chan struct{})
	external, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		close(externalStarted)
		<-releaseExternal
		return "external", nil
	})
	if err != nil {
		t.Fatalf("enqueue external command: %v", err)
	}
	<-externalStarted

	reentered, err := Reenter(context.Background(), continuation, func(Turn) (string, error) {
		return "reentered", nil
	})
	if err != nil {
		t.Fatalf("reenter continuation: %v", err)
	}
	select {
	case <-reentered.Done():
		t.Fatal("continuation re-entry applied before the command already at the tail")
	default:
	}

	close(releaseExternal)
	if got, err := external.Await(context.Background()); err != nil || got != "external" {
		t.Fatalf("external result = %q, %v", got, err)
	}
	if got, err := reentered.Await(context.Background()); err != nil || got != "reentered" {
		t.Fatalf("reentered result = %q, %v", got, err)
	}
	if err := continuation.Release(); err != nil {
		t.Fatalf("release continuation: %v", err)
	}
}

func TestAuthorityContinuationReentryWaitsBehindCurrentTail(t *testing.T) {
	authority := NewAuthority(2)
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

	externalStarted := make(chan struct{})
	releaseExternal := make(chan struct{})
	external, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
		close(externalStarted)
		<-releaseExternal
		return "external", nil
	})
	if err != nil {
		t.Fatalf("enqueue external command: %v", err)
	}
	<-externalStarted

	reentered, err := Reenter(context.Background(), continuation, func(Turn) (string, error) {
		return "reentered", nil
	})
	if err != nil {
		t.Fatalf("reenter continuation: %v", err)
	}
	select {
	case <-reentered.Done():
		t.Fatal("reentry applied before the current tail")
	default:
	}
	close(releaseExternal)
	if _, err := external.Await(context.Background()); err != nil {
		t.Fatalf("external result: %v", err)
	}
	if _, err := reentered.Await(context.Background()); err != nil {
		t.Fatalf("reentered result: %v", err)
	}
	if err := continuation.Release(); err != nil {
		t.Fatalf("release continuation: %v", err)
	}
}

func TestAuthorityFullCapacityDeferredContinuationsCanReenterAndRelease(t *testing.T) {
	authority := NewAuthority(2)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	continuations := make(chan *Continuation, 2)
	var started sync.WaitGroup
	started.Add(2)
	for range 2 {
		future, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(turn Turn) (string, error) {
			continuation, retainErr := turn.Retain()
			if retainErr != nil {
				return "", retainErr
			}
			continuations <- continuation
			started.Done()
			return "submitted", nil
		})
		if err != nil {
			t.Fatalf("enqueue deferred owner: %v", err)
		}
		if got, err := future.Await(context.Background()); err != nil || got != "submitted" {
			t.Fatalf("deferred owner result = %q, %v", got, err)
		}
	}
	started.Wait()

	for range 2 {
		continuation := <-continuations
		intermediate, err := Reenter(context.Background(), continuation, func(Turn) (string, error) {
			return "intermediate", nil
		})
		if err != nil {
			t.Fatalf("reenter deferred owner: %v", err)
		}
		if got, err := intermediate.Await(context.Background()); err != nil || got != "intermediate" {
			t.Fatalf("intermediate result = %q, %v", got, err)
		}
		if err := continuation.Release(); err != nil {
			t.Fatalf("release deferred owner: %v", err)
		}
	}
}

func TestAuthorityAcceptedInputCanExceedTransientCapacityBeforeDelivery(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit session: %v", err)
	}
	t.Cleanup(func() {
		_ = authority.Close(context.Background())
	})

	const accepted = 8
	var mu sync.Mutex
	var pending []int
	for item := 0; item < accepted; item++ {
		future, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (int, error) {
			mu.Lock()
			pending = append(pending, item)
			mu.Unlock()
			return item, nil
		})
		if err != nil {
			t.Fatalf("enqueue accepted input %d: %v", item, err)
		}
		if got, err := future.Await(context.Background()); err != nil || got != item {
			t.Fatalf("accepted input %d result = %d, %v", item, got, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pending) != accepted {
		t.Fatalf("accepted input count = %d, want %d", len(pending), accepted)
	}
	for index, item := range pending {
		if item != index {
			t.Fatalf("accepted input order = %v, want FIFO", pending)
		}
	}
}

func TestAuthorityCloseWakesStageCapacityWaiters(t *testing.T) {
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

	waiting := make(chan error, 1)
	go func() {
		_, err := Enqueue(context.Background(), authority, SessionTarget(ref), func(Turn) (string, error) {
			return "second", nil
		})
		waiting <- err
	}()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- authority.CloseResource(context.Background(), ref)
	}()
	select {
	case err := <-waiting:
		if !errors.Is(err, ErrResourceUnavailable) {
			t.Fatalf("capacity waiter error = %v, want ErrResourceUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("capacity waiter was not woken by resource close")
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatalf("close resource: %v", err)
	}
	if _, err := first.Await(context.Background()); err != nil {
		t.Fatalf("first result: %v", err)
	}
}

func testResourceRef(t *testing.T) runtimeids.SessionResourceRef {
	t.Helper()
	ref, err := runtimeids.NewSessionResourceRef(runtimeids.NewSessionID(), 1)
	if err != nil {
		t.Fatalf("new resource ref: %v", err)
	}
	return ref
}
