package runtimecommand

import (
	"context"
	"errors"
	"testing"
)

func TestCompletionFenceInvalidatesAttemptWhenInputIsAccepted(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit resource: %v", err)
	}
	t.Cleanup(func() { _ = authority.Close(context.Background()) })

	fence := NewCompletionFence(SessionTarget(ref))
	attempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	if _, err := fence.AcceptInput(); err != nil {
		t.Fatalf("accept input: %v", err)
	}
	if _, err := attempt.Acquire(); !errors.Is(err, ErrCompletionSuperseded) {
		t.Fatalf("acquire superseded attempt = %v, want ErrCompletionSuperseded", err)
	}
}

func TestCompletionFenceRestoresGenerationWhenInputApplicationAborts(t *testing.T) {
	ref := testResourceRef(t)
	fence := NewCompletionFence(SessionTarget(ref))
	attempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	acceptance, err := fence.BeginInput()
	if err != nil {
		t.Fatalf("begin input acceptance: %v", err)
	}
	if err := acceptance.Abort(); err != nil {
		t.Fatalf("abort input acceptance: %v", err)
	}
	lease, err := attempt.Acquire()
	if err != nil {
		t.Fatalf("acquire completion after aborted input: %v", err)
	}
	if err := lease.Abort(); err != nil {
		t.Fatalf("abort completion lease: %v", err)
	}
}

func TestCompletionFenceRejectsInputAfterCompletionWinsAndAbortsOpenAttempt(t *testing.T) {
	ref := testResourceRef(t)
	fence := NewCompletionFence(SessionTarget(ref))
	attempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	lease, err := attempt.Acquire()
	if err != nil {
		t.Fatalf("acquire completion lease: %v", err)
	}
	if err := lease.Commit(); err != nil {
		t.Fatalf("commit completion lease: %v", err)
	}
	if _, err := fence.AcceptInput(); !errors.Is(err, ErrCompletionFenced) {
		t.Fatalf("accept fenced input = %v, want ErrCompletionFenced", err)
	}

	abortedAttempt, err := fence.Begin()
	if !errors.Is(err, ErrCompletionFenced) {
		t.Fatalf("begin after committed completion = %v, want ErrCompletionFenced", err)
	}
	if abortedAttempt != (CompletionAttempt{}) {
		t.Fatalf("failed attempt = %#v, want zero attempt", abortedAttempt)
	}

	reopened := NewCompletionFence(SessionTarget(ref))
	openAttempt, err := reopened.Begin()
	if err != nil {
		t.Fatalf("begin abortable attempt: %v", err)
	}
	openLease, err := openAttempt.Acquire()
	if err != nil {
		t.Fatalf("acquire abortable lease: %v", err)
	}
	if err := openLease.Abort(); err != nil {
		t.Fatalf("abort completion lease: %v", err)
	}
	if _, err := reopened.AcceptInput(); err != nil {
		t.Fatalf("accept input after abort: %v", err)
	}
}

func TestAuthorityCompletionFenceLinearizesAgainstSessionInput(t *testing.T) {
	authority := NewAuthority(1)
	ref := testResourceRef(t)
	if err := authority.Admit(ref); err != nil {
		t.Fatalf("admit resource: %v", err)
	}
	t.Cleanup(func() { _ = authority.Close(context.Background()) })

	attempt, err := authority.BeginCompletionAttempt(context.Background(), ref)
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	if err := authority.AcceptInput(context.Background(), ref); err != nil {
		t.Fatalf("accept input: %v", err)
	}
	if _, err := attempt.Acquire(); !errors.Is(err, ErrCompletionSuperseded) {
		t.Fatalf("completion after accepted input = %v, want ErrCompletionSuperseded", err)
	}
}

func TestCompletionFenceReservesOneLeaseAtATime(t *testing.T) {
	ref := testResourceRef(t)
	fence := NewCompletionFence(SessionTarget(ref))
	firstAttempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin first completion attempt: %v", err)
	}
	secondAttempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin second completion attempt: %v", err)
	}
	first, err := firstAttempt.Acquire()
	if err != nil {
		t.Fatalf("acquire first completion lease: %v", err)
	}
	second, err := secondAttempt.Acquire()
	if err != nil {
		t.Fatalf("acquire second completion lease: %v", err)
	}
	if err := first.Reserve(); err != nil {
		t.Fatalf("reserve first completion lease: %v", err)
	}
	if err := second.Reserve(); !errors.Is(err, ErrCompletionFenced) {
		t.Fatalf("reserve sibling completion lease = %v, want ErrCompletionFenced", err)
	}
	if err := second.Abort(); !errors.Is(err, ErrCompletionFenced) {
		t.Fatalf("abort sibling completion lease = %v, want ErrCompletionFenced", err)
	}
	if err := first.Abort(); err != nil {
		t.Fatalf("abort first completion lease: %v", err)
	}
	if err := second.Reserve(); err != nil {
		t.Fatalf("reserve second completion lease after first abort: %v", err)
	}
}

func TestCompletionFenceReopensForNextExecutionLifecycle(t *testing.T) {
	ref := testResourceRef(t)
	fence := NewCompletionFence(SessionTarget(ref))
	attempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	lease, err := attempt.Acquire()
	if err != nil {
		t.Fatalf("acquire completion lease: %v", err)
	}
	if err := lease.Commit(); err != nil {
		t.Fatalf("commit completion lease: %v", err)
	}
	if err := fence.Reopen(); err != nil {
		t.Fatalf("reopen completion fence: %v", err)
	}
	if _, err := fence.Begin(); err != nil {
		t.Fatalf("begin completion attempt after reopen: %v", err)
	}
}
