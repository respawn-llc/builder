package runtimeops

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/server/requestmemo"
	"core/shared/clientui"
)

func TestCoordinatorRecordsExactInputOperationOutcomes(t *testing.T) {
	coord := NewCoordinator()
	version := mustVersion(t, 1)
	refs := []clientui.RuntimeOperationRef{
		{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"},
		{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: "queue-1"},
		{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "shell-1"},
		{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "compact-1"},
		{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: "pre-compact-1"},
	}

	coord.RecordCommitted("session-1", refs[0])
	coord.RecordSubmitted("session-1", refs[1])
	coord.RecordFailedWithRestore("session-1", refs[2])
	coord.RecordCanceledNotCommitted("session-1", refs[3])

	snapshot := coord.Snapshot("session-1", version, refs)
	assertState(t, snapshot, refs[0], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[1], clientui.RuntimeInputReconciliationSubmitted)
	assertState(t, snapshot, refs[2], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[3], clientui.RuntimeInputReconciliationCanceledNotCommitted)
	assertState(t, snapshot, refs[4], clientui.RuntimeInputReconciliationUnknown)
	if snapshot.Version != version {
		t.Fatalf("snapshot version = %+v, want %+v", snapshot.Version, version)
	}
}

func TestCoordinatorTransitionsUseReadModelVersions(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-versioned"}

	coord.RecordAccepted("session-versioned", ref)
	accepted := coord.Snapshot("session-versioned", mustVersion(t, 50), []clientui.RuntimeOperationRef{ref})
	assertState(t, accepted, ref, clientui.RuntimeInputReconciliationAccepted)
	acceptedVersion := accepted.Operations[0].Version

	coord.RecordCommitted("session-versioned", ref)
	committed := coord.Snapshot("session-versioned", mustVersion(t, 51), []clientui.RuntimeOperationRef{ref})
	assertState(t, committed, ref, clientui.RuntimeInputReconciliationCommitted)
	committedVersion := committed.Operations[0].Version
	if !committedVersion.NewerThan(acceptedVersion) {
		t.Fatalf("committed version = %+v, want newer than accepted %+v", committedVersion, acceptedVersion)
	}
}

func TestCoordinatorCancelTerminalOperationDoesNotInterruptActiveRuntime(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "terminal-cancel"}
	for _, record := range []func(string, clientui.RuntimeOperationRef){
		coord.RecordCommitted,
		coord.RecordSubmitted,
	} {
		record("session-1", ref)
		result, err := coord.CancelOperationTarget("session-1", ref)
		if err != nil {
			t.Fatalf("CancelOperationTarget: %v", err)
		}
		if result.InterruptActive {
			t.Fatalf("terminal operation %s requested active interrupt", ref.Key())
		}
	}
}

func TestCoordinatorIgnoresTextAndKeysOnlyByOperationRef(t *testing.T) {
	coord := NewCoordinator()
	version := mustVersion(t, 1)
	submit := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "same"}
	shell := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "same"}

	coord.RecordCommitted("session-1", submit)

	snapshot := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{submit, shell})
	assertState(t, snapshot, submit, clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, shell, clientui.RuntimeInputReconciliationUnknown)
}

func TestCoordinatorExactRecordersMapRuntimeFactsToReconciliation(t *testing.T) {
	coord := NewCoordinator()
	version := mustVersion(t, 1)
	refs := []clientui.RuntimeOperationRef{
		{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "flush-1"},
		{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: "queued-status-1"},
		{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "shell-failed-1"},
		{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "compact-ok-1"},
		{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: "access-failed-1"},
		{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: "interrupt-1"},
	}

	coord.RecordUserMessageFlushed("session-1", refs[0])
	coord.RecordQueuedMessageStatus("session-1", refs[1], true)
	coord.RecordShellCompletion("session-1", refs[2], errRecorderTest)
	coord.RecordCompactCompletion("session-1", refs[3], nil)
	coord.RecordRuntimeAccessFailure("session-1", refs[4])
	coord.RecordInterruptCancellation("session-1", refs[5])

	snapshot := coord.Snapshot("session-1", version, refs)
	assertState(t, snapshot, refs[0], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[1], clientui.RuntimeInputReconciliationSubmitted)
	assertState(t, snapshot, refs[2], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[3], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[4], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[5], clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorRecordsQueuedMessageStatusByServerQueueItemID(t *testing.T) {
	coord := NewCoordinator()
	version := mustVersion(t, 1)
	serverQueued := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: "server-queue-1"}
	clientOnly := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: "client-req-1"}

	coord.RecordQueuedMessageStatus("session-1", serverQueued, true)

	snapshot := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{serverQueued, clientOnly})
	assertState(t, snapshot, serverQueued, clientui.RuntimeInputReconciliationSubmitted)
	assertState(t, snapshot, clientOnly, clientui.RuntimeInputReconciliationUnknown)
}

