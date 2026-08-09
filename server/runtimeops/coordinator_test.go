package runtimeops

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/server/requestmemo"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestCoordinatorRecordsExactInputOperationOutcomes(t *testing.T) {
	coord := NewCoordinator()
	refs := []clientui.RuntimeOperationRef{
		testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit),
		testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage),
		testRuntimeOperationRef(clientui.RuntimeOperationKindUserShell),
		testRuntimeOperationRef(clientui.RuntimeOperationKindCompact),
		testRuntimeOperationRef(clientui.RuntimeOperationKindPreSubmitCompact),
	}

	coord.RecordCommitted("session-1", refs[0])
	coord.RecordSubmitted("session-1", refs[1])
	coord.RecordFailedWithRestore("session-1", refs[2])
	coord.RecordCanceledNotCommitted("session-1", refs[3])

	snapshot := mustFeedSnapshot(t, coord, "session-1", refs)
	assertState(t, snapshot, refs[0], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[1], clientui.RuntimeInputReconciliationSubmitted)
	assertState(t, snapshot, refs[2], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[3], clientui.RuntimeInputReconciliationCanceledNotCommitted)
	assertState(t, snapshot, refs[4], clientui.RuntimeInputReconciliationUnknown)
}

func TestCoordinatorCancelTerminalOperationDoesNotInterruptActiveRuntime(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	submit := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: clientRequestID}
	shell := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: clientRequestID}

	coord.RecordCommitted("session-1", submit)

	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{submit}), submit, clientui.RuntimeInputReconciliationCommitted)
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{shell}), shell, clientui.RuntimeInputReconciliationUnknown)
}

func TestCoordinatorExactRecordersMapRuntimeFactsToReconciliation(t *testing.T) {
	coord := NewCoordinator()
	refs := []clientui.RuntimeOperationRef{
		testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit),
		testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage),
		testRuntimeOperationRef(clientui.RuntimeOperationKindUserShell),
		testRuntimeOperationRef(clientui.RuntimeOperationKindCompact),
		testRuntimeOperationRef(clientui.RuntimeOperationKindPreSubmitCompact),
		testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit),
	}

	coord.RecordUserMessageFlushed("session-1", refs[0])
	coord.RecordSubmitted("session-1", refs[1])
	coord.RecordShellCompletion("session-1", refs[2], errRecorderTest)
	coord.RecordCompactCompletion("session-1", refs[3], session.CommitReceipt{}, nil)
	coord.RecordRuntimeAccessFailure("session-1", refs[4])
	coord.RecordInterruptCancellation("session-1", refs[5])

	snapshot := mustFeedSnapshot(t, coord, "session-1", refs)
	assertState(t, snapshot, refs[0], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[1], clientui.RuntimeInputReconciliationSubmitted)
	assertState(t, snapshot, refs[2], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[3], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[4], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[5], clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorRecordsQueuedMessageSubmittedByServerQueueItemID(t *testing.T) {
	coord := NewCoordinator()
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	queueItemID := runtimeids.NewQueueItemID()
	serverQueued := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: clientRequestID, QueueItemID: &queueItemID}

	if err := coord.RecordQueuedMessageStatus("session-1", clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
		QueueItemID:     &queueItemID,
	}, clientui.RuntimeInputReconciliationSubmitted); err != nil {
		t.Fatalf("RecordQueuedMessageSubmitted: %v", err)
	}

	snapshot := mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{serverQueued})
	assertState(t, snapshot, serverQueued, clientui.RuntimeInputReconciliationSubmitted)

	feedSnapshot, err := coord.FeedSnapshot("session-1", []clientui.RuntimeOperationRef{serverQueued})
	if err != nil {
		t.Fatalf("FeedSnapshot: %v", err)
	}
	if len(feedSnapshot.Operations) != 1 {
		t.Fatalf("feed operations = %+v, want one queued operation", feedSnapshot.Operations)
	}
	operation := feedSnapshot.Operations[0]
	if operation.Operation.ClientRequestID != clientRequestID ||
		operation.Operation.QueueItemID == nil ||
		*operation.Operation.QueueItemID != queueItemID ||
		operation.State != clientui.RuntimeInputReconciliationSubmitted {
		t.Fatalf("canonical queued reconciliation = %+v, want both identities and submitted state", operation)
	}
}

func TestCoordinatorCommitMutationAllowsQueuedStatusPublication(t *testing.T) {
	coord := NewCoordinator()
	sessionID := "session-queued-status-publication"
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	queueItemID := runtimeids.NewQueueItemID()
	ref := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
	}
	canonicalRef := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
		QueueItemID:     &queueItemID,
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := Do(
			coord,
			context.Background(),
			sessionID,
			ref,
			"queue request",
			func(left, right string) bool { return left == right },
			func(context.Context, Attempt) (struct{}, error) {
				committed, commitErr := coord.TryCommitOperationMutation(sessionID, ref, func() error {
					if recordErr := coord.RecordQueuedMessageStatus(
						sessionID,
						canonicalRef,
						clientui.RuntimeInputReconciliationAccepted,
					); recordErr != nil {
						return recordErr
					}
					snapshot, snapshotErr := coord.FeedSnapshot(sessionID, []clientui.RuntimeOperationRef{canonicalRef})
					if snapshotErr != nil {
						return snapshotErr
					}
					if len(snapshot.Operations) != 1 ||
						snapshot.Operations[0].State != clientui.RuntimeInputReconciliationAccepted {
						return errors.New("accepted queued status was not visible during commit mutation")
					}
					return nil
				})
				if commitErr != nil {
					return struct{}{}, commitErr
				}
				if !committed {
					return struct{}{}, errors.New("queued operation mutation was not committed")
				}
				return struct{}{}, nil
			},
		)
		done <- runErr
	}()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("queued operation: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("queued status publication deadlocked inside commit mutation")
	}
}

func TestCoordinatorCancellationWaitsForCommitMutation(t *testing.T) {
	coord := NewCoordinator()
	sessionID := "session-commit-cancellation"
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		_, runErr := Do(
			coord,
			context.Background(),
			sessionID,
			ref,
			"queue request",
			func(left, right string) bool { return left == right },
			func(context.Context, Attempt) (struct{}, error) {
				committed, commitErr := coord.TryCommitOperationMutation(sessionID, ref, func() error {
					close(mutationStarted)
					<-releaseMutation
					return nil
				})
				if commitErr != nil {
					return struct{}{}, commitErr
				}
				if !committed {
					return struct{}{}, errors.New("queued operation mutation was not committed")
				}
				return struct{}{}, nil
			},
		)
		operationDone <- runErr
	}()
	<-mutationStarted

	cancelDone := make(chan error, 1)
	go func() {
		_, cancelErr := coord.CancelOperationTarget(sessionID, ref)
		cancelDone <- cancelErr
	}()
	select {
	case cancelErr := <-cancelDone:
		t.Fatalf("cancellation crossed active commit mutation: %v", cancelErr)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseMutation)
	select {
	case runErr := <-operationDone:
		if runErr != nil {
			t.Fatalf("queued operation: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("queued operation did not finish after releasing commit mutation")
	}
	select {
	case cancelErr := <-cancelDone:
		if cancelErr != nil {
			t.Fatalf("cancel committed operation: %v", cancelErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not resume after commit mutation")
	}
	assertState(
		t,
		mustFeedSnapshot(t, coord, sessionID, []clientui.RuntimeOperationRef{ref}),
		ref,
		clientui.RuntimeInputReconciliationCommitted,
	)
}

func TestCoordinatorCommitMutationPanicReleasesCancellationBarrier(t *testing.T) {
	coord := NewCoordinator()
	sessionID := "session-commit-panic"
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("commit mutation panic was swallowed")
			}
		}()
		_, _ = coord.TryCommitOperationMutation(sessionID, ref, func() error {
			panic("commit mutation panic")
		})
	}()

	done := make(chan error, 1)
	go func() {
		_, cancelErr := coord.CancelOperationTarget(sessionID, ref)
		done <- cancelErr
	}()
	select {
	case cancelErr := <-done:
		if cancelErr != nil {
			t.Fatalf("cancel after commit mutation panic: %v", cancelErr)
		}
	case <-time.After(time.Second):
		t.Fatal("commit mutation panic left cancellation barrier locked")
	}
}