var errRecorderTest = testRecorderError{}

type testRecorderError struct{}

func (testRecorderError) Error() string { return "recorder failure" }

func TestCoordinatorEvictsOldRecordsConservatively(t *testing.T) {
	coord := NewCoordinator(WithLimit(1))
	evicted := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "old"}
	current := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "new"}
	version := mustVersion(t, 1)

	coord.RecordCommitted("session-1", evicted)
	coord.RecordCommitted("session-1", current)

	snapshot := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{evicted, current})
	assertState(t, snapshot, evicted, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, snapshot, current, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorMemoDedupesInFlightAndSuccessfulRetry(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-memo"}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	run := func(ctx context.Context, attempt Attempt) (string, error) {
		_ = ctx
		if attempt.Context() == nil {
			t.Fatal("attempt context is required")
		}
		calls.Add(1)
		close(started)
		<-release
		return "ok", nil
	}

	first := make(chan string, 1)
	firstErr := make(chan error, 1)
	go func() {
		resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
		first <- resp
		firstErr <- err
	}()
	<-started
	second := make(chan string, 1)
	secondErr := make(chan error, 1)
	go func() {
		resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
		second <- resp
		secondErr <- err
	}()
	time.Sleep(25 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("in-flight calls = %d, want 1", got)
	}
	close(release)
	for label, respCh := range map[string]<-chan string{"first": first, "second": second} {
		select {
		case resp := <-respCh:
			if resp != "ok" {
				t.Fatalf("%s response = %q, want ok", label, resp)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s response", label)
		}
	}
	for label, errCh := range map[string]<-chan error{"first": firstErr, "second": secondErr} {
		if err := <-errCh; err != nil {
			t.Fatalf("%s error = %v", label, err)
		}
	}
	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
	if err != nil || resp != "ok" {
		t.Fatalf("successful retry = (%q, %v), want cached ok", resp, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls after successful retry = %d, want 1", got)
	}
}

func TestCoordinatorMemoRejectsReusedOperationRefWithDifferentParamsBeforeLedgerMutation(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "shell-mismatch"}
	_, err := Do(coord, context.Background(), "session-1", ref, "pwd", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("first operation: %v", err)
	}
	version := mustVersion(t, 1)
	before := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{ref}).Operations[0].Version
	_, err = Do(coord, context.Background(), "session-1", ref, "ls", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		return struct{}{}, nil
	})
	if !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatch error = %v, want request id reused", err)
	}
	after := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{ref}).Operations[0].Version
	if after != before {
		t.Fatalf("mismatch mutated ledger version: before=%+v after=%+v", before, after)
	}
}

func TestCoordinatorFailedAttemptRejectsRetryWithDifferentParams(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "failed-mismatch"}
	_, err := Do(coord, context.Background(), "session-1", ref, "original", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		coord.RecordFailedWithRestore("session-1", ref)
		return struct{}{}, errRecorderTest
	})
	if !errors.Is(err, errRecorderTest) {
		t.Fatalf("first error = %v, want recorder failure", err)
	}
	version := mustVersion(t, 20)
	before := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{ref}).Operations[0].Version
	_, err = Do(coord, context.Background(), "session-1", ref, "different", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		t.Fatal("mismatched failed retry must not run")
		return struct{}{}, nil
	})
	if !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatched failed retry error = %v, want request id reused", err)
	}
	after := coord.Snapshot("session-1", version, []clientui.RuntimeOperationRef{ref}).Operations[0].Version
	if after != before {
		t.Fatalf("mismatched failed retry mutated ledger version: before=%+v after=%+v", before, after)
	}
}

func TestCoordinatorPrunesSuccessfulMemoResponsesWithTerminalRecord(t *testing.T) {
	now := time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "ttl-success"}
	var calls atomic.Int32
	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordUserMessageFlushed("session-1", ref)
		return "first", nil
	})
	if err != nil || resp != "first" {
		t.Fatalf("first operation = (%q, %v)", resp, err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "trigger-prune"})
	resp, err = Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		return "second", nil
	})
	if err != nil || resp != "second" {
		t.Fatalf("post-ttl operation = (%q, %v), want second nil", resp, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want cache evicted and rerun", got)
	}
}

func TestCoordinatorTombstoneExpiresAndAllowsLateOperationAfterTTL(t *testing.T) {
	now := time.Date(2026, 6, 30, 14, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "tombstone-ttl"}
	if err := coord.CancelOperation("session-1", ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "trigger-prune"})
	ran := false
	_, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		ran = true
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("operation after tombstone TTL: %v", err)
	}
	if !ran {
		t.Fatal("operation did not run after tombstone TTL")
	}
}

func TestCoordinatorTombstoneRemainsTerminalUntilTTL(t *testing.T) {
	coord := NewCoordinator(WithLimit(1))
	canceled := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "consumed-tombstone"}
	if err := coord.CancelOperation("session-1", canceled); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, err := Do(coord, context.Background(), "session-1", canceled, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
			t.Fatal("consumed tombstone operation must not run")
			return struct{}{}, nil
		})
		if !errors.Is(err, ErrOperationCanceled) {
			t.Fatalf("late operation attempt %d error = %v, want canceled", i+1, err)
		}
	}
	for _, id := range []string{"new-a", "new-b"} {
		coord.RecordCommitted("session-1", clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: id})
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 8), []clientui.RuntimeOperationRef{canceled}), canceled, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorTombstonePreventsLateSuccessfulRecorderOverwrite(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "late-success-after-cancel"}
	if err := coord.CancelOperation("session-1", ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	coord.RecordCommitted("session-1", ref)
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 9), []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorTombstonedSuccessfulAttemptDoesNotReplayAfterTTL(t *testing.T) {
	now := time.Date(2026, 6, 30, 15, 30, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "late-success-memo"}
	calls := atomic.Int32{}
	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		if err := coord.CancelOperation("session-1", ref); err != nil {
			t.Fatalf("CancelOperation: %v", err)
		}
		return "stale-success", nil
	})
	if !errors.Is(err, ErrOperationCanceled) || resp != "" {
		t.Fatalf("tombstoned success = (%q, %v), want canceled zero response", resp, err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "trigger-prune"})
	resp, err = Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		return "fresh", nil
	})
	if err != nil || resp != "fresh" {
		t.Fatalf("post-ttl operation = (%q, %v), want fresh nil", resp, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want stale attempt not replayed", got)
	}
}