func TestCoordinatorRetainsQueuedIdentityThroughEvictionWindowThenReleasesIt(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	queueItemID := runtimeids.NewQueueItemID()
	operation := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
		QueueItemID:     &queueItemID,
	}
	if err := coord.RecordQueuedMessageStatus("session-1", operation, clientui.RuntimeInputReconciliationSubmitted); err != nil {
		t.Fatalf("record submitted queued message: %v", err)
	}
	queueRef := operation

	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
	evicted, err := coord.FeedSnapshot("session-1", []clientui.RuntimeOperationRef{queueRef})
	if err != nil {
		t.Fatalf("FeedSnapshot during eviction window: %v", err)
	}
	if len(evicted.Operations) != 1 || evicted.Operations[0].State != clientui.RuntimeInputReconciliationEvicted {
		t.Fatalf("evicted reconciliation = %+v, want evicted", evicted.Operations)
	}

	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
	unknown, err := coord.FeedSnapshot("session-1", []clientui.RuntimeOperationRef{queueRef})
	if err != nil {
		t.Fatalf("FeedSnapshot after queued identity eviction: %v", err)
	}
	if len(unknown.Operations) != 1 ||
		unknown.Operations[0].State != clientui.RuntimeInputReconciliationUnknown ||
		unknown.Operations[0].Operation != queueRef {
		t.Fatalf("unknown reconciliation = %+v, want original queued ref in unknown state", unknown.Operations)
	}
}

var errRecorderTest = testRecorderError{}

type testRecorderError struct{}

func (testRecorderError) Error() string { return "recorder failure" }

func TestCoordinatorEvictsOldRecordsConservatively(t *testing.T) {
	coord := NewCoordinator(WithLimit(1))
	evicted := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	current := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)

	coord.RecordCommitted("session-1", evicted)
	coord.RecordCommitted("session-1", current)

	snapshot := mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{evicted, current})
	assertState(t, snapshot, evicted, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, snapshot, current, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorMemoDedupesInFlightAndSuccessfulRetry(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindUserShell)
	_, err := Do(coord, context.Background(), "session-1", ref, "pwd", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("first operation: %v", err)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationAccepted)
	_, err = Do(coord, context.Background(), "session-1", ref, "ls", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		return struct{}{}, nil
	})
	if !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatch error = %v, want request id reused", err)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationAccepted)
}

func TestCoordinatorFailedAttemptRejectsRetryWithDifferentParams(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	_, err := Do(coord, context.Background(), "session-1", ref, "original", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		coord.RecordFailedWithRestore("session-1", ref)
		return struct{}{}, errRecorderTest
	})
	if !errors.Is(err, errRecorderTest) {
		t.Fatalf("first error = %v, want recorder failure", err)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationFailedWithRestore)
	_, err = Do(coord, context.Background(), "session-1", ref, "different", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (struct{}, error) {
		t.Fatal("mismatched failed retry must not run")
		return struct{}{}, nil
	})
	if !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("mismatched failed retry error = %v, want request id reused", err)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationFailedWithRestore)
}

func TestCoordinatorPrunesSuccessfulMemoResponsesWithTerminalRecord(t *testing.T) {
	now := time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
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
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	if err := coord.CancelOperation("session-1", ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
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
	canceled := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	for range 2 {
		coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{canceled}), canceled, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorTombstonePreventsLateSuccessfulRecorderOverwrite(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
	if err := coord.CancelOperation("session-1", ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	coord.RecordCommitted("session-1", ref)
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorTombstonedSuccessfulAttemptDoesNotReplayAfterTTL(t *testing.T) {
	now := time.Date(2026, 6, 30, 15, 30, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
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
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
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
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
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
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorQueuedMessageCommitMutationPreventsPreCommitCreate(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
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
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorCommittedRuntimeAcceptanceWinsLateCancel(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorCommittedAttemptErrorIsNotRerun(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	var calls atomic.Int32
	run := func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordUserMessageFlushed("session-1", ref)
		return "accepted", errRecorderTest
	}

	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
	if !errors.Is(err, errRecorderTest) || resp != "accepted" {
		t.Fatalf("first committed error = (%q, %v), want accepted recorder error", resp, err)
	}
	resp, err = Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
	if !errors.Is(err, errRecorderTest) || resp != "accepted" {
		t.Fatalf("retry committed error = (%q, %v), want cached accepted recorder error", resp, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("committed error calls = %d, want 1", got)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorPrunesCommittedAttemptErrorAfterTTL(t *testing.T) {
	now := time.Date(2026, 6, 30, 16, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	var calls atomic.Int32
	run := func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordUserMessageFlushed("session-1", ref)
		return "accepted", errRecorderTest
	}

	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
	if !errors.Is(err, errRecorderTest) || resp != "accepted" {
		t.Fatalf("first committed error = (%q, %v), want accepted recorder error", resp, err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
	resp, err = Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		return "fresh", nil
	})
	if err != nil || resp != "fresh" {
		t.Fatalf("post-ttl committed error retry = (%q, %v), want fresh nil", resp, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("committed error calls = %d, want cache evicted and rerun", got)
	}
}

func TestCoordinatorPrunesSubmittedAttemptErrorByCapacity(t *testing.T) {
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Hour))
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage)
	var calls atomic.Int32
	run := func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordSubmitted("session-1", ref)
		return "submitted", errRecorderTest
	}

	resp, err := Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, run)
	if !errors.Is(err, errRecorderTest) || resp != "submitted" {
		t.Fatalf("first submitted error = (%q, %v), want submitted recorder error", resp, err)
	}
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
	resp, err = Do(coord, context.Background(), "session-1", ref, "same", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		return "fresh", nil
	})
	if err != nil || resp != "fresh" {
		t.Fatalf("post-capacity submitted error retry = (%q, %v), want fresh nil", resp, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("submitted error calls = %d, want cache evicted and rerun", got)
	}
}

func TestCoordinatorPrunesExpiredTombstonesBeforeCapacityCheck(t *testing.T) {
	now := time.Date(2026, 6, 30, 15, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Minute), WithNow(func() time.Time { return now }))
	old := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	if err := coord.CancelOperation("session-1", old); err != nil {
		t.Fatalf("old CancelOperation: %v", err)
	}
	now = now.Add(2 * time.Minute)
	newRef := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	if err := coord.CancelOperation("session-1", newRef); err != nil {
		t.Fatalf("new CancelOperation after old TTL: %v", err)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{old, newRef}), old, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{newRef}), newRef, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorBoundsEvictedMarkers(t *testing.T) {
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Hour))
	evictedA := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	evictedB := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	current := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	coord.RecordCommitted("session-1", evictedA)
	coord.RecordCommitted("session-1", evictedB)
	coord.RecordCommitted("session-1", current)
	snapshot := mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{evictedA, evictedB, current})
	assertState(t, snapshot, evictedA, clientui.RuntimeInputReconciliationUnknown)
	assertState(t, snapshot, evictedB, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, snapshot, current, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorFailedAttemptCanRetryWithSameOperationRecord(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindCompact)
	var calls atomic.Int32
	_, err := Do(coord, context.Background(), "session-1", ref, "args", func(a string, b string) bool { return a == b }, func(context.Context, Attempt) (string, error) {
		calls.Add(1)
		coord.RecordFailedWithRestore("session-1", ref)
		return "", errRecorderTest
	})
	if !errors.Is(err, errRecorderTest) {
		t.Fatalf("first error = %v, want recorder failure", err)
	}
	failed := mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref})
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
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestCoordinatorCancelBeforeRegisterCreatesNonEvictableTombstone(t *testing.T) {
	coord := NewCoordinator(WithLimit(1))
	canceled := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{canceled}), canceled, clientui.RuntimeInputReconciliationCanceledNotCommitted)
	for range 3 {
		ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
		coord.RecordCommitted("session-1", ref)
	}
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{canceled}), canceled, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorCancelInFlightCancelsAttemptContext(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
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
	assertState(t, mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}), ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorTerminalTTLMarksEvictedButKeepsUnexpiredTombstones(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	coord := NewCoordinator(WithLimit(1), WithTTL(time.Hour), WithNow(func() time.Time { return now }))
	terminal := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	tombstone := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	coord.RecordCommitted("session-1", terminal)
	if err := coord.CancelOperation("session-1", tombstone); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	now = now.Add(2 * time.Minute)
	coord.RecordCommitted("session-1", testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit))
	snapshot := mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{terminal, tombstone})
	assertState(t, snapshot, terminal, clientui.RuntimeInputReconciliationEvicted)
	assertState(t, snapshot, tombstone, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func assertState(t *testing.T, snapshot clientui.RuntimeInputReconciliationSnapshot, ref clientui.RuntimeOperationRef, want clientui.RuntimeInputReconciliationState) {
	t.Helper()
	for _, record := range snapshot.Operations {
		if record.Operation.Key() == ref.Key() {
			if record.State != want {
				t.Fatalf("state for %+v = %q, want %q", ref, record.State, want)
			}
			return
		}
	}
	t.Fatalf("missing record for %+v in %+v", ref, snapshot.Operations)
}

func mustFeedSnapshot(t *testing.T, coord *Coordinator, sessionID string, refs []clientui.RuntimeOperationRef) clientui.RuntimeInputReconciliationSnapshot {
	t.Helper()
	snapshot, err := coord.FeedSnapshot(sessionID, refs)
	if err != nil {
		t.Fatalf("FeedSnapshot: %v", err)
	}
	return snapshot
}

func testRuntimeOperationRef(kind clientui.RuntimeOperationKind) clientui.RuntimeOperationRef {
	return clientui.RuntimeOperationRef{
		Kind:            kind,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}
}