func TestCoordinatorQueuedMessageCommitBarrierPreventsLateCancelTombstone(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queued-commit-barrier"}
	created := false
	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		committed, err := coord.TryCommitOperationMutation("session-1", ref, func() error {
			created = true
			return nil
		})
		if err != nil {
			t.Fatalf("TryCommitOperationMutation: %v", err)
		}
		if !committed {
			t.Fatal("TryCommitOperationMutation rejected uncanceled queued message")
		}
		result, err := coord.CancelOperationTarget("session-1", ref)
		if err != nil {
			t.Fatalf("CancelOperationTarget: %v", err)
		}
		if result.InterruptActive {
			t.Fatal("queued-message create after commit barrier must not request active interrupt")
		}
		return "queue-created", nil
	})
	if err != nil || resp != "queue-created" {
		t.Fatalf("queued create result = (%q, %v), want success", resp, err)
	}
	if !created {
		t.Fatal("queued create mutation did not run")
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 10), []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorQueuedMessageCommitMutationPreventsPreCommitCreate(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queued-pre-commit-cancel"}
	created := false
	if err := coord.CancelOperation("session-1", ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	committed, err := coord.TryCommitOperationMutation("session-1", ref, func() error {
		created = true
		return nil
	})
	if err != nil {
		t.Fatalf("TryCommitOperationMutation: %v", err)
	}
	if committed || created {
		t.Fatalf("committed=%t created=%t, want canceled before queued mutation", committed, created)
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 11), []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorCommittedRuntimeAcceptanceWinsLateCancel(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-late-cancel"}
	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		if err := coord.CancelOperation("session-1", ref); err != nil {
			t.Fatalf("CancelOperation: %v", err)
		}
		coord.RecordUserMessageFlushed("session-1", ref)
		return "accepted", nil
	})
	if err != nil || resp != "accepted" {
		t.Fatalf("accepted operation = (%q, %v), want success despite late cancel", resp, err)
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 12), []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorPrunesExpiredTombstonesBeforeCapacityCheck(t *testing.T) {
	now := time.Date(2026, 6, 30, 15, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	old := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "old-tombstone"}
	if err := coord.CancelOperation("session-1", old); err != nil {
		t.Fatalf("old CancelOperation: %v", err)
	}
	now = now.Add(2 * time.Minute)
	newRef := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "new-tombstone"}
	if err := coord.CancelOperation("session-1", newRef); err != nil {
		t.Fatalf("new CancelOperation after old TTL: %v", err)
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 9), []clientui.RuntimeOperationRef{old, newRef}), old, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 10), []clientui.RuntimeOperationRef{newRef}), newRef, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorBoundsEvictedMarkers(t *testing.T) {
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Hour))
	evictedA := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "evicted-a"}
	evictedB := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "evicted-b"}
	current := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "current"}
	coord.RecordCommitted("session-1", evictedA)
	coord.RecordCommitted("session-1", evictedB)
	coord.RecordCommitted("session-1", current)
	snapshot := coord.Snapshot("session-1", mustVersion(t, 11), []clientui.RuntimeOperationRef{evictedA, evictedB, current})
	assertState(t, snapshot, evictedA, clientui.RuntimeInputReconciliationUnknown)
	assertState(t, snapshot, evictedB, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, snapshot, current, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorFailedAttemptCanRetryWithSameOperationRecord(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "compact-retry"}
	var calls atomic.Int32
	_, err := Do(coord, context.Background(), "session-1", ref, "args", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordFailedWithRestore("session-1", ref)
		return "", errRecorderTest
	})
	if !errors.Is(err, errRecorderTest) {
		t.Fatalf("first error = %v, want recorder failure", err)
	}
	failed := coord.Snapshot("session-1", mustVersion(t, 3), []clientui.RuntimeOperationRef{ref})
	assertState(t, failed, ref, clientui.RuntimeInputReconciliationFailedWithRestore)

	resp, err := Do(coord, context.Background(), "session-1", ref, "args", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordCommitted("session-1", ref)
		return "retried", nil
	})
	if err != nil || resp != "retried" {
		t.Fatalf("retry = (%q, %v), want retried nil", resp, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want failed attempt plus retry", got)
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 4), []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorCancelBeforeRegisterCreatesNonEvictableTombstone(t *testing.T) {
	coord := NewCoordinator(WithLimit(1))
	canceled := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: "queued-cancel"}
	if err := coord.CancelOperation("session-1", canceled); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	_, err := Do(coord, context.Background(), "session-1", canceled, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		t.Fatal("late original operation must not run after tombstone")
		return struct{}{}, nil
	})
	if !errors.Is(err, ErrOperationCanceled) {
		t.Fatalf("late original error = %v, want operation canceled", err)
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 5), []clientui.RuntimeOperationRef{canceled}), canceled, clientui.RuntimeInputReconciliationCanceledNotCommitted)
	for i := 0; i < 3; i++ {
		ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "terminal-" + string(rune('a'+i))}
		coord.RecordCommitted("session-1", ref)
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 6), []clientui.RuntimeOperationRef{canceled}), canceled, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorCancelInFlightCancelsAttemptContext(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-cancel"}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(_ context.Context, attempt Attempt) (struct{}, error) {
			close(started)
			<-attempt.Context().Done()
			return struct{}{}, attempt.Context().Err()
		})
		done <- err
	}()
	<-started
	if err := coord.CancelOperation("session-1", ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight operation cancellation")
	}
	assertState(t, coord.Snapshot("session-1", mustVersion(t, 6), []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorCancelActiveSubmitQueuedRequestsRuntimeInterrupt(t *testing.T) {
	coord := NewCoordinator()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: "submit-queued-active-cancel"}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(_ context.Context, attempt Attempt) (struct{}, error) {
			coord.MarkOperationActive("session-1", ref)
			close(started)
			<-attempt.Context().Done()
			return struct{}{}, attempt.Context().Err()
		})
		done <- err
	}()
	<-started
	result, err := coord.CancelOperationTarget("session-1", ref)
	if err != nil {
		t.Fatalf("CancelOperationTarget: %v", err)
	}
	if !result.InterruptActive {
		t.Fatal("active submit-queued operation must request runtime interrupt")
	}
	result.CancelOperationAttempt()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active submit-queued cancellation")
	}
}

func TestCoordinatorTerminalTTLMarksEvictedButKeepsUnexpiredTombstones(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Hour), WithNow(func() time.Time { return now }))
	terminal := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "terminal"}
	tombstone := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "tombstone"}
	coord.RecordCommitted("session-1", terminal)
	if err := coord.CancelOperation("session-1", tombstone); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "new"})
	snapshot := coord.Snapshot("session-1", mustVersion(t, 7), []clientui.RuntimeOperationRef{terminal, tombstone})
	assertState(t, snapshot, terminal, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, snapshot, tombstone, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func assertState(t *testing.T, snapshot clientui.RuntimeInputReconciliationSnapshot, ref clientui.RuntimeOperationRef, want clientui.RuntimeInputReconciliationState) {
	t.Helper()
	for _, record := range snapshot.Operations {
		if record.OperationRef == ref {
			if record.State != want {
				t.Fatalf("state for %+v = %q, want %q", ref, record.State, want)
			}
			return
		}
	}
	t.Fatalf("missing record for %+v in %+v", ref, snapshot.Operations)
}

func mustVersion(t *testing.T, sequence uint64) clientui.ReadModelVersion {
	t.Helper()
	version, err := clientui.NewReadModelVersion("epoch-1", 1, sequence)
	if err != nil {
		t.Fatalf("NewReadModelVersion: %v", err)
	}
	return version
}
